package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/logbuf"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/phoneapi"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
)

func testWeb(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.StateDir = dir
	cfg.Radio.Driver = "none"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := mesh.NewHost(cfg.MeshConfig(), null.New(), log)
	if err != nil {
		t.Fatal(err)
	}
	h.SetHoster(&fakeHoster{remote: &fakeRemote{}})
	relay, _ := mesh.NewIdentity(nil, "Relay", "RLY")
	relay.IsRelay = true
	if err := h.AddIdentity(relay); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done }) // let the host write its final state before TempDir is removed
	go func() { _ = h.Run(ctx); close(done) }()
	s, err := New(Options{Config: cfg, Host: h, API: phoneapi.NewManager(h, log), Logs: logbuf.New(10), Version: "test", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token string, body any) (int, map[string]any, []any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var obj map[string]any
	var arr []any
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		_ = json.Unmarshal(raw, &arr)
	} else {
		_ = json.Unmarshal(raw, &obj)
	}
	return resp.StatusCode, obj, arr
}

func TestSetupLoginIdentitiesAndMessages(t *testing.T) {
	srv := testWeb(t)
	if code, obj, _ := call(t, srv, "GET", "/api/v1/setup", "", nil); code != 200 || obj["needed"] != true {
		t.Fatalf("setup needed: %d %v", code, obj)
	}
	if code, _, _ := call(t, srv, "GET", "/api/v1/status", "", nil); code != 401 {
		t.Fatalf("status without token: %d", code)
	}
	code, obj, _ := call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse", "region": "EU_868", "preset": "LONG_FAST"})
	if code != 200 {
		t.Fatalf("setup: %d %v", code, obj)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "wrong"}); code != 401 {
		t.Fatal("wrong password accepted")
	}
	_, obj, _ = call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok, _ := obj["token"].(string)
	if tok == "" {
		t.Fatal("no token")
	}

	code, st, _ := call(t, srv, "GET", "/api/v1/status", tok, nil)
	if code != 200 || st["phy"].(map[string]any)["frequency_mhz"].(float64) != 869.525 {
		t.Fatalf("status %d %v", code, st)
	}

	code, pv, _ := call(t, srv, "POST", "/api/v1/identities/preview-key", tok, map[string]any{})
	if code != 200 || !strings.HasPrefix(pv["node_id"].(string), "!") {
		t.Fatalf("preview %d %v", code, pv)
	}
	code, a, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Base Camp", "short_name": "BASE", "api_port": 0})
	if code != 201 {
		t.Fatalf("create %d %v", code, a)
	}
	code, b, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Ops Desk", "short_name": "OPS"})
	if code != 201 {
		t.Fatalf("create 2 %d %v", code, b)
	}
	if code, _, list := call(t, srv, "GET", "/api/v1/identities", tok, nil); code != 200 || len(list) != 3 {
		t.Fatalf("list %d %d", code, len(list))
	}

	aID, bID := a["node_id"].(string), b["node_id"].(string)
	code, m, _ := call(t, srv, "POST", "/api/v1/identities/"+aID+"/messages", tok, map[string]any{"to": bID, "text": "hello ops"})
	if code != 202 {
		t.Fatalf("send %d %v", code, m)
	}
	// Handed to A's node, which delivers it (the hosted nodes' air carries local DMs).
	_, _, msgs := call(t, srv, "GET", "/api/v1/identities/"+aID+"/messages?conversation=dm:"+bID, tok, nil)
	if len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "hello ops" || msgs[0].(map[string]any)["status"] != "queued" {
		t.Fatalf("DM not queued: %v", msgs)
	}

	code, u, _ := call(t, srv, "GET", "/api/v1/identities/"+aID+"/channels/url", tok, nil)
	if code != 200 || !strings.HasPrefix(u["url"].(string), "https://meshtastic.org/e/#") {
		t.Fatalf("channel url %d %v", code, u)
	}
	if code, e, _ := call(t, srv, "PUT", "/api/v1/identities/"+aID+"/channels/0", tok,
		map[string]any{"name": "Renamed", "psk": "AQ==", "role": "PRIMARY"}); code != 409 {
		t.Fatalf("primary rename should be refused: %d %v", code, e)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/identities/"+aID+"/channels/1", tok,
		map[string]any{"name": "Ops", "psk": "AQ==", "role": "SECONDARY"}); code != 200 {
		t.Fatalf("secondary channel %d", code)
	}
	code, tk, _ := call(t, srv, "POST", "/api/v1/tokens", tok, map[string]any{"name": "Home Assistant"})
	if code != 201 {
		t.Fatalf("token %d", code)
	}
	if code, _, _ := call(t, srv, "GET", "/api/v1/nodes", tk["token"].(string), nil); code != 200 {
		t.Fatalf("api token rejected: %d", code)
	}
	if code, _, _ := call(t, srv, "GET", "/some/spa/route", "", nil); code != 200 {
		t.Fatalf("spa fallback %d", code)
	}
}

// A new identity has no message history, but its channels must still be listed as
// conversations: the chat page can only open a conversation that is listed, so without
// this a fresh identity could send DMs but never post in a channel.
func TestConversationsListChannelsWithoutHistory(t *testing.T) {
	srv := testWeb(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse", "region": "EU_868", "preset": "LONG_FAST"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)
	_, a, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Base Camp", "short_name": "BASE", "api_port": 0})
	aID := a["node_id"].(string)

	keys := func() map[string]string {
		t.Helper()
		code, _, list := call(t, srv, "GET", "/api/v1/identities/"+aID+"/conversations", tok, nil)
		if code != 200 {
			t.Fatalf("conversations: %d", code)
		}
		out := map[string]string{}
		for _, c := range list {
			m := c.(map[string]any)
			out[m["key"].(string)] = m["title"].(string)
		}
		return out
	}

	if got := keys(); got["ch:0"] != "LongFast" || len(got) != 1 {
		t.Fatalf("fresh identity conversations = %v, want only ch:0 LongFast", got)
	}

	code, _, _ := call(t, srv, "PUT", "/api/v1/identities/"+aID+"/channels/1", tok,
		map[string]any{"name": "Ops", "psk": "AQ==", "role": "SECONDARY"})
	if code != 200 {
		t.Fatalf("add secondary channel: %d", code)
	}
	if got := keys(); got["ch:1"] != "Ops" || got["ch:0"] != "LongFast" || len(got) != 2 {
		t.Fatalf("after adding a channel conversations = %v, want ch:0 and ch:1", got)
	}

	call(t, srv, "PUT", "/api/v1/identities/"+aID+"/channels/1", tok, map[string]any{"name": "Ops", "role": "DISABLED"})
	if got := keys(); len(got) != 1 {
		t.Fatalf("disabled channel still listed: %v", got)
	}
}

func TestPasswordChangeKeepsThisSessionAndSignOutEverywhere(t *testing.T) {
	srv := testWeb(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, a, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	_, b, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tokA, tokB := a["token"].(string), b["token"].(string)

	code, res, _ := call(t, srv, "PUT", "/api/v1/auth/password", tokA, map[string]any{"current": "correct horse", "new": "battery staple"})
	fresh, _ := res["token"].(string)
	if code != 200 || fresh == "" {
		t.Fatalf("password change %d %v", code, res)
	}
	if code, _, _ := call(t, srv, "GET", "/api/v1/status", fresh, nil); code != 200 {
		t.Fatalf("fresh session after password change: %d", code)
	}
	if code, _, _ := call(t, srv, "GET", "/api/v1/status", tokB, nil); code != 401 {
		t.Fatalf("other session still valid after password change: %d", code)
	}

	if code, _, _ := call(t, srv, "POST", "/api/v1/auth/logout-all", fresh, nil); code != 204 {
		t.Fatalf("logout-all: %d", code)
	}
	if code, _, _ := call(t, srv, "GET", "/api/v1/status", fresh, nil); code != 401 {
		t.Fatalf("session still valid after signing out everywhere: %d", code)
	}
}

func TestSetupMeshtasticdCheck(t *testing.T) {
	srv := testWeb(t)
	check := func(body map[string]any) (int, map[string]any) {
		code, obj, _ := call(t, srv, "POST", "/api/v1/setup/meshtasticd", "", body)
		return code, obj
	}
	// Before a password exists, nothing but meshtasticd may be run.
	if code, _ := check(map[string]any{"meshtasticd": "/bin/sh"}); code != 400 {
		t.Fatalf("other program: %d", code)
	}
	if code, _ := check(map[string]any{"docker_image": "evil/image:latest"}); code != 400 {
		t.Fatalf("unofficial image: %d", code)
	}
	code, obj := check(map[string]any{"meshtasticd": "/nonexistent/meshtasticd"})
	if code != 200 || obj["ok"] != false || !strings.Contains(obj["error"].(string), "no meshtasticd at") || obj["min_version"] != "2.8.0" {
		t.Fatalf("missing program: %d %v", code, obj)
	}
	code, obj, _ = call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse",
		"hosted": map[string]any{"persona": true, "docker_image": "evil/image:latest"}})
	if code != 400 || !strings.Contains(obj["error"].(string), "meshtastic/meshtasticd") {
		t.Fatalf("setup with an unofficial image: %d %v", code, obj)
	}
}
