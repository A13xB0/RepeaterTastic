package web

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/logbuf"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/phoneapi"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
	"github.com/A13xB0/RepeaterTastic/internal/site"
)

// Two simulated radios (LongFast main + MediumFast extra) behind one web server.
func testWebTwoRadios(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	cfg.Radio.Driver = "none"
	cfg.Radios = []config.RadioInstance{{ID: "mf", Name: "MediumFast", Radio: config.Radio{Driver: "none"},
		Mesh: config.Mesh{Region: "EU_868", Preset: "MEDIUM_FAST", HopLimit: 3}, Relay: config.Relay{Role: "mute", LongName: "MF Relay", ShortName: "MF"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := site.New(0)
	ctx, cancel := context.WithCancel(context.Background())
	var hosts []*mesh.Host
	var apis []*phoneapi.Manager
	for _, rc := range cfg.RadioConfigs() {
		h, err := mesh.NewHost(rc.MeshConfig(), null.New(), log)
		if err != nil {
			t.Fatal(err)
		}
		relay, _ := mesh.NewIdentity(nil, rc.Relay.LongName, rc.Relay.ShortName)
		relay.IsRelay = true
		if err := h.AddIdentity(relay); err != nil {
			t.Fatal(err)
		}
		st.Add(h)
		hosts = append(hosts, h)
		apis = append(apis, phoneapi.NewManager(h, log))
	}
	done := make(chan struct{}, len(hosts))
	for _, h := range hosts {
		go func() { _ = h.Run(ctx); done <- struct{}{} }()
	}
	t.Cleanup(func() {
		cancel()
		for range hosts {
			<-done
		}
	})
	rcs := cfg.RadioConfigs()
	s, err := New(Options{Config: cfg, Host: hosts[0], API: apis[0], Logs: logbuf.New(10), Version: "test", Log: log, Site: st,
		Radios: []Radio{{ID: "mf", Name: "MediumFast", Config: rcs[1].Config, Host: hosts[1], API: apis[1]}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestTwoRadiosRouteByRadioAndIdentity(t *testing.T) {
	srv := testWebTwoRadios(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)

	code, rs, _ := call(t, srv, "GET", "/api/v1/radios", tok, nil)
	radios, _ := rs["radios"].([]any)
	if code != 200 || len(radios) != 2 {
		t.Fatalf("radios %d %v", code, rs)
	}
	mf := radios[1].(map[string]any)
	if mf["id"] != "mf" || mf["phy"].(map[string]any)["preset_name"] != "MediumFast" || len(mf["overlaps"].([]any)) != 1 {
		t.Fatalf("mf radio summary = %v", mf)
	}

	_, st, _ := call(t, srv, "GET", "/api/v1/status?radio=mf", tok, nil)
	if st["radio_id"] != "mf" || st["phy"].(map[string]any)["preset_name"] != "MediumFast" {
		t.Fatalf("status?radio=mf = %v", st["phy"])
	}

	code, a, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "MF Desk", "short_name": "MFD", "radio_id": "mf"})
	if code != 201 || a["role"] != "CLIENT_MUTE" {
		t.Fatalf("create on mf %d %v", code, a)
	}
	if _, _, list := call(t, srv, "GET", "/api/v1/identities", tok, nil); len(list) != 1 {
		t.Fatalf("main radio should still only have its relay, got %d identities", len(list))
	}
	if _, _, list := call(t, srv, "GET", "/api/v1/identities?radio=mf", tok, nil); len(list) != 2 {
		t.Fatalf("mf radio should have relay + MF Desk, got %d", len(list))
	}
	// identity-scoped endpoints find the right radio without ?radio=
	if code, convs, _ := call(t, srv, "GET", "/api/v1/identities/"+a["node_id"].(string)+"/conversations", tok, nil); code != 200 {
		t.Fatalf("conversations on mf identity: %d %v", code, convs)
	} else if _, _, list := call(t, srv, "GET", "/api/v1/identities/"+a["node_id"].(string)+"/conversations", tok, nil); len(list) != 1 ||
		list[0].(map[string]any)["title"] != "MediumFast" {
		t.Fatalf("mf identity conversations = %v", list)
	}
}
