package nodes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func TestSettingName(t *testing.T) {
	cases := map[string]*pb.AdminMessage{
		"Lora config": {PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Lora{}}}},
		"Telemetry module config": {PayloadVariant: &pb.AdminMessage_SetModuleConfig{SetModuleConfig: &pb.ModuleConfig{
			PayloadVariant: &pb.ModuleConfig_Telemetry{}}}},
		"channel 2":       {PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: &pb.Channel{Index: 2}}},
		"names":           {PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: &pb.User{}}},
		"position":        {PayloadVariant: &pb.AdminMessage_RemoveFixedPosition{RemoveFixedPosition: true}},
		"SetFavoriteNode": {PayloadVariant: &pb.AdminMessage_SetFavoriteNode{SetFavoriteNode: 1}},
	}
	for want, m := range cases {
		if got := settingName(m); got != want {
			t.Errorf("settingName = %q, want %q", got, want)
		}
	}
	if got := settingName(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetFixedPosition{}}); got != "position" {
		t.Errorf("fixed position: %q", got)
	}
}

func TestIdentityRoleAllowed(t *testing.T) {
	roles := map[pb.Config_DeviceConfig_Role]bool{
		pb.Config_DeviceConfig_TRACKER:     true,
		pb.Config_DeviceConfig_SENSOR:      true,
		pb.Config_DeviceConfig_CLIENT_MUTE: true,
		pb.Config_DeviceConfig_ROUTER:      false,
		pb.Config_DeviceConfig_CLIENT:      false,
	}
	for role, want := range roles {
		if IdentityRoleAllowed(role) != want {
			t.Errorf("%v: %v", role, !want)
		}
	}
}

func TestNodeSendPacket(t *testing.T) {
	fake := mtclienttest.New(hostedNum)
	r := startNode(t, fake, t.TempDir(), 2*time.Second)
	id, err := r.n.SendPacket(&pb.MeshPacket{To: 0x0badcafe, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP}}})
	if err != nil || id == 0 {
		t.Fatalf("send: %d, %v", id, err)
	}
	eventually(t, "packet at the node", func() bool {
		for _, p := range fake.Packets() {
			if p.Id == id {
				return true
			}
		}
		return false
	})
}

// recordingLogf collects log formats.
type recordingLogf struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingLogf) logf(f string, a ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, f)
}

func (r *recordingLogf) count(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// silentNode is a Node whose meshtasticd never finishes the handshake, bound to a host.
func silentNode(t *testing.T, logf func(string, ...any)) (*Node, *mesh.Host, *mesh.Identity) {
	t.Helper()
	fake := mtclienttest.New(hostedNum)
	fake.SetSilent(true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := mtclient.New(mtclient.Options{Address: "fake", Dial: fake.Dial, ReconnectInterval: 10 * time.Millisecond})
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	n := newNode("fake", "", c, logf)
	n.rebootWait = 10 * time.Millisecond
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST},
		null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := mesh.NewIdentity(nil, "Desk", "DESK")
	if err != nil {
		t.Fatal(err)
	}
	return n, h, id
}

func TestPushSettingsGivesUp(t *testing.T) {
	var rec recordingLogf
	n, h, id := silentNode(t, rec.logf)
	for range maxSeedTries + 1 {
		n.pushSettings(context.Background(), h, id.NodeID())
	}
	if rec.count("trying again") != maxSeedTries || rec.count("ERROR node %s didn't take") != 1 {
		t.Fatalf("log %q", rec.lines)
	}
	// A cancelled push neither retries nor complains.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n.pushSettings(ctx, h, id.NodeID())
	if rec.count("ERROR") != 1 || rec.count("trying again") != maxSeedTries {
		t.Fatalf("log after cancel %q", rec.lines)
	}
}

func TestGiveKeyGivesUp(t *testing.T) {
	var rec recordingLogf
	n, h, id := silentNode(t, rec.logf)
	n.SetSeed(id.Record())
	if n.OnAir() {
		t.Fatal("a node without its key is on air")
	}
	// The key doesn't take: the node is asked again on a fresh connection.
	n.applySeed(context.Background(), h, id)
	if rec.count("didn't take %s's key") != 1 {
		t.Fatalf("log %q", rec.lines)
	}
	n.mu.Lock()
	n.seedTries = maxSeedTries
	n.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n.giveKey(ctx, h, id)
	n.giveKey(ctx, h, id)
	if rec.count("won't keep") != 1 {
		t.Fatalf("give-up not logged once: %q", rec.lines)
	}
}

func TestIdentityWithBadSeed(t *testing.T) {
	n, _, _ := silentNode(t, nil)
	n.SetSeed(mesh.IdentityRecord{LongName: "broken", PrivateKey: "not base64"})
	if _, err := n.Identity(context.Background(), 0); err == nil {
		t.Fatal("identity from a bad key")
	}
	if priv, pub := n.seedKey(); priv != nil || pub != nil {
		t.Fatal("bad seed gave a key")
	}
}

func TestLoadStateRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	n := newNode("fake", dir, nil, nil)
	for _, body := range []string{"{", `{"node_num": 0}`} {
		if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := n.loadState(); ok {
			t.Errorf("%q loaded", body)
		}
	}
	if _, ok := newNode("fake", "", nil, nil).loadState(); ok {
		t.Fatal("state without a directory")
	}
	// Unreadable channel entries are skipped.
	body := `{"node_num": 5, "user": {"longName": "x"}, "channels": [{"index": 1}, {"index": "bad"}]}`
	if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, ok := n.loadState()
	if !ok || st.NodeNum != 5 || st.User.GetLongName() != "x" || len(st.Channels) != 1 {
		t.Fatalf("state %+v %v", st, ok)
	}
}

func TestPlaceholderStateIsStable(t *testing.T) {
	a, b := placeholderState("127.0.0.1:4501"), placeholderState("127.0.0.1:4502")
	if a.NodeNum == b.NodeNum || a.NodeNum != placeholderState("127.0.0.1:4501").NodeNum || a.NodeNum&0x10000000 == 0 {
		t.Fatalf("placeholders %08x %08x", a.NodeNum, b.NodeNum)
	}
}

func TestHostIdentityRefusals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, StateDir: t.TempDir()},
		null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	board := newNode("board", "", nil, nil)
	air := NewLoRaAir(h, nil).WithRelay(board)
	if air.Relay() != board {
		t.Fatal("relay not set")
	}
	x := NewHosting(ctx, HostingOptions{Launcher: &fakeLauncher{}, Air: air, Radio: "main", Dir: t.TempDir(), PortBase: 45900})
	if x.Launcher().Describe() != "fake" {
		t.Fatal("launcher")
	}
	relay, _ := mesh.NewIdentity(nil, "Relay", "RLY")
	rec := relay.Record()
	rec.IsRelay = true
	if _, err := x.HostIdentity(ctx, h, rec); err == nil || !strings.Contains(err.Error(), "board is its relay") {
		t.Fatalf("relay on a board radio: %v", err)
	}
	if _, err := x.HostIdentity(ctx, h, mesh.IdentityRecord{LongName: "keyless"}); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("bad key: %v", err)
	}
	x.mu.Lock()
	for s := 1; s < SlotsPerRadio; s++ {
		x.starting[s] = true
	}
	x.mu.Unlock()
	desk, _ := mesh.NewIdentity(nil, "Desk", "DSK")
	if _, err := x.HostIdentity(ctx, h, desk.Record()); err == nil || !strings.Contains(err.Error(), "no free port") {
		t.Fatalf("full radio: %v", err)
	}
	if _, ok := x.InstanceLog("anything"); ok {
		t.Fatal("log of a node that isn't hosted")
	}
}

func TestStartHostedUnwritableDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartHosted(context.Background(), &fakeLauncher{}, Instance{Name: "x", Dir: filepath.Join(file, "sub"), Port: 1}, nil); err == nil {
		t.Fatal("started in a directory under a file")
	}
}

func TestHostedStopAndLog(t *testing.T) {
	h, err := StartHosted(context.Background(), &fakeLauncher{}, Instance{Name: "persona-x", Dir: t.TempDir(), Port: 45998, HWID: HWIDFor("y")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "log lines", func() bool { return len(h.Log()) >= 3 })
	if h.label() != "persona-x" || h.Instance().Name != "persona-x" {
		t.Fatalf("label %q", h.label())
	}
	h.Stop()
	if h.Context().Err() == nil {
		t.Fatal("context still live after stop")
	}
	if st := h.Status(); st.Running || st.Since != 0 {
		t.Fatalf("status after stop %+v", st)
	}
}

func TestDownReason(t *testing.T) {
	cases := []struct {
		st   HostedStatus
		want string
	}{
		{HostedStatus{LastError: "exit status 3"}, "exit status 3"},
		{HostedStatus{}, "not running"},
		{HostedStatus{Running: true}, "not connected"},
	}
	for _, tc := range cases {
		if got := downReason(tc.st); got != tc.want {
			t.Errorf("%+v: %q", tc.st, got)
		}
	}
	now := time.Now()
	recent := HostedStatus{Stops: []HostedStop{{Time: now.UnixMilli(), Reboot: true}}}
	old := HostedStatus{Stops: []HostedStop{{Time: now.Add(-2 * startGrace).UnixMilli(), Reboot: true}}}
	if !rebooting(recent, now) || rebooting(old, now) || rebooting(HostedStatus{}, now) {
		t.Fatal("rebooting")
	}
}

func TestRecordStopKeepsRecentStops(t *testing.T) {
	h := &Hosted{}
	for i := range stopKeep + 5 {
		h.recordStop(errors.New("stop "+string(rune('a'+i%26))), i%2 == 0)
	}
	if len(h.stops) != stopKeep || h.reboots+h.restarts != stopKeep+5 {
		t.Fatalf("stops %d, reboots %d, restarts %d", len(h.stops), h.reboots, h.restarts)
	}
}
