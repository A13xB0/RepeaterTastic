package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
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
		Federation: mesh.NewFederation(hosts...),
		Radios:     []Radio{{ID: "mf", Name: "MediumFast", Config: rcs[1].Config, Host: hosts[1], API: apis[1]}}})
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

func TestConfigPositionHardwareMQTTPerRadio(t *testing.T) {
	srv := testWebTwoRadios(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)

	code, cfg, _ := call(t, srv, "GET", "/api/v1/config?radio=mf", tok, nil)
	if code != 200 || cfg["radio_id"] != "mf" || cfg["main"] != false || cfg["hardware"].(map[string]any)["hw_model"] != "AUTO" {
		t.Fatalf("mf config %d %v", code, cfg)
	}
	code, res, _ := call(t, srv, "PUT", "/api/v1/config?radio=mf", tok, map[string]any{
		"position": map[string]any{"latitude": 56.2, "longitude": -3.16, "altitude": 90, "precision_bits": 16, "interval": "6h", "identities": "all"},
		"hardware": map[string]any{"hw_model": "HELTEC_V3"},
		// the single-connection shape still works
		"mqtt": map[string]any{"name": "public", "enabled": true, "address": "mqtt.example:1883", "username": "u", "password": "secret", "root": "msh/EU_868/Scotland",
			"ok_to_mqtt": true, "downlink_per_minute": 10, "map_report": map[string]any{"enabled": true, "interval": "1h", "position_precision": 14}},
	})
	if code != 200 || res["restart_required"] != true {
		t.Fatalf("put mf config %d %v", code, res)
	}
	out := res["config"].(map[string]any)
	if out["hardware"].(map[string]any)["effective"] != "HELTEC_V3" || out["position"].(map[string]any)["precision_bits"] != float64(16) {
		t.Fatalf("mf config after put = %v", out)
	}
	m := out["mqtt"].([]any)[0].(map[string]any)
	if m["password"] != "" || m["password_set"] != true || m["root"] != "msh/EU_868/Scotland" || m["key"] != "public" || m["mode"] != "gateway" {
		t.Fatalf("mqtt dto leaked or lost the password: %v", m)
	}

	// a second connection; the first keeps its password when the field comes back empty
	code, res, _ = call(t, srv, "PUT", "/api/v1/config?radio=mf", tok, map[string]any{"mqtt": []any{
		map[string]any{"key": "public", "name": "public", "enabled": true, "address": "mqtt.example:1883", "password": "",
			"map_report": map[string]any{"interval": "1h"}},
		map[string]any{"name": "logger", "enabled": true, "address": "127.0.0.1:1883", "mode": "monitor",
			"uplink_channels": []string{"MediumFast"}, "map_report": map[string]any{"interval": "1h"}},
	}})
	if code != 200 {
		t.Fatalf("put two connections %d %v", code, res)
	}
	list := res["config"].(map[string]any)["mqtt"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["password_set"] != true || list[1].(map[string]any)["format"] != "json" ||
		list[1].(map[string]any)["channel_selection"] != "override" {
		t.Fatalf("two connections = %v", list)
	}
	// a bridge needs the acknowledgement
	if code, _, _ := call(t, srv, "PUT", "/api/v1/config?radio=mf", tok, map[string]any{"mqtt": []any{
		map[string]any{"name": "sites", "enabled": true, "address": "b:1883", "mode": "bridge", "map_report": map[string]any{"interval": "1h"}},
	}}); code != 400 {
		t.Fatalf("unacknowledged bridge accepted: %d", code)
	}
	if code, _, links := call(t, srv, "GET", "/api/v1/links?radio=mf", tok, nil); code != 200 || len(links) != 3 ||
		links[2].(map[string]any)["name"] != "mqtt:logger" || links[2].(map[string]any)["mode"] != "monitor" {
		t.Fatalf("links = %d %v", code, links)
	}
	// the main radio is untouched
	if _, main, _ := call(t, srv, "GET", "/api/v1/config", tok, nil); len(main["mqtt"].([]any)) != 0 ||
		main["position"].(map[string]any)["latitude"] != float64(0) {
		t.Fatalf("main radio changed: %v", main)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/config?radio=mf", tok, map[string]any{"web": map[string]any{"port": 9}}); code != 400 {
		t.Fatalf("web settings on an extra radio accepted: %d", code)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{"hardware": map[string]any{"hw_model": "NOT_A_BOARD"}}); code != 400 {
		t.Fatalf("unknown hardware model accepted: %d", code)
	}
}

func TestAddRenameRemoveRadios(t *testing.T) {
	srv := testWebTwoRadios(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)

	if code, res, _ := call(t, srv, "POST", "/api/v1/radios", tok, map[string]any{"id": "Long Slow"}); code != 400 {
		t.Fatalf("bad id accepted: %d %v", code, res)
	}
	code, res, _ := call(t, srv, "POST", "/api/v1/radios", tok, map[string]any{"id": "ls", "name": "LongSlow", "driver": "none",
		"region": "EU_868", "preset": "LONG_SLOW", "tx_power_dbm": 20})
	if code != 201 || res["restart_required"] != true {
		t.Fatalf("add radio %d %v", code, res)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/radios", tok, map[string]any{"id": "ls", "driver": "none"}); code != 400 {
		t.Fatalf("duplicate id accepted: %d", code)
	}
	_, list, _ := call(t, srv, "GET", "/api/v1/radios", tok, nil)
	pending := list["pending"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["id"] != "ls" || pending[0].(map[string]any)["relay_role"] != "mute" || list["restart_required"] != true {
		t.Fatalf("pending = %v", list)
	}

	// a radio that hasn't started can be changed; a running one can't be changed this way
	if code, res, _ := call(t, srv, "PUT", "/api/v1/radios/ls", tok, map[string]any{"name": "Long Slow", "driver": "none", "device": "",
		"region": "EU_868", "preset": "SHORT_FAST", "tx_power_dbm": 14, "relay_role": "client"}); code != 200 {
		t.Fatalf("edit pending radio %d %v", code, res)
	}
	_, list, _ = call(t, srv, "GET", "/api/v1/radios", tok, nil)
	if p := list["pending"].([]any)[0].(map[string]any); p["preset"] != "SHORT_FAST" || p["name"] != "Long Slow" || p["relay_role"] != "client" || p["tx_power_dbm"] != float64(14) {
		t.Fatalf("pending after edit = %v", p)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/radios/ls", tok, map[string]any{"driver": "none", "preset": "NOPE"}); code != 400 {
		t.Fatalf("bad preset accepted: %d", code)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/radios/mf", tok, map[string]any{"preset": "LONG_FAST"}); code != 409 {
		t.Fatalf("running radio edited through PUT /radios: %d", code)
	}

	if code, res, _ := call(t, srv, "PATCH", "/api/v1/radios/mf", tok, map[string]any{"name": "Medium Fast"}); code != 200 || res["name"] != "Medium Fast" {
		t.Fatalf("rename mf %d %v", code, res)
	}
	if code, _, _ := call(t, srv, "PATCH", "/api/v1/radios/main", tok, map[string]any{"name": "LongFast"}); code != 200 {
		t.Fatalf("rename main %d", code)
	}
	_, list, _ = call(t, srv, "GET", "/api/v1/radios", tok, nil)
	radios := list["radios"].([]any)
	if radios[0].(map[string]any)["name"] != "LongFast" || radios[1].(map[string]any)["name"] != "Medium Fast" {
		t.Fatalf("names = %v", radios)
	}

	if code, _, _ := call(t, srv, "DELETE", "/api/v1/radios/main", tok, nil); code != 400 {
		t.Fatalf("main radio removed: %d", code)
	}
	if code, res, _ := call(t, srv, "DELETE", "/api/v1/radios/ls", tok, nil); code != 200 || res["restart_required"] != false {
		t.Fatalf("remove a radio that never started %d %v", code, res)
	}
	if code, res, _ := call(t, srv, "DELETE", "/api/v1/radios/mf", tok, nil); code != 200 || res["restart_required"] != true {
		t.Fatalf("remove running mf %d %v", code, res)
	}

	if code, res, _ := call(t, srv, "PUT", "/api/v1/site", tok, map[string]any{"duty_cycle_percent": 8}); code != 200 ||
		res["running_duty_cycle_percent"] != float64(8) || res["restart_required"] != false {
		t.Fatalf("site %d %v", code, res)
	}
}

func TestMoveIdentityBetweenRadios(t *testing.T) {
	srv := testWebTwoRadios(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)

	_, desk, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "short_name": "DESK", "api_port": 4460})
	deskID := desk["node_id"].(string)
	if desk["radio_id"] != "main" {
		t.Fatalf("new identity radio = %v", desk["radio_id"])
	}
	// a message still waiting to transmit is marked failed by the move (see TestDropOutgoing)
	_, busy, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Busy", "api_port": 4462})
	busyID := busy["node_id"].(string)
	if code, _, _ := call(t, srv, "POST", "/api/v1/identities/"+busyID+"/messages", tok, map[string]any{"channel": 0, "text": "queued", "want_ack": false}); code >= 300 {
		t.Fatalf("send %d", code)
	}
	if code, res, _ := call(t, srv, "POST", "/api/v1/identities/"+busyID+"/move", tok, map[string]any{"radio_id": "mf"}); code != 200 {
		t.Fatalf("move busy %d %v", code, res)
	}
	_, _, msgs := call(t, srv, "GET", "/api/v1/identities/"+busyID+"/messages?conversation=ch:0", tok, nil)
	// "failed" when the move cancelled it, "sent" when the test radio got it out first
	if st := msgs[0].(map[string]any)["status"]; len(msgs) != 1 || (st != "failed" && st != "sent") || msgs[0].(map[string]any)["text"] != "queued" {
		t.Fatalf("busy identity's messages after the move = %v", msgs)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/identities/"+busyID+"/move", tok, map[string]any{"radio_id": "main"}); code != 200 {
		t.Fatalf("move busy back %d", code)
	}

	// relay personas stay put; unknown radios are refused
	_, _, mainList := call(t, srv, "GET", "/api/v1/identities", tok, nil)
	relayID := ""
	for _, x := range mainList {
		if m := x.(map[string]any); m["is_relay"] == true {
			relayID = m["node_id"].(string)
		}
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/identities/"+relayID+"/move", tok, map[string]any{"radio_id": "mf"}); code != 409 {
		t.Fatalf("relay moved: %d", code)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/identities/"+deskID+"/move", tok, map[string]any{"radio_id": "nope"}); code != 400 {
		t.Fatalf("move to unknown radio: %d", code)
	}

	code, moved, _ := call(t, srv, "POST", "/api/v1/identities/"+deskID+"/move", tok, map[string]any{"radio_id": "mf"})
	if code != 200 || moved["radio_id"] != "mf" || moved["node_id"] != deskID || moved["api"].(map[string]any)["port"] != float64(4460) {
		t.Fatalf("move %d %v", code, moved)
	}
	if ch := moved["channels"].([]any)[0].(map[string]any); ch["display_name"] != "MediumFast" {
		t.Fatalf("primary channel after move = %v, want MediumFast", ch["display_name"])
	}
	if _, _, list := call(t, srv, "GET", "/api/v1/identities", tok, nil); len(list) != 2 {
		t.Fatalf("main radio lists %d identities, want relay + Busy", len(list))
	}
	if _, _, all := call(t, srv, "GET", "/api/v1/identities?radio=all", tok, nil); len(all) != 4 {
		t.Fatalf("all radios = %d identities, want 2 relays + Desk + Busy", len(all))
	}
	// the same key can't be imported onto the other radio
	_, key, _ := call(t, srv, "GET", "/api/v1/identities/"+deskID+"/key", tok, nil)
	if code, res, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk again", "private_key": key["private_key"]}); code != 409 {
		t.Fatalf("duplicate key imported: %d %v", code, res)
	}
	// ports are unique across radios when editing too
	_, other, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Other", "api_port": 4461})
	if code, _, _ := call(t, srv, "PATCH", "/api/v1/identities/"+other["node_id"].(string), tok, map[string]any{"api_port": 4460}); code != 409 {
		t.Fatalf("port clash across radios accepted: %d", code)
	}
}

func TestConfigurationGaps(t *testing.T) {
	srv := testWebTwoRadios(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)

	_, cfg, _ := call(t, srv, "GET", "/api/v1/config", tok, nil)
	air := cfg["airtime"].(map[string]any)
	if air["telemetry_interval"] != "off" || air["cw_min"] != float64(3) || cfg["radio"].(map[string]any)["hop_limit"] != float64(3) {
		t.Fatalf("config = %v", cfg)
	}
	if _, has := air["position"]; has {
		t.Fatal("the unsaved airtime.position control is still in the config")
	}
	code, res, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{
		"airtime": map[string]any{"duty_cycle_percent": 10, "identity_share_percent": 25, "nodeinfo_interval": "3h", "telemetry_interval": "3h", "override_duty_cycle": false},
		"radio":   map[string]any{"type": "none", "port": "", "region": "EU_868", "preset": "LONG_FAST", "tx_power_dbm": 22, "hop_limit": 2, "baud": 115200},
		"web":     map[string]any{"bind": "0.0.0.0", "port": 8080, "session_ttl": "24h", "log_level": "debug", "mdns": false, "map_tile_url": "https://tiles.example/{z}/{x}/{y}.png"},
	})
	if code != 200 {
		t.Fatalf("put %d %v", code, res)
	}
	out := res["config"].(map[string]any)
	if out["airtime"].(map[string]any)["telemetry_interval"] != "3h" || out["radio"].(map[string]any)["hop_limit"] != float64(2) ||
		out["web"].(map[string]any)["log_level"] != "debug" || out["web"].(map[string]any)["map_tile_url"] != "https://tiles.example/{z}/{x}/{y}.png" {
		t.Fatalf("after put = %v", out)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{"airtime": map[string]any{"telemetry_interval": "5m", "duty_cycle_percent": 10, "identity_share_percent": 25, "nodeinfo_interval": "3h"}}); code != 400 {
		t.Fatalf("5 minute telemetry accepted: %d", code)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{"web": map[string]any{"bind": "0.0.0.0", "port": 8080, "session_ttl": "24h", "map_tile_url": "ftp://nope"}}); code != 400 {
		t.Fatalf("bad tile URL accepted: %d", code)
	}

	// UDP multicast is per radio now, and a pending change shows up as a restart reason
	if code, l, _ := call(t, srv, "PATCH", "/api/v1/links/udp?radio=mf", tok, map[string]any{"enabled": true, "group": "239.0.0.70:4403"}); code != 200 || l["group"] != "239.0.0.70:4403" || l["restart_required"] != true {
		t.Fatalf("udp on mf %d %v", code, l)
	}
	if _, _, main := call(t, srv, "GET", "/api/v1/links", tok, nil); main[0].(map[string]any)["enabled"] != false {
		t.Fatalf("mf's UDP change landed on the main radio: %v", main[0])
	}
	if code, _, _ := call(t, srv, "PATCH", "/api/v1/links/udp", tok, map[string]any{"group": "10.0.0.1:4403"}); code != 400 {
		t.Fatalf("non-multicast group accepted: %d", code)
	}
	_, st, _ := call(t, srv, "GET", "/api/v1/status", tok, nil)
	reasons := fmt.Sprint(st["restart_reasons"])
	if !strings.Contains(reasons, "MediumFast UDP multicast") || !strings.Contains(reasons, "mDNS") || strings.Contains(reasons, "MQTT") {
		t.Fatalf("restart reasons = %v", reasons)
	}

	// experimental switch
	if code, e, _ := call(t, srv, "PUT", "/api/v1/experimental", tok, map[string]any{"multi_radio_identities": true}); code != 200 || e["multi_radio_identities"] != true {
		t.Fatalf("experimental %d %v", code, e)
	}

	// a backup carries every radio's identities
	call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "MF Desk", "radio_id": "mf"})
	_, b, _ := call(t, srv, "GET", "/api/v1/backup", tok, nil)
	if len(b["identities"].([]any)) != 1 || len(b["radio_identities"].(map[string]any)["mf"].([]any)) != 2 {
		t.Fatalf("backup = identities %v radio_identities %v", b["identities"], b["radio_identities"])
	}
	b["radio_identities"].(map[string]any)["../evil"] = []any{}
	if code, _, _ := call(t, srv, "POST", "/api/v1/restore", tok, b); code != 400 {
		t.Fatalf("restore with a path-like radio id accepted: %d", code)
	}
}

func TestMultiRadioIdentityAPI(t *testing.T) {
	srv := testWebTwoRadios(t)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)

	_, desk, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "api_port": 4470})
	path := "/api/v1/identities/" + desk["node_id"].(string)

	// a slot's radio needs the switch on
	if code, _, _ := call(t, srv, "PUT", path+"/channels/1", tok, map[string]any{"name": "Scotland", "psk": "AQ==", "role": "SECONDARY", "radio": "mf"}); code != 400 {
		t.Fatalf("slot radio with the switch off: %d", code)
	}
	call(t, srv, "PUT", "/api/v1/experimental", tok, map[string]any{"multi_radio_identities": true})

	for name, body := range map[string]map[string]any{
		"slot 0":        {"name": "", "psk": "AQ==", "role": "PRIMARY", "radio": "mf"},
		"unknown radio": {"name": "Scotland", "psk": "AQ==", "role": "SECONDARY", "radio": "nope"},
	} {
		idx := "1"
		if name == "slot 0" {
			idx = "0"
		}
		if code, _, _ := call(t, srv, "PUT", path+"/channels/"+idx, tok, body); code != 400 {
			t.Errorf("%s accepted: %d", name, code)
		}
	}

	// Scotland on LongFast (slot 1, default radio) and on MediumFast (slot 2)
	call(t, srv, "PUT", path+"/channels/1", tok, map[string]any{"name": "Scotland", "psk": "AQ==", "role": "SECONDARY"})
	code, res, _ := call(t, srv, "PUT", path+"/channels/2", tok, map[string]any{"name": "Scotland", "psk": "AQ==", "role": "SECONDARY", "radio": "mf"})
	if code != 200 {
		t.Fatalf("slot 2 on mf %d %v", code, res)
	}
	chans := res["channels"].([]any)
	if c := chans[1].(map[string]any); c["radio"] != "main" {
		t.Fatalf("slot 1 = %v, want the default (main) radio", c)
	}
	if c := chans[2].(map[string]any); c["radio"] != "mf" || c["radio_name"] != "MediumFast" {
		t.Fatalf("slot 2 = %v", c)
	}
	if r := res["radios"].([]any); len(r) != 2 || r[1] != "mf" {
		t.Fatalf("radios = %v", r)
	}

	// default radio: slot 0 becomes MediumFast's primary, other slots stay
	code, res, _ = call(t, srv, "PATCH", path, tok, map[string]any{"multi_radio": map[string]any{"default_radio": "mf", "dm": "auto", "fallback": true}})
	if code != 200 {
		t.Fatalf("default radio %d %v", code, res)
	}
	chans = res["channels"].([]any)
	if c := chans[0].(map[string]any); c["radio"] != "mf" || c["display_name"] != "MediumFast" {
		t.Fatalf("slot 0 after default mf = %v", c)
	}
	if c := chans[2].(map[string]any); c["radio"] != "mf" {
		t.Fatalf("slot 2 moved: %v", c)
	}
	if mr := res["multi_radio"].(map[string]any); mr["fallback"] != true || mr["channels"].(map[string]any)["2"] != "mf" {
		t.Fatalf("multi_radio = %v", mr)
	}
	if code, _, _ := call(t, srv, "PATCH", path, tok, map[string]any{"multi_radio": map[string]any{"dm": "somewhere"}}); code != 400 {
		t.Fatalf("bad dm accepted: %d", code)
	}

	// a radio that hasn't started yet can be chosen; the slot waits on the default radio until then
	call(t, srv, "POST", "/api/v1/radios", tok, map[string]any{"id": "ls", "driver": "none", "region": "EU_868", "preset": "LONG_SLOW"})
	code, res, _ = call(t, srv, "PUT", path+"/channels/4", tok, map[string]any{"name": "Slow", "psk": "AQ==", "role": "SECONDARY", "radio": "ls"})
	if code != 200 {
		t.Fatalf("slot on a radio that hasn't started %d %v", code, res)
	}
	for _, x := range res["channels"].([]any) {
		if c := x.(map[string]any); c["index"] == float64(4) && (c["radio_pending"] != "ls" || c["radio_removed"] != nil) {
			t.Fatalf("slot 4 = %v, want radio_pending ls", c)
		}
	}
	call(t, srv, "PUT", path+"/channels/4", tok, map[string]any{"name": "", "psk": "", "role": "DISABLED"})

	// a different channel in slot 2 goes back to the default radio (mf here); default back to main
	call(t, srv, "PATCH", path, tok, map[string]any{"multi_radio": map[string]any{"default_radio": ""}})
	call(t, srv, "PUT", path+"/channels/2", tok, map[string]any{"name": "", "psk": "", "role": "DISABLED"})
	_, res, _ = call(t, srv, "PUT", path+"/channels/2", tok, map[string]any{"name": "Other", "psk": "AQ==", "role": "SECONDARY"})
	if c := res["channels"].([]any)[2].(map[string]any); c["radio"] != "main" {
		t.Fatalf("new channel in slot 2 kept the old slot's radio: %v", c)
	}

	_, res, _ = call(t, srv, "GET", path+"/route?channel=1", tok, nil)
	if radios := res["radios"].([]any); len(radios) != 1 || radios[0] != "main" {
		t.Fatalf("route for slot 1 = %v", res)
	}
	if code, _, sightings := call(t, srv, "GET", "/api/v1/nodes/"+desk["node_id"].(string)+"/sightings", tok, nil); code != 200 || sightings == nil {
		t.Fatalf("sightings %d %v", code, sightings)
	}

	// moving the identity carries slots on its old home to the new one
	call(t, srv, "PUT", path+"/channels/3", tok, map[string]any{"name": "Pinned", "psk": "AQ==", "role": "SECONDARY", "radio": "main"})
	_, res, _ = call(t, srv, "POST", path+"/move", tok, map[string]any{"radio_id": "mf"})
	if c := res["channels"].([]any)[3].(map[string]any); c["radio"] != "mf" {
		t.Fatalf("slot pinned to the old home after the move = %v", c)
	}
}
