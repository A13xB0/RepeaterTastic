package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ScotMesh/RepeaterTastic/internal/plugins"
)

// assetKeys are capability keys in plugin asset URLs. The GUI authenticates with a bearer token,
// which <img> and <iframe> can't send, so the authenticated API hands out
// /plugin-assets/{id}/{key}/... URLs instead. Keys last until the daemon restarts.
type assetKeys struct {
	mu   sync.Mutex
	keys map[string]string
}

func (k *assetKeys) get(id string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = map[string]string{}
	}
	if v, ok := k.keys[id]; ok {
		return v
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	k.keys[id] = hex.EncodeToString(b)
	return k.keys[id]
}

func (k *assetKeys) valid(id, key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.keys[id]
	return ok && subtle.ConstantTimeCompare([]byte(key), []byte(v)) == 1
}

// forget drops a removed plugin's key, so its old asset URLs stop working.
func (k *assetKeys) forget(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, id)
}

func (s *Server) pluginRoutes(priv func(string, http.HandlerFunc)) {
	priv("GET /api/v1/plugins", s.listPlugins)
	priv("POST /api/v1/plugins", s.installPlugin)
	priv("POST /api/v1/plugins/attach", s.attachPlugin)
	priv("PUT /api/v1/plugins/limits", s.putPluginLimits)
	priv("GET /api/v1/plugins/{id}", s.getPlugin)
	priv("DELETE /api/v1/plugins/{id}", s.removePlugin)
	priv("POST /api/v1/plugins/{id}/enable", s.enablePlugin)
	priv("POST /api/v1/plugins/{id}/disable", s.disablePlugin)
	priv("POST /api/v1/plugins/{id}/restart", s.restartPlugin)
	priv("PUT /api/v1/plugins/{id}/settings", s.putPluginSettings)
	priv("POST /api/v1/plugins/{id}/token", s.newPluginToken)
	priv("GET /api/v1/plugins/{id}/logs", s.pluginLogs)
	priv("GET /api/v1/plugins/{id}/panel-data", s.pluginPanelData)
	priv("POST /api/v1/plugins/{id}/panel-action", s.pluginPanelAction)
	s.mux.HandleFunc("GET /plugin-assets/{id}/{key}/{file...}", s.pluginAsset)
	s.storeRoutes(priv)
}

// pluginJSON adds the asset URLs to a plugin's info.
func (s *Server) pluginJSON(in plugins.Info) map[string]any {
	b, _ := json.Marshal(in)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	base := "/plugin-assets/" + in.ID + "/" + s.pluginKeys.get(in.ID) + "/"
	if in.HasLogo {
		out["logo_url"] = base + "logo"
	}
	if in.HasPanel {
		out["panel_url"] = base + "panel/"
	}
	return out
}

func (s *Server) pluginManager(w http.ResponseWriter) *plugins.Manager {
	if s.opt.Plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugins are turned off (plugins.enabled in the config file)")
	}
	return s.opt.Plugins
}

func pluginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, plugins.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, plugins.ErrPinned), errors.Is(err, plugins.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	m := s.opt.Plugins
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "plugins": []any{}})
		return
	}
	list := []map[string]any{}
	for _, in := range m.List() {
		list = append(list, s.pluginJSON(in))
	}
	perms := map[string]string{}
	for k, v := range plugins.Permissions {
		perms[k] = v
	}
	s.cfgMu.Lock()
	pc := s.cfg.Plugins
	s.cfgMu.Unlock()
	// The choices for "identities" settings.
	identities := []map[string]any{}
	for _, rc := range s.radios {
		for _, id := range rc.host.Identities() {
			u := id.UserCopy()
			identities = append(identities, map[string]any{"node_id": id.NodeID(), "long_name": u.GetLongName(), "short_name": u.GetShortName(),
				"radio_id": rc.id, "radio_name": rc.name, "is_relay": id.IsRelay})
		}
	}
	// The update count comes off the cached index, so the Plugins tab can badge Updates without
	// waiting on the store.
	store := map[string]any{"enabled": m.Store() != nil, "updates": len(m.StoreUpdates())}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "error": m.StartError(), "plugins": list, "permissions": perms, "identities": identities,
		"attach_address": m.Listening(), "allow_url_install": pc.AllowURLInstall, "folder": m.InboxDir(),
		"messages_per_hour": pc.MessagesPerHour, "traceroutes_per_hour": pc.TraceroutesPerHour, "store": store})
}

// putPluginLimits is PUT /plugins/limits {"messages_per_hour", "traceroutes_per_hour"}: every
// plugin's send limits, applied at once and saved to the config file.
func (s *Server) putPluginLimits(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	var req plugins.Limits
	if !readJSON(w, r, &req) {
		return
	}
	if err := m.SetLimits(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfgMu.Lock()
	s.cfg.Plugins.MessagesPerHour, s.cfg.Plugins.TraceroutesPerHour = req.MessagesPerHour, req.TraceroutesPerHour
	if s.booted != nil { // applied live: not a reason to restart
		s.booted.Plugins.MessagesPerHour, s.booted.Plugins.TraceroutesPerHour = req.MessagesPerHour, req.TraceroutesPerHour
	}
	s.cfgMu.Unlock()
	if err := s.saveIfPath(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) getPlugin(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	in, err := m.Get(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pluginJSON(in))
}

// installPlugin is POST /plugins: a multipart upload (field "bundle") or JSON {"url"}.
func (s *Server) installPlugin(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	var f *os.File
	var ok bool
	source := "upload"
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "application/json" {
		f, ok = s.downloadBundle(w, r, m)
		source = "url"
	} else {
		f, ok = uploadedBundle(w, r, m)
	}
	if !ok {
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	man, err := m.Install(f, st.Size(), source)
	if err != nil {
		pluginError(w, err)
		return
	}
	in, err := m.Get(man.ID)
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.pluginJSON(in))
}

// downloadBundle fetches the bundle at the URL in a JSON body into the inbox, if URL installs are
// allowed. It writes the error itself and reports false on failure.
func (s *Server) downloadBundle(w http.ResponseWriter, r *http.Request, m *plugins.Manager) (*os.File, bool) {
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return nil, false
	}
	s.cfgMu.Lock()
	allowed := s.cfg.Plugins.AllowURLInstall
	s.cfgMu.Unlock()
	if !allowed {
		writeError(w, http.StatusForbidden, "installing from a URL is turned off (plugins.allow_url_install)")
		return nil, false
	}
	f, err := plugins.Download(r.Context(), strings.TrimSpace(req.URL), m.InboxDir()+"/.tmp")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return f, true
}

// uploadedBundle copies the multipart "bundle" field into a file in the inbox. It writes the error
// itself and reports false on failure.
func uploadedBundle(w http.ResponseWriter, r *http.Request, m *plugins.Manager) (*os.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, plugins.MaxBundleBytes+1<<20)
	file, _, err := r.FormFile("bundle")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "the bundle is larger than 100 MB")
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "send the plugin bundle as a multipart form field named bundle (up to 100 MB)")
		return nil, false
	}
	defer file.Close()
	if err := os.MkdirAll(m.InboxDir()+"/.tmp", 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	f, err := os.CreateTemp(m.InboxDir()+"/.tmp", "upload-*.zip")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		os.Remove(f.Name())
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return f, true
}

func (s *Server) attachPlugin(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	var req struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	tok, err := m.Attach(req.ID, req.Name, req.Permissions)
	if err != nil {
		pluginError(w, err)
		return
	}
	in, _ := m.Get(req.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"plugin": s.pluginJSON(in), "token": tok, "address": m.Listening()})
}

func (s *Server) removePlugin(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	if err := m.Remove(r.PathValue("id"), r.URL.Query().Get("keep_data") == "1"); err != nil {
		pluginError(w, err)
		return
	}
	s.pluginKeys.forget(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enablePlugin(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	var req struct {
		Permissions []string `json:"permissions"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.pluginDone(w, r, m.Enable(r.PathValue("id"), req.Permissions))
}

func (s *Server) disablePlugin(w http.ResponseWriter, r *http.Request) {
	if m := s.pluginManager(w); m != nil {
		s.pluginDone(w, r, m.Disable(r.PathValue("id")))
	}
}

func (s *Server) restartPlugin(w http.ResponseWriter, r *http.Request) {
	if m := s.pluginManager(w); m != nil {
		s.pluginDone(w, r, m.Restart(r.PathValue("id")))
	}
}

func (s *Server) putPluginSettings(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	var values map[string]any
	if !readJSON(w, r, &values) {
		return
	}
	s.pluginDone(w, r, m.SetSettings(r.PathValue("id"), values))
}

// pluginDone answers a change with the plugin's new info.
func (s *Server) pluginDone(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		pluginError(w, err)
		return
	}
	in, err := s.opt.Plugins.Get(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pluginJSON(in))
}

func (s *Server) newPluginToken(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	tok, err := m.NewToken(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "address": m.Listening()})
}

func (s *Server) pluginLogs(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	lines, err := m.Logs(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lines)
}

func (s *Server) pluginPanelData(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	data, err := m.PanelData(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	if data == "" {
		data = "null"
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, data)
}

func (s *Server) pluginPanelAction(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	var req struct {
		Name    string          `json:"name"`
		Payload json.RawMessage `json:"payload"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "an action needs a name")
		return
	}
	payload := string(req.Payload)
	if payload == "" {
		payload = "null"
	}
	if err := m.PanelAction(r.PathValue("id"), req.Name, payload); err != nil {
		if errors.Is(err, plugins.ErrConflict) {
			writeError(w, http.StatusConflict, "the plugin isn't connected")
			return
		}
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// pluginAsset serves a plugin's logo and panel files. Everything is sandboxed: a panel runs
// scripts in an opaque origin with no access to the GUI, its token or the API.
func (s *Server) pluginAsset(w http.ResponseWriter, r *http.Request) {
	m := s.opt.Plugins
	id, key, file := r.PathValue("id"), r.PathValue("key"), r.PathValue("file")
	if m == nil || !s.pluginKeys.valid(id, key) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups; default-src 'self' data: blob:; "+
		"script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'none'; frame-ancestors 'self'")
	w.Header().Set(cacheControl, "no-cache")
	dir, name, ok := pluginAssetFile(m, id, file)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// An os.Root keeps the file inside the plugin's folder whatever links it holds, and works
	// when the plugins folder itself sits behind a link.
	root, err := os.OpenRoot(dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	if st, err := root.Stat(name); err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, root.FS(), name)
}

// pluginAssetFile finds a plugin's logo or a panel file: the folder to serve from and the name
// inside it. It reports false if there's no such file.
func pluginAssetFile(m *plugins.Manager, id, file string) (dir, name string, ok bool) {
	if file == "logo" {
		p, err := m.LogoPath(id)
		if err != nil {
			return "", "", false
		}
		return filepath.Dir(p), filepath.Base(p), true
	}
	rest, ok := strings.CutPrefix(file, "panel/")
	if !ok && file != "panel" {
		return "", "", false
	}
	root, index, err := m.PanelRoot(id)
	if err != nil {
		return "", "", false
	}
	if rest == "" {
		rest = index
	}
	return root, strings.TrimPrefix(path.Clean("/"+rest), "/"), true
}
