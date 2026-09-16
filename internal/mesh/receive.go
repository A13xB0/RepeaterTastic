package mesh

import (
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

type delivery struct {
	id    *Identity
	index int
}

type decodeResult struct {
	ok         bool
	data       *pb.Data
	pki        bool
	group      *chanGroup // channel that decoded it (nil for PKI)
	target     *Identity  // local identity the packet is addressed to
	deliveries []delivery
	matched    bool // a channel hash matched or PKI was attempted, but decryption failed
	pkiNoKey   bool // DM to us from a node whose key we don't have
}

// HandleReceived logs and sniffs an encrypted packet from the radio or a link: the node DB, the
// packet log and the links learn from it. The hosted nodes do the receiving, answering and
// relaying (they hear the frame over the air bridge). raw is the full LoRa frame when it came off
// the air, else nil.
func (h *Host) HandleReceived(p *pb.MeshPacket, raw []byte) {
	if raw == nil && p.GetEncrypted() != nil { // from a link, not the radio
		h.tapLinkHeard(p)
	}
	if h.remoteOnly() {
		return // a real node does its own receiving; links can't feed it frames
	}
	now := time.Now()
	h.Counters.Rx.Add(1)
	k := pktKey{p.From, p.Id}
	relayByte := h.relayLastByte()
	rec := h.baseRecord(p, raw, "rx", "heard")

	if badHops(p) {
		h.Counters.RxBad.Add(1)
		rec.Kind = "bad"
		h.publishPacket(rec)
		return
	}

	// One of our own packets relayed back to us.
	if h.Identity(p.From) != nil {
		h.hist.Observe(k, p.HopLimit, uint8(p.RelayNode), uint8(p.NextHop), relayByte, now)
		rec.Kind = "echo"
		h.describe(&rec, p)
		h.publishPacket(rec)
		return
	}

	sr := h.hist.Observe(k, p.HopLimit, uint8(p.RelayNode), uint8(p.NextHop), relayByte, now)
	if sr.Seen || sr.Upgraded {
		h.heardAgain(p, k, sr)
		h.Counters.RxDupe.Add(1)
		rec.Kind = "dup"
		h.describe(&rec, p)
		h.publishPacket(rec)
		return
	}
	h.handleFirstSighting(p, rec, now)
}

// tapLinkHeard tells the air taps that want them about a packet a link brought in.
func (h *Host) tapLinkHeard(p *pb.MeshPacket) {
	for _, t := range h.airTaps() {
		if lt, ok := t.(LinkTap); ok {
			lt.LinkHeard(p)
		}
	}
}

// relayLastByte is the relay persona's relay byte, 0 without one.
func (h *Host) relayLastByte() uint8 {
	if relay := h.Relay(); relay != nil {
		return wire.LastByte(relay.NodeNum)
	}
	return 0
}

// badHops reports a packet whose hop limit is above the hop count it started with.
func badHops(p *pb.MeshPacket) bool {
	return p.HopStart != 0 && p.HopStart < p.HopLimit
}

// heardAgain handles a duplicate: a better copy replaces our queued relay, and a copy someone
// else relayed first makes a relay still waiting for the channel stand down.
func (h *Host) heardAgain(p *pb.MeshPacket, k pktKey, sr seenResult) {
	if sr.Upgraded {
		h.txq.RemoveLowerHop(k, p.HopLimit)
		return
	}
	if sr.WeWereNextHop || p.TransportMechanism != pb.MeshPacket_TRANSPORT_LORA || h.relaysAsRouter(p) {
		return
	}
	if h.txq.Cancel(k, false) {
		h.Counters.RelayCancelled.Add(1)
	}
}

// handleFirstSighting decodes a packet heard for the first time, sniffs it and passes channel
// packets to the links that want them.
func (h *Host) handleFirstSighting(p *pb.MeshPacket, rec PacketRecord, now time.Time) {
	dec := h.decode(p)
	h.DB.UpdateFromPacket(p, now)
	if !dec.ok {
		h.Counters.RxUndecryptable.Add(1)
		rec.Kind = "undecryptable"
		h.publishPacket(rec)
		return
	}

	if dec.data.Bitfield != nil && *dec.data.Bitfield&2 != 0 {
		dec.data.WantResponse = true
	}
	h.fillRecordFromDecoded(&rec, dec)

	// Pre-2.3 firmware never set hop_start; 2.8 skips handling such packets (MESHTASTIC_PREHOP_DROP).
	if p.HopStart == 0 && dec.data.Bitfield == nil {
		rec.Kind = "legacy"
		h.publishPacket(rec)
		return
	}

	decoded := clonePacket(p)
	decoded.PayloadVariant = &pb.MeshPacket_Decoded{Decoded: dec.data}
	decoded.PkiEncrypted = dec.pki

	if !dec.pki && dec.group != nil {
		h.channelPacketToLinks(p, dec)
	}

	h.sniffContent(decoded, dec, now)
	h.sniffRouting(decoded, dec)
	if len(dec.deliveries) > 0 {
		rec.Kind = "delivered"
	}
	h.publishPacket(rec)
}

// channelPacketToLinks passes a packet decoded on one of our channels to the channel links.
func (h *Host) channelPacketToLinks(p *pb.MeshPacket, dec decodeResult) {
	ref := dec.group.ref()
	ref.OKToMQTT = dec.data.Bitfield != nil && *dec.data.Bitfield&1 != 0
	h.linkMu.RLock()
	for _, l := range h.links {
		if cl, ok := l.(ChannelLink); ok {
			cl.ChannelPacketHeard(p, ref, dec.data)
		}
	}
	h.linkMu.RUnlock()
}

// decode tries PKI for DMs to our identities, then every channel whose hash matches.
func (h *Host) decode(p *pb.MeshPacket) decodeResult {
	var r decodeResult
	r.target = h.Identity(p.To)
	if p.Channel == 0 && r.target != nil && len(p.GetEncrypted()) > wire.PKIOverhead && h.decodePKI(p, &r) {
		return r
	}
	for _, g := range h.channelGroups(uint8(p.Channel)) {
		r.matched = true
		d := decryptOnChannel(g, p)
		if d == nil {
			continue
		}
		if r.target != nil && d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
			// "Rejecting legacy DM": direct text must be PKI encrypted.
			return decodeResult{target: r.target, matched: true}
		}
		r.ok, r.data, r.group = true, d, g
		r.deliveries = channelDeliveries(g, p.To)
		return r
	}
	return r
}

// decodePKI tries a DM to r.target with the sender's public key. It reports true when the
// packet authenticated, whether or not its payload was usable.
func (h *Host) decodePKI(p *pb.MeshPacket, r *decodeResult) bool {
	r.matched = true
	peer := h.peerKey(p.From)
	if peer == nil {
		r.pkiNoKey = true
		return false
	}
	plain, ok := wire.PKIDecrypt(r.target.PrivateKey, peer, p.From, p.Id, p.GetEncrypted())
	if !ok {
		return false
	}
	d := &pb.Data{}
	if proto.Unmarshal(plain, d) == nil && d.Portnum != pb.PortNum_UNKNOWN_APP {
		r.ok, r.data, r.pki = true, d, true
		r.deliveries = []delivery{{r.target, 0}}
	}
	return true // authenticated, though perhaps malformed
}

// decryptOnChannel decrypts p with a channel's key, nil if that doesn't yield a known payload.
func decryptOnChannel(g *chanGroup, p *pb.MeshPacket) *pb.Data {
	enc := p.GetEncrypted()
	var plain []byte
	if g.aead {
		var ok bool
		if plain, ok = wire.AEADDecrypt(g.key, p.From, p.To, p.Id, enc); !ok {
			return nil
		}
	} else {
		plain = wire.AESCTR(g.key, p.From, p.Id, enc)
	}
	d := &pb.Data{}
	if proto.Unmarshal(plain, d) != nil || d.Portnum == pb.PortNum_UNKNOWN_APP {
		return nil
	}
	return d
}

// channelDeliveries lists the enabled channel members a packet to `to` is for.
func channelDeliveries(g *chanGroup, to uint32) []delivery {
	var out []delivery
	for _, m := range g.members {
		if m.id.Enabled && (to == wire.Broadcast || to == m.id.NodeNum) {
			out = append(out, delivery(m))
		}
	}
	return out
}

func (h *Host) peerKey(num uint32) []byte {
	if id := h.Identity(num); id != nil {
		return id.PublicKey
	}
	e, ok := h.DB.Get(num)
	if !ok {
		return nil
	}
	return e.PublicKey()
}

// sniffContent updates the shared node DB from NodeInfo, Position and Telemetry, once per packet.
func (h *Host) sniffContent(p *pb.MeshPacket, dec decodeResult, now time.Time) {
	d := dec.data
	switch d.Portnum {
	case pb.PortNum_NODEINFO_APP:
		u := &pb.User{}
		if proto.Unmarshal(d.Payload, u) == nil {
			if h.DB.SetUser(p.From, u) {
				h.Bus.Publish(Event{Type: "node", Data: wire.NodeID(p.From)})
			}
		}
	case pb.PortNum_POSITION_APP:
		h.sniffPosition(p.From, d.Payload, now)
	case pb.PortNum_TELEMETRY_APP:
		t := &pb.Telemetry{}
		if proto.Unmarshal(d.Payload, t) == nil && t.GetDeviceMetrics() != nil {
			h.DB.Update(p.From, func(e *NodeEntry) { e.Metrics = t.GetDeviceMetrics() })
		}
	}
}

// sniffPosition records a node's reported position, stamping it with now if it has no time.
func (h *Host) sniffPosition(from uint32, payload []byte, now time.Time) {
	pos := &pb.Position{}
	if proto.Unmarshal(payload, pos) != nil || (pos.GetLatitudeI() == 0 && pos.GetLongitudeI() == 0) {
		return
	}
	if pos.Time == 0 {
		pos.Time = uint32(now.Unix())
	}
	h.DB.Update(from, func(e *NodeEntry) { e.Position = pos })
	h.Bus.Publish(Event{Type: "node", Data: wire.NodeID(from)})
}

// sniffRouting learns next hops from responses and stands down relays already answered.
func (h *Host) sniffRouting(p *pb.MeshPacket, dec decodeResult) {
	d := dec.data
	if d.RequestId != 0 || d.ReplyId != 0 {
		orig := pktKey{p.To, d.RequestId}
		if p.RelayNode != 0 && h.hist.WeRelayed(orig) {
			if _, ok := h.DB.ResolveLastByte(uint8(p.RelayNode), false); ok {
				h.DB.Update(p.From, func(e *NodeEntry) { e.NextHop = uint8(p.RelayNode) })
			}
		}
		if h.Identity(p.To) == nil {
			h.txq.Cancel(orig, false)
		}
	}
}

func keepOffline(p *pb.MeshPacket) bool {
	switch p.GetDecoded().GetPortnum() {
	case pb.PortNum_TEXT_MESSAGE_APP, pb.PortNum_TEXT_MESSAGE_COMPRESSED_APP, pb.PortNum_WAYPOINT_APP,
		pb.PortNum_ROUTING_APP, pb.PortNum_TRACEROUTE_APP, pb.PortNum_ALERT_APP:
		return true
	}
	return false
}

// describe fills the port and summary of a packet we only log (echoes, duplicates) when one of
// our channels or keys can read it, so the packet log shows what was said, not just "Encrypted".
func (h *Host) describe(rec *PacketRecord, p *pb.MeshPacket) {
	if rec.Summary != "" {
		return
	}
	if dec := h.decode(p); dec.ok {
		kind := rec.Kind
		h.fillRecordFromDecoded(rec, dec)
		rec.Kind = kind
	}
}
