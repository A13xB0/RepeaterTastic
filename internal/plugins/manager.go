package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	pluginv1 "github.com/A13xB0/RepeaterTastic/pluginapi/v1"
)

// Radio is one of the site's radios, as plugins see it.
type Radio struct {
	ID, Name string
	Host     *mesh.Host
}

// Options configure a Manager.
type Options struct {
	Config  config.Plugins
	Dir     string
	Radios  []Radio // the main radio first
	Version string
	Log     *slog.Logger
	// Notify is called (never under a lock) when a plugin's state, status or panel data changes.
	Notify func(id string)
}

// Errors the web API maps to status codes.
var (
	ErrNotFound = errors.New("no such plugin")
	ErrPinned   = errors.New("this plugin is set in the config file (plugins.entries); change it there")
	ErrConflict = errors.New("conflict")
)

// Manager owns the installed plugins, runs the managed ones and serves the Plugin API.
type Manager struct {
	opt Options
	log *slog.Logger

	mu        sync.Mutex
	st        stateFile
	stMod     time.Time
	plugins   map[string]*plugin
	ctx       context.Context // set by Start
	closing   bool            // shutting down: start nothing new
	done      chan struct{}   // closed when Start's loop has stopped every plugin
	listening string          // TCP address attached plugins use, once listening

	notifyMu      sync.Mutex
	notifyPending map[string]bool
}

// plugin is an installed (or attached) plugin and what it is doing now. Guarded by Manager.mu.
type plugin struct {
	id       string
	rec      *record
	manifest *Manifest // nil for an attached plugin that hasn't said what it is
	dir      string    // bundle folder; "" for attached
	pinned   *config.PluginEntry

	run       *runner  // the managed process supervisor, until its process has exited
	busy      int      // lifecycle operations (install, restart, remove) in progress: reconcile leaves it alone
	sess      *session // the connected session, if any
	token     string   // current managed process's token
	state     string
	detail    string
	status    *pluginv1.Status
	panel     string
	startedAt time.Time
	connected time.Time
	restarts  int
	logs      *logRing
	msgBudget *bucket
	trBudget  *bucket
}

// New loads the plugins folder. Call Run to start them.
func New(opt Options) (*Manager, error) {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	m := &Manager{opt: opt, log: opt.Log.With("component", "plugins"), plugins: map[string]*plugin{}, notifyPending: map[string]bool{}}
	for _, d := range []string{m.installedDir(), m.dataRoot(), m.inboxDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	// Leftovers from an install or upload that was interrupted.
	if entries, err := os.ReadDir(m.installedDir()); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".staging-") || strings.HasPrefix(e.Name(), ".old-") {
				_ = os.RemoveAll(filepath.Join(m.installedDir(), e.Name()))
			}
		}
	}
	_ = os.RemoveAll(filepath.Join(m.inboxDir(), ".tmp"))
	_ = os.Chmod(opt.Dir, 0o700)
	if err := m.reload(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) installedDir() string { return filepath.Join(m.opt.Dir, "installed") }
func (m *Manager) dataRoot() string     { return filepath.Join(m.opt.Dir, "data") }
func (m *Manager) inboxDir() string     { return filepath.Join(m.opt.Dir, "inbox") }
func (m *Manager) statePath() string    { return filepath.Join(m.opt.Dir, "state.json") }
func (m *Manager) socketPath() string   { return filepath.Join(m.opt.Dir, "host.sock") }

// InboxDir is the folder bundles can be dropped into.
func (m *Manager) InboxDir() string { return m.inboxDir() }

// SocketPath is the Unix socket managed plugins connect to.
func (m *Manager) SocketPath() string { return m.socketPath() }

// reload reads state.json and the installed folders, and reconciles the running plugins with
// them. It runs at startup and whenever state.json changes behind our back (the CLI).
func (m *Manager) reload() error {
	st, err := loadState(m.statePath())
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.st = st
	if fi, err := os.Stat(m.statePath()); err == nil {
		m.stMod = fi.ModTime()
	}
	seen := map[string]bool{}
	entries, _ := os.ReadDir(m.installedDir())
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(m.installedDir(), e.Name())
		b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
		if err != nil {
			continue
		}
		man, err := ParseManifest(b)
		if err == nil && man.ID != e.Name() {
			err = fmt.Errorf("its folder is %s but plugin.yaml says id %s", e.Name(), man.ID)
		}
		if err == nil {
			err = man.checkFiles(dir)
		}
		if err != nil {
			m.log.Warn("ignoring installed plugin", "folder", e.Name(), "err", err)
			continue
		}
		seen[man.ID] = true
		rec := m.st.Plugins[man.ID]
		if rec == nil || rec.Attached {
			rec = &record{InstalledAt: time.Now(), Source: "folder"}
			m.st.Plugins[man.ID] = rec
			m.log.Info("found a new plugin folder", "plugin", man.ID)
		}
		p := m.pluginLocked(man.ID)
		p.rec, p.manifest, p.dir = rec, man, dir
	}
	for id, rec := range m.st.Plugins {
		if rec.Attached {
			seen[id] = true
			p := m.pluginLocked(id)
			p.rec, p.dir = rec, ""
			p.manifest = nil
			if rec.ManifestYAML != "" {
				if man, err := ParseManifest([]byte(rec.ManifestYAML)); err == nil && man.ID == id {
					p.manifest = man
				}
			}
		} else if !seen[id] {
			m.log.Warn("plugin folder is gone; forgetting the plugin", "plugin", id)
			delete(m.st.Plugins, id)
		}
	}
	var gone []*plugin
	for id, p := range m.plugins {
		if !seen[id] {
			gone = append(gone, p)
			delete(m.plugins, id)
		}
	}
	for i := range m.opt.Config.Entries {
		e := &m.opt.Config.Entries[i]
		if p := m.plugins[e.ID]; p != nil {
			p.pinned = e
		} else {
			m.log.Warn("plugins.entries names a plugin that isn't installed", "plugin", e.ID)
		}
	}
	err = saveState(m.statePath(), m.st)
	if fi, serr := os.Stat(m.statePath()); serr == nil {
		m.stMod = fi.ModTime()
	}
	m.mu.Unlock()
	for _, p := range gone {
		m.stopPlugin(p, "removed")
	}
	m.reconcile()
	return err
}

func (m *Manager) pluginLocked(id string) *plugin {
	p := m.plugins[id]
	if p == nil {
		p = &plugin{id: id, logs: newLogRing(1000), state: "disabled",
			msgBudget: newBucket(m.opt.Config.MessagesPerHour), trBudget: newBucket(m.opt.Config.TraceroutesPerHour)}
		m.plugins[id] = p
	}
	return p
}

// saveLocked writes state.json.
func (m *Manager) saveLocked() error {
	err := saveState(m.statePath(), m.st)
	if fi, serr := os.Stat(m.statePath()); serr == nil {
		m.stMod = fi.ModTime()
	}
	return err
}

// effective returns the switch, grants and settings in force: the config file's when pinned.
func (p *plugin) effective() (enabled bool, granted []string, settings map[string]any) {
	var schema []Setting
	if p.manifest != nil {
		schema = p.manifest.Settings
	}
	if p.pinned != nil {
		return p.pinned.Enabled, p.pinned.Permissions, resolveSettings(schema, p.pinned.Settings, true)
	}
	return p.rec.Enabled, p.rec.Granted, resolveSettings(schema, p.rec.Settings, false)
}

// blocker says why an enabled plugin can't run yet ("" = it can).
func (p *plugin) blocker() (state, detail string) {
	enabled, granted, settings := p.effective()
	switch {
	case !enabled:
		return "disabled", ""
	case p.manifest == nil:
		return "waiting", "Waiting for the plugin to connect"
	}
	var missing []string
	for _, perm := range p.manifest.Permissions {
		if p.pinned == nil && !slices.Contains(p.rec.Reviewed, perm) && !slices.Contains(granted, perm) {
			missing = append(missing, perm)
		}
	}
	if len(missing) > 0 {
		return "needs_review", "The plugin asks for new permissions: " + strings.Join(missing, ", ")
	}
	if miss := missingSettings(p.manifest.Settings, settings); len(miss) > 0 {
		return "needs_settings", "Fill in " + strings.Join(miss, ", ")
	}
	if p.rec.Attached {
		return "waiting", "Waiting for the plugin to connect"
	}
	if _, err := p.manifest.ExecPath(); err != nil {
		return "unsupported", "The plugin has no program to run"
	}
	return "", ""
}

// reconcile starts the plugins that should run and stops the ones that shouldn't.
func (m *Manager) reconcile() {
	m.mu.Lock()
	if m.ctx == nil {
		for _, p := range m.plugins {
			if st, d := p.blocker(); st != "" {
				p.state, p.detail = st, d
			} else {
				p.state, p.detail = "stopped", ""
			}
		}
		m.mu.Unlock()
		return
	}
	var start, stop []*plugin
	for _, p := range m.plugins {
		if p.busy > 0 {
			continue // installing, restarting or removing: that operation reconciles when done
		}
		st, detail := p.blocker()
		if st == "" && p.run == nil && p.state != "crashed" && !p.rec.Attached && !m.closing {
			start = append(start, p)
		}
		if st != "" {
			// A connected attached plugin shows "waiting" as its blocker; that isn't a reason to stop it.
			if p.run != nil || (p.sess != nil && p.rec.Attached && st != "waiting") {
				stop = append(stop, p)
			}
			if st != "waiting" || p.sess == nil {
				p.state, p.detail = st, detail
			}
		}
	}
	ctx := m.ctx
	m.mu.Unlock()
	for _, p := range stop {
		m.stopPlugin(p, "disabled")
	}
	for _, p := range start {
		m.startPlugin(ctx, p)
	}
}

// Run serves the Plugin API and supervises plugins until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	if err := m.Start(ctx); err != nil {
		return err
	}
	m.Wait()
	return nil
}

// Start listens for plugins and starts the enabled ones; they run until ctx ends. It fails when
// the Plugin API can't listen (a taken plugins.listen port).
func (m *Manager) Start(ctx context.Context) error {
	srv, err := m.serve(ctx)
	if err != nil {
		return err
	}
	for _, r := range m.opt.Radios {
		r.Host.PacketCopies.Store(true)
	}
	m.mu.Lock()
	m.ctx = ctx
	m.done = make(chan struct{})
	for _, p := range m.plugins {
		if p.state == "crashed" {
			p.state = "stopped"
		}
	}
	m.mu.Unlock()
	m.reconcile()
	go m.loop(ctx, srv)
	return nil
}

// Wait returns once Start's plugins have all stopped after ctx ended.
func (m *Manager) Wait() {
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (m *Manager) loop(ctx context.Context, srv interface{ Stop() }) {
	defer close(m.done)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	inbox := map[string]int64{}
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.closing = true
			all := slices.Collect(maps.Values(m.plugins))
			m.mu.Unlock()
			var wg sync.WaitGroup
			for _, p := range all {
				wg.Go(func() { m.stopPlugin(p, "RepeaterTastic is stopping") })
			}
			wg.Wait()
			srv.Stop()
			return
		case <-tick.C:
			m.scanInbox(inbox)
			if fi, err := os.Stat(m.statePath()); err == nil {
				m.mu.Lock()
				changed := !fi.ModTime().Equal(m.stMod)
				m.mu.Unlock()
				if changed {
					m.log.Info("plugin state changed on disk; reloading")
					if err := m.reload(); err != nil {
						m.log.Error("reloading plugin state", "err", err)
					}
					m.notify("")
				}
			}
		}
	}
}

// scanInbox installs bundles dropped into <dir>/inbox once their size has stopped changing.
func (m *Manager) scanInbox(sizes map[string]int64) {
	entries, _ := os.ReadDir(m.inboxDir())
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".zip") {
			continue
		}
		path := filepath.Join(m.inboxDir(), name)
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if last, ok := sizes[name]; !ok || last != fi.Size() {
			sizes[name] = fi.Size()
			continue
		}
		delete(sizes, name)
		f, err := os.Open(path)
		if err == nil {
			_, err = m.Install(f, fi.Size(), "inbox")
			f.Close()
		}
		if err != nil {
			m.log.Warn("rejected plugin bundle from the inbox", "file", name, "err", err)
			rej := filepath.Join(m.inboxDir(), ".rejected")
			_ = os.MkdirAll(rej, 0o755)
			_ = os.Rename(path, filepath.Join(rej, name))
			_ = os.WriteFile(filepath.Join(rej, name+".error.txt"), []byte(err.Error()+"\n"), 0o644)
			continue
		}
		_ = os.Remove(path)
	}
}

// Install unpacks a bundle and installs or upgrades the plugin. An upgrade keeps its switch,
// settings and grants; if it asks for new permissions it waits for review before running.
func (m *Manager) Install(r io.ReaderAt, size int64, source string) (*Manifest, error) {
	if size > MaxBundleBytes {
		return nil, fmt.Errorf("the bundle is larger than %d MB", MaxBundleBytes>>20)
	}
	staging, man, err := unpack(r, size, m.installedDir())
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	m.mu.Lock()
	if rec := m.st.Plugins[man.ID]; rec != nil && rec.Attached {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s is an attached plugin; remove it first", ErrConflict, man.ID)
	}
	p := m.plugins[man.ID]
	release := func() {}
	if p != nil {
		p.busy++ // keep reconcile from starting the old version while it's replaced
		held := p
		var once sync.Once
		release = func() {
			once.Do(func() {
				m.mu.Lock()
				held.busy--
				m.mu.Unlock()
			})
		}
	}
	m.mu.Unlock()
	defer release()
	if p != nil {
		m.stopPlugin(p, "upgrading")
	}

	final := filepath.Join(m.installedDir(), man.ID)
	old := filepath.Join(m.installedDir(), ".old-"+man.ID+"-"+randomHex(4))
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			return nil, err
		}
		defer os.RemoveAll(old)
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.Rename(old, final)
		return nil, err
	}

	m.mu.Lock()
	rec := m.st.Plugins[man.ID]
	upgrade := rec != nil
	if rec == nil {
		rec = &record{InstalledAt: time.Now(), Source: source}
		m.st.Plugins[man.ID] = rec
	}
	rec.Granted = slices.DeleteFunc(rec.Granted, func(g string) bool { return !slices.Contains(man.Permissions, g) })
	for k := range rec.Settings {
		if !slices.ContainsFunc(man.Settings, func(s Setting) bool { return s.Key == k }) {
			delete(rec.Settings, k)
		}
	}
	p = m.pluginLocked(man.ID)
	p.rec, p.manifest, p.dir = rec, man, final
	if p.state == "crashed" {
		p.state = "stopped"
	}
	for i := range m.opt.Config.Entries {
		if m.opt.Config.Entries[i].ID == man.ID {
			p.pinned = &m.opt.Config.Entries[i]
		}
	}
	err = m.saveLocked()
	m.mu.Unlock()
	release()
	verb := "installed"
	if upgrade {
		verb = "upgraded"
	}
	p.logs.add("info", "host", fmt.Sprintf("%s %s %s from %s", verb, man.Name, man.Version, source))
	m.log.Info("plugin "+verb, "plugin", man.ID, "version", man.Version, "source", source)
	m.reconcile()
	m.notify(man.ID)
	return man, err
}

// Enable turns a plugin on with the permissions the operator granted.
func (m *Manager) Enable(id string, granted []string) error {
	m.mu.Lock()
	p := m.plugins[id]
	switch {
	case p == nil:
		m.mu.Unlock()
		return ErrNotFound
	case p.pinned != nil:
		m.mu.Unlock()
		return ErrPinned
	}
	if p.manifest != nil {
		for _, g := range granted {
			if !slices.Contains(p.manifest.Permissions, g) {
				m.mu.Unlock()
				return fmt.Errorf("the plugin doesn't ask for %s", g)
			}
		}
		if miss := missingSettings(p.manifest.Settings, resolveSettings(p.manifest.Settings, p.rec.Settings, false)); len(miss) > 0 {
			m.mu.Unlock()
			return fmt.Errorf("fill in %s first", strings.Join(miss, ", "))
		}
	} else {
		for _, g := range granted {
			if _, ok := Permissions[g]; !ok {
				m.mu.Unlock()
				return fmt.Errorf("unknown permission %s", g)
			}
		}
	}
	before := slices.Clone(p.rec.Granted)
	wasOn := p.rec.Enabled
	p.rec.Enabled, p.rec.Granted = true, slices.Compact(slices.Sorted(slices.Values(granted)))
	// A running plugin's events and Welcome follow its grants from connect time: reconnect it.
	regrant := wasOn && !slices.Equal(before, p.rec.Granted) && (p.run != nil || p.sess != nil)
	sess := p.sess
	if p.manifest != nil {
		p.rec.Reviewed = slices.Clone(p.manifest.Permissions)
	}
	if p.state == "crashed" {
		p.state = "stopped"
	}
	err := m.saveLocked()
	m.mu.Unlock()
	p.logs.add("info", "host", "enabled with "+permList(granted))
	if regrant {
		p.logs.add("info", "host", "permissions changed; reconnecting the plugin")
		if p.rec.Attached {
			if sess != nil {
				sess.close("permissions changed")
			}
		} else {
			m.stopPlugin(p, "permissions changed")
		}
	}
	m.reconcile()
	m.notify(id)
	return err
}

// Disable stops a plugin and keeps it off.
func (m *Manager) Disable(id string) error {
	m.mu.Lock()
	p := m.plugins[id]
	switch {
	case p == nil:
		m.mu.Unlock()
		return ErrNotFound
	case p.pinned != nil:
		m.mu.Unlock()
		return ErrPinned
	}
	p.rec.Enabled = false
	err := m.saveLocked()
	m.mu.Unlock()
	p.logs.add("info", "host", "disabled")
	m.reconcile()
	m.notify(id)
	return err
}

// Restart stops and starts a running plugin (or retries a crashed one).
func (m *Manager) Restart(id string) error {
	m.mu.Lock()
	p := m.plugins[id]
	if p == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	if st, d := p.blocker(); st != "" && st != "waiting" {
		m.mu.Unlock()
		return fmt.Errorf("%w: the plugin can't run: %s", ErrConflict, strings.TrimSpace(st+" "+d))
	}
	p.busy++
	m.mu.Unlock()
	m.stopPlugin(p, "restarting")
	m.mu.Lock()
	p.busy--
	if p.state == "crashed" || p.state == "stopped" {
		p.state = "stopped"
	}
	m.mu.Unlock()
	m.reconcile()
	m.notify(id)
	return nil
}

// SetSettings saves new settings values and passes them to the running plugin.
func (m *Manager) SetSettings(id string, values map[string]any) error {
	m.mu.Lock()
	p := m.plugins[id]
	switch {
	case p == nil:
		m.mu.Unlock()
		return ErrNotFound
	case p.pinned != nil:
		m.mu.Unlock()
		return ErrPinned
	case p.manifest == nil:
		m.mu.Unlock()
		return fmt.Errorf("%w: the plugin hasn't described its settings yet", ErrConflict)
	}
	merged, err := mergeSettings(p.manifest.Settings, p.rec.Settings, values, m.choices())
	if err != nil {
		m.mu.Unlock()
		return err
	}
	p.rec.Settings = merged
	err = m.saveLocked()
	_, _, eff := p.effective()
	sess := p.sess
	m.mu.Unlock()
	if sess != nil {
		sess.send(&pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Settings{Settings: &pluginv1.SettingsChanged{SettingsJson: jsonString(eff)}}})
	}
	p.logs.add("info", "host", "settings changed")
	m.reconcile()
	m.notify(id)
	return err
}

// Remove stops and deletes a plugin, and its data folder unless keepData.
func (m *Manager) Remove(id string, keepData bool) error {
	m.mu.Lock()
	p := m.plugins[id]
	if p == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	if p.pinned != nil {
		m.mu.Unlock()
		return ErrPinned
	}
	p.busy++ // never released: the plugin is gone
	m.mu.Unlock()
	m.stopPlugin(p, "removed")
	m.mu.Lock()
	delete(m.plugins, id)
	delete(m.st.Plugins, id)
	err := m.saveLocked()
	dir := p.dir
	m.mu.Unlock()
	if dir != "" {
		if rerr := os.RemoveAll(dir); rerr != nil && err == nil {
			err = rerr
		}
	}
	if !keepData {
		_ = os.RemoveAll(filepath.Join(m.dataRoot(), id))
	}
	m.log.Info("plugin removed", "plugin", id, "kept_data", keepData)
	m.notify(id)
	return err
}

// Attach registers a plugin that runs elsewhere and returns its token (shown once).
func (m *Manager) Attach(id, name string, granted []string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("id %q must be 2-40 lowercase letters, digits and dashes", id)
	}
	for _, g := range granted {
		if _, ok := Permissions[g]; !ok {
			return "", fmt.Errorf("unknown permission %s", g)
		}
	}
	tok := "rtp_" + randomHex(24)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plugins[id] != nil {
		return "", fmt.Errorf("%w: a plugin with id %s is already installed", ErrConflict, id)
	}
	rec := &record{Enabled: true, Granted: granted, InstalledAt: time.Now(), Source: "attach", Attached: true,
		Name: strings.TrimSpace(name), TokenHash: hashToken(tok)}
	m.st.Plugins[id] = rec
	p := m.pluginLocked(id)
	p.rec, p.state, p.detail = rec, "waiting", "Waiting for the plugin to connect"
	if err := m.saveLocked(); err != nil {
		return "", err
	}
	go m.notify(id)
	return tok, nil
}

// NewToken replaces an attached plugin's token and disconnects it.
func (m *Manager) NewToken(id string) (string, error) {
	m.mu.Lock()
	p := m.plugins[id]
	if p == nil || !p.rec.Attached {
		m.mu.Unlock()
		return "", ErrNotFound
	}
	tok := "rtp_" + randomHex(24)
	p.rec.TokenHash = hashToken(tok)
	sess := p.sess
	err := m.saveLocked()
	m.mu.Unlock()
	if sess != nil {
		sess.close("token replaced")
	}
	return tok, err
}

// Limits caps what one plugin may send each hour; 0 means it may not send at all.
type Limits struct {
	MessagesPerHour    int `json:"messages_per_hour"`
	TraceroutesPerHour int `json:"traceroutes_per_hour"`
}

// MaxPerHour bounds the limits: a traceroute every 30 s is the firmware's own ceiling, and a
// message a minute already means a busy plugin.
const (
	MaxMessagesPerHour    = 600
	MaxTraceroutesPerHour = 120
)

// SetLimits changes every plugin's send limits at once, live.
func (m *Manager) SetLimits(l Limits) error {
	if l.MessagesPerHour < 0 || l.MessagesPerHour > MaxMessagesPerHour {
		return fmt.Errorf("messages per hour must be between 0 and %d", MaxMessagesPerHour)
	}
	if l.TraceroutesPerHour < 0 || l.TraceroutesPerHour > MaxTraceroutesPerHour {
		return fmt.Errorf("traceroutes per hour must be between 0 and %d", MaxTraceroutesPerHour)
	}
	m.mu.Lock()
	m.opt.Config.MessagesPerHour, m.opt.Config.TraceroutesPerHour = l.MessagesPerHour, l.TraceroutesPerHour
	for _, p := range m.plugins {
		p.msgBudget.setPerHour(l.MessagesPerHour)
		p.trBudget.setPerHour(l.TraceroutesPerHour)
	}
	m.mu.Unlock()
	m.log.Info("plugin send limits changed", "messages_per_hour", l.MessagesPerHour, "traceroutes_per_hour", l.TraceroutesPerHour)
	return nil
}

// Listening is the TCP address for attached plugins ("" = attaching is off).
func (m *Manager) Listening() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listening
}

// notify tells the GUI a plugin changed, at most every 250 ms per plugin: a plugin sending status
// in a loop mustn't flood the event bus that also carries packets.
func (m *Manager) notify(id string) {
	if m.opt.Notify == nil {
		return
	}
	m.notifyMu.Lock()
	if m.notifyPending == nil {
		m.notifyPending = map[string]bool{}
	}
	if m.notifyPending[id] {
		m.notifyMu.Unlock()
		return
	}
	m.notifyPending[id] = true
	m.notifyMu.Unlock()
	time.AfterFunc(250*time.Millisecond, func() {
		m.notifyMu.Lock()
		delete(m.notifyPending, id)
		m.notifyMu.Unlock()
		m.opt.Notify(id)
	})
}

// choices lists the site's radios and identities for list settings.
func (m *Manager) choices() siteChoices {
	var c siteChoices
	if len(m.opt.Radios) == 0 {
		return c // the CLI: nothing to check against
	}
	c.radios, c.identities = []string{}, []string{}
	for _, r := range m.opt.Radios {
		c.radios = append(c.radios, r.ID)
		for _, id := range r.Host.Identities() {
			c.identities = append(c.identities, id.NodeID())
		}
	}
	return c
}

func (m *Manager) radio(id string) *Radio {
	if id == "" && len(m.opt.Radios) > 0 {
		return &m.opt.Radios[0]
	}
	for i := range m.opt.Radios {
		if m.opt.Radios[i].ID == id {
			return &m.opt.Radios[i]
		}
	}
	return nil
}

func permList(p []string) string {
	if len(p) == 0 {
		return "no permissions"
	}
	s := slices.Clone(p)
	sort.Strings(s)
	return strings.Join(s, ", ")
}
