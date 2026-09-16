package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/logbuf"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/phoneapi"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/internal/site"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Two simulated radios (LongFast main + MediumFast extra) behind one web server.
func testWebTwoRadios(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := testWebTwoRadiosHosts(t)
	return srv
}

func testWebTwoRadiosHosts(t *testing.T) (*httptest.Server, []*mesh.Host) {
	t.Helper()
	_, srv, hosts := newSiteServer(t)
	return srv, hosts
}

// newSiteServer is the two-radio server itself, its test server and the radios' hosts.
func newSiteServer(t *testing.T) (*Server, *httptest.Server, []*mesh.Host) {
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
		h.SetHoster(&fakeHoster{remote: &fakeRemote{}})
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
	return s, srv, hosts
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

// setupAndSignIn finishes setup with the test password and returns a session token.
func setupAndSignIn(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	return obj["token"].(string)
}

// objectAt is a nested object in a decoded JSON response.
func objectAt(v any, key string) map[string]any {
	return v.(map[string]any)[key].(map[string]any)
}

func TestConfigPositionHardwareMQTTPerRadio(t *testing.T) {
	srv := testWebTwoRadios(t)
	tok := setupAndSignIn(t, srv)

	code, cfg, _ := call(t, srv, "GET", "/api/v1/config?radio=mf", tok, nil)
	if code != 200 || cfg["radio_id"] != "mf" || cfg["main"] != false || cfg["hardware"].(map[string]any)["hw_model"] != "AUTO" {
		t.Fatalf("mf config %d %v", code, cfg)
	}
	checkPutMFConfig(t, srv, tok)
	checkTwoMQTTConnections(t, srv, tok)
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

// checkPutMFConfig sets mf's position, hardware and a single MQTT connection (the old shape).
func checkPutMFConfig(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
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
	if objectAt(out, "hardware")["effective"] != "HELTEC_V3" || objectAt(out, "position")["precision_bits"] != float64(16) {
		t.Fatalf("mf config after put = %v", out)
	}
	m := out["mqtt"].([]any)[0].(map[string]any)
	if m["password"] != "" || m["password_set"] != true || m["root"] != "msh/EU_868/Scotland" || m["key"] != "public" || m["mode"] != "gateway" {
		t.Fatalf("mqtt dto leaked or lost the password: %v", m)
	}
}

// checkTwoMQTTConnections adds a second connection to mf, keeping the first one's password, and
// checks a bridge needs its acknowledgement.
func checkTwoMQTTConnections(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	// a second connection; the first keeps its password when the field comes back empty
	code, res, _ := call(t, srv, "PUT", "/api/v1/config?radio=mf", tok, map[string]any{"mqtt": []any{
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
}

func TestAddRenameRemoveRadios(t *testing.T) {
	srv := testWebTwoRadios(t)
	tok := setupAndSignIn(t, srv)
	checkAddRadio(t, srv, tok)
	checkEditPendingRadio(t, srv, tok)
	checkRenameRadios(t, srv, tok)
	checkRemoveRadios(t, srv, tok)
	if code, res, _ := call(t, srv, "PUT", "/api/v1/site", tok, map[string]any{"duty_cycle_percent": 8}); code != 200 ||
		res["running_duty_cycle_percent"] != float64(8) || res["restart_required"] != false {
		t.Fatalf("site %d %v", code, res)
	}
}

// checkAddRadio adds the ls radio, refusing a bad or duplicate id, and checks it's pending.
func checkAddRadio(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
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
	if len(pending) != 1 || pending[0].(map[string]any)["id"] != "ls" || pending[0].(map[string]any)["relay_role"] != "client_mute" || list["restart_required"] != true {
		t.Fatalf("pending = %v", list)
	}
}

// checkEditPendingRadio checks a radio that hasn't started can be changed and a running one can't
// be changed this way.
func checkEditPendingRadio(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	if code, res, _ := call(t, srv, "PUT", "/api/v1/radios/ls", tok, map[string]any{"name": "Long Slow", "driver": "none", "device": "",
		"region": "EU_868", "preset": "SHORT_FAST", "tx_power_dbm": 14, "relay_role": "client"}); code != 200 {
		t.Fatalf("edit pending radio %d %v", code, res)
	}
	_, list, _ := call(t, srv, "GET", "/api/v1/radios", tok, nil)
	if p := list["pending"].([]any)[0].(map[string]any); p["preset"] != "SHORT_FAST" || p["name"] != "Long Slow" || p["relay_role"] != "client" || p["tx_power_dbm"] != float64(14) {
		t.Fatalf("pending after edit = %v", p)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/radios/ls", tok, map[string]any{"driver": "none", "preset": "NOPE"}); code != 400 {
		t.Fatalf("bad preset accepted: %d", code)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/radios/mf", tok, map[string]any{"preset": "LONG_FAST"}); code != 409 {
		t.Fatalf("running radio edited through PUT /radios: %d", code)
	}
}

// checkRenameRadios renames mf and the main radio.
func checkRenameRadios(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	if code, res, _ := call(t, srv, "PATCH", "/api/v1/radios/mf", tok, map[string]any{"name": "Medium Fast"}); code != 200 || res["name"] != "Medium Fast" {
		t.Fatalf("rename mf %d %v", code, res)
	}
	if code, _, _ := call(t, srv, "PATCH", "/api/v1/radios/main", tok, map[string]any{"name": "LongFast"}); code != 200 {
		t.Fatalf("rename main %d", code)
	}
	_, list, _ := call(t, srv, "GET", "/api/v1/radios", tok, nil)
	radios := list["radios"].([]any)
	if radios[0].(map[string]any)["name"] != "LongFast" || radios[1].(map[string]any)["name"] != "Medium Fast" {
		t.Fatalf("names = %v", radios)
	}
}

// checkRemoveRadios removes the pending and the running extra radio; the main radio stays.
func checkRemoveRadios(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	if code, _, _ := call(t, srv, "DELETE", "/api/v1/radios/main", tok, nil); code != 400 {
		t.Fatalf("main radio removed: %d", code)
	}
	if code, res, _ := call(t, srv, "DELETE", "/api/v1/radios/ls", tok, nil); code != 200 || res["restart_required"] != false {
		t.Fatalf("remove a radio that never started %d %v", code, res)
	}
	if code, res, _ := call(t, srv, "DELETE", "/api/v1/radios/mf", tok, nil); code != 200 || res["restart_required"] != true {
		t.Fatalf("remove running mf %d %v", code, res)
	}
}

func TestMoveIdentityBetweenRadios(t *testing.T) {
	srv := testWebTwoRadios(t)
	tok := setupAndSignIn(t, srv)

	_, desk, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "short_name": "DESK", "api_port": 4460,
		"private_key": freeLastByteKey(t, srv, tok)})
	deskID := desk["node_id"].(string)
	if desk["radio_id"] != "main" {
		t.Fatalf("new identity radio = %v", desk["radio_id"])
	}
	checkMoveWithMessages(t, srv, tok)
	checkMoveRefused(t, srv, tok, deskID)
	checkMoveDesk(t, srv, tok, deskID)
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

// checkMoveWithMessages moves an identity with a message just sent to mf and back: its chat moves too.
func checkMoveWithMessages(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	_, busy, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Busy", "api_port": 4462,
		"private_key": freeLastByteKey(t, srv, tok)})
	busyID := busy["node_id"].(string)
	if code, _, _ := call(t, srv, "POST", "/api/v1/identities/"+busyID+"/messages", tok, map[string]any{"channel": 0, "text": "queued", "want_ack": false}); code >= 300 {
		t.Fatalf("send %d", code)
	}
	if code, res, _ := call(t, srv, "POST", "/api/v1/identities/"+busyID+"/move", tok, map[string]any{"radio_id": "mf"}); code != 200 {
		t.Fatalf("move busy %d %v", code, res)
	}
	_, _, msgs := call(t, srv, "GET", "/api/v1/identities/"+busyID+"/messages?conversation=ch:0", tok, nil)
	// the chat moves with it (its node already has the message: still queued)
	if st := msgs[0].(map[string]any)["status"]; len(msgs) != 1 || st != "queued" || msgs[0].(map[string]any)["text"] != "queued" {
		t.Fatalf("busy identity's messages after the move = %v", msgs)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/identities/"+busyID+"/move", tok, map[string]any{"radio_id": "main"}); code != 200 {
		t.Fatalf("move busy back %d", code)
	}
}

// checkMoveRefused checks relay personas stay put and unknown radios are refused.
func checkMoveRefused(t *testing.T, srv *httptest.Server, tok, deskID string) {
	t.Helper()
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
}

// checkMoveDesk moves Desk to mf, keeping its number and port, with mf's primary channel.
func checkMoveDesk(t *testing.T, srv *httptest.Server, tok, deskID string) {
	t.Helper()
	code, moved, _ := call(t, srv, "POST", "/api/v1/identities/"+deskID+"/move", tok, map[string]any{"radio_id": "mf"})
	if code != 200 || moved["radio_id"] != "mf" || moved["node_id"] != deskID || objectAt(moved, "api")["port"] != float64(4460) {
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
}

func TestConfigurationGaps(t *testing.T) {
	srv := testWebTwoRadios(t)
	tok := setupAndSignIn(t, srv)

	_, cfg, _ := call(t, srv, "GET", "/api/v1/config", tok, nil)
	air := cfg["airtime"].(map[string]any)
	if air["telemetry_interval"] != "off" || air["cw_min"] != float64(3) || objectAt(cfg, "radio")["hop_limit"] != float64(3) {
		t.Fatalf("config = %v", cfg)
	}
	if _, has := air["position"]; has {
		t.Fatal("the unsaved airtime.position control is still in the config")
	}
	checkPutMainConfig(t, srv, tok)
	// Relay roles and rebroadcast modes use Meshtastic's names; the old "mute" still reads.
	if objectAt(cfg, "relay")["rebroadcast"] != "all" {
		t.Fatalf("unset rebroadcast = %v", cfg["relay"])
	}
	checkRelayRoles(t, srv, tok)
	checkBadConfigRefused(t, srv, tok)
	checkUDPPerRadio(t, srv, tok)
	checkBackupAllRadios(t, srv, tok)
}

// checkPutMainConfig saves airtime, radio and web settings on the main radio.
func checkPutMainConfig(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	code, res, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{
		"airtime": map[string]any{"duty_cycle_percent": 10, "identity_share_percent": 25, "nodeinfo_interval": "3h", "telemetry_interval": "3h", "override_duty_cycle": false},
		"radio":   map[string]any{"type": "none", "port": "", "region": "EU_868", "preset": "LONG_FAST", "tx_power_dbm": 22, "hop_limit": 2, "baud": 115200},
		"web":     map[string]any{"bind": "0.0.0.0", "port": 8080, "session_ttl": "24h", "log_level": "debug", "mdns": false, "map_tile_url": "https://tiles.example/{z}/{x}/{y}.png"},
	})
	if code != 200 {
		t.Fatalf("put %d %v", code, res)
	}
	out := res["config"].(map[string]any)
	if objectAt(out, "airtime")["telemetry_interval"] != "3h" || objectAt(out, "radio")["hop_limit"] != float64(2) ||
		objectAt(out, "web")["log_level"] != "debug" || objectAt(out, "web")["map_tile_url"] != "https://tiles.example/{z}/{x}/{y}.png" {
		t.Fatalf("after put = %v", out)
	}
}

// checkRelayRoles sets relay roles and rebroadcast modes by their Meshtastic and older names.
func checkRelayRoles(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	relay := func(role, rebroadcast string) (int, map[string]any) {
		code, res, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{
			"relay": map[string]any{"role": role, "rebroadcast": rebroadcast, "long_name": "RT Relay", "short_name": "RTR", "local_dm": "software"}})
		return code, res
	}
	if code, res := relay("mute", "LOCAL_ONLY"); code != 200 || objectAt(res["config"], "relay")["role"] != "client_mute" ||
		objectAt(res["config"], "relay")["rebroadcast"] != "local_only" {
		t.Fatalf("relay put %d %v", code, res)
	}
	if code, res := relay("router_late", "all"); code != 200 || objectAt(res["config"], "relay")["rebroadcast"] != "all" {
		t.Fatalf("router_late %d %v", code, res)
	}
	if _, st, _ := call(t, srv, "GET", "/api/v1/status", tok, nil); objectAt(st, "relay")["role"] != "router_late" {
		t.Fatalf("live role = %v", st["relay"])
	}
	for _, bad := range [][2]string{{"repeater", "all"}, {"client", "sometimes"}} {
		if code, _ := relay(bad[0], bad[1]); code != 400 {
			t.Errorf("relay %v accepted: %d", bad, code)
		}
	}
}

// checkBadConfigRefused checks a too-short telemetry interval and a non-HTTP tile URL are refused.
func checkBadConfigRefused(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	if code, _, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{"airtime": map[string]any{"telemetry_interval": "5m", "duty_cycle_percent": 10, "identity_share_percent": 25, "nodeinfo_interval": "3h"}}); code != 400 {
		t.Fatalf("5 minute telemetry accepted: %d", code)
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/config", tok, map[string]any{"web": map[string]any{"bind": "0.0.0.0", "port": 8080, "session_ttl": "24h", "map_tile_url": "ftp://nope"}}); code != 400 {
		t.Fatalf("bad tile URL accepted: %d", code)
	}
}

// checkUDPPerRadio checks UDP multicast is per radio, and a pending change shows up as a restart reason.
func checkUDPPerRadio(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
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
}

// checkBackupAllRadios checks a backup carries every radio's identities and restores, and that a
// path-like radio id is refused.
func checkBackupAllRadios(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "MF Desk", "radio_id": "mf"})
	_, b, _ := call(t, srv, "GET", "/api/v1/backup", tok, nil)
	if len(b["identities"].([]any)) != 1 || len(b["radio_identities"].(map[string]any)["mf"].([]any)) != 2 {
		t.Fatalf("backup = identities %v radio_identities %v", b["identities"], b["radio_identities"])
	}
	if code, res, _ := call(t, srv, "POST", "/api/v1/restore", tok, b); code != 200 {
		t.Fatalf("restore %d %v", code, res)
	}
	_, st, _ := call(t, srv, "GET", "/api/v1/status", tok, nil)
	if !strings.Contains(fmt.Sprint(st["restart_reasons"]), "restored backup") {
		t.Fatalf("restore isn't a restart reason: %v", st["restart_reasons"])
	}
	b["radio_identities"].(map[string]any)["../evil"] = []any{}
	if code, _, _ := call(t, srv, "POST", "/api/v1/restore", tok, b); code != 400 {
		t.Fatalf("restore with a path-like radio id accepted: %d", code)
	}
}

func TestSiteWideViews(t *testing.T) {
	srv, hosts := testWebTwoRadiosHosts(t)
	tok := setupAndSignIn(t, srv)

	// A node heard on both radios, best on mf; one heard on main only.
	now := time.Now()
	hosts[0].DB.Update(0x0badcafe, func(e *mesh.NodeEntry) { e.LastHeard, e.SNR, e.RSSI = now.Add(-time.Minute), 1, -100 })
	hosts[1].DB.Update(0x0badcafe, func(e *mesh.NodeEntry) { e.LastHeard, e.SNR, e.RSSI, e.HopsAway = now, 7, -90, 0 })
	hosts[0].DB.Update(0x11112222, func(e *mesh.NodeEntry) { e.LastHeard = now })
	for i, h := range hosts {
		h.LogInternal(&pb.MeshPacket{From: 1, To: 2, Id: uint32(100 + i)}, nil)
	}
	checkSiteNodes(t, srv, tok)
	checkSitePacketsAndLinks(t, srv, tok)
	statuses := siteStreamStatuses(t, srv, tok)
	if !statuses["main"] || !statuses["mf"] {
		t.Fatalf("statuses for %v", statuses)
	}
}

// checkSiteNodes checks the site's node list merges sightings from both radios.
func checkSiteNodes(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	_, _, nodes := call(t, srv, "GET", "/api/v1/nodes?radio=all", tok, nil)
	seen := map[string][]any{}
	for _, n := range nodes {
		m := n.(map[string]any)
		seen[m["node_id"].(string)] = m["heard_by"].([]any)
		if m["node_id"] == "!0badcafe" && m["snr"] != float64(7) {
			t.Errorf("merged node should be the freshest sighting: %v", m)
		}
	}
	if fmt.Sprint(seen["!0badcafe"]) != "[main mf]" || fmt.Sprint(seen["!11112222"]) != "[main]" {
		t.Fatalf("heard_by = %v", seen)
	}
	if _, _, one := call(t, srv, "GET", "/api/v1/nodes?radio=mf", tok, nil); len(one) >= len(nodes) {
		t.Fatalf("one radio's nodes (%d) should be fewer than the site's (%d)", len(one), len(nodes))
	}
}

// checkSitePacketsAndLinks checks the site's packets and links come from both radios, main first.
func checkSitePacketsAndLinks(t *testing.T, srv *httptest.Server, tok string) {
	t.Helper()
	_, _, pkts := call(t, srv, "GET", "/api/v1/packets?radio=all", tok, nil)
	radios := map[any]bool{}
	for _, p := range pkts {
		radios[p.(map[string]any)["radio_id"]] = true
	}
	if !radios["main"] || !radios["mf"] {
		t.Fatalf("packets from %v", radios)
	}
	_, _, links := call(t, srv, "GET", "/api/v1/links?radio=all", tok, nil)
	if len(links) < 2 || links[0].(map[string]any)["radio_id"] != "main" || links[len(links)-1].(map[string]any)["radio_id"] != "mf" {
		t.Fatalf("links = %v", links)
	}
}

// siteStreamStatuses reads the site-wide event stream until it has a status from two radios,
// returning the radios it heard from.
func siteStreamStatuses(t *testing.T, srv *httptest.Server, tok string) map[string]bool {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/events?radio=all", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	statuses := map[string]bool{}
	for sc.Scan() && len(statuses) < 2 {
		if id := statusRadio(sc.Text()); id != "" {
			statuses[id] = true
		}
	}
	return statuses
}

// statusRadio is the radio id of a status event's data line, or "" for any other line.
func statusRadio(line string) string {
	if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, `"radio_id"`) {
		return ""
	}
	var st map[string]any
	if json.Unmarshal([]byte(line[6:]), &st) != nil || st["phy"] == nil {
		return ""
	}
	return st["radio_id"].(string)
}
