// Package web serves the REST/SSE API in docs/api.md and the embedded GUI.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/links/mqtt"
	"github.com/A13xB0/RepeaterTastic/internal/links/udp"
	"github.com/A13xB0/RepeaterTastic/internal/logbuf"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/phoneapi"
	"github.com/A13xB0/RepeaterTastic/internal/radio"
	"github.com/A13xB0/RepeaterTastic/internal/site"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

type Options struct {
	Config  *config.Config
	Host    *mesh.Host // the main radio
	API     *phoneapi.Manager
	Logs    *logbuf.Buffer
	UDP     *udp.Link
	MQTT    []*mqtt.Link
	Radios  []Radio    // additional radios on the same site
	Site    *site.Site // nil with a single radio and no site budget
	Version string
	Log     *slog.Logger
	// MapAPIKey fills {api_key} in the map tile URL (empty drops the api_key parameter).
	MapAPIKey string
}

// Radio is an additional radio served by the same web GUI.
type Radio struct {
	ID, Name string
	Config   *config.Config // that radio's view of the configuration
	Host     *mesh.Host
	API      *phoneapi.Manager
	UDP      *udp.Link
	MQTT     []*mqtt.Link
}

// radioCtx is everything the web server keeps per radio.
type radioCtx struct {
	id, name string
	cfg      *config.Config // nil for the main radio: it follows Server.cfg, which the config API replaces
	host     *mesh.Host
	api      *phoneapi.Manager
	udp      *udp.Link
	mqtt     []*mqtt.Link

	// Modem stats cost serial round trips; share one poll between all viewers.
	statsMu   sync.Mutex
	statsAt   time.Time
	lastStats radio.Stats

	rf rfHistory
}

type Server struct {
	opt  Options
	cfg  *config.Config
	host *mesh.Host
	auth *Auth
	log  *slog.Logger
	mux  *http.ServeMux

	cfgMu      sync.Mutex
	loginFails sync.Map // ip → *loginState

	radios []*radioCtx // main first
	traces traceWait
}

func (rc *radioCtx) stats(ctx context.Context) radio.Stats {
	rc.statsMu.Lock()
	defer rc.statsMu.Unlock()
	if time.Since(rc.statsAt) > 5*time.Second {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		rc.lastStats = rc.host.Radio().Stats(cctx)
		cancel()
		rc.statsAt = time.Now()
	}
	return rc.lastStats
}

// radioStats is the main radio's modem stats (kept for callers without a request).
func (s *Server) radioStats(ctx context.Context) radio.Stats { return s.radios[0].stats(ctx) }

// radioFor picks the radio a request is about: the radio holding the identity in the
// path, else ?radio=<id>, else the main radio. Every endpoint therefore keeps working
// unchanged on a single-radio host.
func (s *Server) radioFor(r *http.Request) *radioCtx {
	if r != nil {
		if raw := r.PathValue("id"); raw != "" {
			if num, err := wire.ParseNodeID(raw); err == nil {
				for _, rc := range s.radios {
					if rc.host.Identity(num) != nil {
						return rc
					}
				}
			}
		}
		if want := r.URL.Query().Get("radio"); want != "" {
			for _, rc := range s.radios {
				if rc.id == want {
					return rc
				}
			}
		}
	}
	return s.radios[0]
}

func (s *Server) hostFor(r *http.Request) *mesh.Host { return s.radioFor(r).host }

// radioOf finds the radio an identity lives on.
func (s *Server) radioOf(id *mesh.Identity) *radioCtx {
	for _, rc := range s.radios {
		if rc.host.Identity(id.NodeNum) == id {
			return rc
		}
	}
	return s.radios[0]
}

// radioConfig is a radio's current configuration view.
func (s *Server) radioConfig(rc *radioCtx) *config.Config {
	if rc.cfg != nil {
		return rc.cfg
	}
	return s.cfg
}

type loginState struct {
	mu    sync.Mutex
	fails int
	until time.Time
}

func New(o Options) (*Server, error) {
	a, err := loadAuth(o.Config.StateDir)
	if err != nil {
		return nil, err
	}
	s := &Server{opt: o, cfg: o.Config, host: o.Host, auth: a, log: o.Log.With("component", "web"), mux: http.NewServeMux()}
	s.radios = append(s.radios, &radioCtx{id: config.MainRadioID, name: "Main", host: o.Host, api: o.API, udp: o.UDP, mqtt: o.MQTT})
	for _, x := range o.Radios {
		s.radios = append(s.radios, &radioCtx{id: x.ID, name: x.Name, cfg: x.Config, host: x.Host, api: x.API, udp: x.UDP, mqtt: x.MQTT})
	}
	s.traces.pending = map[string]time.Time{}
	a.ttl = func() time.Duration {
		s.cfgMu.Lock()
		defer s.cfgMu.Unlock()
		return s.cfg.Web.SessionTTL
	}
	s.routes()
	return s, nil
}

// Handler exposes the router (tests).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) Run(ctx context.Context) error {
	for _, rc := range s.radios {
		go s.sampleRF(ctx, rc)
		go s.watchTraceroutes(ctx, rc)
	}
	addr := net.JoinHostPort(s.cfg.Web.Bind, strconv.Itoa(s.cfg.Web.Port))
	srv := &http.Server{Addr: addr, Handler: securityHeaders(s.mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	s.log.Info("web GUI listening", "addr", addr)
	if s.auth.SetupNeeded() {
		s.log.Warn("no admin password yet: open the web GUI to finish setup", "addr", addr)
	}
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		// Not same-origin: map tile servers (OpenStreetMap's policy) refuse requests without a
		// Referer. Cross-origin requests still only see the origin, never paths or queries.
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	pub := func(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }
	priv := func(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, s.requireAuth(h)) }

	pub("GET /api/v1/setup", s.getSetup)
	pub("POST /api/v1/setup", s.postSetup)
	pub("POST /api/v1/auth/login", s.login)
	setup := func(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, s.setupOrAuth(h)) }
	setup("GET /api/v1/serial-ports", s.serialPorts)
	setup("GET /api/v1/regions", s.regions)
	setup("POST /api/v1/phy/preview", s.phyPreview)
	setup("POST /api/v1/setup/probe", s.probe)
	priv("PUT /api/v1/auth/password", s.changePassword)
	priv("POST /api/v1/auth/logout-all", s.logoutAll)

	priv("GET /api/v1/status", s.getStatus)
	priv("GET /api/v1/radios", s.listRadios)
	priv("POST /api/v1/restart", s.restartDaemon)
	priv("PUT /api/v1/relay", s.putRelay)

	priv("GET /api/v1/identities", s.listIdentities)
	priv("POST /api/v1/identities", s.createIdentity)
	priv("POST /api/v1/identities/preview-key", s.previewKey)
	priv("PATCH /api/v1/identities/{id}", s.patchIdentity)
	priv("DELETE /api/v1/identities/{id}", s.deleteIdentity)
	priv("GET /api/v1/identities/{id}/key", s.getKey)
	priv("PUT /api/v1/identities/{id}/channels/{index}", s.putChannel)
	priv("GET /api/v1/identities/{id}/channels/url", s.getChannelURL)
	priv("POST /api/v1/identities/{id}/channels/url", s.postChannelURL)
	priv("GET /api/v1/identities/{id}/conversations", s.conversations)
	priv("GET /api/v1/identities/{id}/messages", s.listMessages)
	priv("POST /api/v1/identities/{id}/messages", s.sendMessage)

	priv("GET /api/v1/nodes", s.listNodes)
	priv("POST /api/v1/nodes/{id}/traceroute", s.traceroute)
	priv("POST /api/v1/nodes/{id}/request-nodeinfo", s.requestNodeInfo)
	priv("DELETE /api/v1/nodes/{id}", s.deleteNode)

	priv("GET /api/v1/packets", s.listPackets)
	priv("GET /api/v1/events", s.events)
	priv("GET /api/v1/stats/airtime", s.statsAirtime)
	priv("GET /api/v1/stats/ports", s.statsPorts)

	priv("GET /api/v1/config", s.getConfig)
	priv("PUT /api/v1/config", s.putConfig)
	priv("GET /api/v1/tokens", s.listTokens)
	priv("POST /api/v1/tokens", s.createToken)
	priv("DELETE /api/v1/tokens/{id}", s.deleteToken)
	priv("GET /api/v1/backup", s.backup)
	priv("POST /api/v1/restore", s.restore)
	priv("GET /api/v1/logs", s.logs)
	priv("GET /api/v1/links", s.links)
	priv("PATCH /api/v1/links/{name}", s.patchLink)
	priv("POST /api/v1/identities/{id}/api/restart", s.restartAPI)
	priv("POST /api/v1/identities/{id}/conversations/{key}/read", s.markRead)
	priv("GET /api/v1/stats/rf", s.statsRF)
	priv("GET /api/v1/stats/identities", s.statsIdentities)

	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	s.mux.Handle("/", s.spa())
}

func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if !s.auth.Valid(tok) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r)
	}
}

// spa serves the embedded GUI with history-mode fallback.
func (s *Server) spa() http.Handler {
	dist, err := fs.Sub(Dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(dist, p); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			p = "index.html"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------------------------ helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
