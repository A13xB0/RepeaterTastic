package nodes

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

const hostedNum = 0x1ee7a001

type nodeRig struct {
	fake *mtclienttest.Node
	n    *Node
	h    *mesh.Host
	id   *mesh.Identity
}

// startNode connects a Node to a fake meshtasticd and binds it as the relay of a host, as the
// daemon does for a hosted persona.
func startNode(t *testing.T, fake *mtclienttest.Node, dir string, wait time.Duration) *nodeRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := mtclient.New(mtclient.Options{Address: "fake", Dial: fake.Dial, ReconnectInterval: 10 * time.Millisecond, RequestTimeout: 2 * time.Second})
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	n := newNode("127.0.0.1:4500", dir, c, testLogf(t))
	n.rebootWait = 100 * time.Millisecond
	id, err := n.Identity(ctx, wait)
	if err != nil {
		t.Fatal(err)
	}
	id.IsRelay = true
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, RelayRole: mesh.RoleRouter, HopLimit: 4},
		null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	h.AddConfigApplier(n)
	n.Bind(h, id)
	done := make(chan struct{})
	go func() { defer close(done); n.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done }) // Run saves state: let it stop before the dir goes
	return &nodeRig{fake: fake, n: n, h: h, id: id}
}

func TestHostedNodeTakesHostSettings(t *testing.T) {
	fake := mtclienttest.New(hostedNum)
	fake.Update(func(s *mtclienttest.State) {
		s.Config.Lora.Region = pb.Config_LoRaConfig_UNSET
		s.Owner.LongName = "Meshtastic a001"
	})
	r := startNode(t, fake, t.TempDir(), 2*time.Second)
	r.n.SetOwner("RT Relay", "RTRL")
	r.fake.Drop() // reconnect: the handshake applies the host's settings and names
	eventually(t, "settings on the node", func() bool {
		c := fake.Config()
		return c.Lora.Region == pb.Config_LoRaConfig_EU_868 && c.Lora.HopLimit == 4 && c.Device.Role == pb.Config_DeviceConfig_ROUTER
	})
	eventually(t, "owner on the node and the identity", func() bool {
		return r.id.UserCopy().GetLongName() == "RT Relay" || r.h.Relay().UserCopy().GetLongName() == "RT Relay"
	})
	// The first region goes alone (it may move the node number); the rest in one transaction.
	admins := fake.Admins()
	if admins[0].GetSetConfig().GetLora().GetRegion() != pb.Config_LoRaConfig_EU_868 || !admins[1].GetBeginEditSettings() {
		t.Fatalf("settings order: %v", admins[:2])
	}
	if r.h.Config().Region != "EU_868" {
		t.Fatal("the node's settings must not flow back into the host")
	}
}

func TestHostedNodeFollowsConfigChanges(t *testing.T) {
	fake := mtclienttest.New(hostedNum)
	r := startNode(t, fake, t.TempDir(), 2*time.Second)
	eventually(t, "first push", func() bool { return fake.Config().Device.Role == pb.Config_DeviceConfig_ROUTER })
	cfg := r.h.Config()
	cfg.Preset = pb.Config_LoRaConfig_MEDIUM_FAST
	cfg.PrimaryChannel = "Ops"
	cfg.RelayRole = mesh.RoleClientMute
	if err := r.h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	c := fake.Config()
	if c.Lora.ModemPreset != pb.Config_LoRaConfig_MEDIUM_FAST || c.Device.Role != pb.Config_DeviceConfig_CLIENT_MUTE {
		t.Fatalf("node config %v", c)
	}
	if fake.Channels()[0].GetSettings().GetName() != "Ops" {
		t.Fatal("primary channel name not pushed")
	}
	// Nothing changed: nothing is sent.
	eventually(t, "reconnected", func() bool { return r.n.client.Snapshot().Connected })
	before := len(fake.Admins())
	if err := r.h.PushConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.Admins()) != before {
		t.Fatalf("unchanged settings sent again: %v", fake.Admins()[before:])
	}
	// Monitor switches the transmitter off and keeps the role.
	cfg = r.h.Config()
	cfg.RelayRole = mesh.RoleMonitor
	if err := r.h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if c := fake.Config(); c.Lora.TxEnabled || c.Device.Role != pb.Config_DeviceConfig_CLIENT_MUTE {
		t.Fatalf("monitor: %v", c)
	}
}

func TestHostedNodeRebootsOnCommit(t *testing.T) {
	fake := mtclienttest.New(hostedNum)
	fake.SetRebootOnCommit(true)
	r := startNode(t, fake, t.TempDir(), 2*time.Second)
	cfg := r.h.Config()
	cfg.HopLimit = 6
	if err := r.h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reconnect after reboot", func() bool {
		return fake.Dials() >= 2 && r.n.client.Snapshot().Config.GetLora().GetHopLimit() == 6
	})
}

func TestHostedNodeLateStart(t *testing.T) {
	dir := t.TempDir()
	fake := mtclienttest.New(hostedNum)
	fake.SetSilent(true)
	r := startNode(t, fake, dir, 50*time.Millisecond)
	placeholder := r.id.NodeNum
	if placeholder == hostedNum || r.h.Relay() != r.id || !strings.Contains(r.id.UserCopy().GetLongName(), "starting") {
		t.Fatalf("placeholder %s %v", r.id.NodeID(), r.id.UserCopy())
	}
	if err := r.n.ApplyConfig(context.Background(), r.h.Config()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("settings to a node that isn't up: %v", err)
	}
	// meshtasticd comes up: the placeholder is swapped for the real node, which is remembered.
	fake.SetSilent(false)
	r.n.client.Reconnect()
	eventually(t, "swap", func() bool { return r.h.Relay() != nil && r.h.Relay().NodeNum == hostedNum })
	if r.h.Identity(placeholder) != nil || r.n.Current().NodeNum != hostedNum {
		t.Fatal("placeholder still in use")
	}
	// Next start with meshtasticd slow again: the saved state stands in.
	fake2 := mtclienttest.New(hostedNum)
	fake2.SetSilent(true)
	r2 := startNode(t, fake2, dir, 50*time.Millisecond)
	if r2.id.NodeNum != hostedNum {
		t.Fatalf("from saved state: %s", r2.id.NodeID())
	}
}

// fakeLauncher exits straight away, as a meshtasticd that can't start does.
type fakeLauncher struct {
	mu   sync.Mutex
	runs int
}

func (f *fakeLauncher) Run(ctx context.Context, in Instance, out io.Writer) error {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	_, _ = io.WriteString(out, "INFO  | booting\nERROR | Can't open/read /prefs/config.proto\nERROR | radio exploded\n")
	return errors.New("exit status 1")
}
func (f *fakeLauncher) Version(context.Context) (string, error) { return "2.8.1.abc", nil }
func (f *fakeLauncher) Describe() string                        { return "fake" }

func TestSupervisorRestarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &fakeLauncher{}
	var logged []string
	var mu sync.Mutex
	logf := func(f string, a ...any) {
		mu.Lock()
		logged = append(logged, f)
		mu.Unlock()
	}
	h, err := StartHosted(ctx, l, Instance{Name: "persona-main", Dir: t.TempDir(), Port: 45999, HWID: HWIDFor("x")}, logf)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	eventually(t, "a restart", func() bool { return h.Status().Restarts >= 1 })
	st := h.Status()
	if st.Connected || st.LastError != "exit status 1" || st.Launcher != "fake" || st.Port != 45999 {
		t.Fatalf("status %+v", st)
	}
	eventually(t, "log tail", func() bool { return len(h.Log()) >= 3 })
	if stops := h.Status().Stops; len(stops) == 0 || stops[0].Reboot || stops[0].Reason != "exit status 1" {
		t.Fatalf("stops %+v", stops)
	}

	// A stop right after a settings commit is the reboot that applies them.
	h.committed.Store(time.Now().UnixMilli())
	eventually(t, "a reboot", func() bool { return h.Status().Reboots >= 1 })
	mu.Lock()
	defer mu.Unlock()
	errs := 0
	for _, f := range logged {
		if strings.Contains(f, "meshtasticd %s: %s") {
			errs++
		}
	}
	if errs == 0 {
		t.Fatal("a real error line wasn't logged")
	}
}

// testLogf logs to t until the test ends; goroutines that outlive it log nowhere.
func testLogf(t *testing.T) func(string, ...any) {
	var mu sync.Mutex
	done := false
	t.Cleanup(func() { mu.Lock(); done = true; mu.Unlock() })
	return func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		if !done {
			t.Logf(f, a...)
		}
	}
}

func TestFirstRegionMovesTheNodeNumber(t *testing.T) {
	fake := mtclienttest.New(hostedNum)
	fake.Update(func(s *mtclienttest.State) { s.Config.Lora.Region = pb.Config_LoRaConfig_UNSET })
	fake.SetMintKeyOnFirstRegion(true)
	r := startNode(t, fake, t.TempDir(), 2*time.Second)
	r.n.SetOwner("Mast", "MAST")
	eventually(t, "renumbered board configured", func() bool {
		c := fake.Config()
		return fake.Num() != hostedNum && c.Lora.Region == pb.Config_LoRaConfig_EU_868 && c.Device.Role == pb.Config_DeviceConfig_ROUTER &&
			fake.Owner().GetLongName() == "Mast"
	})
	eventually(t, "the host follows the new number", func() bool {
		return r.h.Relay() != nil && r.h.Relay().NodeNum == fake.Num() && r.h.Identity(hostedNum) == nil
	})
	// Admin now reaches the node under its new number.
	if _, err := r.n.Admin(context.Background(), &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: &pb.User{LongName: "Mast 2", ShortName: "MST2"}}}); err != nil {
		t.Fatal(err)
	}
	if fake.Owner().GetLongName() != "Mast 2" {
		t.Fatalf("owner %v", fake.Owner())
	}
}

func TestClientBaseRelayFavorites(t *testing.T) {
	const friend, stranger, oldFav = 0x11112222, 0x33334444, 0x55556666
	fake := mtclienttest.New(hostedNum)
	fake.Update(func(s *mtclienttest.State) {
		s.Others = []*pb.NodeInfo{{Num: friend}, {Num: stranger}, {Num: oldFav, IsFavorite: true}}
	})
	r := startNode(t, fake, t.TempDir(), 2*time.Second)
	desk, _ := mesh.NewIdentity(nil, "Desk", "DESK")
	if err := r.h.AddIdentity(desk); err != nil {
		t.Fatal(err)
	}
	fake.Update(func(s *mtclienttest.State) { s.Others = append(s.Others, &pb.NodeInfo{Num: desk.NodeNum}) })
	r.n.client.Reconnect()
	eventually(t, "reconnected with the desk known", func() bool {
		s := r.n.client.Snapshot()
		_, known := s.Nodes[desk.NodeNum]
		return s.Connected && known
	})
	cfg := r.h.Config()
	cfg.RelayRole = mesh.RoleClientBase
	const heardByHost = 0x77778888 // the relay never heard it; the host did
	r.h.DB.SetUser(heardByHost, &pb.User{Id: "!77778888", LongName: "Far handheld", PublicKey: make([]byte, 32)})
	cfg.Favorites = []uint32{friend, heardByHost}
	if err := r.h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	set, removed, added := map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}
	for _, m := range fake.Admins() {
		if c := m.GetAddContact(); c != nil {
			added[c.NodeNum] = true
		}
		if n := m.GetSetFavoriteNode(); n != 0 {
			set[n] = true
		}
		if n := m.GetRemoveFavoriteNode(); n != 0 {
			removed[n] = true
		}
	}
	if !set[friend] || !set[desk.NodeNum] || set[stranger] || !removed[oldFav] || !added[heardByHost] || !set[heardByHost] {
		t.Fatalf("favourites set %v, removed %v", set, removed)
	}
	if fake.Config().Device.Role != pb.Config_DeviceConfig_CLIENT_BASE {
		t.Fatalf("role %v", fake.Config().Device.Role)
	}
}
