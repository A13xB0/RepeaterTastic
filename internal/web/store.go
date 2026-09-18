package web

import (
	"bytes"
	"errors"
	"net/http"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/plugins"
)

// The store endpoints back the Plugins tab's Browse and Updates views. The daemon does the
// fetching rather than the browser, so the store works on a node whose GUI is reached over the
// mesh or a VPN, and so a downloaded bundle is checked against the store's checksum here, where
// the install happens.

// storeKeyID is the asset-key holder for store logos. It isn't a plugin id (no plugin can be
// called this), so it can't collide with an installed plugin's key.
const storeKeyID = "\x00store"

func (s *Server) storeRoutes(priv func(string, http.HandlerFunc)) {
	priv("GET /api/v1/plugins/store", s.getStore)
	priv("POST /api/v1/plugins/store/{id}/install", s.installFromStore)
	s.mux.HandleFunc("GET /plugin-store-logo/{key}/{id}", s.storeLogo)
}

// storeEntry is a store plugin plus the URL the browser can load its logo from. The GUI can't send
// a bearer token on an <img>, so logos go through a capability URL like installed plugins' assets.
type storeEntry struct {
	plugins.StoreEntry
	LogoURL string `json:"logo_url,omitempty"`
}

// getStore is GET /plugins/store: what the store offers, against what's installed here.
// ?refresh=1 asks the store again instead of using the cached copy.
func (s *Server) getStore(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	list, fetched, err := m.StoreList(r.Context(), r.URL.Query().Get("refresh") == "1")
	if errors.Is(err, plugins.ErrNoStore) {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "plugins": []storeEntry{}})
		return
	}
	entries := make([]storeEntry, 0, len(list))
	for _, e := range list {
		entry := storeEntry{StoreEntry: e}
		if e.Logo != "" {
			entry.LogoURL = s.storeLogoURL(e.ID)
		}
		entries = append(entries, entry)
	}
	out := map[string]any{"enabled": true, "url": m.Store().URL(), "plugins": entries}
	if !fetched.IsZero() {
		out["fetched_at"] = fetched.Unix()
	}
	// A failed fetch still answers 200 with whatever the cache holds: the GUI lists the old copy
	// and says why it's old, which beats an empty tab when the node is off the internet.
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) storeLogoURL(id string) string {
	return "/plugin-store-logo/" + s.pluginKeys.get(storeKeyID) + "/" + id
}

// storeLogo serves a store logo through the daemon, so the browser never talks to the store
// itself and a node on a private network still shows the pictures.
func (s *Server) storeLogo(w http.ResponseWriter, r *http.Request) {
	m := s.opt.Plugins
	if m == nil || m.Store() == nil || !s.pluginKeys.valid(storeKeyID, r.PathValue("key")) {
		http.NotFound(w, r)
		return
	}
	b, contentType, err := m.Store().Logo(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// An SVG fetched from a store is a document on this origin, so it gets the same sandbox an
	// installed plugin's assets get: inside an <img> it is inert either way, but opened in a tab
	// it would otherwise run with the operator's session.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set(cacheControl, "no-cache")
	http.ServeContent(w, r, "logo", time.Time{}, bytes.NewReader(b))
}

// installFromStore is POST /plugins/store/{id}/install: download the plugin the store lists under this id,
// check it against the store's checksum, and install it. It arrives switched off, like any other
// install, so its permissions are granted deliberately.
func (s *Server) installFromStore(w http.ResponseWriter, r *http.Request) {
	m := s.pluginManager(w)
	if m == nil {
		return
	}
	man, err := m.InstallFromStore(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, plugins.ErrNoStore) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
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
