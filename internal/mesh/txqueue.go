package mesh

import (
	"context"
	"sync"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

type txItem struct {
	key      pktKey
	pkt      *pb.MeshPacket // encrypted
	due      time.Time
	prio     pb.MeshPacket_Priority
	relay    bool
	origin   uint32 // identity that originated it (0 for relays)
	attempts int    // channel-busy deferrals
	seq      uint64
	plain    *pb.Data // our own packets' payload, so the packet log can show what was sent
}

// TxQueue holds packets waiting for their contention delay. Relays can be cancelled when a
// neighbour relays first (FloodingRouter::perhapsCancelDupe).
type TxQueue struct {
	mu    sync.Mutex
	items []*txItem
	wake  chan struct{}
	seq   uint64
	max   int
}

func NewTxQueue(max int) *TxQueue { return &TxQueue{wake: make(chan struct{}, 1), max: max} }

func (q *TxQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Enqueue adds an item; returns false when the queue is full (the item is dropped).
func (q *TxQueue) Enqueue(it *txItem) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.max {
		// Evict the lowest-priority relay if the newcomer matters more.
		worst := -1
		for i, x := range q.items {
			if x.relay && x.prio < it.prio && (worst < 0 || x.prio < q.items[worst].prio) {
				worst = i
			}
		}
		if worst < 0 {
			return false
		}
		q.items = append(q.items[:worst], q.items[worst+1:]...)
	}
	q.seq++
	it.seq = q.seq
	q.items = append(q.items, it)
	q.signal()
	return true
}

// Cancel removes a queued packet (from, id). Only relays unless includeOwn.
func (q *TxQueue) Cancel(k pktKey, includeOwn bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, x := range q.items {
		if x.key == k && (x.relay || includeOwn) {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true
		}
	}
	return false
}

// RemoveLowerHop removes a queued relay whose hop limit is below threshold.
func (q *TxQueue) RemoveLowerHop(k pktKey, threshold uint32) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, x := range q.items {
		if x.key == k && x.relay && x.pkt.HopLimit < threshold {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true
		}
	}
	return false
}

func (q *TxQueue) Contains(k pktKey) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, x := range q.items {
		if x.key == k {
			return true
		}
	}
	return false
}

func (q *TxQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Next blocks until an item is due. Highest priority wins among due items, then FIFO.
func (q *TxQueue) Next(ctx context.Context) (*txItem, error) {
	for {
		q.mu.Lock()
		best, earliest := q.pickDue(time.Now())
		if best >= 0 {
			it := q.items[best]
			q.items = append(q.items[:best], q.items[best+1:]...)
			q.mu.Unlock()
			return it, nil
		}
		q.mu.Unlock()
		wait := time.Hour
		if !earliest.IsZero() {
			wait = time.Until(earliest)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-q.wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// pickDue returns the index of the item to send now (-1 if none is due) and the earliest due
// time of the rest. Called with q.mu held.
func (q *TxQueue) pickDue(now time.Time) (best int, earliest time.Time) {
	best = -1
	for i, x := range q.items {
		if !x.due.After(now) {
			if best < 0 || sendsBefore(x, q.items[best]) {
				best = i
			}
		} else if earliest.IsZero() || x.due.Before(earliest) {
			earliest = x.due
		}
	}
	return best, earliest
}

// sendsBefore reports whether due item a goes before due item b: higher priority, then FIFO.
func sendsBefore(a, b *txItem) bool {
	return a.prio > b.prio || (a.prio == b.prio && a.seq < b.seq)
}

// DropOrigin removes every queued packet an identity originated and returns their packet IDs.
func (q *TxQueue) DropOrigin(origin uint32) []uint32 {
	q.mu.Lock()
	defer q.mu.Unlock()
	var ids []uint32
	kept := q.items[:0]
	for _, it := range q.items {
		if it.origin == origin {
			ids = append(ids, it.pkt.GetId())
			continue
		}
		kept = append(kept, it)
	}
	q.items = kept
	return ids
}
