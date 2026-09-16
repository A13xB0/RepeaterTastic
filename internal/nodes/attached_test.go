package nodes

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const boardNum = 0x1ee7a001

type rig struct {
	node *mtclienttest.Node
	a    *Attached
	h    *mesh.Host
	id   *mesh.Identity
	dir  string
}

// start opens an attached node on a fake board and runs its host, as the daemon does.
func start(t *testing.T, node *mtclienttest.Node, dir string, wait time.Duration) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a, err := OpenAttachedWith(ctx, mtclient.Options{Address: "/dev/ttyFAKE", Dial: node.Dial,
		ReconnectInterval: 10 * time.Millisecond, RequestTimeout: 2 * time.Second}, dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	a.rebootWait = 100 * time.Millisecond
	t.Cleanup(func() { a.Close() })
	id, err := a.Identity(ctx, wait)
	if err != nil {
		t.Fatal(err)
	}
	h, err := mesh.NewHost(mesh.Config{Region: "US", Preset: pb.Config_LoRaConfig_MEDIUM_FAST, RelayRole: mesh.RoleRouter},
		a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	go a.Bind(ctx, h, id)
	go func() { _ = h.Run(ctx) }()
	return &rig{node: node, a: a, h: h, id: id, dir: dir}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAttachedMirrorsTheNode(t *testing.T) {
	node := mtclienttest.New(boardNum)
	node.Update(func(s *mtclienttest.State) {
		s.Owner.LongName = "Heltec on the mast"
		s.Config.Device.Role = pb.Config_DeviceConfig_ROUTER_LATE
		s.Config.Lora.HopLimit = 5
		s.Others = append(s.Others, &pb.NodeInfo{Num: 0x0badcafe, User: &pb.User{LongName: "Neighbour"}})
	})
	var mirrored mesh.Config
	r := start(t, node, t.TempDir(), 2*time.Second)
	r.a.OnConfig(func(c mesh.Config) { mirrored = c })
	if r.id.NodeNum != boardNum || r.h.Relay() != r.id || r.id.UserCopy().GetLongName() != "Heltec on the mast" {
		t.Fatalf("identity %s %v", r.id.NodeID(), r.id.UserCopy())
	}
	eventually(t, "host settings from the node", func() bool {
		c := r.h.Config()
		return c.Region == "EU_868" && c.Preset == pb.Config_LoRaConfig_LONG_FAST && c.HopLimit == 5 && c.RelayRole == mesh.RoleRouter
	})
	if _, ok := r.h.DB.Get(0x0badcafe); !ok {
		t.Fatal("node DB not filled from the node")
	}
	info := r.a.Info()
	if info.Driver != Driver || info.Firmware != "Meshtastic 2.8.0.fake" || info.Name != "Heltec on the mast" || !r.a.Stats(context.Background()).Connected {
		t.Fatalf("info %+v", info)
	}
	// A reconnect runs OnConfig with what the node reports.
	r.a.Client().Reconnect()
	eventually(t, "config hook", func() bool { return mirrored.Region == "EU_868" })
}

func TestAttachedSendAndReceive(t *testing.T) {
	node := mtclienttest.New(boardNum)
	r := start(t, node, t.TempDir(), 2*time.Second)
	if _, err := r.h.SendText(r.id, wire.Broadcast, 0, "hello mesh", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "packet at the node", func() bool {
		for _, p := range node.Packets() {
			if string(p.GetDecoded().GetPayload()) == "hello mesh" && p.From == 0 && p.HopLimit == 3 {
				return true
			}
		}
		return false
	})
	node.Deliver(&pb.MeshPacket{From: 0x0badcafe, To: boardNum, Id: 42, PkiEncrypted: true, RxSnr: 6,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hi board")}}})
	// A sim-radio meshtasticd hands the client its own transmissions: those aren't messages.
	cm, _ := proto.Marshal(&pb.Compressed{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Data: []byte("echo")})
	node.Deliver(&pb.MeshPacket{From: boardNum, To: wire.Broadcast, Id: 43,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_SIMULATOR_APP, Payload: cm}}})
	eventually(t, "message stored", func() bool {
		for _, m := range r.h.Messages.List(r.id.NodeNum, r.id.NodeID(), "", 0, 10) {
			if m.Text == "hi board" && m.Direction == "in" {
				return true
			}
		}
		return false
	})
	for _, m := range r.h.Messages.List(r.id.NodeNum, r.id.NodeID(), "", 0, 10) {
		if m.Text == "echo" {
			t.Fatal("sim echo stored as a message")
		}
	}
}

func TestAttachedApplyConfig(t *testing.T) {
	node := mtclienttest.New(boardNum)
	r := start(t, node, t.TempDir(), 2*time.Second)
	eventually(t, "mirror", func() bool { return r.h.Config().Region == "EU_868" })
	cfg := r.h.Config()
	cfg.Preset = pb.Config_LoRaConfig_MEDIUM_FAST
	cfg.PrimaryChannel = "Ops"
	cfg.RelayRole = mesh.RoleRouter
	cfg.TxPowerDBm = 17
	if err := r.h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	admins := node.Admins()
	if len(admins) < 4 || !admins[0].GetBeginEditSettings() || !admins[len(admins)-1].GetCommitEditSettings() {
		t.Fatalf("admin sequence %v", admins)
	}
	c := node.Config()
	if c.Lora.ModemPreset != pb.Config_LoRaConfig_MEDIUM_FAST || c.Lora.TxPower != 17 || !c.Lora.TxEnabled || c.Device.Role != pb.Config_DeviceConfig_ROUTER {
		t.Fatalf("node config %v", c)
	}
	if node.Channels()[0].GetSettings().GetName() != "Ops" {
		t.Fatal("primary channel name not set")
	}
	// The client re-reads the node; nothing changes, so nothing more is sent.
	eventually(t, "reconnect", func() bool { return node.Dials() >= 2 && r.a.Client().Snapshot().Connected })
	before := len(node.Admins())
	if err := r.a.ApplyConfig(context.Background(), r.h.Config()); err != nil {
		t.Fatal(err)
	}
	if len(node.Admins()) != before {
		t.Fatalf("unchanged settings sent again: %v", node.Admins()[before:])
	}
	// Monitor switches the transmitter off without touching the role.
	cfg = r.h.Config()
	cfg.RelayRole = mesh.RoleMonitor
	if err := r.a.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if c := node.Config(); c.Lora.TxEnabled || c.Device.Role != pb.Config_DeviceConfig_ROUTER {
		t.Fatalf("monitor: %v", c)
	}
}

func TestAttachedRebootOnCommit(t *testing.T) {
	node := mtclienttest.New(boardNum)
	node.SetRebootOnCommit(true)
	r := start(t, node, t.TempDir(), 2*time.Second)
	cfg := r.h.Config()
	cfg.HopLimit = 6
	if err := r.a.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reconnect after reboot", func() bool {
		return node.Dials() >= 2 && r.a.Client().Snapshot().Config.GetLora().GetHopLimit() == 6
	})
}

func TestAttachedAwayAtStart(t *testing.T) {
	dir := t.TempDir()
	node := mtclienttest.New(boardNum)
	node.SetSilent(true)
	r := start(t, node, dir, 50*time.Millisecond)
	placeholder := r.id.NodeNum
	if placeholder == boardNum || r.h.Relay() != r.id {
		t.Fatalf("placeholder %s", r.id.NodeID())
	}
	if err := r.a.ApplyConfig(context.Background(), r.h.Config()); err != ErrNotReady {
		t.Fatalf("settings to a missing node: %v", err)
	}
	// The node answers: the placeholder is replaced and its state is saved.
	node.SetSilent(false)
	r.a.Client().Reconnect()
	eventually(t, "swap", func() bool { return r.h.Relay() != nil && r.h.Relay().NodeNum == boardNum })
	if r.h.Identity(placeholder) != nil {
		t.Fatal("placeholder still there")
	}

	// Next start with the node away: its saved state stands in.
	node2 := mtclienttest.New(boardNum)
	node2.SetSilent(true)
	r2 := start(t, node2, dir, 50*time.Millisecond)
	if r2.id.NodeNum != boardNum || r2.id.UserCopy().GetLongName() != "Fake" {
		t.Fatalf("from saved state: %s %v", r2.id.NodeID(), r2.id.UserCopy())
	}
}

func TestRelayRoles(t *testing.T) {
	for role, want := range map[pb.Config_DeviceConfig_Role]string{
		pb.Config_DeviceConfig_ROUTER:      mesh.RoleRouter,
		pb.Config_DeviceConfig_REPEATER:    mesh.RoleRouter,
		pb.Config_DeviceConfig_CLIENT_MUTE: mesh.RoleMute,
		pb.Config_DeviceConfig_TRACKER:     mesh.RoleClient,
	} {
		if got := RelayRole(role, true); got != want {
			t.Errorf("%s: %s", role, got)
		}
	}
	if RelayRole(pb.Config_DeviceConfig_ROUTER, false) != mesh.RoleMonitor {
		t.Error("tx disabled is monitor")
	}
	if deviceRole(mesh.RoleRouter, pb.Config_DeviceConfig_ROUTER_LATE) != pb.Config_DeviceConfig_ROUTER_LATE ||
		deviceRole(mesh.RoleClient, pb.Config_DeviceConfig_TRACKER) != pb.Config_DeviceConfig_TRACKER ||
		deviceRole(mesh.RoleRouter, pb.Config_DeviceConfig_CLIENT) != pb.Config_DeviceConfig_ROUTER ||
		deviceRole(mesh.RoleOff, pb.Config_DeviceConfig_CLIENT) != pb.Config_DeviceConfig_CLIENT {
		t.Error("deviceRole")
	}
}

func TestOnConfigAfterHandshake(t *testing.T) {
	r := start(t, mtclienttest.New(boardNum), t.TempDir(), 2*time.Second)
	eventually(t, "mirror", func() bool { return r.h.Config().Region == "EU_868" })
	got := make(chan mesh.Config, 1)
	r.a.OnConfig(func(c mesh.Config) { got <- c })
	select {
	case c := <-got:
		if c.Region != "EU_868" {
			t.Fatalf("config %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("a hook registered late never heard the node's settings")
	}
}
