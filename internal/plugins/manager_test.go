package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/ScotMesh/RepeaterTastic/api/plugin/v1"
	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestManager is a Manager on a fresh plugins folder, with no radios.
func newTestManager(t *testing.T, entries []config.PluginEntry) *Manager {
	t.Helper()
	cfg := config.Default().Plugins
	cfg.Entries = entries
	m, err := New(Options{Config: cfg, Dir: shortPluginDir(t), Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// installBundle installs a bundle with manifest and a run.sh holding script.
func installBundle(t *testing.T, m *Manager, manifest, script string) {
	t.Helper()
	b := zipBundle(t, map[string]string{"plugin.yaml": manifest, "run.sh": "#!/bin/sh\n" + script + "\n"},
		map[string]os.FileMode{"run.sh": 0o755})
	if _, err := m.Install(bytes.NewReader(b), int64(len(b)), "test"); err != nil {
		t.Fatal(err)
	}
}

func writeStateFile(t *testing.T, dir string, st stateFile) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "state.json"), string(b))
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "state.json"), future, future); err != nil {
		t.Fatal(err)
	}
}

func TestNewCleansLeftovers(t *testing.T) {
	dir := shortPluginDir(t)
	for _, d := range []string{"installed/.staging-1", "installed/.old-x-1", "inbox/.tmp", "installed/.keep"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(Options{Config: config.Default().Plugins, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]bool{"installed/.staging-1": false, "installed/.old-x-1": false, "inbox/.tmp": false, "installed/.keep": true} {
		if _, err := os.Stat(filepath.Join(dir, d)); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", d, err == nil, want)
		}
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("plugins folder mode %v", fi.Mode())
	}
}

func TestNewFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	if _, err := New(Options{Dir: file, Log: quietLog()}); err == nil {
		t.Fatal("New under a file succeeded")
	}
	dir := shortPluginDir(t)
	writeFile(t, filepath.Join(dir, "state.json"), "{")
	if _, err := New(Options{Dir: dir, Log: quietLog()}); err == nil {
		t.Fatal("New with a broken state.json succeeded")
	}
}

// seedFolders lays out installed folders and a state.json for TestReloadFindsFolders.
func seedFolders(t *testing.T, dir string) {
	t.Helper()
	inst := filepath.Join(dir, "installed")
	writeFile(t, filepath.Join(inst, "demo", "plugin.yaml"), "id: demo\nname: Demo\napi: 1\nrun:\n  managed:\n    exec: run.sh\n")
	writeFile(t, filepath.Join(inst, "demo", "run.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(inst, "wrong", "plugin.yaml"), "id: other\napi: 1\n")
	writeFile(t, filepath.Join(inst, "broken", "plugin.yaml"), "id: [")
	writeFile(t, filepath.Join(inst, "noyaml", "x"), "")
	writeFile(t, filepath.Join(inst, "loose-file"), "")
	writeFile(t, filepath.Join(inst, "nofiles", "plugin.yaml"), "id: nofiles\napi: 1\nlogo: l.png\n")
	writeStateFile(t, dir, stateFile{Plugins: map[string]*record{
		"gone":  {Enabled: true},
		"att":   {Attached: true, Enabled: true, Name: "Att", ManifestYAML: "id: att\nname: Attached\napi: 1\n"},
		"att2":  {Attached: true, ManifestYAML: "id: someone-else\napi: 1\n"},
		"att3":  {Attached: true},
		"other": {Attached: true, ManifestYAML: "id: ["},
	}})
}

func TestReloadFindsFolders(t *testing.T) {
	dir := shortPluginDir(t)
	seedFolders(t, dir)
	cfg := config.Default().Plugins
	cfg.Entries = []config.PluginEntry{{ID: "demo", Enabled: true, Settings: map[string]any{"x": "${RT_TEST_PIN}"}}, {ID: "missing"}}
	t.Setenv("RT_TEST_PIN", "pinned")
	m, err := New(Options{Config: cfg, Dir: dir, Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, in := range m.List() {
		ids = append(ids, in.ID)
	}
	if strings.Join(ids, ",") != "att,demo,att2,att3,other" { // by name: Att, Demo, att2, att3, other
		t.Fatalf("plugins %v", ids)
	}
	demo, _ := m.Get("demo")
	if !demo.Pinned || !demo.Enabled || demo.Source != "folder" || demo.Values["x"] != "pinned" || demo.State != "stopped" {
		t.Fatalf("demo %+v", demo)
	}
	att, _ := m.Get("att")
	if att.Kind != "attached" || att.Name != "Att" || att.Version != "" || att.State != "waiting" {
		t.Fatalf("att %+v", att)
	}
	if in, _ := m.Get("att2"); in.Name != "att2" {
		t.Fatalf("att2 took a foreign manifest: %+v", in)
	}
	if _, err := m.Get("gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gone: %v", err)
	}
	st, _ := loadState(filepath.Join(dir, "state.json"))
	if st.Plugins["gone"] != nil || st.Plugins["demo"] == nil {
		t.Fatalf("state not updated: %v", st.Plugins)
	}
	checkPinnedRefusals(t, m)
}

func checkPinnedRefusals(t *testing.T, m *Manager) {
	t.Helper()
	calls := map[string]error{
		"enable":   m.Enable("demo", nil),
		"disable":  m.Disable("demo"),
		"settings": m.SetSettings("demo", map[string]any{}),
		"remove":   m.Remove("demo", false),
	}
	for name, err := range calls {
		if !errors.Is(err, ErrPinned) {
			t.Errorf("%s on a pinned plugin: %v", name, err)
		}
	}
}

func TestNotFoundErrors(t *testing.T) {
	m := newTestManager(t, nil)
	_, tokErr := m.NewToken("x")
	_, logErr := m.Logs("x")
	_, _, panelErr := m.PanelRoot("x")
	_, dataErr := m.PanelData("x")
	_, logoErr := m.LogoPath("x")
	calls := map[string]error{
		"enable": m.Enable("x", nil), "disable": m.Disable("x"), "restart": m.Restart("x"),
		"settings": m.SetSettings("x", nil), "remove": m.Remove("x", true), "token": tokErr,
		"logs": logErr, "panel root": panelErr, "panel data": dataErr, "logo": logoErr,
		"action": m.PanelAction("x", "a", "{}"),
	}
	for name, err := range calls {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAttachValidation(t *testing.T) {
	m := newTestManager(t, nil)
	if _, err := m.Attach("Bad!", "", nil); err == nil {
		t.Fatal("bad id accepted")
	}
	if _, err := m.Attach("ok-id", "", []string{"radio.own"}); err == nil {
		t.Fatal("unknown permission accepted")
	}
	if _, err := m.Attach("ok-id", " Name ", []string{"nodes.read"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Attach("ok-id", "", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("second attach: %v", err)
	}
	if in, _ := m.Get("ok-id"); in.Name != "Name" || in.State != "waiting" || len(in.Permissions) != 1 || !in.Permissions[0].Granted {
		t.Fatalf("attached %+v", in)
	}
	// Before it describes itself, only real permissions can be granted; settings can't be set.
	if err := m.Enable("ok-id", []string{"radio.own"}); err == nil {
		t.Fatal("enabled with an unknown permission")
	}
	if err := m.SetSettings("ok-id", map[string]any{"a": 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("settings before a manifest: %v", err)
	}
	if err := m.Restart("ok-id"); err != nil {
		t.Fatalf("restart a waiting plugin: %v", err)
	}
}

func TestNewToken(t *testing.T) {
	m := newTestManager(t, nil)
	installBundle(t, m, echoManifest, "")
	if _, err := m.NewToken("echo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token for a managed plugin: %v", err)
	}
	old, _ := m.Attach("remote", "", nil)
	sess := &session{out: make(chan *pluginv1.HostMessage, 1), done: make(chan struct{})}
	m.plugins["remote"].sess = sess
	tok, err := m.NewToken("remote")
	if err != nil || tok == old {
		t.Fatalf("new token %q %v", tok, err)
	}
	select {
	case <-sess.done:
	default:
		t.Fatal("session not closed")
	}
	p := m.plugins["remote"]
	if p.hasToken(old) || !p.hasToken(tok) {
		t.Fatal("token not replaced")
	}
}

func TestEnableChecks(t *testing.T) {
	m := newTestManager(t, nil)
	installBundle(t, m, echoManifest, "")
	if err := m.Enable("echo", []string{"nodes.read"}); err == nil || !strings.Contains(err.Error(), "doesn't ask") {
		t.Fatalf("unasked permission: %v", err)
	}
	if err := m.SetSettings("echo", map[string]any{"greeting": 5}); err == nil {
		t.Fatal("bad setting accepted")
	}
	if err := m.SetSettings("echo", map[string]any{"greeting": "hi", "api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable("echo", nil); err != nil {
		t.Fatal(err)
	}
	in, _ := m.Get("echo")
	if !in.Enabled || in.State != "stopped" || strings.Join(in.SecretsSet, ",") != "api_key" || in.Values["api_key"] != SecretMask {
		t.Fatalf("enabled %+v", in)
	}
	if lines, _ := m.Logs("echo"); !strings.Contains(logText(lines), "enabled with no permissions") {
		t.Fatalf("log %s", logText(lines))
	}
	if err := m.Restart("echo"); err != nil {
		t.Fatalf("restart a stopped plugin: %v", err)
	}
	if err := m.Disable("echo"); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart("echo"); !errors.Is(err, ErrConflict) {
		t.Fatalf("restart a disabled plugin: %v", err)
	}
}

func TestSetSettingsReachesSession(t *testing.T) {
	m := newTestManager(t, nil)
	installBundle(t, m, echoManifest, "")
	sess := &session{out: make(chan *pluginv1.HostMessage, 4), done: make(chan struct{})}
	m.plugins["echo"].sess = sess
	if err := m.SetSettings("echo", map[string]any{"greeting": "hey"}); err != nil {
		t.Fatal(err)
	}
	msg := <-sess.out
	if got := msg.GetSettings().GetSettingsJson(); got != `{"greeting":"hey"}` {
		t.Fatalf("settings sent %q", got)
	}
	if err := m.PanelAction("echo", "go", `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if a := (<-sess.out).GetAction(); a.GetName() != "go" || a.GetPayloadJson() != `{"a":1}` {
		t.Fatalf("action %v", a)
	}
	m.plugins["echo"].sess = nil
	if err := m.PanelAction("echo", "go", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("action without a session: %v", err)
	}
}

func TestRemoveKeepsData(t *testing.T) {
	m := newTestManager(t, nil)
	for _, keep := range []bool{true, false} {
		installBundle(t, m, echoManifest, "")
		data := filepath.Join(m.dataRoot(), "echo")
		writeFile(t, filepath.Join(data, "db"), "x")
		if err := m.Remove("echo", keep); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(data); (err == nil) != keep {
			t.Fatalf("keep=%v: data exists=%v", keep, err == nil)
		}
		_ = os.RemoveAll(data)
	}
	if _, err := m.Attach("remote", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("remote", false); err != nil {
		t.Fatal(err)
	}
}

func TestInfoFromManifest(t *testing.T) {
	m := newTestManager(t, nil)
	manifest := echoManifest + "logo: logo.png\nui:\n  panel: ui/index.html\nauthor: A\nnetwork: [example.org]\n"
	b := zipBundle(t, map[string]string{"plugin.yaml": manifest, "run.sh": "", "logo.png": "p", "ui/index.html": "<p>"}, nil)
	if _, err := m.Install(bytes.NewReader(b), int64(len(b)), "upload"); err != nil {
		t.Fatal(err)
	}
	in, _ := m.Get("echo")
	if !in.HasLogo || !in.HasPanel || in.Author != "A" || in.Network[0] != "example.org" || in.Source != "upload" {
		t.Fatalf("info %+v", in)
	}
	logo, err := m.LogoPath("echo")
	if err != nil || filepath.Base(logo) != "logo.png" {
		t.Fatalf("logo %q %v", logo, err)
	}
	root, index, err := m.PanelRoot("echo")
	if err != nil || filepath.Base(root) != "ui" || index != "index.html" {
		t.Fatalf("panel %q %q %v", root, index, err)
	}
	m.mu.Lock()
	p := m.plugins["echo"]
	p.status = &pluginv1.Status{Summary: "s", State: "weird"}
	p.run, p.startedAt = &runner{}, time.Now()
	p.rec.Granted = []string{"nodes.read"} // granted but not asked for (an older manifest)
	m.mu.Unlock()
	in, _ = m.Get("echo")
	if in.Status.State != "ok" || in.StartedAt == 0 || len(in.Permissions) != 4 || in.Permissions[3].Key != "nodes.read" {
		t.Fatalf("info %+v", in)
	}
	m.mu.Lock()
	p.run = nil
	m.mu.Unlock()
}

func TestAttachedNameOverridesManifest(t *testing.T) {
	in := Info{}
	in.fromManifest(&plugin{rec: &record{Attached: true, Name: "Mine"}}, &Manifest{Name: "Theirs", Logo: "l.png"})
	if in.Name != "Mine" || in.HasLogo {
		t.Fatalf("%+v", in)
	}
	for _, st := range []string{"ok", "warning", "error"} {
		if statusJSON(&pluginv1.Status{State: st}).State != st {
			t.Errorf("state %s changed", st)
		}
	}
}

func TestCheckStateFile(t *testing.T) {
	notified := make(chan string, 16)
	cfg := config.Default().Plugins
	dir := shortPluginDir(t)
	m, err := New(Options{Config: cfg, Dir: dir, Log: quietLog(), Notify: func(id string) { notified <- id }})
	if err != nil {
		t.Fatal(err)
	}
	m.checkStateFile() // unchanged: nothing happens
	writeStateFile(t, dir, stateFile{Plugins: map[string]*record{"cli-added": {Attached: true}}})
	m.checkStateFile()
	if _, err := m.Get("cli-added"); err != nil {
		t.Fatalf("reload missed the new plugin: %v", err)
	}
	select {
	case id := <-notified:
		if id != "" {
			t.Fatalf("notified %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification")
	}
	writeStateFile(t, dir, stateFile{})
	writeFile(t, filepath.Join(dir, "state.json"), "{")
	m.checkStateFile() // a broken file is logged, the plugins stay
	if _, err := m.Get("cli-added"); err != nil {
		t.Fatalf("broken reload dropped plugins: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "state.json"))
	m.checkStateFile()
}

func TestNotifyCoalesces(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	done := make(chan struct{}, 4)
	m := &Manager{opt: Options{Notify: func(id string) {
		mu.Lock()
		calls[id]++
		mu.Unlock()
		done <- struct{}{}
	}}}
	for i := 0; i < 5; i++ {
		m.notify("a")
	}
	m.notify("b")
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("notify never fired")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["a"] != 1 || calls["b"] != 1 {
		t.Fatalf("calls %v", calls)
	}
}

func TestScanInbox(t *testing.T) {
	m := newTestManager(t, nil)
	inbox := m.InboxDir()
	good := zipBundle(t, goodBundle, nil)
	writeFile(t, filepath.Join(inbox, "echo.ZIP"), string(good))
	writeFile(t, filepath.Join(inbox, "bad.zip"), "not a zip")
	writeFile(t, filepath.Join(inbox, "notes.txt"), "")
	writeFile(t, filepath.Join(inbox, ".part.zip"), "")
	if err := os.Mkdir(filepath.Join(inbox, "dir.zip"), 0o755); err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int64{}
	m.scanInbox(sizes) // first sight: remember sizes
	if _, err := m.Get("echo"); err == nil {
		t.Fatal("installed before the size settled")
	}
	m.scanInbox(sizes)
	if in, err := m.Get("echo"); err != nil || in.Source != "inbox" {
		t.Fatalf("not installed from the inbox: %v", err)
	}
	for name, want := range map[string]bool{"echo.ZIP": false, "bad.zip": false, "notes.txt": true, ".rejected/bad.zip": true} {
		if _, err := os.Stat(filepath.Join(inbox, name)); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", name, err == nil, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(inbox, ".rejected", "bad.zip.error.txt")); !strings.Contains(string(b), "not a zip") {
		t.Fatalf("rejection reason %q", b)
	}
}

func TestSocketPathAndRadio(t *testing.T) {
	short := &Manager{opt: Options{Dir: "/tmp/p"}}
	if short.SocketPath() != "/tmp/p/host.sock" || short.InboxDir() != "/tmp/p/inbox" {
		t.Fatalf("paths %s %s", short.SocketPath(), short.InboxDir())
	}
	long := &Manager{opt: Options{Dir: "/" + strings.Repeat("d", 120)}}
	if p := long.socketPath(); len(p) >= 100 || filepath.Base(p) != "host.sock" || p != long.socketPath() {
		t.Fatalf("long dir socket %q", p)
	}
	m := &Manager{opt: Options{Radios: []Radio{{ID: "main"}, {ID: "mf"}}}}
	if m.radio("").ID != "main" || m.radio("mf").ID != "mf" || m.radio("nope") != nil {
		t.Fatal("radio lookup")
	}
	if (&Manager{}).radio("") != nil {
		t.Fatal("radio with no radios")
	}
	if permList(nil) != "no permissions" || permList([]string{"b", "a"}) != "a, b" {
		t.Fatal("permList")
	}
	if len((&Manager{}).choices().radios) != 0 {
		t.Fatal("choices without radios")
	}
}

func TestSetLimitsRejectsNegative(t *testing.T) {
	m := &Manager{plugins: map[string]*plugin{}, log: quietLog()}
	if err := m.SetLimits(Limits{MessagesPerHour: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
	b := newBucket(6)
	b.take()
	b.refund()
	b.refund()
	if ok, _ := b.take(); !ok {
		t.Fatal("refund didn't give the send back")
	}
	if ok, _ := b.take(); ok {
		t.Fatal("refund went over the burst")
	}
}

func TestRunFailsOnTakenPort(t *testing.T) {
	cfg := config.Default().Plugins
	cfg.Listen = "256.0.0.1:1"
	m, err := New(Options{Config: cfg, Dir: shortPluginDir(t), Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Run(context.Background()); err == nil || !strings.Contains(m.StartError(), "plugins.listen") {
		t.Fatalf("Run: %v, StartError %q", err, m.StartError())
	}
	m.Wait() // never started: returns at once
}

func TestRunFailsOnBadSocketFolder(t *testing.T) {
	m := newTestManager(t, nil)
	writeFile(t, filepath.Join(m.opt.Dir, "host.sock", "x"), "") // a folder where the socket goes
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("started without a socket")
	}
	file := filepath.Join(t.TempDir(), "f")
	writeFile(t, file, "")
	m2 := &Manager{opt: Options{Dir: filepath.Join(file, "x")}, log: quietLog()}
	if err := m2.Start(context.Background()); err == nil {
		t.Fatal("started with the socket folder under a file")
	}
}

func TestRunUntilCancelled(t *testing.T) {
	m := newTestManager(t, nil)
	installBundle(t, m, echoManifest, "")
	m.plugins["echo"].state = "crashed"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	// The poll loop picks up a bundle dropped in the inbox.
	writeFile(t, filepath.Join(m.InboxDir(), "other.zip"), string(zipBundle(t, map[string]string{
		"plugin.yaml": strings.Replace(echoManifest, "id: echo", "id: other", 1), "run.sh": ""}, nil)))
	waitFor(t, 5*time.Second, func() bool { _, err := m.Get("other"); return err == nil }, nil)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return")
	}
	if in, _ := m.Get("echo"); in.State == "crashed" {
		t.Fatal("Start didn't clear the crashed state")
	}
}
