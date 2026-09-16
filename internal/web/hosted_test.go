package web

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// fakeRemote is a node that takes everything.
type fakeRemote struct {
	mu     sync.Mutex
	admins int
}

func (f *fakeRemote) SendPacket(*pb.MeshPacket) (uint32, error) { return 1, nil }
func (f *fakeRemote) Admin(context.Context, *pb.AdminMessage) (*pb.AdminMessage, error) {
	f.mu.Lock()
	f.admins++
	f.mu.Unlock()
	return nil, nil
}
func (f *fakeRemote) ApplyConfig(context.Context, mesh.Config) error {
	f.mu.Lock()
	f.admins++
	f.mu.Unlock()
	return nil
}

// fakeHoster hosts every identity that isn't the relay or routed across radios.
type fakeHoster struct {
	mu       sync.Mutex
	unhosted []uint32
	remote   *fakeRemote
}

func (x *fakeHoster) Takes(rec mesh.IdentityRecord) bool {
	return !rec.IsRelay && rec.MultiRadio == nil
}

func (x *fakeHoster) HostIdentity(_ context.Context, h *mesh.Host, rec mesh.IdentityRecord) (*mesh.Identity, error) {
	if !x.Takes(rec) {
		return nil, mesh.ErrRunHere
	}
	st, err := mesh.RecordState(rec)
	if err != nil {
		return nil, err
	}
	id, err := mesh.NewHostedIdentity(x.remote, st, rec)
	if err != nil {
		return nil, err
	}
	return id, h.AddIdentity(id)
}

func (x *fakeHoster) Unhost(id *mesh.Identity) {
	x.mu.Lock()
	x.unhosted = append(x.unhosted, id.NodeNum)
	x.mu.Unlock()
}

func TestHostedIdentities(t *testing.T) {
	srv, hosts := testWebTwoRadiosHosts(t)
	hs := &fakeHoster{remote: &fakeRemote{}}
	hosts[0].SetHoster(hs)
	call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse"})
	_, obj, _ := call(t, srv, "POST", "/api/v1/auth/login", "", map[string]any{"password": "correct horse"})
	tok := obj["token"].(string)

	if code, res, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Rep", "role": "CLIENT"}); code != 400 {
		t.Fatalf("a repeating role on meshtasticd accepted: %d %v", code, res)
	}
	// A key whose last byte is free on the other radio too, so the moves below can't clash.
	_, _, mfIDs := call(t, srv, "GET", "/api/v1/identities?radio=mf", tok, nil)
	taken := map[uint8]bool{}
	for _, x := range mfIDs {
		taken[uint8(x.(map[string]any)["last_byte"].(float64))] = true
	}
	var key string
	for {
		id, _ := mesh.NewIdentity(nil, "Desk", "DESK")
		if !taken[wire.LastByte(id.NodeNum)] {
			key = base64.StdEncoding.EncodeToString(id.PrivateKey)
			break
		}
	}
	code, a, _ := call(t, srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "short_name": "DESK", "private_key": key})
	if code != 201 || a["hosted"] != true || a["real_node"] != true || a["role"] != "CLIENT_MUTE" {
		t.Fatalf("create %d %v", code, a)
	}
	node := a["node_id"].(string)
	if code, k, _ := call(t, srv, "GET", "/api/v1/identities/"+node+"/key", tok, nil); code != 200 || k["private_key"] == "" {
		t.Fatalf("key of a hosted identity %d %v", code, k)
	}
	if code, _, _ := call(t, srv, "PATCH", "/api/v1/identities/"+node, tok, map[string]any{"role": "ROUTER"}); code != 400 {
		t.Fatalf("repeating role accepted on patch: %d", code)
	}
	if code, res, _ := call(t, srv, "PATCH", "/api/v1/identities/"+node, tok, map[string]any{"role": "TRACKER", "hop_limit": 2}); code != 200 || res["hop_limit"] != float64(2) {
		t.Fatalf("patch %d %v", code, res)
	}
	if hs.remote.admins == 0 {
		t.Fatal("settings not pushed to the node")
	}
	if code, _, _ := call(t, srv, "PATCH", "/api/v1/identities/"+node, tok, map[string]any{"multi_radio": map[string]any{"default_radio": "mf"}}); code != 409 {
		t.Fatalf("multi-radio routing accepted for a hosted identity: %d", code)
	}

	// Moving it to a radio without meshtasticd runs it there, with the same number.
	code, m, _ := call(t, srv, "POST", "/api/v1/identities/"+node+"/move", tok, map[string]any{"radio_id": "mf"})
	if code != 200 || m["node_id"] != node || m["hosted"] != false || m["hop_limit"] != float64(2) {
		t.Fatalf("move %d %v", code, m)
	}
	if len(hs.unhosted) != 1 {
		t.Fatalf("the node wasn't stopped on the move: %v", hs.unhosted)
	}
	// And back again onto meshtasticd; deleting it stops the node.
	if code, m, _ := call(t, srv, "POST", "/api/v1/identities/"+node+"/move?radio=mf", tok, map[string]any{"radio_id": "main"}); code != 200 || m["hosted"] != true {
		t.Fatalf("move back %d %v", code, m)
	}
	if code, _, _ := call(t, srv, "DELETE", "/api/v1/identities/"+node, tok, nil); code != 204 {
		t.Fatalf("delete %d", code)
	}
	if len(hs.unhosted) != 2 {
		t.Fatalf("the node wasn't stopped on delete: %v", hs.unhosted)
	}

	// Hosted identities need a hosted persona.
	if code, _, _ := call(t, srv, "PUT", "/api/v1/hosted", tok, map[string]any{"identities": true}); code != 400 {
		t.Fatalf("identities without a persona accepted: %d", code)
	}
	if _, h, _ := call(t, srv, "GET", "/api/v1/hosted", tok, nil); h["identities"] != false {
		t.Fatalf("hosted = %v", h)
	}
}

func TestSetupMeshtasticBoard(t *testing.T) {
	srv := testWebTwoRadios(t)
	// Before a password exists, a board's address must be local.
	if code, res, _ := call(t, srv, "POST", "/api/v1/setup/probe", "", map[string]any{"driver": "meshtastic", "device": "8.8.8.8"}); code != 400 {
		t.Fatalf("public board address probed before setup: %d %v", code, res)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/setup/probe", "", map[string]any{"driver": "meshtastic", "device": "/etc/passwd"}); code != 400 {
		t.Fatalf("non-serial path probed: %d", code)
	}
	// A local address that nothing answers on is reported, not refused.
	if code, res, _ := call(t, srv, "POST", "/api/v1/setup/probe", "", map[string]any{"driver": "meshtastic", "device": "127.0.0.1:1"}); code != 200 || res["ok"] != false || res["error"] == "" {
		t.Fatalf("probe of a closed port: %d %v", code, res)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse", "driver": "meshtastic"}); code != 400 {
		t.Fatalf("board without a device accepted: %d", code)
	}
	code, res, _ := call(t, srv, "POST", "/api/v1/setup", "", map[string]any{"password": "correct horse", "driver": "meshtastic", "device": "127.0.0.1:4403"})
	if code != 200 || res["restart_required"] != true {
		t.Fatalf("setup with a board %d %v", code, res)
	}
}
