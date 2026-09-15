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
	"github.com/A13xB0/RepeaterTastic/internal/links/udp"
	"github.com/A13xB0/RepeaterTastic/internal/logbuf"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/phoneapi"
	"github.com/A13xB0/RepeaterTastic/internal/radio"
)

type Options struct {
	Config  *config.Config
	Host    *mesh.Host
	API     *phoneapi.Manager
	Logs    *logbuf.Buffer
	UDP     *udp.Link
	Version string
	Log     *slog.Logger
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

	// Modem stats cost serial round trips; share one poll between all viewers.
	statsMu   sync.Mutex
	statsAt   time.Time
	lastStats radio.Stats
}

func (s *Server) radioStats(ctx context.Context) radio.Stats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if time.Since(s.statsAt) > 5*time.Second {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		s.lastStats = s.host.Radio().Stats(cctx)
		cancel()
		s.statsAt = time.Now()
	}
	return s.lastStats
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
	s.routes()
	return s, nil
}

// Handler exposes the router (tests).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) Run(ctx context.Context) error {
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
		w.Header().Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	pub := func(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }
	priv := func(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, s.requireAuth(h)) }

	pub("GET /api/v1/setup", s.getSetup)
	pub("POST /api/v1/setup", s.postSetup)
	pub("POST /api/v1/auth/login", s.login)

	priv("GET /api/v1/status", s.getStatus)
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
	priv("GET /api/v1/serial-ports", s.serialPorts)
	priv("GET /api/v1/regions", s.regions)
	priv("POST /api/v1/phy/preview", s.phyPreview)
	priv("GET /api/v1/tokens", s.listTokens)
	priv("POST /api/v1/tokens", s.createToken)
	priv("DELETE /api/v1/tokens/{id}", s.deleteToken)
	priv("GET /api/v1/backup", s.backup)
	priv("POST /api/v1/restore", s.restore)
	priv("GET /api/v1/logs", s.logs)
	priv("GET /api/v1/links", s.links)

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
