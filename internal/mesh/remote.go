package mesh

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Remote is a real Meshtastic node (firmware on a board, or meshtasticd) that an identity stands
// for. The node does its own routing, encryption, acknowledgements and module replies; the host
// keeps the identity's chat, node DB, packet log and app clients.
type Remote interface {
	// SendPacket hands a decoded packet to the node to originate.
	SendPacket(p *pb.MeshPacket) (uint32, error)
	// Admin sends an admin message to the node itself.
	Admin(ctx context.Context, m *pb.AdminMessage) (*pb.AdminMessage, error)
}

// RemoteState is the node as its client last read it.
type RemoteState struct {
	NodeNum  uint32
	User     *pb.User
	Channels []*pb.Channel
	Nodes    []*pb.NodeInfo
}

// NewRemoteIdentity creates an identity for a real node. It holds no private key.
func NewRemoteIdentity(r Remote, st RemoteState) (*Identity, error) {
	if r == nil || st.NodeNum < wire.NumReserved || st.NodeNum == wire.Broadcast {
		return nil, errors.New("remote node has no usable node number")
	}
	id := &Identity{NodeNum: st.NodeNum, Enabled: true, CreatedAt: time.Now(), remote: r,
		sinks: map[ClientSink]struct{}{}, nodeInfoReplied: map[uint32]time.Time{}}
	id.applyRemoteState(st)
	return id, nil
}

// NewHostedIdentity creates the identity for a node RepeaterTastic runs from a saved record: the
// record's key (which the node is given) and the settings the host keeps (app port, hop limit,
// position, airtime share) come from rec, the names and channels from the node.
func NewHostedIdentity(r Remote, st RemoteState, rec IdentityRecord) (*Identity, error) {
	id, err := NewRemoteIdentity(r, st)
	if err != nil {
		return nil, err
	}
	priv, err := rec.Key()
	if err != nil {
		return nil, err
	}
	id.PrivateKey = priv
	id.applyRecordSettings(rec)
	return id, nil
}

// RecordState is what a node seeded from rec looks like before it first answers.
func RecordState(rec IdentityRecord) (RemoteState, error) {
	id, err := IdentityFromRecord(rec)
	if err != nil {
		return RemoteState{}, err
	}
	st := RemoteState{NodeNum: id.NodeNum, User: id.UserCopy()}
	for i := range id.Channels {
		st.Channels = append(st.Channels, id.ChannelCopy(i))
	}
	return st, nil
}

// Hosted reports whether the identity is a node RepeaterTastic runs and holds the key of.
func (id *Identity) Hosted() bool {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.remote != nil && len(id.PrivateKey) == 32
}

// Remote returns the node an identity stands for, or nil for a virtual identity.
func (id *Identity) Remote() Remote {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.remote
}

func (id *Identity) applyRemoteState(st RemoteState) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if st.User != nil {
		id.User = proto.Clone(st.User).(*pb.User)
		id.User.Id = wire.NodeID(id.NodeNum)
		id.PublicKey = st.User.GetPublicKey()
		id.MACAddr = st.User.GetMacaddr()
	}
	if id.User == nil {
		id.User = &pb.User{Id: wire.NodeID(id.NodeNum)}
	}
	for i := 0; i < MaxChannels; i++ {
		var ch *pb.Channel
		if i < len(st.Channels) && st.Channels[i] != nil {
			ch = proto.Clone(st.Channels[i]).(*pb.Channel)
		} else {
			ch = &pb.Channel{Role: pb.Channel_DISABLED, Settings: &pb.ChannelSettings{}}
		}
		ch.Index = int32(i)
		if ch.Settings == nil {
			ch.Settings = &pb.ChannelSettings{}
		}
		id.Channels[i] = ch
	}
}

// SyncRemote refreshes a remote identity and the node DB from its node's latest state.
func (h *Host) SyncRemote(id *Identity, st RemoteState) {
	if id.Remote() == nil {
		return
	}
	id.applyRemoteState(st)
	h.DB.Update(id.NodeNum, func(e *NodeEntry) { e.Local = true; e.User = id.UserCopy(); e.HopsAway = 0 })
	for _, n := range st.Nodes {
		if n.GetNum() == id.NodeNum || n.GetNum() == 0 {
			continue
		}
		if n.User != nil {
			h.DB.SetUser(n.GetNum(), n.User)
		}
		h.DB.Update(n.GetNum(), func(e *NodeEntry) {
			if n.Position != nil && (n.Position.GetLatitudeI() != 0 || n.Position.GetLongitudeI() != 0) {
				e.Position = proto.Clone(n.Position).(*pb.Position)
			}
			if n.DeviceMetrics != nil {
				e.Metrics = proto.Clone(n.DeviceMetrics).(*pb.DeviceMetrics)
			}
			if n.LastHeard != 0 {
				if t := time.Unix(int64(n.LastHeard), 0); t.After(e.LastHeard) {
					e.LastHeard = t
				}
			}
			e.SNR = n.Snr
			if n.HopsAway != nil {
				e.HopsAway = int(n.GetHopsAway())
			}
			e.ViaMQTT = n.ViaMqtt
			e.Favorite = n.IsFavorite
			e.Ignored = n.IsIgnored
			e.Channel = n.Channel
		})
	}
	h.ChannelsChanged()
	h.Bus.Publish(Event{Type: "identity", Data: id.NodeID()})
}

// sendRemote hands a packet to the node an identity stands for.
func (h *Host) sendRemote(from *Identity, r Remote, p *pb.MeshPacket) error {
	if p.GetDecoded() == nil {
		return errors.New("a real node only takes decoded packets")
	}
	q := clonePacket(p)
	q.From = 0 // the node fills in its own number
	if _, err := r.SendPacket(q); err != nil {
		if p.GetDecoded().GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP {
			h.nakLocal(from, p.Id, pb.Routing_NO_INTERFACE)
		}
		return &RoutingError{pb.Routing_NO_INTERFACE}
	}
	if !h.remoteOnly() {
		return nil // a hosted node's frame is logged when the host puts it on air
	}
	h.Counters.Tx.Add(1)
	rec := h.baseRecord(p, nil, "tx", "sent")
	rec.Size = 0
	h.fillRecordFromDecoded(&rec, p, decodeResult{ok: true, data: p.GetDecoded(), pki: p.To != wire.Broadcast && p.Channel == 0})
	h.remoteChannel(&rec, from, p)
	h.publishPacket(rec)
	return nil
}

// RemoteReceived takes a packet the node gave its client: something it received (decoded, or
// opaque if it couldn't decrypt it) or a result for one of our sends.
func (h *Host) RemoteReceived(id *Identity, p *pb.MeshPacket) {
	if id.Remote() == nil || p == nil {
		return
	}
	now := time.Now()
	h.Counters.Rx.Add(1)
	d := p.GetDecoded()
	if p.From != id.NodeNum {
		h.DB.UpdateFromPacket(p, now)
	}
	if d == nil {
		if h.remoteOnly() {
			h.publishPacket(h.baseRecord(p, nil, "rx", "undecryptable"))
		}
		return
	}
	var target *Identity
	if p.To == id.NodeNum {
		target = id
	}
	dec := decodeResult{ok: true, data: d, pki: p.PkiEncrypted, target: target,
		deliveries: []delivery{{id: id, index: int(p.Channel)}}}
	h.sniffContent(p, dec, now)
	if d.Portnum == pb.PortNum_ROUTING_APP && d.RequestId != 0 && p.To == id.NodeNum {
		h.routingResult(id, d)
	}
	if h.remoteOnly() { // with an air bridge the host logs the frame itself
		rec := h.baseRecord(p, nil, "rx", "delivered")
		rec.Size = 0
		h.fillRecordFromDecoded(&rec, p, dec)
		h.remoteChannel(&rec, id, p)
		if p.From == id.NodeNum {
			rec.Direction, rec.Kind = "local", "local" // the node talking to its own client
		}
		h.publishPacket(rec)
	}

	switch d.Portnum {
	case pb.PortNum_TEXT_MESSAGE_APP:
		if p.From != id.NodeNum {
			h.storeIncomingText(id, p)
		}
	case pb.PortNum_TRACEROUTE_APP:
		if d.RequestId != 0 && p.To == id.NodeNum {
			rd := &pb.RouteDiscovery{}
			if proto.Unmarshal(d.Payload, rd) == nil {
				h.Bus.Publish(Event{Type: "traceroute", Data: TracerouteResult{Identity: id.NodeID(), Target: wire.NodeID(p.From),
					Route: nodeIDs(rd.Route), SNRTowards: snrs(rd.SnrTowards), RouteBack: nodeIDs(rd.RouteBack),
					SNRBack: snrs(rd.SnrBack)}})
			}
		}
	}
	dp := clonePacket(p)
	if dp.RxTime == nil {
		dp.RxTime = u32p(uint32(now.Unix()))
	}
	id.deliverToClients(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: dp}}, keepOffline(dp))
}

// remoteChannel names the channel of a decoded packet by the identity's channel index.
func (h *Host) remoteChannel(rec *PacketRecord, id *Identity, p *pb.MeshPacket) {
	if p.PkiEncrypted || (p.Channel == 0 && p.To != wire.Broadcast && p.GetDecoded().GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP && rec.PKI) {
		rec.Channel = "PKI"
		return
	}
	for _, c := range id.resolvedChannels(h.presetDisplay()) {
		if c.index == int(p.Channel) {
			rec.Channel, rec.ChannelHash = c.name, c.hash
			return
		}
	}
	rec.Channel = ""
}

// routingResult applies an ACK or NAK for one of an identity's packets to its message store.
func (h *Host) routingResult(target *Identity, d *pb.Data) {
	rt := &pb.Routing{}
	_ = proto.Unmarshal(d.Payload, rt)
	errReason := rt.GetErrorReason()
	status, errText := "acked", ""
	if errReason != pb.Routing_NONE {
		status, errText = "failed", errReason.String()
		h.Counters.AckFail.Add(1)
	} else {
		h.Counters.AckOK.Add(1)
	}
	if m, ok := h.storeFor(target).SetStatus(target.NodeNum, d.RequestId, status, errText); ok {
		h.publishMessage(target, m)
	}
}

// storeIncomingText records a text message delivered to an identity.
func (h *Host) storeIncomingText(id *Identity, p *pb.MeshPacket) {
	d := p.GetDecoded()
	m := &Message{ID: p.Id, From: wire.NodeID(p.From), To: wire.NodeID(p.To), Channel: int(p.Channel),
		Text: string(d.Payload), Time: time.Now().UnixMilli(), Direction: "in", Status: "received", PKI: p.PkiEncrypted,
		SNR: p.RxSnr, Hops: wire.HopsAway(p)}
	if h.multiRadioOf(id) != nil {
		m.Radio = h.RadioID()
	}
	if p.RxRssi != nil {
		m.RSSI = *p.RxRssi
	}
	if p.To == wire.Broadcast {
		m.To = "!ffffffff"
	}
	h.storeFor(id).Add(id.NodeNum, m)
	h.publishMessage(id, *m)
}

// SwapRemote replaces a remote identity with another for the same node under a new number (a
// placeholder created before the node first answered, or a node that was re-keyed). The relay
// role, settings and connected clients carry over.
func (h *Host) SwapRemote(old, id *Identity) error {
	if old.Remote() == nil || id.Remote() == nil {
		return errors.New("only remote identities can be swapped")
	}
	h.mu.Lock()
	if h.ids[old.NodeNum] != old {
		h.mu.Unlock()
		return fmt.Errorf("no identity %s", old.NodeID())
	}
	if other, dup := h.ids[id.NodeNum]; dup && other != old {
		h.mu.Unlock()
		return fmt.Errorf("identity %s already exists", id.NodeID())
	}
	old.mu.Lock()
	id.mu.Lock()
	id.IsRelay, id.APIBind, id.APIPort, id.CreatedAt = old.IsRelay, old.APIBind, old.APIPort, old.CreatedAt
	for s := range old.sinks {
		id.sinks[s] = struct{}{}
	}
	id.backlog = old.backlog
	id.mu.Unlock()
	old.mu.Unlock()
	delete(h.ids, old.NodeNum)
	h.ids[id.NodeNum] = id
	if h.relay == old {
		h.relay = id
	}
	h.mu.Unlock()
	h.DB.Delete(old.NodeNum)
	h.DB.Update(id.NodeNum, func(e *NodeEntry) { e.Local = true; e.User = id.UserCopy(); e.HopsAway = 0; e.LastHeard = time.Now() })
	h.ChannelsChanged()
	h.Bus.Publish(Event{Type: "identity", Data: old.NodeID()})
	h.Bus.Publish(Event{Type: "identity", Data: id.NodeID()})
	return nil
}
