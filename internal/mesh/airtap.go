package mesh

import (
	"errors"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// AirTap is a hosted node sharing this host's radio (the air bridge): it hears every frame the
// radio receives and every frame the host puts on air.
type AirTap interface {
	// Heard is a frame off the air.
	Heard(f radio.Frame)
	// Transmitted is a frame this host sent; origin is the node that originated or relayed it.
	Transmitted(frame []byte, pkt *pb.MeshPacket, origin uint32)
}

// AddAirTap attaches a tap. Frames reach taps in the radio's order, from the host's loops.
func (h *Host) AddAirTap(t AirTap) {
	h.tapMu.Lock()
	h.taps = append(h.taps, t)
	h.tapMu.Unlock()
}

func (h *Host) airTaps() []AirTap {
	h.tapMu.RLock()
	defer h.tapMu.RUnlock()
	return h.taps
}

// ErrQueueFull is returned when the transmit queue has no room for a hosted node's frame.
var ErrQueueFull = errors.New("transmit queue full")

// QueueHosted puts an encrypted packet from a hosted node (origin) on air through this host's
// transmit queue: channel-busy checks, duty cycle and site turns apply as for our own packets.
// The node already waited its contention delay, so the packet is due now. plain, when known, is
// its decoded payload for the packet log.
func (h *Host) QueueHosted(p *pb.MeshPacket, plain *pb.Data, origin uint32) error {
	if p.GetEncrypted() == nil {
		return wire.ErrNotEncrypted
	}
	relay := p.From != origin
	k := pktKey{p.From, p.Id}
	if relay {
		h.hist.MarkTx(k, p.HopLimit, uint8(p.NextHop), time.Now())
	}
	if !h.txq.Enqueue(&txItem{key: k, pkt: p, due: time.Now(), prio: p.Priority, relay: relay, origin: origin, plain: plain}) {
		return ErrQueueFull
	}
	return nil
}

// LogInternal records a packet one hosted node handed straight to another on this host, without
// going on air (local DMs). plain is its payload when known.
func (h *Host) LogInternal(p *pb.MeshPacket, plain *pb.Data) {
	rec := PacketRecord{Direction: "local", Kind: "local", ID: p.Id, From: wire.NodeID(p.From), To: wire.NodeID(p.To),
		HopLimit: p.HopLimit, HopStart: p.HopStart, WantAck: p.WantAck, DecodedBy: wire.NodeID(p.To),
		PKI: plain == nil, Transport: "internal", Port: "PKI (direct message)"}
	if plain != nil {
		rec.Port, rec.Summary = plain.GetPortnum().String(), summarize(plain)
	}
	h.Packets.Add(rec)
}

// ChannelKey resolves an identity's channel slot for encryption: its hash, key and whether it is
// an AEAD channel.
func (h *Host) ChannelKey(id *Identity, index int) (hash uint8, key []byte, aead bool, ok bool) {
	for _, c := range id.resolvedChannels(h.presetDisplay()) {
		if c.index == index {
			return c.hash, c.key, c.aead, true
		}
	}
	return 0, nil, false, false
}

// remoteRelay reports whether the relay persona is a real node: it relays and answers for itself.
func (h *Host) remoteRelay() bool {
	r := h.Relay()
	return r != nil && r.Remote() != nil
}

// remoteOnly reports whether every identity is a real node with its own radio (an attached node):
// the host then never decodes frames itself.
func (h *Host) remoteOnly() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, id := range h.ids {
		if id.remote == nil {
			return false
		}
	}
	return len(h.taps) == 0
}
