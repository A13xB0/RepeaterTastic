package mesh

import (
	"container/list"
	"sync"
	"time"
)

type pktKey struct {
	From uint32
	ID   uint32
}

type histEntry struct {
	key        pktKey
	firstSeen  time.Time
	highestHop uint32
	ourTx      bool
	relayers   [3]uint8
	nextHop    uint8
	elem       *list.Element
}

// History is the host-wide duplicate cache (PacketHistory), keyed by (from, id).
type History struct {
	mu      sync.Mutex
	entries map[pktKey]*histEntry
	order   *list.List
	cap     int
	ttl     time.Duration
}

func NewHistory(capacity int, ttl time.Duration) *History {
	return &History{entries: map[pktKey]*histEntry{}, order: list.New(), cap: capacity, ttl: ttl}
}

type seenResult struct {
	Seen          bool
	Upgraded      bool // seen before, but this copy has more hops left
	WeRelayed     bool
	WeWereNextHop bool
}

// Observe records a received packet and reports whether it is a duplicate.
func (h *History) Observe(k pktKey, hopLimit uint32, relayNode, nextHop uint8, ourRelayByte uint8, now time.Time) seenResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[k]
	if ok && now.Sub(e.firstSeen) > h.ttl {
		h.remove(e)
		ok = false
	}
	if !ok {
		e = h.add(k, now)
		e.highestHop = hopLimit
		e.nextHop = nextHop
		e.addRelayer(relayNode)
		return seenResult{WeWereNextHop: nextHop != 0 && nextHop == ourRelayByte}
	}
	h.order.MoveToFront(e.elem)
	r := seenResult{Seen: true, WeRelayed: e.ourTx, WeWereNextHop: e.nextHop != 0 && e.nextHop == ourRelayByte}
	if hopLimit > e.highestHop {
		r.Upgraded = true
		e.highestHop = hopLimit
	}
	e.addRelayer(relayNode)
	return r
}

// MarkTx records that we transmitted (originated or relayed) this packet.
func (h *History) MarkTx(k pktKey, hopLimit uint32, nextHop uint8, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[k]
	if !ok {
		e = h.add(k, now)
	}
	e.ourTx = true
	if hopLimit > e.highestHop {
		e.highestHop = hopLimit
	}
	if nextHop != 0 {
		e.nextHop = nextHop
	}
}

// WeRelayed reports whether we transmitted the given packet.
func (h *History) WeRelayed(k pktKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[k]
	return ok && e.ourTx
}

// Relayers returns the relay bytes seen for the packet.
func (h *History) Relayers(k pktKey) []uint8 {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[k]
	if !ok {
		return nil
	}
	var out []uint8
	for _, r := range e.relayers {
		if r != 0 {
			out = append(out, r)
		}
	}
	return out
}

func (e *histEntry) addRelayer(b uint8) {
	if b == 0 {
		return
	}
	for i, r := range e.relayers {
		if r == b {
			return
		}
		if r == 0 {
			e.relayers[i] = b
			return
		}
	}
	copy(e.relayers[:], e.relayers[1:])
	e.relayers[len(e.relayers)-1] = b
}

func (h *History) add(k pktKey, now time.Time) *histEntry {
	e := &histEntry{key: k, firstSeen: now}
	e.elem = h.order.PushFront(e)
	h.entries[k] = e
	for h.order.Len() > h.cap {
		h.remove(h.order.Back().Value.(*histEntry))
	}
	return e
}

func (h *History) remove(e *histEntry) {
	h.order.Remove(e.elem)
	delete(h.entries, e.key)
}
