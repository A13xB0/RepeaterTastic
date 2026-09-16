package nodes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// tcpLauncher "runs" meshtasticd as a fake node listening on the instance's port. Each port gets
// a fresh node with its own key, as a new meshtasticd has.
type tcpLauncher struct {
	mu    sync.Mutex
	nodes map[int]*mtclienttest.Node
	next  uint32
}

func (l *tcpLauncher) Run(ctx context.Context, in Instance, _ io.Writer) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(in.Port))
	if err != nil {
		return err
	}
	l.mu.Lock()
	n := l.nodes[in.Port]
	if n == nil {
		l.next++
		n = mtclienttest.New(0x5e000000 + l.next)
		l.nodes[in.Port] = n
	}
	l.mu.Unlock()
	go n.Serve(ln)
	<-ctx.Done()
	ln.Close()
	n.Drop()
	return ctx.Err()
}

func (l *tcpLauncher) Version(context.Context) (string, error) { return "2.8.0.test", nil }
func (l *tcpLauncher) Describe() string                        { return "test" }

func (l *tcpLauncher) node(port int) *mtclienttest.Node {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nodes[port]
}

// freePortBase finds a port with a free block after it (good enough for a test).
func freePortBase(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestHostingRunsIdentitiesWithTheirKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := t.TempDir()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, RelayRole: mesh.RoleRouter, HopLimit: 4,
		StateDir: state, TelemetryInterval: time.Hour,
		Position: mesh.FixedPosition{Latitude: 56.1, Longitude: -3.2, Altitude: 40}},
		null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	l := &tcpLauncher{nodes: map[int]*mtclienttest.Node{}}
	base := freePortBase(t)
	x := NewHosting(ctx, HostingOptions{Launcher: l, Air: NewLoRaAir(h, testLogf(t)), Radio: "main", Dir: t.TempDir(),
		PortBase: base, Persona: true, Identities: true, Logf: testLogf(t),
		RelayOwner: func() (string, string) { return "RT Relay", "RTR" }})
	h.SetHoster(x)

	used := map[uint8]bool{}
	newID := func(long, short string) *mesh.Identity { // distinct last bytes, as a host needs
		for {
			id, _ := mesh.NewIdentity(nil, long, short)
			if b := wire.LastByte(id.NodeNum); !used[b] {
				used[b] = true
				return id
			}
		}
	}
	relay := newID("Old relay", "OLD")
	relay.IsRelay = true
	desk := newID("Desk", "DESK")
	_ = desk.SetRole("TRACKER")
	_ = desk.SetMaxHops(2)
	_ = desk.SetFixedPosition(&mesh.IdentityPosition{Latitude: 55.9, Longitude: -3.1, Altitude: 12})
	desk.Channels[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "Ops", Psk: []byte("0123456789abcdef")}}
	roam := newID("Roamer", "ROAM")
	roam.SetMultiRadio(&mesh.MultiRadio{DefaultRadio: "mf"})
	for _, id := range []*mesh.Identity{relay, desk, roam} {
		if _, err := h.AddRecord(ctx, id.Record()); err != nil {
			t.Fatal(err)
		}
	}

	// Everyone keeps their number, straight away.
	if h.Relay() == nil || h.Relay().NodeNum != relay.NodeNum || !h.Relay().Hosted() {
		t.Fatalf("relay = %+v", h.Relay())
	}
	if got := h.Identity(desk.NodeNum); got == nil || !got.Hosted() || got.MaxHops() != 2 {
		t.Fatalf("desk = %+v", got)
	}
	if got := h.Identity(roam.NodeNum); got == nil || got.Remote() != nil {
		t.Fatal("an identity routed across radios must stay in RepeaterTastic")
	}

	// The fresh nodes are given the saved identities.
	var persona, node *mtclienttest.Node
	eventually(t, "nodes started", func() bool {
		persona, node = l.node(base), l.node(base+1)
		return persona != nil && node != nil
	})
	eventually(t, "persona seeded", func() bool {
		c := persona.Config()
		return persona.Num() == relay.NodeNum && persona.Owner().GetLongName() == "RT Relay" &&
			c.Device.Role == pb.Config_DeviceConfig_ROUTER && persona.Modules().Telemetry.GetDeviceUpdateInterval() == 3600
	})
	eventually(t, "identity seeded", func() bool {
		c := node.Config()
		chs := node.Channels()
		return node.Num() == desk.NodeNum && node.Owner().GetLongName() == "Desk" &&
			c.Device.Role == pb.Config_DeviceConfig_TRACKER && c.Device.RebroadcastMode == pb.Config_DeviceConfig_NONE &&
			c.Lora.HopLimit == 2 && len(chs) > 1 && chs[1].GetSettings().GetName() == "Ops" &&
			node.Position().GetLatitudeI() == 559000000 && !node.Modules().Telemetry.GetDeviceTelemetryEnabled()
	})
	eventually(t, "identity mirrored", func() bool {
		id := h.Identity(desk.NodeNum)
		return id != nil && id.ChannelCopy(1).GetSettings().GetName() == "Ops" && len(x.Nodes()) == 2
	})
	if n := x.Nodes(); n[0].Role != "persona" || n[1].Role != "identity" || n[1].Port != base+1 {
		t.Fatalf("nodes = %+v", n)
	}

	// Saved with their keys, so they come back as they were (here or on meshtasticd).
	if err := h.SaveIdentities(); err != nil {
		t.Fatal(err)
	}
	recs, err := mesh.LoadIdentityRecords(state)
	if err != nil || len(recs) != 3 {
		t.Fatalf("saved %d identities: %v", len(recs), err)
	}
	for _, r := range recs {
		if r.NodeNum() != relay.NodeNum && r.NodeNum() != desk.NodeNum && r.NodeNum() != roam.NodeNum {
			t.Fatalf("saved %q under another key", r.LongName)
		}
	}

	// A disabled identity's node stops transmitting.
	id := h.Identity(desk.NodeNum)
	id.SetSettings(func(s *mesh.IdentitySettings) { s.Enabled = false })
	if err := h.PushConfig(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "transmitter off", func() bool { return !node.Config().Lora.TxEnabled })

	// Removing it stops the node and deletes its state.
	dir := instanceDir(x, desk.NodeNum)
	if err := h.DropIdentity(desk.NodeNum); err != nil {
		t.Fatal(err)
	}
	if len(x.Nodes()) != 1 {
		t.Fatalf("nodes after removal = %+v", x.Nodes())
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state left behind: %v", err)
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(base+1), time.Second); err == nil {
		c.Close()
		t.Fatal("the removed node still listens")
	}
}

// instanceDir is the state directory of the instance standing in for num.
func instanceDir(x *Hosting, num uint32) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, e := range x.nodes {
		if id := e.hn.Current(); id != nil && id.NodeNum == num {
			return e.hn.Instance().Dir
		}
	}
	return ""
}
