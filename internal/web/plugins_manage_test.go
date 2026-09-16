package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/plugins"
)

// newPluginEnv is a signed-in server with plugins on and a config file.
func newPluginEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	env := newTestEnv(t, func(o *Options) {
		pm, err := plugins.New(plugins.Options{Config: o.Config.Plugins, Dir: filepath.Join(o.Config.StateDir, "plugins"),
			Radios: []plugins.Radio{{ID: "main", Host: o.Host}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		o.Plugins = pm
	})
	return env, env.signIn(t)
}

func TestPluginsTurnedOff(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	if code, obj, _ := call(t, env.srv, "GET", "/api/v1/plugins", tok, nil); code != 200 || obj["enabled"] != false {
		t.Fatalf("list: %d %v", code, obj)
	}
	cases := []struct{ method, path string }{
		{"GET", "/api/v1/plugins/x"}, {"POST", "/api/v1/plugins"}, {"POST", "/api/v1/plugins/attach"},
		{"PUT", "/api/v1/plugins/limits"}, {"DELETE", "/api/v1/plugins/x"}, {"POST", "/api/v1/plugins/x/enable"},
		{"POST", "/api/v1/plugins/x/disable"}, {"POST", "/api/v1/plugins/x/restart"}, {"PUT", "/api/v1/plugins/x/settings"},
		{"POST", "/api/v1/plugins/x/token"}, {"GET", "/api/v1/plugins/x/logs"}, {"GET", "/api/v1/plugins/x/panel-data"},
		{"POST", "/api/v1/plugins/x/panel-action"},
	}
	for _, c := range cases {
		if code, _, _ := call(t, env.srv, c.method, c.path, tok, map[string]any{}); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: %d", c.method, c.path, code)
		}
	}
	if code, _ := getAsset(t, env.srv.URL+"/plugin-assets/x/k/logo"); code != 404 {
		t.Errorf("asset with plugins off: %d", code)
	}
	if _, ok := env.s.pluginEventPayload(mesh.Event{Type: "plugin", Data: "x"}); ok {
		t.Error("plugin event with plugins off")
	}
}

func TestPluginListAndLimits(t *testing.T) {
	env, tok := newPluginEnv(t)
	code, obj, _ := call(t, env.srv, "GET", "/api/v1/plugins", tok, nil)
	ids, _ := obj["identities"].([]any)
	if code != 200 || obj["enabled"] != true || obj["messages_per_hour"] != float64(30) || len(ids) != 1 || obj["permissions"] == nil {
		t.Fatalf("list: %d %v", code, obj)
	}
	if ids[0].(map[string]any)["is_relay"] != true {
		t.Fatalf("identities = %v", ids)
	}
	code, obj, _ = call(t, env.srv, "PUT", "/api/v1/plugins/limits", tok, map[string]any{"messages_per_hour": 60, "traceroutes_per_hour": 6})
	if code != 200 || obj["messages_per_hour"] != float64(60) || env.cfg.Plugins.TraceroutesPerHour != 6 {
		t.Fatalf("limits: %d %v", code, obj)
	}
	// Applied live: no restart needed.
	if _, st, _ := call(t, env.srv, "GET", "/api/v1/status", tok, nil); strings.Contains(jsonOf(st["restart_reasons"]), "plugins") {
		t.Fatalf("restart reasons = %v", st["restart_reasons"])
	}
	if code, _, _ := call(t, env.srv, "PUT", "/api/v1/plugins/limits", tok, map[string]any{"messages_per_hour": 9999}); code != 400 {
		t.Fatalf("too many messages: %d", code)
	}
	if code, _ := env.do(t, "PUT", "/api/v1/plugins/limits", tok, "application/json", "nope"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
	blockConfigFile(t, env.cfg.Path())
	if code, _, _ := call(t, env.srv, "PUT", "/api/v1/plugins/limits", tok, map[string]any{"messages_per_hour": 1}); code != http.StatusInternalServerError {
		t.Fatalf("unsaveable limits: %d", code)
	}
}

func TestAttachedPlugin(t *testing.T) {
	env, tok := newPluginEnv(t)
	for _, body := range []map[string]any{{"id": "Bad Id"}, {"id": "ext", "permissions": []string{"everything"}}} {
		if code, _, _ := call(t, env.srv, "POST", "/api/v1/plugins/attach", tok, body); code != 400 {
			t.Errorf("attach %v: %d", body, code)
		}
	}
	code, obj, _ := call(t, env.srv, "POST", "/api/v1/plugins/attach", tok, map[string]any{"id": " ext ", "name": "External", "permissions": []string{"nodes.read"}})
	if code != http.StatusCreated || !strings.HasPrefix(obj["token"].(string), "rtp_") || objectAt(obj, "plugin")["id"] != "ext" {
		t.Fatalf("attach: %d %v", code, obj)
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/plugins/attach", tok, map[string]any{"id": "ext"}); code != http.StatusConflict {
		t.Fatalf("attach twice: %d", code)
	}
	code, fresh, _ := call(t, env.srv, "POST", "/api/v1/plugins/ext/token", tok, nil)
	if code != 200 || fresh["token"] == obj["token"] || fresh["token"] == "" {
		t.Fatalf("new token: %d %v", code, fresh)
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/plugins/nope/token", tok, nil); code != 404 {
		t.Fatalf("token for a missing plugin: %d", code)
	}
	if code, _ := env.do(t, "POST", "/api/v1/plugins/attach", tok, "application/json", "nope"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
	// Not connected, so its panel can't take actions.
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/plugins/ext/panel-action", tok, map[string]any{"name": "go"}); code != http.StatusConflict {
		t.Fatalf("panel action: %d", code)
	}
	p, ok := env.s.pluginEventPayload(mesh.Event{Type: "plugin", Data: "ext"})
	if !ok || p.(map[string]any)["id"] != "ext" {
		t.Fatalf("plugin event = %v %v", p, ok)
	}
	p, ok = env.s.pluginEventPayload(mesh.Event{Type: "plugin", Data: "gone"})
	if !ok || p.(map[string]any)["deleted"] != true {
		t.Fatalf("removed plugin event = %v %v", p, ok)
	}
}

func TestInstalledPluginControls(t *testing.T) {
	env, tok := newPluginEnv(t)
	uploadWidget(t, env.srv, tok)
	base := "/api/v1/plugins/widget"
	if code, obj, _ := call(t, env.srv, "POST", base+"/disable", tok, nil); code != 200 || obj["id"] != "widget" {
		t.Fatalf("disable: %d %v", code, obj)
	}
	if code, _, _ := call(t, env.srv, "POST", base+"/restart", tok, nil); code != 200 && code != http.StatusConflict {
		t.Fatalf("restart: %d", code)
	}
	if code, _, logs := call(t, env.srv, "GET", base+"/logs", tok, nil); code != 200 || !strings.Contains(jsonOf(logs), "disabled") {
		t.Fatalf("logs: %d %v", code, logs)
	}
	if code, body := env.do(t, "GET", base+"/panel-data", tok, "", ""); code != 200 || strings.TrimSpace(body) != "null" {
		t.Fatalf("panel data: %d %q", code, body)
	}
	checkPluginBadRequests(t, env, tok, base)
	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/plugins/nope", tok, nil); code != 404 {
		t.Fatalf("remove a missing plugin: %d", code)
	}
	if code, _, _ := call(t, env.srv, "DELETE", base+"?keep_data=1", tok, nil); code != http.StatusNoContent {
		t.Fatalf("remove keeping data: %d", code)
	}
}

// checkPluginBadRequests checks malformed requests to a plugin, and requests to a missing one, are refused.
func checkPluginBadRequests(t *testing.T, env *testEnv, tok, base string) {
	t.Helper()
	for _, name := range []string{"", strings.Repeat("n", 101)} {
		if code, _, _ := call(t, env.srv, "POST", base+"/panel-action", tok, map[string]any{"name": name}); code != 400 {
			t.Errorf("action %q: %d", name, code)
		}
	}
	for _, c := range [][2]string{{"POST", "/panel-action"}, {"POST", "/enable"}, {"PUT", "/settings"}} {
		if code, _ := env.do(t, c[0], base+c[1], tok, "application/json", "nope"); code != 400 {
			t.Errorf("bad body for %s: %d", c[1], code)
		}
	}
	for _, c := range [][2]string{{"GET", "/nope/logs"}, {"GET", "/nope/panel-data"}, {"POST", "/nope/disable"}, {"POST", "/nope/restart"}} {
		if code, _, _ := call(t, env.srv, c[0], "/api/v1/plugins"+c[1], tok, nil); code != 404 {
			t.Errorf("%s %s: %d", c[0], c[1], code)
		}
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/plugins/nope/panel-action", tok, map[string]any{"name": "go"}); code != 404 {
		t.Errorf("action on a missing plugin: %d", code)
	}
}

func TestInstallPluginFromURLAndBadUploads(t *testing.T) {
	env, tok := newPluginEnv(t)
	bundles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/widget.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(widgetBundle())
	}))
	t.Cleanup(bundles.Close)
	code, body := env.do(t, "POST", "/api/v1/plugins", tok, "application/json", jsonOf(map[string]any{"url": " " + bundles.URL + "/widget.zip "}))
	if code != http.StatusCreated || !strings.Contains(body, `"id":"widget"`) {
		t.Fatalf("install from URL: %d %s", code, body)
	}
	for _, u := range []string{bundles.URL + "/missing.zip", "ftp://example/x.zip"} {
		if code, _ := env.do(t, "POST", "/api/v1/plugins", tok, "application/json", jsonOf(map[string]any{"url": u})); code != 400 {
			t.Errorf("install from %s: %d", u, code)
		}
	}
	if code, _ := env.do(t, "POST", "/api/v1/plugins", tok, "application/json", "nope"); code != 400 {
		t.Errorf("bad JSON: %d", code)
	}
	if code, _ := env.do(t, "POST", "/api/v1/plugins", tok, "text/plain", "hello"); code != 400 {
		t.Errorf("not a form: %d", code)
	}
	if code := uploadBundle(t, env, tok, []byte("not a zip")); code != 400 {
		t.Errorf("not a zip: %d", code)
	}
	env.s.cfgMu.Lock()
	env.s.cfg.Plugins.AllowURLInstall = false
	env.s.cfgMu.Unlock()
	if code, _ := env.do(t, "POST", "/api/v1/plugins", tok, "application/json; charset=utf-8", jsonOf(map[string]any{"url": bundles.URL + "/widget.zip"})); code != http.StatusForbidden {
		t.Errorf("URL install turned off: %d", code)
	}
}

// uploadBundle posts data as the bundle form field and returns the status.
func uploadBundle(t *testing.T, env *testEnv, tok string, data []byte) int {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("bundle", "x.zip")
	_, _ = fw.Write(data)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", env.srv.URL+"/api/v1/plugins", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp.StatusCode
}

func TestPluginErrorCodes(t *testing.T) {
	for err, want := range map[error]int{plugins.ErrNotFound: 404, plugins.ErrPinned: 409, plugins.ErrConflict: 409, io.EOF: 400} {
		rec := httptest.NewRecorder()
		pluginError(rec, err)
		if rec.Code != want {
			t.Errorf("pluginError(%v) = %d, want %d", err, rec.Code, want)
		}
	}
}

func TestUploadWithoutAnInbox(t *testing.T) {
	env, tok := newPluginEnv(t)
	inbox := env.s.opt.Plugins.InboxDir()
	if err := os.RemoveAll(inbox); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := uploadBundle(t, env, tok, widgetBundle()); code != http.StatusInternalServerError {
		t.Fatalf("upload with the inbox blocked: %d", code)
	}
}
