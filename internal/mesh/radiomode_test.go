package mesh

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/radio/sim"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
	"github.com/A13xB0/RepeaterTastic/pb"
)

// Monitor hears everything and transmits nothing: no relaying, no identity traffic. Off ignores
// the radio altogether.
func TestMonitorAndOffModes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0)
	hub.SetLink("A", "B", sim.Link{Drop: 1})
	hub.SetLink("B", "A", sim.Link{Drop: 1})
	a := newTestHost(t, ctx, hub, "A", "Alice")
	m := newTestHost(t, ctx, hub, "M", "Mona") // between A and B
	b := newTestHost(t, ctx, hub, "B", "Bob")
	mona, bob := newSink(), newSink()
	m.ids[0].AddSink(mona)
	b.ids[0].AddSink(bob)
	time.Sleep(100 * time.Millisecond)

	if err := m.SetRelayRole(RoleMonitor); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendText(a.ids[0], wire.Broadcast, 0, "heard, not relayed", false); err != nil {
		t.Fatal(err)
	}
	mona.waitPacket(t, 5*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "heard, not relayed" })
	if _, err := m.SendText(m.ids[0], wire.Broadcast, 0, "from monitor", false); !errors.Is(err, ErrNotTransmitting) {
		t.Fatalf("send in monitor mode: %v", err)
	}
	time.Sleep(3 * time.Second) // longer than any relay delay
	if n := m.Counters.Tx.Load(); n != 0 {
		t.Fatalf("monitor transmitted %d packets", n)
	}
	select {
	case fr := <-bob.ch:
		if p := fr.GetPacket(); p != nil {
			t.Fatalf("B heard %q through a monitor", text(p))
		}
	default:
	}
	failed := false
	for _, msg := range m.Messages.List(m.ids[0].NodeNum, m.ids[0].NodeID(), "", 0, 10) {
		failed = failed || (msg.Text == "from monitor" && msg.Status == "failed")
	}
	if !failed {
		t.Fatal("the monitor's send wasn't marked failed")
	}

	if err := m.SetRelayRole(RoleOff); err != nil {
		t.Fatal(err)
	}
	rx := m.Counters.Rx.Load()
	if _, err := a.SendText(a.ids[0], wire.Broadcast, 0, "nobody home", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if m.Counters.Rx.Load() != rx {
		t.Fatal("an off radio processed a packet")
	}

	if err := m.SetRelayRole(RoleClient); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendText(a.ids[0], wire.Broadcast, 0, "relayed again", false); err != nil {
		t.Fatal(err)
	}
	bob.waitPacket(t, 10*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "relayed again" })
}
