package web

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/logbuf"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func TestDecodeKey(t *testing.T) {
	raw := make([]byte, 32)
	raw[0] = 0xfb // makes the two encodings differ
	for _, s := range []string{base64.StdEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(raw), " " + base64.StdEncoding.EncodeToString(raw) + " "} {
		if b, err := decodeKey(s); err != nil || len(b) != 32 || b[0] != 0xfb {
			t.Errorf("decodeKey(%q) = %v, %v", s, b, err)
		}
	}
	if b, err := decodeKey(""); b != nil || err != nil {
		t.Errorf("empty key = %v, %v", b, err)
	}
	for _, s := range []string{"AQ==", "!!!"} {
		if _, err := decodeKey(s); err == nil {
			t.Errorf("decodeKey(%q) accepted", s)
		}
	}
}

func TestPreviewKeyCollisions(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	path := "/api/v1/identities/preview-key"
	if code, _, _ := call(t, env.srv, "POST", path, tok, map[string]any{"private_key": "AQ=="}); code != 400 {
		t.Fatalf("bad key: %d", code)
	}
	if code, _ := env.do(t, "POST", path, tok, "application/json", "nope"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
	if code, obj, _ := call(t, env.srv, "POST", path, tok, map[string]any{}); code != 200 || obj["private_key"] == "" {
		t.Fatalf("preview a new key: %d %v", code, obj)
	}
	_, fresh, _ := call(t, env.srv, "POST", path, tok, map[string]any{"private_key": keyWithLastByte(t, freeLastByte(env.host))})
	if fresh["collision"] != nil {
		t.Fatalf("free key collides: %v", fresh)
	}
	// A heard node with the same last byte collides.
	num := uint32(fresh["node_num"].(float64))
	other := num ^ 0x100
	env.host.DB.Update(other, func(e *mesh.NodeEntry) {})
	_, pv, _ := call(t, env.srv, "POST", path, tok, map[string]any{"private_key": fresh["private_key"]})
	if pv["collision"] != wire.NodeID(other) || pv["node_id"] != fresh["node_id"] {
		t.Fatalf("preview with a heard clash = %v", pv)
	}
	// The key of an identity already here collides with itself.
	_, key := relayKey(t, env)
	_, pv, _ = call(t, env.srv, "POST", path, tok, map[string]any{"private_key": key})
	if pv["collision"] != env.host.Relay().NodeID() {
		t.Fatalf("preview of the relay's key = %v", pv)
	}
}

// relayKey is the relay persona's node id and base64 private key.
func relayKey(t *testing.T, env *testEnv) (string, string) {
	t.Helper()
	r := env.host.Relay()
	return r.NodeID(), base64.StdEncoding.EncodeToString(r.PrivateKey)
}

// keyWithLastByte generates keys until one's node number ends in b and returns it.
func keyWithLastByte(t *testing.T, b uint8) string {
	t.Helper()
	for i := 0; i < 100000; i++ {
		id, err := mesh.NewIdentity(nil, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if wire.LastByte(id.NodeNum) == b {
			return base64.StdEncoding.EncodeToString(id.PrivateKey)
		}
	}
	t.Fatalf("no key ending in %02x", b)
	return ""
}

func TestCreateIdentityRefusals(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	_, desk, _ := call(t, env.srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "api_port": 4500})
	_, relayKeyB64 := relayKey(t, env)
	clash := keyWithLastByte(t, wire.LastByte(env.host.Relay().NodeNum))
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown radio", map[string]any{"long_name": "X", "radio_id": "nope"}, 400},
		{"blank name", map[string]any{"long_name": "  "}, 400},
		{"bad key", map[string]any{"long_name": "X", "private_key": "AQ=="}, 400},
		{"key already here", map[string]any{"long_name": "X", "private_key": relayKeyB64}, 409},
		{"port in use", map[string]any{"long_name": "X", "api_port": 4500}, 409},
		{"bad hop limit", map[string]any{"long_name": "X", "hop_limit": 99}, 400},
		{"bad role", map[string]any{"long_name": "X", "role": "NOPE"}, 400},
		{"last byte taken", map[string]any{"long_name": "X", "private_key": clash}, 409},
	}
	for _, c := range cases {
		if code, obj, _ := call(t, env.srv, "POST", "/api/v1/identities", tok, c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, obj, c.want)
		}
	}
	if code, _ := env.do(t, "POST", "/api/v1/identities", tok, "application/json", "nope"); code != 400 {
		t.Errorf("bad body: %d", code)
	}
	if api := desk["api"].(map[string]any); api["port"] != float64(4500) || api["bind"] != "0.0.0.0" {
		t.Errorf("desk api = %v", api)
	}
}

func TestRestartAPIAndGetKey(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	deskID, _ := newDesk(t, env, tok)
	relayID := env.host.Relay().NodeID()
	cases := map[string]int{
		"/api/v1/identities/" + relayID + "/api/restart": http.StatusConflict,
		"/api/v1/identities/" + deskID + "/api/restart":  http.StatusNoContent,
		"/api/v1/identities/zzz/api/restart":             http.StatusBadRequest,
		"/api/v1/identities/!0000dead/api/restart":       http.StatusNotFound,
	}
	for path, want := range cases {
		if code, _, _ := call(t, env.srv, "POST", path, tok, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	// A real node that keeps its own key has none to give.
	node, err := mesh.NewRemoteIdentity(&fakeRemote{}, mesh.RemoteState{NodeNum: 0x0bad0001, User: &pb.User{LongName: "Board"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.host.AddIdentity(node); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/identities/"+node.NodeID()+"/key", tok, nil); code != http.StatusNotFound {
		t.Fatalf("key of a board node: %d", code)
	}
	if code, k, _ := call(t, env.srv, "GET", "/api/v1/identities/"+relayID+"/key", tok, nil); code != 200 || k["public_key"] == "" {
		t.Fatalf("relay key: %d %v", code, k)
	}
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/identities/!0000dead/key", tok, nil); code != http.StatusNotFound {
		t.Fatalf("key of a stranger: %d", code)
	}
}

func TestDeleteIdentityRefusals(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/identities/"+env.host.Relay().NodeID(), tok, nil); code != http.StatusConflict {
		t.Fatalf("delete the relay: %d", code)
	}
	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/identities/zzz", tok, nil); code != http.StatusBadRequest {
		t.Fatalf("delete a bad id: %d", code)
	}
}

func TestPatchIdentityChecks(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	deskID, _ := newDesk(t, env, tok)
	call(t, env.srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Other", "api_port": 4600})
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown role", map[string]any{"role": "NOPE"}, 400},
		{"position out of range", map[string]any{"position": map[string]any{"latitude": 91, "longitude": 0}}, 400},
		{"position at 0,0", map[string]any{"position": map[string]any{"latitude": 0, "longitude": 0}}, 400},
		{"position not an object", map[string]any{"position": "north"}, 400},
		{"short position interval", map[string]any{"position_secs": 60}, 400},
		{"hop limit", map[string]any{"hop_limit": 8}, 400},
		{"share", map[string]any{"share_limit_pct": 101}, 400},
		{"bind", map[string]any{"api_bind": "localhost"}, 400},
		{"port range", map[string]any{"api_port": 0}, 400},
		{"port taken", map[string]any{"api_port": 4600}, 409},
	}
	for _, c := range cases {
		if code, obj, _ := call(t, env.srv, "PATCH", "/api/v1/identities/"+deskID, tok, c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, obj, c.want)
		}
	}
	// A free port and address, and the other settings, all apply at once.
	code, res, _ := call(t, env.srv, "PATCH", "/api/v1/identities/"+deskID, tok, map[string]any{"api_port": 4601, "api_bind": " 127.0.0.1 ",
		"enabled": false, "share_limit_pct": 12.5, "position_secs": 3600, "position": map[string]any{"latitude": 55.9, "longitude": -3.2, "altitude": 10}})
	if code != 200 {
		t.Fatalf("patch: %d %v", code, res)
	}
	api := res["api"].(map[string]any)
	pos, _ := res["position"].(map[string]any)
	if api["port"] != float64(4601) || api["bind"] != "127.0.0.1" || res["enabled"] != false || res["share_limit_pct"] != 12.5 ||
		res["position_secs"] != float64(3600) || pos == nil {
		t.Fatalf("patched = %v", res)
	}
	code, res, _ = call(t, env.srv, "PATCH", "/api/v1/identities/"+deskID, tok, map[string]any{"position": nil})
	if code != 200 || res["position"] != nil {
		t.Fatalf("position removed: %d %v", code, res["position"])
	}
	if code, _ := env.do(t, "PATCH", "/api/v1/identities/"+deskID, tok, "application/json", "nope"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
}

func TestPatchRelayAndLocalIdentity(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	relayID := env.host.Relay().NodeID()
	// The relay persona ignores role, enabled and port changes but takes a new name.
	code, res, _ := call(t, env.srv, "PATCH", "/api/v1/identities/"+relayID, tok, map[string]any{"role": "NOPE", "enabled": false,
		"api_port": 1, "long_name": "Mast", "short_name": "MST"})
	if code != 200 || res["long_name"] != "Mast" || res["short_name"] != "MST" || res["enabled"] != true || res["api"] != nil {
		t.Fatalf("relay patch: %d %v", code, res)
	}
	if e, ok := env.host.DB.Get(env.host.Relay().NodeNum); !ok || e.User.GetLongName() != "Mast" {
		t.Fatalf("node DB entry = %v", e.User)
	}
}

func TestMoveIdentityRefusals(t *testing.T) {
	_, srv, hosts := newSiteServer(t)
	tok := setupAndSignIn(t, srv)
	free := freeLastByte(hosts...)
	_, desk, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "private_key": keyWithLastByte(t, free)})
	deskID := desk["node_id"].(string)
	move := func(id string, body any) (int, map[string]any) {
		code, obj, _ := call(t, srv, "POST", "/api/v1/identities/"+id+"/move", tok, body)
		return code, obj
	}
	if code, _ := move(deskID, map[string]any{"radio_id": "nope"}); code != 400 {
		t.Errorf("unknown radio: %d", code)
	}
	if code, obj := move(deskID, map[string]any{"radio_id": "main"}); code != 200 || obj["radio_id"] != "main" {
		t.Errorf("move to its own radio: %d %v", code, obj)
	}
	if code, _ := move(hosts[0].Relay().NodeID(), map[string]any{"radio_id": "mf"}); code != 409 {
		t.Errorf("move the relay: %d", code)
	}
	if code, _ := move(deskID, "nope"); code != 400 {
		t.Errorf("bad body: %d", code)
	}
	if code, _ := move("!0000dead", map[string]any{"radio_id": "mf"}); code != 404 {
		t.Errorf("move a stranger: %d", code)
	}
	// Same last byte as mf's relay: can't share its radio.
	if mfByte := wire.LastByte(hosts[1].Relay().NodeNum); mfByte != wire.LastByte(hosts[0].Relay().NodeNum) {
		_, clash, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Clash", "private_key": keyWithLastByte(t, mfByte)})
		if code, obj := move(clash["node_id"].(string), map[string]any{"radio_id": "mf"}); code != 409 || !strings.Contains(obj["error"].(string), "last byte") {
			t.Errorf("last-byte clash: %d %v", code, obj)
		}
	}
	checkMoveRepeaterRefused(t, srv, tok, hosts[0])
	checkMoveWithoutHoster(t, srv, tok, hosts, deskID)
}

// freeLastByte is a last byte no identity on the hosts has.
func freeLastByte(hosts ...*mesh.Host) uint8 {
	used := map[uint8]bool{}
	for _, h := range hosts {
		for _, id := range h.Identities() {
			used[wire.LastByte(id.NodeNum)] = true
		}
	}
	b := uint8(1)
	for used[b] {
		b++
	}
	return b
}

// checkMoveRepeaterRefused checks an identity that repeats can't move onto meshtasticd.
func checkMoveRepeaterRefused(t *testing.T, srv *httptest.Server, tok string, main *mesh.Host) {
	t.Helper()
	key, _ := decodeKey(keyWithLastByte(t, freeLastByte(main)))
	rep, _ := mesh.NewIdentity(key, "Repeater", "REP")
	_ = rep.SetRole("ROUTER")
	if err := main.AddIdentity(rep); err != nil {
		t.Fatal(err)
	}
	code, obj, _ := call(t, srv, "POST", "/api/v1/identities/"+rep.NodeID()+"/move", tok, map[string]any{"radio_id": "mf"})
	if code != 409 || !strings.Contains(obj["error"].(string), "never repeats") {
		t.Errorf("move a repeater: %d %v", code, obj)
	}
}

// checkMoveWithoutHoster checks a move to a radio that can't run the node puts it back as it was.
func checkMoveWithoutHoster(t *testing.T, srv *httptest.Server, tok string, hosts []*mesh.Host, deskID string) {
	t.Helper()
	hosts[1].SetHoster(nil)
	num := mustNum(t, deskID)
	hosts[0].Messages.Add(num, &mesh.Message{ID: 5, From: "!12345678", To: deskID, Text: "keep me", Direction: "in"})
	if code, _, _ := call(t, srv, "POST", "/api/v1/identities/"+deskID+"/move", tok, map[string]any{"radio_id": "mf"}); code != 409 {
		t.Fatalf("move without a hoster: %d", code)
	}
	if hosts[0].Identity(num) == nil || hosts[1].Identity(num) != nil {
		t.Fatal("the identity wasn't put back")
	}
	if msgs := hosts[0].Messages.List(num, deskID, "", 0, 10); len(msgs) != 1 {
		t.Fatalf("messages after the failed move = %v", msgs)
	}
}

func TestIdentitySaveFailureIsLogged(t *testing.T) {
	env := newTestEnv(t, func(o *Options) {
		o.Log = slog.New(logbuf.NewHandler(slog.NewTextHandler(io.Discard, nil), o.Logs))
	})
	tok := env.signIn(t)
	// A folder where identities.json goes: the change applies, the failure is logged.
	if err := os.MkdirAll(filepath.Join(env.cfg.StateDir, "identities.json", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := call(t, env.srv, "PATCH", "/api/v1/identities/"+env.host.Relay().NodeID(), tok, map[string]any{"long_name": "Mast"}); code != 200 {
		t.Fatalf("patch: %d", code)
	}
	if !strings.Contains(jsonOf(env.logs.Recent(10)), "saving identities") {
		t.Fatalf("logs = %v", env.logs.Recent(10))
	}
}

func TestAppSettingsSwitch(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	_, created, _ := call(t, env.srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk"})
	if created["app_settings"] != false {
		t.Fatalf("a new identity lets its app change node settings: %v", created["app_settings"])
	}
	path := "/api/v1/identities/" + created["node_id"].(string)
	if code, res, _ := call(t, env.srv, "PATCH", path, tok, map[string]any{"app_settings": true}); code != 200 || res["app_settings"] != true {
		t.Fatalf("patch: %d %v", code, res)
	}
	num, _ := wire.ParseNodeID(created["node_id"].(string))
	if !env.host.Identity(num).Settings().AppSettings || !env.host.Identity(num).Record().AppSettings {
		t.Fatal("switch not kept with the identity")
	}
}
