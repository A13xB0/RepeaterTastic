// Sign-in, password and API token endpoints.

package web

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !s.auth.CheckPassword(req.Current) {
		writeError(w, http.StatusBadRequest, "the current password is wrong")
		return
	}
	if err := s.auth.SetPassword(req.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A new password signs out every session; this browser gets a fresh one to stay signed in.
	tok, exp := s.auth.IssueJWT()
	s.log.Info("admin password changed; other sessions signed out")
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires": exp.UnixMilli()})
}

// logoutAll is POST /api/v1/auth/logout-all: sign out every browser session, this one included.
func (s *Server) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.RevokeSessions(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("all web sessions signed out")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v, _ := s.loginFails.LoadOrStore(clientIP(r), &loginState{})
	ls := v.(*loginState)
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if time.Now().Before(ls.until) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again in a minute")
		return
	}
	if !s.auth.CheckPassword(req.Password) {
		ls.fails++
		if ls.fails >= 5 {
			ls.until, ls.fails = time.Now().Add(time.Minute), 0
		}
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	ls.fails = 0
	tok, exp := s.auth.IssueJWT()
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires": exp.UnixMilli()})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, t := range s.auth.Tokens() {
		var last any
		if t.LastUsed > 0 {
			last = t.LastUsed
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "created_at": t.Created, "last_used": last})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "give the token a name, e.g. \"Home Assistant\"")
		return
	}
	t, secret, err := s.auth.CreateToken(req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": t.ID, "name": t.Name, "token": secret, "created_at": t.Created, "last_used": nil})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if !s.auth.DeleteToken(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "no such token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
