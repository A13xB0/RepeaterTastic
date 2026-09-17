package web

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"google.golang.org/protobuf/proto"
)

// remoteHoster hosts every identity on one given node.
type remoteHoster struct{ remote mesh.Remote }

func (x remoteHoster) HostIdentity(_ context.Context, h *mesh.Host, rec mesh.IdentityRecord) (*mesh.Identity, error) {
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

func (remoteHoster) Unhost(*mesh.Identity) {}

// preparingRemote is a node that wants every channel prepared before it takes it.
type preparingRemote struct {
	*fakeRemote
	mu       sync.Mutex
	prepared []int32
}

func (p *preparingRemote) PrepareChannel(ch *pb.Channel) {
	p.mu.Lock()
	p.prepared = append(p.prepared, ch.GetIndex())
	p.mu.Unlock()
}

// channelURL encodes channel settings as a Meshtastic channel URL.
func channelURL(settings ...*pb.ChannelSettings) string {
	b, _ := proto.Marshal(&pb.ChannelSet{Settings: settings})
	return "https://meshtastic.org/e/#" + base64.RawURLEncoding.EncodeToString(b)
}

func TestParseChannelURL(t *testing.T) {
	good := channelURL(&pb.ChannelSettings{Name: "Ops", Psk: []byte{1}})
	frag := good[strings.Index(good, "#")+1:]
	// Standard base64 with padding is forgiven.
	std := base64.StdEncoding.EncodeToString(mustDecode(t, frag))
	for _, u := range []string{good, frag, " " + good + " ", "https://meshtastic.org/e/#" + std} {
		set, ok := parseChannelURL(u)
		if !ok || set.Settings[0].GetName() != "Ops" {
			t.Errorf("parse %q = %v %v", u, set, ok)
		}
	}
	for _, u := range []string{"", "https://meshtastic.org/e/#!!!", "https://meshtastic.org/e/#" + base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xff}), channelURL()} {
		if _, ok := parseChannelURL(u); ok {
			t.Errorf("parse %q accepted", u)
		}
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestImportChannelURL(t *testing.T) {
	env := newTestEnv(t, nil)
	remote := &preparingRemote{fakeRemote: &fakeRemote{}}
	env.host.SetHoster(remoteHoster{remote: remote})
	tok := env.signIn(t)
	deskID, deskNum := newDesk(t, env, tok)
	path := "/api/v1/identities/" + deskID + "/channels/url"

	if code, _, _ := call(t, env.srv, "POST", path, tok, map[string]any{"url": "https://example.com"}); code != http.StatusBadRequest {
		t.Fatalf("not a channel URL: %d", code)
	}
	if code, _ := env.do(t, "POST", path, tok, "application/json", "nope"); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/identities/!0000dead/channels/url", tok, map[string]any{"url": ""}); code != http.StatusNotFound {
		t.Fatalf("stranger: %d", code)
	}
	key := []byte("0123456789abcdef")
	u := channelURL(&pb.ChannelSettings{Name: "", Psk: key}, &pb.ChannelSettings{Name: "Ops", Psk: []byte{1}})
	code, obj, _ := call(t, env.srv, "POST", path, tok, map[string]any{"url": u})
	if code != 200 {
		t.Fatalf("import: %d %v", code, obj)
	}
	id := env.host.Identity(deskNum)
	if string(id.ChannelCopy(0).GetSettings().GetPsk()) != string(key) || id.ChannelCopy(1).GetSettings().GetName() != "Ops" {
		t.Fatalf("channels after import: %v %v", id.ChannelCopy(0), id.ChannelCopy(1))
	}
	remote.mu.Lock()
	prepared := append([]int32(nil), remote.prepared...)
	remote.mu.Unlock()
	if len(prepared) != 2 || prepared[0] != 0 || prepared[1] != 1 {
		t.Fatalf("channels pushed to the node = %v", prepared)
	}
	checkImportIntoFullSlots(t, env, tok, deskID, deskNum)
}

// checkImportIntoFullSlots fills every secondary slot and checks a further import skips its channels.
func checkImportIntoFullSlots(t *testing.T, env *testEnv, tok, deskID string, deskNum uint32) {
	t.Helper()
	id := env.host.Identity(deskNum)
	for i := 2; i < mesh.MaxChannels; i++ {
		if freeChannelSlot(id) != i {
			t.Fatalf("free slot = %d, want %d", freeChannelSlot(id), i)
		}
		call(t, env.srv, "PUT", "/api/v1/identities/"+deskID+"/channels/"+strconv.Itoa(i), tok, map[string]any{"name": "C", "psk": "AQ==", "role": "SECONDARY"})
	}
	if freeChannelSlot(id) != -1 {
		t.Fatalf("free slot with all in use = %d", freeChannelSlot(id))
	}
	before := channelSnapshot(id)
	u := channelURL(&pb.ChannelSettings{Name: "Other"}, &pb.ChannelSettings{Name: "More"})
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/identities/"+deskID+"/channels/url", tok, map[string]any{"url": u}); code != 200 {
		t.Fatalf("import into full slots: %d", code)
	}
	if changed := changedChannels(before, id); len(changed) != 0 {
		t.Fatalf("channels changed without a free slot: %v", changed)
	}
}

func TestImportChannelsRefusesBadKeys(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	deskID, deskNum := newDesk(t, env, tok)
	id := env.host.Identity(deskNum)
	long := make([]byte, 33)
	if _, err := importChannels(env.host, id, &pb.ChannelSet{Settings: []*pb.ChannelSettings{{Psk: long}}}); err == nil || !strings.HasPrefix(err.Error(), "primary channel") {
		t.Fatalf("long primary key: %v", err)
	}
	if _, err := importChannels(env.host, id, &pb.ChannelSet{Settings: []*pb.ChannelSettings{{Name: "Other"}, {Name: "Bad", Psk: long}}}); err == nil || !strings.Contains(err.Error(), `"Bad"`) {
		t.Fatalf("long secondary key: %v", err)
	}
	u := channelURL(&pb.ChannelSettings{Name: "Bad", Psk: long})
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/identities/"+deskID+"/channels/url", tok, map[string]any{"url": u}); code != http.StatusBadRequest {
		t.Fatalf("import with a bad key: %d", code)
	}
}

func TestPutChannelChecks(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	deskID, _ := newDesk(t, env, tok)
	base := "/api/v1/identities/" + deskID + "/channels/"
	long := base64.StdEncoding.EncodeToString(make([]byte, 33))
	cases := []struct {
		slot string
		body map[string]any
		want int
	}{
		{"8", map[string]any{"name": "x", "psk": "AQ=="}, 400},
		{"x", map[string]any{"name": "x", "psk": "AQ=="}, 400},
		{"1", map[string]any{"name": "x", "psk": "not base64!"}, 400},
		{"1", map[string]any{"name": "twelve chars", "psk": "AQ=="}, 400},
		{"1", map[string]any{"name": "x", "psk": long}, 400},
		{"1", map[string]any{"name": "x", "psk": "AQ==", "role": "PRIMARY"}, 400},
		{"0", map[string]any{"name": "", "psk": "AQ==", "role": "DISABLED"}, 409},
		{"0", map[string]any{"name": "", "psk": "AQ==", "role": "PRIMARY"}, 200},
		{"1", map[string]any{"name": "Ops", "psk": "AQ==", "uplink": true, "downlink": true}, 200},
	}
	for _, c := range cases {
		if code, obj, _ := call(t, env.srv, "PUT", base+c.slot, tok, c.body); code != c.want {
			t.Errorf("slot %s %v: %d %v, want %d", c.slot, c.body, code, obj, c.want)
		}
	}
	if code, _ := env.do(t, "PUT", base+"1", tok, "application/json", "nope"); code != 400 {
		t.Errorf("bad body: %d", code)
	}
}

func TestChannelChangesTheNodeRefuses(t *testing.T) {
	env := newTestEnv(t, nil)
	remote := &fakeRemote{fail: errors.New("node says no")}
	env.host.SetHoster(&fakeHoster{remote: remote})
	tok := env.signIn(t)
	deskID, _ := newDesk(t, env, tok)
	code, obj, _ := call(t, env.srv, "PUT", "/api/v1/identities/"+deskID+"/channels/1", tok, map[string]any{"name": "Ops", "psk": "AQ=="})
	if code != http.StatusBadGateway || !strings.Contains(obj["error"].(string), "node says no") {
		t.Fatalf("put channel: %d %v", code, obj)
	}
	u := channelURL(&pb.ChannelSettings{Name: "More", Psk: []byte{1}})
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/identities/"+deskID+"/channels/url", tok, map[string]any{"url": u}); code != http.StatusBadGateway {
		t.Fatalf("import: %d", code)
	}
	if code, _, _ := call(t, env.srv, "PATCH", "/api/v1/identities/"+deskID, tok, map[string]any{"long_name": "Renamed"}); code != http.StatusBadGateway {
		t.Fatalf("rename: %d", code)
	}
	if code, _, _ := call(t, env.srv, "PATCH", "/api/v1/identities/"+deskID, tok, map[string]any{"hop_limit": 2}); code != http.StatusBadGateway {
		t.Fatalf("settings: %d", code)
	}
}

func TestPushesSkipLocalIdentities(t *testing.T) {
	env := newTestEnv(t, nil)
	relay := env.host.Relay()
	if err := pushOwner(context.Background(), relay); err != nil {
		t.Fatalf("pushOwner on a local identity: %v", err)
	}
	if err := pushChannels(context.Background(), relay, 0); err != nil {
		t.Fatalf("pushChannels on a local identity: %v", err)
	}
	remote := &fakeRemote{}
	env.host.SetHoster(&fakeHoster{remote: remote})
	tok := env.signIn(t)
	_, deskNum := newDesk(t, env, tok)
	before := remote.adminCount()
	if err := pushChannels(context.Background(), env.host.Identity(deskNum)); err != nil || remote.adminCount() != before {
		t.Fatalf("pushChannels with no slots: %v, %d admin messages", err, remote.adminCount()-before)
	}
	if err := pushOwner(context.Background(), env.host.Identity(deskNum)); err != nil || remote.adminCount() != before+1 {
		t.Fatalf("pushOwner: %v, %d admin messages", err, remote.adminCount()-before)
	}
}
