package web

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPITokensListAndDelete(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	if code, _, list := call(t, env.srv, "GET", "/api/v1/tokens", tok, nil); code != 200 || len(list) != 0 {
		t.Fatalf("empty token list: %d %v", code, list)
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/tokens", tok, map[string]any{"name": "  "}); code != http.StatusBadRequest {
		t.Fatalf("blank token name: %d", code)
	}
	_, created, _ := call(t, env.srv, "POST", "/api/v1/tokens", tok, map[string]any{"name": "Home Assistant"})
	secret := created["token"].(string)
	// Using it records when it was last used.
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/status", secret, nil); code != 200 {
		t.Fatalf("token rejected: %d", code)
	}
	_, _, list := call(t, env.srv, "GET", "/api/v1/tokens", tok, nil)
	if len(list) != 1 {
		t.Fatalf("tokens = %v", list)
	}
	got := list[0].(map[string]any)
	if got["name"] != "Home Assistant" || got["id"] != created["id"] || got["last_used"] == nil || got["token"] != nil {
		t.Fatalf("listed token = %v", got)
	}
	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/tokens/"+got["id"].(string), tok, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/tokens/"+got["id"].(string), tok, nil); code != http.StatusNotFound {
		t.Fatalf("delete again: %d", code)
	}
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/status", secret, nil); code != http.StatusUnauthorized {
		t.Fatalf("deleted token still works: %d", code)
	}
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/status", "rpt_unknown", nil); code != http.StatusUnauthorized {
		t.Fatalf("unknown API token accepted: %d", code)
	}
}

func TestChangePasswordRefusals(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	if code, obj, _ := call(t, env.srv, "PUT", "/api/v1/auth/password", tok, map[string]any{"current": "wrong", "new": "battery staple"}); code != http.StatusBadRequest {
		t.Fatalf("wrong current password: %d %v", code, obj)
	}
	code, obj, _ := call(t, env.srv, "PUT", "/api/v1/auth/password", tok, map[string]any{"current": "correct horse", "new": "short"})
	if code != http.StatusBadRequest || obj["error"] != "password must be at least 8 characters" {
		t.Fatalf("short new password: %d %v", code, obj)
	}
	// The old password still works.
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"}); code != 200 {
		t.Fatalf("login after refused change: %d", code)
	}
}

func TestLoginLocksOutAfterFiveFailures(t *testing.T) {
	env := newTestEnv(t, nil)
	env.signIn(t)
	for i := 0; i < 5; i++ {
		if code, _, _ := call(t, env.srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "nope"}); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, code)
		}
	}
	// Even the right password waits now.
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"}); code != http.StatusTooManyRequests {
		t.Fatalf("after five failures: %d", code)
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/auth/login", "", "not an object"); code != http.StatusBadRequest {
		t.Fatalf("bad login body: %d", code)
	}
}

func TestAuthSaveFailuresAreReported(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	_, created, _ := call(t, env.srv, "POST", "/api/v1/tokens", tok, map[string]any{"name": "Keep"})
	secret := created["token"].(string)
	// From now on auth.json can't be written.
	env.s.auth.mu.Lock()
	env.s.auth.path = filepath.Join(t.TempDir(), "missing", "auth.json")
	env.s.auth.mu.Unlock()
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/tokens", secret, map[string]any{"name": "HA"}); code != http.StatusInternalServerError {
		t.Fatalf("token create with an unwritable file: %d", code)
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/auth/logout-all", secret, nil); code != http.StatusInternalServerError {
		t.Fatalf("logout-all with an unwritable file: %d", code)
	}
}

func TestValidJWTRefusesForgeries(t *testing.T) {
	a, err := loadAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good, _ := a.IssueJWT()
	parts := strings.Split(good, ".")
	signed := func(header, body string) string {
		p := header + "." + body
		return p + "." + b64.EncodeToString(a.sign(p))
	}
	expired := b64.EncodeToString([]byte(`{"sub":"admin","exp":1}`))
	for name, tok := range map[string]string{
		"two parts":     parts[0] + "." + parts[1],
		"bad signature": parts[0] + "." + parts[1] + ".!!",
		"other body":    parts[0] + "." + expired + "." + parts[2],
		"bad body":      signed(parts[0], "!!"),
		"expired":       signed(parts[0], expired),
	} {
		if a.Valid(tok) {
			t.Errorf("%s accepted", name)
		}
	}
	if !a.Valid(good) || a.Valid("") {
		t.Fatal("Valid is wrong for a good or empty token")
	}
}

func TestSetupFailsWhenThePasswordCantBeSaved(t *testing.T) {
	env := newTestEnv(t, nil)
	env.s.auth.mu.Lock()
	env.s.auth.path = filepath.Join(t.TempDir(), "missing", "auth.json")
	env.s.auth.mu.Unlock()
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"}); code != http.StatusBadRequest {
		t.Fatalf("setup with an unwritable auth file: %d", code)
	}
}
