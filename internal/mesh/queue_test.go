package mesh

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAirtimeTotals(t *testing.T) {
	a := NewAirtime()
	a.AddTx(t0, 1000, 0)
	a.AddTx(t0.Add(time.Minute), 500, 42)
	a.AddRx(t0.Add(2*time.Minute), 250)

	now := t0.Add(3 * time.Minute)
	if tx, rx := a.HourTotals(now); tx != 1500 || rx != 250 {
		t.Fatalf("hour totals = %v, %v", tx, rx)
	}
	if got := a.TxPercent(now); !approx(got, 1500.0/3600000*100) {
		t.Fatalf("tx percent = %v", got)
	}
	// The first transmission is more than a minute older than the reception: pruned.
	if got := a.ChannelUtilPercent(t0.Add(2 * time.Minute)); !approx(got, 750.0/60000*100) {
		t.Fatalf("channel util = %v", got)
	}
	if got := a.ChannelUtilPercent(t0.Add(10 * time.Minute)); got != 0 {
		t.Fatalf("stale channel util = %v", got)
	}
	if got := a.IdentityHourMs(now, 42); got != 500 {
		t.Fatalf("identity airtime = %v", got)
	}
	later := t0.Add(2 * time.Hour)
	if tx, rx := a.HourTotals(later); tx != 0 || rx != 0 {
		t.Fatalf("old minutes counted: %v, %v", tx, rx)
	}
	if got := a.IdentityHourMs(later, 42); got != 0 {
		t.Fatalf("old buckets counted: %v", got)
	}
}

func TestAirtimeBuckets(t *testing.T) {
	a := NewAirtime()
	a.AddTx(t0, 1000, 0)
	a.AddTx(t0.Add(time.Minute), 500, 42)
	a.AddRx(t0.Add(2*time.Minute), 250)
	a.AddTx(t0.Add(15*time.Minute), 100, 7) // next 10-minute bucket

	now := t0.Add(15 * time.Minute)
	bs := a.Buckets(now, time.Hour)
	if len(bs) != 2 {
		t.Fatalf("buckets = %+v", bs)
	}
	first, second := bs[0], bs[1]
	if !first.Start.Equal(t0) || first.TxMs != 1500 || first.RelayMs != 1000 || first.RxMs != 250 || first.ByIdentity[42] != 500 {
		t.Fatalf("first bucket = %+v", first)
	}
	if !second.Start.Equal(t0.Add(10*time.Minute)) || second.TxMs != 100 || second.ByIdentity[7] != 100 {
		t.Fatalf("second bucket = %+v", second)
	}
	first.ByIdentity[42] = 0 // a copy: the tracker is unchanged
	if a.IdentityHourMs(now, 42) != 500 {
		t.Fatal("Buckets returned the live map")
	}
	if bs := a.Buckets(now, 10*time.Minute); len(bs) != 1 || bs[0].TxMs != 100 {
		t.Fatalf("windowed buckets = %+v", bs)
	}
}

func qItem(from, id uint32, prio pb.MeshPacket_Priority, relay bool) *txItem {
	it := &txItem{key: pktKey{from, id}, pkt: &pb.MeshPacket{From: from, Id: id, HopLimit: 3}, prio: prio, relay: relay}
	if !relay {
		it.origin = from
	}
	return it
}

func TestTxQueueEviction(t *testing.T) {
	q := NewTxQueue(2)
	low := qItem(1, 1, pb.MeshPacket_BACKGROUND, true)
	own := qItem(2, 2, pb.MeshPacket_DEFAULT, false)
	if !q.Enqueue(low) || !q.Enqueue(own) || q.Len() != 2 {
		t.Fatal("enqueue into an empty queue failed")
	}
	if q.Enqueue(qItem(3, 3, pb.MeshPacket_BACKGROUND, true)) {
		t.Fatal("full queue took an item that outranks nothing")
	}
	if !q.Enqueue(qItem(4, 4, pb.MeshPacket_HIGH, true)) {
		t.Fatal("a high-priority item didn't evict the background relay")
	}
	if q.Contains(low.key) || !q.Contains(own.key) || !q.Contains(pktKey{4, 4}) || q.Len() != 2 {
		t.Fatal("wrong item evicted")
	}
}

func TestTxQueueCancelAndDrop(t *testing.T) {
	q := NewTxQueue(8)
	relay := qItem(1, 1, pb.MeshPacket_DEFAULT, true)
	own := qItem(2, 2, pb.MeshPacket_DEFAULT, false)
	other := qItem(2, 3, pb.MeshPacket_DEFAULT, false)
	for _, it := range []*txItem{relay, own, other} {
		q.Enqueue(it)
	}
	if q.Cancel(own.key, false) {
		t.Fatal("own packet cancelled without includeOwn")
	}
	if !q.Cancel(own.key, true) || q.Contains(own.key) {
		t.Fatal("own packet not cancelled with includeOwn")
	}
	if q.Cancel(pktKey{9, 9}, true) {
		t.Fatal("cancelled a packet that isn't queued")
	}
	if q.RemoveLowerHop(relay.key, 3) {
		t.Fatal("removed a relay whose hop limit isn't lower")
	}
	if !q.RemoveLowerHop(relay.key, 4) || q.Contains(relay.key) {
		t.Fatal("lower-hop relay not removed")
	}
	if ids := q.DropOrigin(2); len(ids) != 1 || ids[0] != 3 || q.Len() != 0 {
		t.Fatalf("drop origin = %v, len %d", ids, q.Len())
	}
}

func TestTxQueueNextOrder(t *testing.T) {
	q := NewTxQueue(8)
	past := time.Now().Add(-time.Second)
	a := qItem(1, 1, pb.MeshPacket_DEFAULT, true)
	b := qItem(1, 2, pb.MeshPacket_HIGH, true)
	c := qItem(1, 3, pb.MeshPacket_DEFAULT, true)
	later := qItem(1, 4, pb.MeshPacket_MAX, true)
	a.due, b.due, c.due, later.due = past, past, past, time.Now().Add(30*time.Millisecond)
	for _, it := range []*txItem{a, b, c, later} {
		q.Enqueue(it)
	}
	ctx := context.Background()
	for _, want := range []uint32{2, 1, 3, 4} { // priority, then FIFO; the future item last
		it, err := q.Next(ctx)
		if err != nil || it.pkt.Id != want {
			t.Fatalf("next = %v, %v; want id %d", it, err, want)
		}
	}
}

func TestTxQueueNextWaits(t *testing.T) {
	q := NewTxQueue(8)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("next on a cancelled context = %v", err)
	}

	got := make(chan uint32, 1)
	go func() {
		it, err := q.Next(context.Background())
		if err == nil {
			got <- it.pkt.Id
		}
	}()
	time.Sleep(10 * time.Millisecond)
	it := qItem(1, 7, pb.MeshPacket_DEFAULT, true)
	it.due = time.Now()
	q.Enqueue(it) // wakes the waiter
	select {
	case id := <-got:
		if id != 7 {
			t.Fatalf("woken with id %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next wasn't woken by Enqueue")
	}
}

func TestHistoryObserve(t *testing.T) {
	h := NewHistory(8, time.Minute)
	k := pktKey{1, 1}
	if r := h.Observe(k, 3, 0, 0x42, 0x42, t0); r.Seen || !r.WeWereNextHop {
		t.Fatalf("first sighting = %+v", r)
	}
	if r := h.Observe(k, 3, 0, 0, 0x42, t0); !r.Seen || r.Upgraded || r.WeRelayed || !r.WeWereNextHop {
		t.Fatalf("second sighting = %+v", r)
	}
	if r := h.Observe(k, 5, 0, 0, 0x42, t0); !r.Seen || !r.Upgraded {
		t.Fatalf("better copy = %+v", r)
	}
	if r := h.Observe(k, 5, 0, 0, 0x42, t0.Add(2*time.Minute)); r.Seen {
		t.Fatalf("expired entry still seen: %+v", r)
	}
}

func TestHistoryMarkTx(t *testing.T) {
	h := NewHistory(8, time.Minute)
	k := pktKey{2, 2}
	if h.WeRelayed(k) {
		t.Fatal("unknown packet reported relayed")
	}
	h.MarkTx(k, 4, 0x10, t0)
	h.MarkTx(k, 2, 0, t0) // a lower hop limit and no next hop keep what we had
	if !h.WeRelayed(k) {
		t.Fatal("marked packet not relayed")
	}
	r := h.Observe(k, 4, 0, 0, 0x10, t0)
	if !r.Seen || !r.WeRelayed || r.Upgraded || !r.WeWereNextHop {
		t.Fatalf("observe after tx = %+v", r)
	}
}

func TestHistoryRelayers(t *testing.T) {
	h := NewHistory(8, time.Minute)
	k := pktKey{3, 3}
	if h.Relayers(k) != nil {
		t.Fatal("relayers for an unknown packet")
	}
	for _, b := range []uint8{1, 0, 2, 1, 3, 4} {
		h.Observe(k, 3, b, 0, 0, t0)
	}
	got := h.Relayers(k)
	if len(got) != 3 || got[0] != 2 || got[1] != 3 || got[2] != 4 {
		t.Fatalf("relayers = %v, want the three newest", got)
	}
}

func TestHistoryCapacity(t *testing.T) {
	h := NewHistory(2, time.Hour)
	for i := uint32(1); i <= 3; i++ {
		h.MarkTx(pktKey{i, i}, 3, 0, t0)
	}
	if h.WeRelayed(pktKey{1, 1}) || !h.WeRelayed(pktKey{2, 2}) || !h.WeRelayed(pktKey{3, 3}) {
		t.Fatal("oldest entry not evicted")
	}
}
