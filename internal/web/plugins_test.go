package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/logbuf"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/phoneapi"
	"github.com/ScotMesh/RepeaterTastic/internal/plugins"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
)

const panelManifest = `id: widget
name: Widget
version: 1.2.3
api: 1
logo: logo.svg
permissions: [nodes.read]
settings:
  - key: api_key
    type: secret
    required: true
ui:
  panel: ui/index.html
`

func TestPluginsAPI(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.StateDir = dir
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := mesh.NewHost(cfg.MeshConfig(), null.New(), log)
	if err != nil {
		t.Fatal(err)
	}
	relay, _ := mesh.NewIdentity(nil, "Relay", "RLY")
	relay.IsRelay = true
	_ = h.AddIdentity(relay)
	pm, err := plugins.New(plugins.Options{Config: cfg.Plugins, Dir: filepath.Join(dir, "plugins"), Radios: []plugins.Radio{{ID: "main", Host: h}}, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Config: cfg, Host: h, API: phoneapi.NewManager(h, log), Logs: logbuf.New(10), Version: "test", Log: log, Plugins: pm})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	_, obj, _ := call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse", "region": "EU_868", "preset": "LONG_FAST"})
	tok, _ := obj["token"].(string)

	// Upload a bundle.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, body := range map[string]string{"plugin.yaml": panelManifest, "logo.svg": "<svg xmlns='http://www.w3.org/2000/svg'/>", "ui/index.html": "<p>hi</p>"} {
		w, _ := zw.Create(name)
		_, _ = io.WriteString(w, body)
	}
	_ = zw.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("bundle", "widget.zip")
	_, _ = fw.Write(zb.Bytes())
	_ = mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/plugins", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var installed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&installed)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || installed["id"] != "widget" || installed["state"] != "disabled" {
		t.Fatalf("install: %d %v", resp.StatusCode, installed)
	}

	// Without a token, nothing.
	if code, _, _ := call(t, srv, "GET", "/api/v1/plugins", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("list without token: %d", code)
	}
	// Enabling needs the required secret first.
	if code, obj, _ := call(t, srv, "POST", "/api/v1/plugins/widget/enable", tok, map[string]any{"permissions": []string{"nodes.read"}}); code != http.StatusBadRequest {
		t.Fatalf("enable without settings: %d %v", code, obj)
	}
	code, obj, _ := call(t, srv, "PUT", "/api/v1/plugins/widget/settings", tok, map[string]any{"api_key": "hunter2"})
	if code != 200 || strings.Contains(jsonOf(obj), "hunter2") || obj["values"].(map[string]any)["api_key"] != plugins.SecretMask {
		t.Fatalf("settings leak or not masked: %d %v", code, obj)
	}
	if code, obj, _ := call(t, srv, "POST", "/api/v1/plugins/widget/enable", tok, map[string]any{"permissions": []string{"radio.own"}}); code != http.StatusBadRequest {
		t.Fatalf("enable with a permission it didn't ask for: %d %v", code, obj)
	}

	// Assets: served with a sandbox policy under the capability key only.
	logo, _ := installed["logo_url"].(string)
	panel, _ := installed["panel_url"].(string)
	for _, u := range []string{logo, panel} {
		r, err := http.Get(srv.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("asset %s: %d %q", u, r.StatusCode, r.Header.Get("Content-Security-Policy"))
		}
	}
	for _, u := range []string{"/plugin-assets/widget/0000/logo", strings.Replace(panel, "panel/", "panel/../plugin.yaml", 1), panel + "..%2fplugin.yaml"} {
		r, err := http.Get(srv.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode == 200 {
			t.Fatalf("asset %s served", u)
		}
	}

	if code, _, _ := call(t, srv, "DELETE", "/api/v1/plugins/widget", tok, nil); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	if code, _, _ := call(t, srv, "GET", "/api/v1/plugins/widget", tok, nil); code != http.StatusNotFound {
		t.Fatalf("after remove: %d", code)
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
