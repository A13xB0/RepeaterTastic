package plugins

import (
	"path/filepath"
	"slices"
	"sort"

	pluginv1 "github.com/ScotMesh/RepeaterTastic/api/plugin/v1"
)

// Info is a plugin as the web API shows it. Secrets are masked.
type Info struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version,omitempty"`
	Description string         `json:"description,omitempty"`
	Author      string         `json:"author,omitempty"`
	Homepage    string         `json:"homepage,omitempty"`
	License     string         `json:"license,omitempty"`
	Kind        string         `json:"kind"` // managed or attached
	Enabled     bool           `json:"enabled"`
	Pinned      bool           `json:"pinned"`
	State       string         `json:"state"` // disabled, needs_review, needs_settings, starting, running, restarting, crashed, stopped, waiting, unsupported
	Detail      string         `json:"detail,omitempty"`
	Permissions []Permission   `json:"permissions"`
	Network     []string       `json:"network,omitempty"`
	Settings    []Setting      `json:"settings"`
	Values      map[string]any `json:"values"`
	SecretsSet  []string       `json:"secrets_set"`
	Status      *Status        `json:"status,omitempty"`
	HasLogo     bool           `json:"has_logo"`
	HasPanel    bool           `json:"has_panel"`
	Connected   bool           `json:"connected"`
	ConnectedAt int64          `json:"connected_at,omitempty"`
	StartedAt   int64          `json:"started_at,omitempty"`
	Restarts    int            `json:"restarts"`
	Dropped     uint64         `json:"dropped_events"`
	InstalledAt int64          `json:"installed_at"`
	Source      string         `json:"source,omitempty"`
}

// Permission is one permission the plugin asks for (or, attached, was granted).
type Permission struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Granted bool   `json:"granted"`
}

// Status is what the plugin last reported.
type Status struct {
	Summary string            `json:"summary"`
	State   string            `json:"state"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// List returns every plugin, by name.
func (m *Manager) List() []Info {
	m.mu.Lock()
	out := make([]Info, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, m.infoLocked(p))
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns one plugin.
func (m *Manager) Get(id string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.plugins[id]
	if p == nil {
		return Info{}, ErrNotFound
	}
	return m.infoLocked(p), nil
}

func (m *Manager) infoLocked(p *plugin) Info {
	enabled, granted, settings := p.effective()
	in := Info{ID: p.id, Name: p.id, Kind: "managed", Enabled: enabled, Pinned: p.pinned != nil, State: p.state, Detail: p.detail,
		Restarts: p.restarts, InstalledAt: p.rec.InstalledAt.UnixMilli(), Source: p.rec.Source, Settings: []Setting{},
		Permissions: []Permission{}, SecretsSet: []string{}}
	if p.rec.Attached {
		in.Kind = "attached"
		if p.rec.Name != "" {
			in.Name = p.rec.Name
		}
	}
	var asked []string
	if man := p.manifest; man != nil {
		in.fromManifest(p, man)
		asked = man.Permissions
	}
	in.Permissions = permissionList(asked, granted)
	in.Values, in.SecretsSet = maskSettings(in.Settings, settings)
	if in.SecretsSet == nil {
		in.SecretsSet = []string{}
	}
	if p.status != nil {
		in.Status = statusJSON(p.status)
	}
	if p.sess != nil {
		in.Connected, in.ConnectedAt, in.Dropped = true, p.connected.UnixMilli(), p.sess.dropped.Load()
	}
	if !p.startedAt.IsZero() && p.run != nil {
		in.StartedAt = p.startedAt.UnixMilli()
	}
	return in
}

// fromManifest fills in what the plugin's manifest says about it.
func (in *Info) fromManifest(p *plugin, man *Manifest) {
	in.Name, in.Version, in.Description, in.Author, in.Homepage, in.License = man.Name, man.Version, man.Description, man.Author, man.Homepage, man.License
	if p.rec.Attached && p.rec.Name != "" {
		in.Name = p.rec.Name
	}
	in.Network, in.Settings = man.Network, man.Settings
	in.HasLogo = man.Logo != "" && p.dir != ""
	in.HasPanel = man.UI.Panel != "" && p.dir != ""
}

// permissionList is the permissions asked for, then any granted that weren't asked for, each
// marked with whether it's granted.
func permissionList(asked, granted []string) []Permission {
	out := []Permission{}
	for _, g := range granted {
		if !slices.Contains(asked, g) {
			asked = append(asked, g)
		}
	}
	for _, k := range asked {
		out = append(out, Permission{Key: k, Text: Permissions[k], Granted: slices.Contains(granted, k)})
	}
	return out
}

func statusJSON(s *pluginv1.Status) *Status {
	st := s.State
	if st != "ok" && st != "warning" && st != "error" {
		st = "ok"
	}
	return &Status{Summary: s.Summary, State: st, Fields: s.Fields}
}

// Logs returns a plugin's recent log lines.
func (m *Manager) Logs(id string) ([]LogLine, error) {
	m.mu.Lock()
	p := m.plugins[id]
	m.mu.Unlock()
	if p == nil {
		return nil, ErrNotFound
	}
	return p.logs.list(), nil
}

// LogoPath is the logo file on disk.
func (m *Manager) LogoPath(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.plugins[id]
	if p == nil || p.manifest == nil || p.manifest.Logo == "" || p.dir == "" {
		return "", ErrNotFound
	}
	return filepath.Join(p.dir, filepath.FromSlash(p.manifest.Logo)), nil
}

// PanelRoot is the folder the panel is served from and the panel file's name in it.
func (m *Manager) PanelRoot(id string) (root, index string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.plugins[id]
	if p == nil || p.manifest == nil || p.manifest.UI.Panel == "" || p.dir == "" {
		return "", "", ErrNotFound
	}
	full := filepath.Join(p.dir, filepath.FromSlash(p.manifest.UI.Panel))
	return filepath.Dir(full), filepath.Base(full), nil
}

// PanelData is the JSON the plugin last sent for its panel ("" = none).
func (m *Manager) PanelData(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.plugins[id]
	if p == nil {
		return "", ErrNotFound
	}
	return p.panel, nil
}

// PanelAction passes an action from the plugin's panel to the plugin.
func (m *Manager) PanelAction(id, name, payloadJSON string) error {
	m.mu.Lock()
	p := m.plugins[id]
	var sess *session
	if p != nil {
		sess = p.sess
	}
	m.mu.Unlock()
	switch {
	case p == nil:
		return ErrNotFound
	case sess == nil:
		return ErrConflict
	}
	sess.send(&pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Action{Action: &pluginv1.PanelAction{Name: name, PayloadJson: payloadJSON}}})
	p.logs.add("debug", "host", "panel action "+name)
	return nil
}
