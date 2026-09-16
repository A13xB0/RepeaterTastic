package udp

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// linkTap records the packets links hand to the host.
type linkTap struct {
	mu   sync.Mutex
	pkts []*pb.MeshPacket
	got  chan struct{}
}

func newLinkTap() *linkTap { return &linkTap{got: make(chan struct{}, 16)} }

func (t *linkTap) Heard(radio.Frame)                                    {}
func (t *linkTap) Transmitted(frame []byte, p *pb.MeshPacket, o uint32) {}
func (t *linkTap) LinkHeard(p *pb.MeshPacket) {
	t.mu.Lock()
	t.pkts = append(t.pkts, p)
	t.mu.Unlock()
	t.got <- struct{}{}
}

func (t *linkTap) packets() []*pb.MeshPacket {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*pb.MeshPacket(nil), t.pkts...)
}

func (t *linkTap) wait(tb testing.TB) *pb.MeshPacket {
	tb.Helper()
	select {
	case <-t.got:
	case <-time.After(2 * time.Second):
		tb.Fatal("no packet reached the host")
	}
	p := t.packets()
	return p[len(p)-1]
}

func newTestHost(t *testing.T) (*mesh.Host, *linkTap) {
	t.Helper()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", StateDir: t.TempDir()}, null.New(), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	tap := newLinkTap()
	h.AddAirTap(tap)
	return h, tap
}

func encPacket(from, id uint32) *pb.MeshPacket {
	return &pb.MeshPacket{
		From: from, To: 0xFFFFFFFF, Id: id, HopLimit: 3, HopStart: 3, Channel: 8,
		RxSnr: 5, RxRssi: proto.Int32(-90), RxTime: proto.Uint32(1234),
		TransportMechanism: pb.MeshPacket_TRANSPORT_MULTICAST_UDP,
		PayloadVariant:     &pb.MeshPacket_Encrypted{Encrypted: []byte{1, 2, 3, id2b(id)}},
	}
}

func id2b(id uint32) byte { return byte(id) }

func marshal(t *testing.T, p *pb.MeshPacket) []byte {
	t.Helper()
	b, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewDefaultsAndErrors(t *testing.T) {
	l, err := New(nil, nil, 0, "", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.groups) != len(DefaultGroups) || l.groups[0].Port != DefaultPort || l.groups[0].IP.String() != DefaultGroups[0] {
		t.Fatalf("groups %v", l.groups)
	}
	if l.Name() != "udp" || l.Connected() || l.iface != nil {
		t.Fatalf("name %q connected %v iface %v", l.Name(), l.Connected(), l.iface)
	}
	if l, err = New(nil, []string{"224.1.2.3"}, 5000, "lo", quietLog()); err != nil || l.groups[0].Port != 5000 || l.iface.Name != "lo" {
		t.Fatalf("custom: %v %+v", err, l)
	}
	if _, err := New(nil, []string{"[::1"}, 0, "", quietLog()); err == nil {
		t.Fatal("bad group accepted")
	}
	if _, err := New(nil, nil, 0, "nosuchif0", quietLog()); err == nil {
		t.Fatal("bad interface accepted")
	}
}

func TestFirstSighting(t *testing.T) {
	l, _ := New(nil, nil, 0, "", quietLog())
	if !l.firstSighting([]byte("a")) || l.firstSighting([]byte("a")) {
		t.Fatal("duplicate not suppressed")
	}
	if !l.firstSighting([]byte("b")) {
		t.Fatal("different datagram suppressed")
	}
	// An old entry no longer suppresses, and a full table sheds stale entries.
	l.seen = map[uint64]time.Time{}
	stale := time.Now().Add(-time.Minute)
	for i := uint64(0); i < 1100; i++ {
		l.seen[i] = stale
	}
	l.firstSighting([]byte("a"))
	for k := range l.seen {
		l.seen[k] = stale
	}
	if !l.firstSighting([]byte("a")) {
		t.Fatal("stale entry still suppresses")
	}
	if len(l.seen) != 1 {
		t.Fatalf("%d entries after pruning, want 1", len(l.seen))
	}
}

// loopbackPair is a link whose socket reads from rx and whose sends go to a 127.0.0.1 socket.
func loopbackPair(t *testing.T, h *mesh.Host) (l *Link, rx, sink *net.UDPConn) {
	t.Helper()
	listen := func() *net.UDPConn {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	rx, sink = listen(), listen()
	out := listen()
	sa := sink.LocalAddr().(*net.UDPAddr)
	l, err := New(h, []string{sa.IP.String()}, sa.Port, "", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	l.out = out
	return l, rx, sink
}

func TestReadLoopFiltersAndForwards(t *testing.T) {
	h, tap := newTestHost(t)
	l, rx, _ := loopbackPair(t, h)
	done := make(chan struct{})
	go func() { defer close(done); l.readLoop(rx) }()

	tx, err := net.DialUDP("udp4", nil, rx.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	send := func(b []byte) {
		if _, err := tx.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	plain := &pb.MeshPacket{From: 5, Id: 1, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}
	send([]byte{0xFF, 0xFF, 0xFF})           // not a protobuf
	send(marshal(t, plain))                  // not encrypted
	send(marshal(t, encPacket(0, 2)))        // from node 0
	good := marshal(t, encPacket(0x1234, 3)) // accepted
	send(good)
	send(good) // duplicate, ignored

	p := tap.wait(t)
	if p.From != 0x1234 || p.Id != 3 || p.RxSnr != 0 || p.RxRssi != nil || p.RxTime != nil ||
		p.TransportMechanism != pb.MeshPacket_TRANSPORT_MULTICAST_UDP {
		t.Fatalf("forwarded %v", p)
	}
	_ = rx.Close()
	<-done
	if l.Rx.Load() != 1 || l.Dropped.Load() != 3 || len(tap.packets()) != 1 {
		t.Fatalf("rx %d dropped %d forwarded %d", l.Rx.Load(), l.Dropped.Load(), len(tap.packets()))
	}
}

func TestSendPacket(t *testing.T) {
	l, _, sink := loopbackPair(t, nil)
	l.SendPacket(&pb.MeshPacket{From: 1, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}})
	if l.Tx.Load() != 0 {
		t.Fatal("plaintext packet sent")
	}
	orig := encPacket(0x99, 7)
	l.SendPacket(orig)
	if l.Tx.Load() != 1 {
		t.Fatalf("tx %d", l.Tx.Load())
	}
	if orig.RxSnr != 5 || orig.TransportMechanism != pb.MeshPacket_TRANSPORT_MULTICAST_UDP {
		t.Fatal("SendPacket modified its argument")
	}
	_ = sink.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, err := sink.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var got pb.MeshPacket
	if err := proto.Unmarshal(buf[:n], &got); err != nil {
		t.Fatal(err)
	}
	if got.From != 0x99 || got.Id != 7 || got.RxSnr != 0 || got.RxTime != nil || got.TransportMechanism != pb.MeshPacket_TRANSPORT_LORA {
		t.Fatalf("sent %v", &got)
	}
	// Our own datagram looping back is not handled again.
	if l.firstSighting(buf[:n]) {
		t.Fatal("own send not remembered")
	}
	// A failing socket is logged, not fatal.
	_ = l.out.Close()
	l.SendPacket(encPacket(0x99, 8))

	var unstarted Link
	unstarted.SendPacket(orig) // no socket yet: nothing happens
}
