package mesh

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/pb"
	"github.com/A13xB0/RepeaterTastic/internal/phy"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

// RoutingError is a send failure the client API reports as a Routing NAK.
type RoutingError struct{ Reason pb.Routing_Error }

func (e *RoutingError) Error() string { return "routing: " + e.Reason.String() }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Send originates a decoded packet from an identity (client app, web UI or a module).
// p.From is overwritten; zero Id, HopLimit and To get defaults.
func (h *Host) Send(from *Identity, p *pb.MeshPacket) error {
	d := p.GetDecoded()
	if d == nil {
		return errors.New("packet has no decoded payload")
	}
	p.From = from.NodeNum
	if p.Id == 0 {
		p.Id = wire.RandomPacketID()
	}
	if p.To == 0 {
		p.To = wire.Broadcast
	}
	if p.HopLimit == 0 || p.HopLimit > wire.HopMax {
		p.HopLimit = h.Config().HopLimit
	}
	if p.Channel >= MaxChannels {
		return &RoutingError{pb.Routing_NO_CHANNEL}
	}
	route, _ := h.sendHosts(from, p.To, int(p.Channel))
	if d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
		m := &Message{ID: p.Id, From: from.NodeID(), To: wire.NodeID(p.To), Channel: int(p.Channel), Text: string(d.Payload),
			Time: time.Now().UnixMilli(), Direction: "out", Status: "queued", PKI: p.To != wire.Broadcast}
		for i, o := range route {
			if i > 0 {
				m.Radio += ", "
			}
			m.Radio += o.RadioID()
		}
		h.storeFor(from).Add(from.NodeNum, m)
		h.publishMessage(from, *m)
	}

	if target := h.Identity(p.To); target != nil && target != from {
		h.deliverLocal(from, target, p)
		if !h.Config().LocalDMOverRF {
			return nil
		}
	}
	if p.To == wire.Broadcast {
		h.deliverLocalBroadcast(from, p)
	}
	if p.To == wire.BroadcastNoLoRa {
		return nil
	}
	if len(route) > 0 { // experimental multi-radio routing
		var err error
		for i, o := range route {
			q := p
			if i > 0 {
				q = clonePacket(p)
			}
			if e := o.transmit(from, q, true); e != nil && err == nil {
				err = e
			}
		}
		return err
	}
	return h.transmit(from, p, true)
}

// transmit encrypts and queues a packet we originate.
func (h *Host) transmit(from *Identity, p *pb.MeshPacket, reliable bool) error {
	now := time.Now()
	broadcast := p.To == wire.Broadcast
	onAir := clonePacket(p)
	d := proto.Clone(p.GetDecoded()).(*pb.Data)
	bf := uint32(0)
	if h.Config().OKToMQTT {
		bf |= 1
	}
	if d.WantResponse {
		bf |= 2
	}
	d.Bitfield = &bf
	d.XeddsaSignature = nil
	plain, err := proto.Marshal(d)
	if err != nil {
		return err
	}
	if len(plain)+wire.HeaderLen > wire.MaxFrame {
		return h.failSend(from, p, pb.Routing_TOO_LARGE)
	}
	usePKI := !broadcast && pkiAllowed(d.Portnum)
	if p.GetPkiEncrypted() && !usePKI {
		return h.failSend(from, p, pb.Routing_PKI_FAILED)
	}
	var enc []byte
	if usePKI {
		peer := h.peerKey(p.To)
		if peer == nil {
			h.sendNodeInfo(from, p.To, true, int(p.Channel), true)
			return h.failSend(from, p, pb.Routing_PKI_SEND_FAIL_PUBLIC_KEY)
		}
		if len(plain)+wire.HeaderLen+wire.PKIOverhead > wire.MaxFrame {
			return h.failSend(from, p, pb.Routing_TOO_LARGE)
		}
		if enc, err = wire.PKIEncrypt(from.PrivateKey, peer, from.NodeNum, p.Id, plain, 0); err != nil {
			return h.failSend(from, p, pb.Routing_PKI_FAILED)
		}
		onAir.Channel = 0
		onAir.PkiEncrypted = true
	} else {
		var rc *resolvedChannel
		for _, c := range from.resolvedChannels(h.presetDisplay()) {
			if c.index == int(p.Channel) {
				c := c
				rc = &c
			}
		}
		if rc == nil {
			return h.failSend(from, p, pb.Routing_NO_CHANNEL)
		}
		if rc.aead {
			if len(plain)+wire.HeaderLen+wire.AEADOverhead > wire.MaxFrame {
				return h.failSend(from, p, pb.Routing_TOO_LARGE)
			}
			enc, _ = wire.AEADEncrypt(rc.key, from.NodeNum, p.To, p.Id, plain)
		} else {
			enc = wire.AESCTR(rc.key, from.NodeNum, p.Id, plain)
		}
		onAir.Channel = uint32(rc.hash)
	}
	onAir.PayloadVariant = &pb.MeshPacket_Encrypted{Encrypted: enc}
	if limit := from.MaxHops(); limit > 0 && onAir.HopLimit > limit {
		onAir.HopLimit = limit
	}
	onAir.HopStart = onAir.HopLimit
	relayByte := wire.LastByte(from.NodeNum)
	onAir.RelayNode = uint32(relayByte)
	if broadcast {
		onAir.WantAck = false
		onAir.NextHop = 0
	} else {
		onAir.NextHop = uint32(h.nextHopFor(p.To, relayByte))
	}
	if onAir.Priority == pb.MeshPacket_UNSET {
		onAir.Priority = pb.MeshPacket_DEFAULT
		if p.WantAck {
			onAir.Priority = pb.MeshPacket_RELIABLE
		}
	}
	onAir.RxTime, onAir.RxSnr, onAir.RxRssi = nil, 0, nil

	k := pktKey{from.NodeNum, p.Id}
	h.hist.MarkTx(k, onAir.HopLimit, uint8(onAir.NextHop), now)
	rp := h.RadioParams()
	if reliable && p.WantAck {
		attempts := numReliableUnicastRetry
		if broadcast {
			attempts = numReliableRetx
		}
		frameLen := wire.HeaderLen + len(enc)
		h.pmu.Lock()
		h.pending[k] = &pendingTx{pkt: clonePacket(onAir), origin: from, remaining: attempts - 1, broadcast: broadcast, plain: d,
			index: int(p.Channel),
			text:  p.GetDecoded().GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP,
			next:  now.Add(time.Duration(phy.RetransmissionMs(frameLen, rp, h.Air.ChannelUtilPercent(now))) * time.Millisecond)}
		h.pmu.Unlock()
	}
	delay := phy.OwnTxDelayMs(h.Air.ChannelUtilPercent(now), rp.SlotTimeMs())
	if !h.txq.Enqueue(&txItem{key: k, pkt: onAir, due: now.Add(time.Duration(delay) * time.Millisecond), prio: onAir.Priority,
		origin: from.NodeNum, plain: d}) {
		h.stopPending(k)
		return h.failSend(from, p, pb.Routing_TIMEOUT)
	}
	return nil
}

// pkiAllowed mirrors the portnum exclusions in wouldEncryptWithPKC.
func pkiAllowed(port pb.PortNum) bool {
	switch port {
	case pb.PortNum_TRACEROUTE_APP, pb.PortNum_NODEINFO_APP, pb.PortNum_ROUTING_APP, pb.PortNum_POSITION_APP:
		return false
	}
	return true
}

func (h *Host) failSend(from *Identity, p *pb.MeshPacket, reason pb.Routing_Error) error {
	h.nakLocal(from, p.Id, reason)
	return &RoutingError{reason}
}

// nakLocal reports a routing result for one of our own packets to the identity's clients.
func (h *Host) nakLocal(id *Identity, reqID uint32, reason pb.Routing_Error) {
	h.deliverRoutingLocal(id, id.NodeNum, reqID, reason, nil)
	status := "failed"
	if reason == pb.Routing_NONE {
		status = "acked"
	}
	if m, ok := h.storeFor(id).SetStatus(id.NodeNum, reqID, status, errString(reason)); ok {
		h.publishMessage(id, m)
	}
}

func errString(r pb.Routing_Error) string {
	if r == pb.Routing_NONE {
		return ""
	}
	return r.String()
}

// deliverRoutingLocal puts a Routing packet (from `from`) into identity id's client stream.
func (h *Host) deliverRoutingLocal(id *Identity, from, reqID uint32, reason pb.Routing_Error, heard *pb.MeshPacket) {
	payload, _ := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}})
	pkt := &pb.MeshPacket{From: from, To: id.NodeNum, Id: wire.RandomPacketID(), RxTime: u32p(uint32(time.Now().Unix())),
		Priority: pb.MeshPacket_ACK,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: payload,
			RequestId: reqID}}}
	if heard != nil {
		pkt.RelayNode, pkt.RxRssi, pkt.RxSnr = heard.RelayNode, heard.RxRssi, heard.RxSnr
	}
	id.deliverToClients(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: pkt}}, true)
}

// sendAckNak transmits a Routing ACK/NAK from an identity to a remote node.
func (h *Host) sendAckNak(from *Identity, reason pb.Routing_Error, to, reqID uint32, channel int, hopLimit uint32, wantAck bool) {
	payload, _ := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}})
	p := &pb.MeshPacket{To: to, Channel: uint32(channel), HopLimit: hopLimit, WantAck: wantAck, Priority: pb.MeshPacket_ACK,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: payload,
			RequestId: reqID}}}
	p.From = from.NodeNum
	p.Id = wire.RandomPacketID()
	// transmit keeps the hop limit as given: zero is meaningful for ACKs.
	if err := h.transmit(from, p, wantAck); err != nil {
		h.log.Debug("ack not sent", "err", err)
	}
}

// implicitAck handles hearing one of our own packets relayed (ReliableRouter).
func (h *Host) implicitAck(origin *Identity, heard *pb.MeshPacket) {
	k := pktKey{heard.From, heard.Id}
	h.pmu.Lock()
	pd, ok := h.pending[k]
	if ok && heard.TransportMechanism == pb.MeshPacket_TRANSPORT_LORA {
		delete(h.pending, k)
	}
	h.pmu.Unlock()
	if !ok {
		return
	}
	h.deliverRoutingLocal(origin, origin.NodeNum, heard.Id, pb.Routing_NONE, heard)
	status := "sent"
	if pd.broadcast {
		status = "acked"
		h.Counters.AckOK.Add(1)
	}
	if m, changed := h.storeFor(origin).SetStatus(origin.NodeNum, heard.Id, status, ""); changed {
		h.publishMessage(origin, m)
	}
}

func (h *Host) stopPending(k pktKey) {
	h.pmu.Lock()
	delete(h.pending, k)
	h.pmu.Unlock()
}

func (h *Host) doRetransmissions(now time.Time) {
	h.pmu.Lock()
	var due []*pendingTx
	var keys []pktKey
	for k, pd := range h.pending {
		if !now.Before(pd.next) {
			due = append(due, pd)
			keys = append(keys, k)
		}
	}
	for i, pd := range due {
		if pd.remaining <= 0 {
			delete(h.pending, keys[i])
		}
	}
	h.pmu.Unlock()

	rp := h.RadioParams()
	for i, pd := range due {
		k := keys[i]
		if pd.remaining <= 0 {
			if h.tryFallback(pd, k) {
				continue
			}
			h.Counters.AckFail.Add(1)
			h.nakLocal(pd.origin, k.ID, pb.Routing_MAX_RETRANSMIT)
			continue
		}
		pkt := clonePacket(pd.pkt)
		if !pd.broadcast {
			if pd.remaining == 1 {
				// Last attempt: forget the learned route and flood.
				h.DB.Update(pkt.To, func(e *NodeEntry) { e.NextHop = 0 })
				pkt.NextHop = 0
			} else {
				pkt.NextHop = uint32(h.nextHopFor(pkt.To, uint8(pkt.RelayNode)))
			}
		}
		h.hist.MarkTx(k, pkt.HopLimit, uint8(pkt.NextHop), now)
		delay := phy.OwnTxDelayMs(h.Air.ChannelUtilPercent(now), rp.SlotTimeMs())
		h.txq.Enqueue(&txItem{key: k, pkt: pkt, due: now.Add(time.Duration(delay) * time.Millisecond), prio: pkt.Priority,
			origin: pd.origin.NodeNum, plain: pd.plain})
		h.pmu.Lock()
		if cur, ok := h.pending[k]; ok && cur == pd {
			pd.remaining--
			frameLen := wire.HeaderLen + len(pkt.GetEncrypted())
			pd.next = now.Add(time.Duration(phy.RetransmissionMs(frameLen, rp, h.Air.ChannelUtilPercent(now))) * time.Millisecond)
		}
		h.pmu.Unlock()
	}
}

// ------------------------------------------------------------------------------ local routing

// deliverLocal hands a DM between two identities on this host straight across, as if it came
// over the air PKI-encrypted from a direct neighbour.
func (h *Host) deliverLocal(from, target *Identity, p *pb.MeshPacket) {
	now := time.Now()
	dp := clonePacket(p)
	dp.HopStart = p.HopLimit
	dp.TransportMechanism = pb.MeshPacket_TRANSPORT_INTERNAL
	index := 0
	pki := pkiAllowed(p.GetDecoded().GetPortnum())
	if !pki {
		var ok bool
		if index, ok = h.matchingChannel(from, target, int(p.Channel)); !ok {
			if p.WantAck {
				h.nakLocal(from, p.Id, pb.Routing_NO_CHANNEL)
			}
			return
		}
	}
	dec := decodeResult{ok: true, data: dp.GetDecoded(), pki: pki, target: target}
	h.deliver(target, dp, dec, index, now)
	h.Packets.Add(PacketRecord{Direction: "local", Kind: "local", ID: p.Id, From: from.NodeID(), To: target.NodeID(),
		Port: dp.GetDecoded().GetPortnum().String(), HopLimit: dp.HopLimit, HopStart: dp.HopStart, WantAck: p.WantAck,
		DecodedBy: target.NodeID(), PKI: pki, Summary: summarize(dp.GetDecoded()), Transport: "internal"})
	if p.WantAck {
		h.stopPending(pktKey{from.NodeNum, p.Id})
		h.deliverRoutingLocal(from, target.NodeNum, p.Id, pb.Routing_NONE, nil)
		h.Counters.AckOK.Add(1)
		if m, ok := h.Messages.SetStatus(from.NodeNum, p.Id, "acked", ""); ok {
			h.Bus.Publish(Event{Type: "message", Data: MessageEvent{Identity: from.NodeID(), Message: m}})
		}
	}
}

// deliverLocalBroadcast gives a broadcast to every other identity that shares the channel.
func (h *Host) deliverLocalBroadcast(from *Identity, p *pb.MeshPacket) {
	now := time.Now()
	for _, other := range h.Identities() {
		if other == from || !other.Enabled {
			continue
		}
		index, ok := h.matchingChannel(from, other, int(p.Channel))
		if !ok {
			continue
		}
		dp := clonePacket(p)
		dp.HopStart = p.HopLimit
		dp.TransportMechanism = pb.MeshPacket_TRANSPORT_INTERNAL
		h.deliver(other, dp, decodeResult{ok: true, data: dp.GetDecoded(), target: nil}, index, now)
	}
}

// matchingChannel finds the index on `to` holding the same channel (name + key) as index on `from`.
func (h *Host) matchingChannel(from, to *Identity, index int) (int, bool) {
	display := h.presetDisplay()
	var want *resolvedChannel
	for _, c := range from.resolvedChannels(display) {
		if c.index == index {
			c := c
			want = &c
		}
	}
	if want == nil {
		return 0, false
	}
	for _, c := range to.resolvedChannels(display) {
		if c.hash == want.hash && string(c.key) == string(want.key) && c.name == want.name {
			return c.index, true
		}
	}
	return 0, false
}

// SendText is a convenience for the web UI.
func (h *Host) SendText(from *Identity, to uint32, channel int, text string, wantAck bool) (uint32, error) {
	if len(text) == 0 {
		return 0, errors.New("empty message")
	}
	if len(text) > 200 {
		return 0, fmt.Errorf("message is %d bytes; the limit is 200", len(text))
	}
	p := &pb.MeshPacket{To: to, Channel: uint32(channel), WantAck: wantAck,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text)}}}
	err := h.Send(from, p)
	return p.Id, err
}

func u32p(v uint32) *uint32 { return &v }
