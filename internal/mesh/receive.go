package mesh

import (
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/phy"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
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

// HandleReceived runs the receive pipeline for an encrypted packet from the radio or a link.
// raw is the full LoRa frame when it came off the air (for the packet log), else nil.
func (h *Host) HandleReceived(p *pb.MeshPacket, raw []byte) {
	now := time.Now()
	h.Counters.Rx.Add(1)
	k := pktKey{p.From, p.Id}
	relay := h.Relay()
	relayByte := wire.LastByte(relay.NodeNum)
	rec := h.baseRecord(p, raw, "rx", "heard")

	if p.HopStart != 0 && p.HopStart < p.HopLimit {
		h.Counters.RxBad.Add(1)
		rec.Kind = "bad"
		h.publishPacket(rec)
		return
	}

	// One of our own packets relayed back to us: implicit ACK (ReliableRouter).
	if origin := h.Identity(p.From); origin != nil {
		h.hist.Observe(k, p.HopLimit, uint8(p.RelayNode), uint8(p.NextHop), relayByte, now)
		h.implicitAck(origin, p)
		rec.Kind = "echo"
		h.describe(&rec, p)
		h.publishPacket(rec)
		return
	}

	sr := h.hist.Observe(k, p.HopLimit, uint8(p.RelayNode), uint8(p.NextHop), relayByte, now)
	if sr.Upgraded && h.txq.RemoveLowerHop(k, p.HopLimit) {
		dec := h.decode(p)
		h.perhapsRelay(p, dec)
		rec.Kind = "dup"
		h.Counters.RxDupe.Add(1)
		h.publishPacket(rec)
		return
	}
	if sr.Seen {
		h.Counters.RxDupe.Add(1)
		rec.Kind = "dup"
		repeated := p.HopStart > 0 && p.HopStart == p.HopLimit
		if repeated && !h.txq.Contains(k) {
			// The originator is retrying (our ACK or relay was lost): handle it again.
			dec := h.decode(p)
			if dec.ok && !h.perhapsRelay(p, dec) && dec.target != nil && p.WantAck {
				h.sendAckNak(dec.target, pb.Routing_NONE, p.From, p.Id, h.ackChannel(dec), 0, false)
			}
		} else if !sr.WeWereNextHop && p.TransportMechanism == pb.MeshPacket_TRANSPORT_LORA && h.Config().RelayRole != RoleRouter {
			if h.txq.Cancel(k, false) {
				h.Counters.RelayCancelled.Add(1)
			}
		}
		h.describe(&rec, p)
		h.publishPacket(rec)
		return
	}

	dec := h.decode(p)
	h.DB.UpdateFromPacket(p, now)

	if !dec.ok {
		h.Counters.RxUndecryptable.Add(1)
		rec.Kind = "undecryptable"
		if dec.target != nil && p.WantAck {
			if p.Channel == 0 && dec.pkiNoKey {
				h.sendAckNak(dec.target, pb.Routing_PKI_UNKNOWN_PUBKEY, p.From, p.Id, 0, h.responseHopLimit(p), false)
				h.sendNodeInfo(dec.target, p.From, true, 0, true)
			} else {
				h.sendAckNak(dec.target, pb.Routing_NO_CHANNEL, p.From, p.Id, 0, h.responseHopLimit(p), false)
			}
		}
		if h.perhapsRelay(p, dec) {
			rec.Kind = "relayed"
		}
		h.publishPacket(rec)
		return
	}

	if dec.data.Bitfield != nil && *dec.data.Bitfield&2 != 0 {
		dec.data.WantResponse = true
	}
	h.fillRecordFromDecoded(&rec, p, dec)

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
	h.askUnknownNode(decoded, dec)
	h.sniffRouting(decoded, dec)

	for _, d := range dec.deliveries {
		h.deliver(d.id, decoded, dec, d.index, now)
	}
	if len(dec.deliveries) > 0 {
		rec.Kind = "delivered"
	}
	if h.perhapsRelay(p, dec) && len(dec.deliveries) == 0 {
		rec.Kind = "relayed"
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
				r.deliveries = append(r.deliveries, delivery{m.id, m.index})
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

// ackChannel picks the channel index an ACK for a decoded packet should use on the target identity.
func (h *Host) ackChannel(dec decodeResult) int {
	for _, d := range dec.deliveries {
		if d.id == dec.target {
			return d.index
		}
	}
	return 0
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

// askUnknownNode sends the relay persona's NodeInfo (want_response) to nodes we have no User for.
// Firmware does this per node; with a shared DB one request covers every identity.
func (h *Host) askUnknownNode(p *pb.MeshPacket, dec decodeResult) {
	if p.From == 0 || dec.data.Portnum == pb.PortNum_NODEINFO_APP || dec.data.Portnum == pb.PortNum_TELEMETRY_APP {
		return
	}
	if e, ok := h.DB.Get(p.From); ok && e.User != nil {
		return
	}
	if hops := wire.HopsAway(p); hops > int(h.Config().HopLimit)+2 {
		return
	}
	if v, ok := h.nodeInfoAsks.Load(p.From); ok && time.Since(v.(time.Time)) < 15*time.Minute {
		return
	}
	relay := h.Relay()
	chIndex := -1
	if dec.group != nil {
		for _, m := range dec.group.members {
			if m.id == relay {
				chIndex = m.index
			}
		}
	} else if dec.pki && dec.target != nil {
		// DM to one of our identities from an unknown node can't happen (we needed its key); ignore.
		return
	}
	if chIndex < 0 || h.Air.ChannelUtilPercent(time.Now()) > 25 {
		return
	}
	h.nodeInfoAsks.Store(p.From, time.Now())
	h.sendNodeInfo(relay, p.From, true, chIndex, false)
}

// sniffRouting handles ACKs, NAKs, next-hop learning and reliable-delivery responses.
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
	target := dec.target
	if target == nil {
		return
	}
	ch := h.ackChannel(dec)
	switch {
	case p.WantAck:
		hl := h.responseHopLimit(p)
		switch {
		case d.Portnum == pb.PortNum_TEXT_MESSAGE_APP || d.Portnum == pb.PortNum_TEXT_MESSAGE_COMPRESSED_APP:
			h.sendAckNak(target, pb.Routing_NONE, p.From, p.Id, ch, hl, true)
		case d.RequestId == 0 && d.ReplyId == 0:
			h.sendAckNak(target, pb.Routing_NONE, p.From, p.Id, ch, hl, false)
		case wire.HopsAway(p) == 0 || p.NextHop != 0:
			h.sendAckNak(target, pb.Routing_NONE, p.From, p.Id, ch, 0, false)
		}
	case p.NextHop == uint32(wire.LastByte(target.NodeNum)) && p.HopLimit > 0:
		h.sendAckNak(target, pb.Routing_NONE, p.From, p.Id, ch, 0, false)
	}
	if d.Portnum == pb.PortNum_ROUTING_APP && d.RequestId != 0 {
		rt := &pb.Routing{}
		_ = proto.Unmarshal(d.Payload, rt)
		key := pktKey{target.NodeNum, d.RequestId}
		errReason := rt.GetErrorReason()
		h.stopPending(key)
		status, errText := "acked", ""
		if errReason != pb.Routing_NONE {
			status, errText = "failed", errReason.String()
			h.Counters.AckFail.Add(1)
		} else {
			h.Counters.AckOK.Add(1)
		}
		if m, ok := h.Messages.SetStatus(target.NodeNum, d.RequestId, status, errText); ok {
			h.Bus.Publish(Event{Type: "message", Data: MessageEvent{Identity: target.NodeID(), Message: m}})
		}
		if errReason == pb.Routing_PKI_UNKNOWN_PUBKEY {
			h.sendNodeInfo(target, p.From, false, ch, true)
		}
	}
}

// responseHopLimit mirrors RoutingModule::getHopLimitForResponse.
func (h *Host) responseHopLimit(p *pb.MeshPacket) uint32 {
	limit := h.Config().HopLimit
	used := wire.HopsAway(p)
	if used >= 0 {
		switch {
		case uint32(used) > limit:
			return uint32(used)
		case p.HopStart == 0:
			return 0
		case uint32(used)+2 < limit:
			return uint32(used) + 2
		}
	}
	return limit
}

// perhapsRelay is the relay persona's rebroadcast decision (NextHopRouter::perhapsRebroadcast).
func (h *Host) perhapsRelay(p *pb.MeshPacket, dec decodeResult) bool {
	cfg := h.Config()
	if cfg.RelayRole == RoleMute || p.To == wire.BroadcastNoLoRa || p.HopLimit == 0 || p.Id == 0 {
		return false
	}
	if p.ViaMqtt && cfg.IgnoreMQTT {
		return false
	}
	if h.Identity(p.To) != nil || h.Identity(p.From) != nil {
		return false
	}
	relay := h.Relay()
	relayByte := wire.LastByte(relay.NodeNum)
	if p.NextHop != 0 && p.NextHop != uint32(relayByte) {
		return false
	}
	out := clonePacket(p)
	out.RxRssi, out.RxSnr, out.RxTime = nil, 0, nil
	out.HopLimit--
	out.RelayNode = uint32(relayByte)
	if p.NextHop != 0 {
		out.NextHop = uint32(h.nextHopFor(out.To, relayByte))
	}
	if dec.ok && !dec.pki && dec.group != nil && dec.data.Portnum == pb.PortNum_TRACEROUTE_APP {
		if d := h.appendTraceroute(p, dec.data, relay.NodeNum, false); d != nil {
			plain, _ := proto.Marshal(d)
			var enc []byte
			if dec.group.aead {
				enc, _ = wire.AEADEncrypt(dec.group.key, p.From, p.To, p.Id, plain)
			} else {
				enc = wire.AESCTR(dec.group.key, p.From, p.Id, plain)
			}
			if len(enc) <= wire.MaxPayload {
				out.PayloadVariant = &pb.MeshPacket_Encrypted{Encrypted: enc}
			}
		}
	}
	rp := h.RadioParams()
	delay := phy.FloodDelayMs(p.RxSnr, rp.SlotTimeMs(), cfg.RelayRole == RoleRouter)
	k := pktKey{p.From, p.Id}
	h.hist.MarkTx(k, out.HopLimit, uint8(out.NextHop), time.Now())
	return h.txq.Enqueue(&txItem{key: k, pkt: out, due: time.Now().Add(time.Duration(delay) * time.Millisecond),
		prio: p.Priority, relay: true})
}

// nextHopFor returns the learned next hop towards dest if it's an unambiguous direct neighbour.
func (h *Host) nextHopFor(dest uint32, relayByte uint8) uint8 {
	if dest == wire.Broadcast {
		return 0
	}
	e, ok := h.DB.Get(dest)
	if !ok || e.NextHop == 0 || e.NextHop == relayByte {
		return 0
	}
	if _, unique := h.DB.ResolveLastByte(e.NextHop, true); !unique {
		return 0
	}
	return e.NextHop
}

// deliver hands a decoded packet to one identity: modules first, then its clients.
func (h *Host) deliver(id *Identity, p *pb.MeshPacket, dec decodeResult, index int, now time.Time) {
	dp := clonePacket(p)
	dp.Channel = uint32(index)
	dp.RxTime = u32p(uint32(now.Unix()))
	if dec.pki {
		dp.PkiEncrypted = true
		dp.PublicKey = h.peerKey(p.From)
	}
	dp = h.runModules(id, dp)
	if dp == nil {
		return
	}
	id.deliverToClients(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: dp}}, keepOffline(dp))
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
		h.fillRecordFromDecoded(rec, p, dec)
		rec.Kind = kind
	}
}
