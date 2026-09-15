package mesh

import (
	"context"
	"testing"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
)

// An identity's hop-limit cap applies to everything it sends, whatever its client asked for.
func TestIdentityHopLimitCapsSends(t *testing.T) {
	h, err := NewHost(Config{Region: "EU_868", HopLimit: 3, StateDir: t.TempDir()}, null.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	relay, _ := NewIdentity(nil, "Relay", "RLY")
	relay.IsRelay = true
	bridge, _ := NewIdentity(nil, "Bridge", "BRG")
	for _, id := range []*Identity{relay, bridge} {
		if err := h.AddIdentity(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := bridge.SetMaxHops(2); err != nil {
		t.Fatal(err)
	}
	if bridge.SetMaxHops(8) == nil {
		t.Fatal("hop limit above 7 accepted")
	}
	send := func(id *Identity, hops uint32) uint32 {
		t.Helper()
		p := &pb.MeshPacket{HopLimit: hops, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_PRIVATE_APP, Payload: []byte("x")}}}
		if err := h.Send(id, p); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		it, err := h.txq.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if it.pkt.HopStart != it.pkt.HopLimit {
			t.Fatalf("hop_start %d != hop_limit %d", it.pkt.HopStart, it.pkt.HopLimit)
		}
		return it.pkt.HopLimit
	}
	if got := send(bridge, 7); got != 2 {
		t.Errorf("capped identity sent with hop limit %d, want 2", got)
	}
	if got := send(bridge, 1); got != 1 {
		t.Errorf("a lower requested hop limit should be kept, got %d", got)
	}
	if got := send(relay, 7); got != 7 {
		t.Errorf("uncapped identity sent with hop limit %d, want 7", got)
	}
}
