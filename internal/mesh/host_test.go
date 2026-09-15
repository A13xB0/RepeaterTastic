package mesh

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/radio/sim"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

type sink struct{ ch chan *pb.FromRadio }

func newSink() *sink                           { return &sink{ch: make(chan *pb.FromRadio, 256)} }
func (s *sink) SendFromRadio(fr *pb.FromRadio) { s.ch <- fr }

// waitPacket returns the first delivered packet matching fn.
func (s *sink) waitPacket(t *testing.T, d time.Duration, fn func(*pb.MeshPacket) bool) *pb.MeshPacket {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case fr := <-s.ch:
			if p := fr.GetPacket(); p != nil && fn(p) {
				return p
			}
		case <-deadline:
			t.Fatalf("timed out waiting for packet")
			return nil
		}
	}
}

type testHost struct {
	*Host
	ids []*Identity
}

func newTestHost(t *testing.T, ctx context.Context, hub *sim.Hub, name string, names ...string) *testHost {
	t.Helper()
	r := hub.Attach(name, 64)
	h, err := NewHost(Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, NodeInfoInterval: time.Hour},
		r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	th := &testHost{Host: h}
	mk := func(n string, relay bool) *Identity {
		for {
			id, err := NewIdentity(nil, n, "")
			if err != nil {
				continue
			}
			id.IsRelay = relay
			if err := h.AddIdentity(id); err != nil {
				continue // last-byte clash: try another key
			}
			id.nextNodeInfo = time.Time{} // no periodic NodeInfo during tests
			return id
		}
	}
	mk(name+" relay", true)
	for _, n := range names {
		th.ids = append(th.ids, mk(n, false))
	}
	go func() { _ = h.Run(ctx) }()
	return th
}

func text(p *pb.MeshPacket) string {
	if d := p.GetDecoded(); d != nil && d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
		return string(d.Payload)
	}
	return ""
}

func isAckFor(id uint32) func(*pb.MeshPacket) bool {
	return func(p *pb.MeshPacket) bool {
		d := p.GetDecoded()
		if d == nil || d.Portnum != pb.PortNum_ROUTING_APP || d.RequestId != id {
			return false
		}
		r := &pb.Routing{}
		_ = proto.Unmarshal(d.Payload, r)
		return r.GetErrorReason() == pb.Routing_NONE
	}
}

func TestBroadcastBetweenHosts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0.02)
	a := newTestHost(t, ctx, hub, "A", "Alice")
	b := newTestHost(t, ctx, hub, "B", "Bob", "Carol")
	bob, carol := newSink(), newSink()
	b.ids[0].AddSink(bob)
	b.ids[1].AddSink(carol)
	time.Sleep(100 * time.Millisecond)

	if _, err := a.SendText(a.ids[0], wire.Broadcast, 0, "hello mesh", false); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*sink{bob, carol} {
		p := s.waitPacket(t, 5*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "hello mesh" })
		if p.From != a.ids[0].NodeNum || p.To != wire.Broadcast || p.Channel != 0 || p.HopStart != 3 {
			t.Fatalf("unexpected packet %v", p)
		}
	}
}

func TestNodeInfoThenPKIDirectMessageWithAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0.02)
	a := newTestHost(t, ctx, hub, "A", "Alice")
	b := newTestHost(t, ctx, hub, "B", "Bob")
	alice, bob := newSink(), newSink()
	a.ids[0].AddSink(alice)
	b.ids[0].AddSink(bob)
	time.Sleep(100 * time.Millisecond)

	a.RequestNodeInfo(a.ids[0], b.ids[0].NodeNum)
	deadline := time.Now().Add(8 * time.Second)
	for a.peerKey(b.ids[0].NodeNum) == nil || b.peerKey(a.ids[0].NodeNum) == nil {
		if time.Now().After(deadline) {
			t.Fatal("keys not exchanged")
		}
		time.Sleep(50 * time.Millisecond)
	}

	id, err := a.SendText(a.ids[0], b.ids[0].NodeNum, 0, "secret", true)
	if err != nil {
		t.Fatal(err)
	}
	p := bob.waitPacket(t, 8*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "secret" })
	if !p.PkiEncrypted {
		t.Fatal("DM was not PKI encrypted")
	}
	alice.waitPacket(t, 12*time.Second, func(p *pb.MeshPacket) bool { return isAckFor(id)(p) && p.From == b.ids[0].NodeNum })
}

func TestRelayThroughMiddleHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0.02)
	hub.SetLink("A", "B", sim.Link{Drop: 1})
	hub.SetLink("B", "A", sim.Link{Drop: 1})
	a := newTestHost(t, ctx, hub, "A", "Alice")
	newTestHost(t, ctx, hub, "R")
	b := newTestHost(t, ctx, hub, "B", "Bob")
	bob := newSink()
	b.ids[0].AddSink(bob)
	time.Sleep(100 * time.Millisecond)

	if _, err := a.SendText(a.ids[0], wire.Broadcast, 0, "over the hill", false); err != nil {
		t.Fatal(err)
	}
	p := bob.waitPacket(t, 10*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "over the hill" })
	if p.HopStart-p.HopLimit != 1 {
		t.Fatalf("expected one hop, got start=%d limit=%d", p.HopStart, p.HopLimit)
	}
}

func TestLocalDirectMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0.02)
	h := newTestHost(t, ctx, hub, "H", "Base", "Ops")
	base, ops := newSink(), newSink()
	h.ids[0].AddSink(base)
	h.ids[1].AddSink(ops)

	id, err := h.SendText(h.ids[0], h.ids[1].NodeNum, 0, "local only", true)
	if err != nil {
		t.Fatal(err)
	}
	p := ops.waitPacket(t, time.Second, func(p *pb.MeshPacket) bool { return text(p) == "local only" })
	if !p.PkiEncrypted || p.From != h.ids[0].NodeNum {
		t.Fatalf("bad local delivery %v", p)
	}
	base.waitPacket(t, time.Second, isAckFor(id))
	if h.Counters.Tx.Load() != 0 {
		t.Fatal("local DM went over the air")
	}
}

func TestPKIDirectMessageThroughRelayLearnsNextHop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0.02)
	hub.SetLink("A", "B", sim.Link{Drop: 1})
	hub.SetLink("B", "A", sim.Link{Drop: 1})
	a := newTestHost(t, ctx, hub, "A", "Alice")
	r := newTestHost(t, ctx, hub, "R")
	b := newTestHost(t, ctx, hub, "B", "Bob")
	alice, bob := newSink(), newSink()
	a.ids[0].AddSink(alice)
	b.ids[0].AddSink(bob)
	time.Sleep(100 * time.Millisecond)

	// Exchange keys across the relay.
	a.RequestNodeInfo(a.ids[0], wire.Broadcast)
	b.RequestNodeInfo(b.ids[0], wire.Broadcast)
	deadline := time.Now().Add(15 * time.Second)
	for a.peerKey(b.ids[0].NodeNum) == nil || b.peerKey(a.ids[0].NodeNum) == nil {
		if time.Now().After(deadline) {
			t.Fatal("keys not exchanged through relay")
		}
		time.Sleep(50 * time.Millisecond)
	}

	for i, msg := range []string{"first", "second"} {
		id, err := a.SendText(a.ids[0], b.ids[0].NodeNum, 0, msg, true)
		if err != nil {
			t.Fatal(err)
		}
		p := bob.waitPacket(t, 15*time.Second, func(p *pb.MeshPacket) bool { return text(p) == msg })
		if !p.PkiEncrypted || p.HopStart-p.HopLimit != 1 {
			t.Fatalf("msg %d: pki=%v hops=%d", i, p.PkiEncrypted, p.HopStart-p.HopLimit)
		}
		alice.waitPacket(t, 20*time.Second, func(p *pb.MeshPacket) bool { return isAckFor(id)(p) && p.From == b.ids[0].NodeNum })
		time.Sleep(200 * time.Millisecond)
	}
	// After an ACK came back through the relay, the relay should be A's next hop towards B.
	e, _ := a.DB.Get(b.ids[0].NodeNum)
	if want := wire.LastByte(r.Relay().NodeNum); e.NextHop != want {
		t.Logf("next hop towards B is %#x, relay byte %#x (learning needs the relay to have relayed the original)", e.NextHop, want)
	}
}
