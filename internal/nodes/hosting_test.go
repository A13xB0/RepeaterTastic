package nodes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
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
		PortBase: base, Logf: testLogf(t),
		RelayOwner: func() (string, string) { return "RT Relay", "RTR" }})
	h.SetHoster(x)

	newID := distinctIdentities()
	relay := newID("Old relay", "OLD")
	relay.IsRelay = true
	desk := newID("Desk", "DESK")
	_ = desk.SetRole("TRACKER")
	_ = desk.SetMaxHops(2)
	_ = desk.SetFixedPosition(&mesh.IdentityPosition{Latitude: 55.9, Longitude: -3.1, Altitude: 12})
	desk.Channels[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "Ops", Psk: []byte("0123456789abcdef")}}
	addRecords(ctx, t, h, relay, desk)

	// Everyone keeps their number, straight away.
	if h.Relay() == nil || h.Relay().NodeNum != relay.NodeNum || !h.Relay().Hosted() {
		t.Fatalf("relay = %+v", h.Relay())
	}
	if got := h.Identity(desk.NodeNum); got == nil || !got.Hosted() || got.MaxHops() != 2 {
		t.Fatalf("desk = %+v", got)
	}

	// The fresh nodes are given the saved identities.
	var persona, node *mtclienttest.Node
	eventually(t, "nodes started", func() bool {
		persona, node = l.node(base), l.node(base+1)
		return persona != nil && node != nil
	})
	eventually(t, "persona seeded", func() bool { return personaSeeded(persona, relay.NodeNum) })
	eventually(t, "identity seeded", func() bool { return deskSeeded(node, desk.NodeNum) })
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
	checkSavedRecords(t, state, relay.NodeNum, desk.NodeNum)

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
	checkInstanceGone(t, dir, base+1)
}

// distinctIdentities makes identities with distinct last bytes, as a host needs.
func distinctIdentities() func(long, short string) *mesh.Identity {
	used := map[uint8]bool{}
	return func(long, short string) *mesh.Identity {
		for {
			id, _ := mesh.NewIdentity(nil, long, short)
			if b := wire.LastByte(id.NodeNum); !used[b] {
				used[b] = true
				return id
			}
		}
	}
}

// addRecords adds the identities' records to h.
func addRecords(ctx context.Context, t *testing.T, h *mesh.Host, ids ...*mesh.Identity) {
	t.Helper()
	for _, id := range ids {
		if _, err := h.AddRecord(ctx, id.Record()); err != nil {
			t.Fatal(err)
		}
	}
}

// personaSeeded reports whether the persona's node has the relay's identity and settings.
func personaSeeded(persona *mtclienttest.Node, num uint32) bool {
	c := persona.Config()
	return persona.Num() == num && persona.Owner().GetLongName() == "RT Relay" &&
		c.Device.Role == pb.Config_DeviceConfig_ROUTER && persona.Modules().Telemetry.GetDeviceUpdateInterval() == 3600
}

// deskSeeded reports whether the node has the Desk identity with its role, hops, channel and position.
func deskSeeded(node *mtclienttest.Node, num uint32) bool {
	c := node.Config()
	chs := node.Channels()
	return node.Num() == num && node.Owner().GetLongName() == "Desk" &&
		c.Device.Role == pb.Config_DeviceConfig_TRACKER && c.Device.RebroadcastMode == pb.Config_DeviceConfig_NONE &&
		c.Lora.HopLimit == 2 && len(chs) > 1 && chs[1].GetSettings().GetName() == "Ops" &&
		node.Position().GetLatitudeI() == 559000000 && !node.Modules().Telemetry.GetDeviceTelemetryEnabled()
}

// checkSavedRecords expects exactly the two identities to be saved in state, under their own keys.
func checkSavedRecords(t *testing.T, state string, a, b uint32) {
	t.Helper()
	recs, err := mesh.LoadIdentityRecords(state)
	if err != nil || len(recs) != 2 {
		t.Fatalf("saved %d identities: %v", len(recs), err)
	}
	for _, r := range recs {
		if r.NodeNum() != a && r.NodeNum() != b {
			t.Fatalf("saved %q under another key", r.LongName)
		}
	}
}

// checkInstanceGone expects a removed instance's state directory and port to be gone.
func checkInstanceGone(t *testing.T, dir string, port int) {
	t.Helper()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state left behind: %v", err)
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
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

func TestCheckLauncherMissingProgram(t *testing.T) {
	_, err := CheckLauncher(context.Background(), ExecLauncher{Binary: "/nonexistent/dir/meshtasticd"})
	if err == nil || err.Error() != "there's no meshtasticd at /nonexistent/dir/meshtasticd" {
		t.Fatalf("err = %v", err)
	}
}

// flakyLauncher runs nodes like tcpLauncher, except on broken ports; kill stops a running one.
type flakyLauncher struct {
	*tcpLauncher
	mu     sync.Mutex
	broken map[int]bool
	stop   map[int]context.CancelFunc
}

func (l *flakyLauncher) Run(ctx context.Context, in Instance, out io.Writer) error {
	l.mu.Lock()
	if l.broken[in.Port] {
		l.mu.Unlock()
		return errors.New("exit status 1")
	}
	ctx, cancel := context.WithCancel(ctx)
	l.stop[in.Port] = cancel
	l.mu.Unlock()
	if err := l.tcpLauncher.Run(ctx, in, out); err != nil && ctx.Err() != nil {
		return errors.New("killed")
	}
	return nil
}

// kill stops the process on port and keeps it from starting again.
func (l *flakyLauncher) kill(port int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.broken[port] = true
	if c := l.stop[port]; c != nil {
		c()
	}
}

func TestHostingHealth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, StateDir: t.TempDir()},
		null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	base := freePortBase(t)
	l := &flakyLauncher{tcpLauncher: &tcpLauncher{nodes: map[int]*mtclienttest.Node{}},
		broken: map[int]bool{base + 2: true}, stop: map[int]context.CancelFunc{}}
	x := NewHosting(ctx, HostingOptions{Launcher: l, Air: NewLoRaAir(h, testLogf(t)), Radio: "main", Dir: t.TempDir(),
		PortBase: base, Logf: testLogf(t)})
	h.SetHoster(x)
	if got := x.Health(); got.State != HealthOK || got.Nodes != 0 {
		t.Fatalf("no nodes: %+v", got)
	}
	x.SetLauncherCheck("", errors.New("there's no meshtasticd at /opt/meshtasticd"))
	if got := x.Health(); got.State != HealthError || len(got.Problems) != 1 {
		t.Fatalf("launcher missing: %+v", got)
	}
	x.SetLauncherCheck("2.8.0.test", nil)

	newID := distinctIdentities()
	add := func(long string, relay bool) {
		id := newID(long, "")
		id.IsRelay = relay
		_ = id.SetRole("CLIENT_MUTE")
		if _, err := h.AddRecord(ctx, id.Record()); err != nil {
			t.Fatal(err)
		}
	}
	add("Relay", true)
	add("Desk", false)
	eventually(t, "all up", func() bool { hh := x.Health(); return hh.State == HealthOK && hh.Up == 2 })

	add("Broken", false) // its meshtasticd exits at once
	eventually(t, "one identity down", func() bool {
		hh := x.Health()
		return hh.State == HealthWarning && len(hh.Problems) == 1 && hh.Up == 2
	})
	if p := x.Health().Problems[0]; !strings.HasPrefix(p, "identity Broken (!") || !strings.HasSuffix(p, "): exit status 1") {
		t.Fatalf("problem = %q", p)
	}

	// The persona's meshtasticd dies too, well after its last settings change.
	x.mu.Lock()
	for _, e := range x.nodes {
		e.hn.committed.Store(0)
	}
	x.mu.Unlock()
	l.kill(base)
	eventually(t, "persona down", func() bool { return x.Health().State == HealthError })
}
