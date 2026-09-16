package mesh

import (
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
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
		for _, t := range h.airTaps() {
			if lt, ok := t.(LinkTap); ok {
				lt.LinkHeard(p)
			}
		}
	}
	if h.remoteOnly() {
		return // a real node does its own receiving; links can't feed it frames
	}
	relay := h.Relay()
	now := time.Now()
	h.Counters.Rx.Add(1)
	k := pktKey{p.From, p.Id}
	var relayByte uint8
	if relay != nil {
		relayByte = wire.LastByte(relay.NodeNum)
	}
	rec := h.baseRecord(p, raw, "rx", "heard")

	if p.HopStart != 0 && p.HopStart < p.HopLimit {
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
		if sr.Upgraded {
			h.txq.RemoveLowerHop(k, p.HopLimit)
		} else if !sr.WeWereNextHop && p.TransportMechanism == pb.MeshPacket_TRANSPORT_LORA && !h.relaysAsRouter(p) {
			// Someone else relayed it first: a relay still waiting for the channel stands down.
			if h.txq.Cancel(k, false) {
				h.Counters.RelayCancelled.Add(1)
			}
		}
		h.Counters.RxDupe.Add(1)
		rec.Kind = "dup"
		h.describe(&rec, p)
		h.publishPacket(rec)
		return
	}

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

	h.sniffContent(decoded, dec, now)
	h.sniffRouting(decoded, dec)
	if len(dec.deliveries) > 0 {
		rec.Kind = "delivered"
	}
	h.publishPacket(rec)
}

// decode tries PKI for DMs to our identities, then every channel whose hash matches.
func (h *Host) decode(p *pb.MeshPacket) decodeResult {
	var r decodeResult
	enc := p.GetEncrypted()
	r.target = h.Identity(p.To)
	if p.Channel == 0 && r.target != nil && len(enc) > wire.PKIOverhead {
		r.matched = true
		if peer := h.peerKey(p.From); peer != nil {
			if plain, ok := wire.PKIDecrypt(r.target.PrivateKey, peer, p.From, p.Id, enc); ok {
				d := &pb.Data{}
				if proto.Unmarshal(plain, d) == nil && d.Portnum != pb.PortNum_UNKNOWN_APP {
					r.ok, r.data, r.pki = true, d, true
					r.deliveries = []delivery{{r.target, 0}}
					return r
				}
				return r // authenticated but malformed
			}
		} else {
			r.pkiNoKey = true
		}
	}
	for _, g := range h.channelGroups(uint8(p.Channel)) {
		r.matched = true
		var plain []byte
		if g.aead {
			var ok bool
			if plain, ok = wire.AEADDecrypt(g.key, p.From, p.To, p.Id, enc); !ok {
				continue
			}
		} else {
			plain = wire.AESCTR(g.key, p.From, p.Id, enc)
		}
		d := &pb.Data{}
		if proto.Unmarshal(plain, d) != nil || d.Portnum == pb.PortNum_UNKNOWN_APP {
			continue
		}
		if r.target != nil && d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
			// "Rejecting legacy DM": direct text must be PKI encrypted.
			return decodeResult{target: r.target, matched: true}
		}
		r.ok, r.data, r.group = true, d, g
		for _, m := range g.members {
			if !m.id.Enabled {
				continue
			}
			if p.To == wire.Broadcast || p.To == m.id.NodeNum {
				r.deliveries = append(r.deliveries, delivery(m))
			}
		}
		return r
	}
	return r
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
		pos := &pb.Position{}
		if proto.Unmarshal(d.Payload, pos) == nil && (pos.GetLatitudeI() != 0 || pos.GetLongitudeI() != 0) {
			if pos.Time == 0 {
				pos.Time = uint32(now.Unix())
			}
			h.DB.Update(p.From, func(e *NodeEntry) { e.Position = pos })
			h.Bus.Publish(Event{Type: "node", Data: wire.NodeID(p.From)})
		}
	case pb.PortNum_TELEMETRY_APP:
		t := &pb.Telemetry{}
		if proto.Unmarshal(d.Payload, t) == nil && t.GetDeviceMetrics() != nil {
			h.DB.Update(p.From, func(e *NodeEntry) { e.Metrics = t.GetDeviceMetrics() })
		}
	}
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
