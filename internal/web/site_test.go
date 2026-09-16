package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

// pendingRadio adds a radio to the config that isn't running.
func pendingRadio(o *Options) {
	o.Config.Radios = []config.RadioInstance{{ID: "ls", Name: "LongSlow", Radio: config.Radio{Driver: "none"},
		Mesh: config.Mesh{Region: "EU_868", Preset: "LONG_SLOW", HopLimit: 3}, Relay: config.Relay{Role: "client_mute"}}}
}

func TestPatchRadioChecks(t *testing.T) {
	env := newTestEnv(t, pendingRadio)
	tok := env.signIn(t)
	if code, obj, _ := call(t, env.srv, "PATCH", "/api/v1/radios/main", tok, map[string]any{}); code != 200 || obj["id"] != "main" || obj["name"] != nil {
		t.Fatalf("no change: %d %v", code, obj)
	}
	if code, _, _ := call(t, env.srv, "PATCH", "/api/v1/radios/main", tok, map[string]any{"name": strings.Repeat("x", 41)}); code != 400 {
		t.Fatalf("long name: %d", code)
	}
	if code, _, _ := call(t, env.srv, "PATCH", "/api/v1/radios/nope", tok, map[string]any{"name": "x"}); code != 404 {
		t.Fatalf("unknown radio: %d", code)
	}
	if code, _ := env.do(t, "PATCH", "/api/v1/radios/main", tok, "application/json", "nope"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
	// A blank name shows as Main, or the id for another radio.
	if code, obj, _ := call(t, env.srv, "PATCH", "/api/v1/radios/main", tok, map[string]any{"name": " "}); code != 200 || obj["name"] != "Main" {
		t.Fatalf("blank main name: %d %v", code, obj)
	}
	if code, obj, _ := call(t, env.srv, "PATCH", "/api/v1/radios/ls", tok, map[string]any{"name": ""}); code != 200 || obj["name"] != "ls" {
		t.Fatalf("blank ls name: %d %v", code, obj)
	}
	if env.cfg.Radios[0].Name != "" || env.cfg.Site.MainRadioName != "" {
		t.Fatalf("saved names = %q %q", env.cfg.Radios[0].Name, env.cfg.Site.MainRadioName)
	}
}

func TestRadioChangesThatCantBeSaved(t *testing.T) {
	env := newTestEnv(t, pendingRadio)
	tok := env.signIn(t)
	blockConfigFile(t, env.cfg.Path())
	cases := []struct {
		method, path string
		body         map[string]any
	}{
		{"PATCH", "/api/v1/radios/main", map[string]any{"name": "Mast"}},
		{"PUT", "/api/v1/radios/ls", map[string]any{"name": "LS", "driver": "none"}},
		{"POST", "/api/v1/radios", map[string]any{"id": "sf", "driver": "none", "region": "EU_868", "preset": "SHORT_FAST"}},
		{"DELETE", "/api/v1/radios/ls", nil},
		{"PUT", "/api/v1/site", map[string]any{"duty_cycle_percent": 5}},
	}
	for _, c := range cases {
		if code, obj, _ := call(t, env.srv, c.method, c.path, tok, c.body); code != http.StatusInternalServerError {
			t.Errorf("%s %s: %d %v", c.method, c.path, code, obj)
		}
	}
}

func TestSiteSettingsOnOneRadio(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	code, site, _ := call(t, env.srv, "GET", "/api/v1/site", tok, nil)
	if code != 200 || site["coordinator"] != false || site["duty_cycle_percent"] != float64(0) {
		t.Fatalf("site: %d %v", code, site)
	}
	for _, pct := range []float64{-1, 101} {
		if code, _, _ := call(t, env.srv, "PUT", "/api/v1/site", tok, map[string]any{"duty_cycle_percent": pct}); code != 400 {
			t.Errorf("site %v%%: %d", pct, code)
		}
	}
	if code, _ := env.do(t, "PUT", "/api/v1/site", tok, "application/json", "nope"); code != 400 {
		t.Errorf("bad body: %d", code)
	}
	// A single radio only gets a site coordinator when it starts.
	code, site, _ = call(t, env.srv, "PUT", "/api/v1/site", tok, map[string]any{"duty_cycle_percent": 5})
	if code != 200 || site["restart_required"] != true || site["duty_cycle_percent"] != float64(5) || site["running_duty_cycle_percent"] != float64(0) {
		t.Fatalf("site cap: %d %v", code, site)
	}
	if _, st, _ := call(t, env.srv, "GET", "/api/v1/status", tok, nil); !strings.Contains(jsonOf(st["restart_reasons"]), "site airtime cap") {
		t.Fatalf("restart reasons = %v", st["restart_reasons"])
	}
}

func TestRestartDaemonCallsRestart(t *testing.T) {
	restarted := make(chan struct{})
	env := newTestEnv(t, func(o *Options) { o.Restart = func() { close(restarted) } })
	tok := env.signIn(t)
	code, obj, _ := call(t, env.srv, "POST", "/api/v1/restart", tok, nil)
	if code != http.StatusAccepted || obj["restarting"] != true {
		t.Fatalf("restart: %d %v", code, obj)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Restart wasn't called")
	}
}

func TestRadioIDOK(t *testing.T) {
	for id, want := range map[string]bool{"mf": true, "radio-2": true, "": false, "Main": false, "a/b": false, strings.Repeat("a", 25): false} {
		if radioIDOK(id) != want {
			t.Errorf("radioIDOK(%q) = %v", id, !want)
		}
	}
}
