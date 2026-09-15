package mesh

import (
	"math"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

const (
	nodeInfoMinInterval    = 10 * time.Minute
	nodeInfoShortInterval  = 60 * time.Second
	nodeInfoReplySuppress  = 12 * time.Hour
	tracerouteRouteMaxHops = 8
)

// runModules is the per-identity module chain for a delivered packet. It may alter the packet
// (traceroute) and returns what the identity's clients should see (nil = swallow).
func (h *Host) runModules(id *Identity, p *pb.MeshPacket) *pb.MeshPacket {
	d := p.GetDecoded()
	toUs := p.To == id.NodeNum
	switch d.Portnum {
	case pb.PortNum_TEXT_MESSAGE_APP:
		m := &Message{ID: p.Id, From: wire.NodeID(p.From), To: wire.NodeID(p.To), Channel: int(p.Channel),
			Text: string(d.Payload), Time: time.Now().UnixMilli(), Direction: "in", Status: "received", PKI: p.PkiEncrypted,
			SNR: p.RxSnr, Hops: wire.HopsAway(p)}
		if p.RxRssi != nil {
			m.RSSI = *p.RxRssi
		}
		if p.To == wire.Broadcast {
			m.To = "!ffffffff"
		}
		h.Messages.Add(id.NodeNum, m)
		h.Bus.Publish(Event{Type: "message", Data: MessageEvent{Identity: id.NodeID(), Message: *m}})

	case pb.PortNum_NODEINFO_APP:
		if d.WantResponse && p.From != id.NodeNum && h.Identity(p.From) == nil {
			h.replyNodeInfo(id, p)
		}

	case pb.PortNum_TRACEROUTE_APP:
		if !toUs {
			return p
		}
		rd := h.appendTraceroute(p, d, id.NodeNum, true)
		if rd == nil {
			return p
		}
		payload, _ := proto.Marshal(rd)
		d.Payload = payload
		if d.RequestId == 0 && d.WantResponse {
			reply := &pb.MeshPacket{To: p.From, Channel: p.Channel, HopLimit: h.responseHopLimit(p), WantAck: p.WantAck,
				PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TRACEROUTE_APP, Payload: payload,
					RequestId: p.Id}}}
			reply.From = id.NodeNum
			reply.Id = wire.RandomPacketID()
			if err := h.transmit(id, reply, p.WantAck); err != nil {
				h.log.Debug("traceroute reply not sent", "err", err)
			}
		} else if d.RequestId != 0 {
			h.Bus.Publish(Event{Type: "traceroute", Data: TracerouteResult{Identity: id.NodeID(), Target: wire.NodeID(p.From),
				Route: nodeIDs(rd.Route), SNRTowards: snrs(rd.SnrTowards), RouteBack: nodeIDs(rd.RouteBack),
				SNRBack: snrs(rd.SnrBack)}})
		}

	case pb.PortNum_ROUTING_APP, pb.PortNum_POSITION_APP, pb.PortNum_TELEMETRY_APP, pb.PortNum_WAYPOINT_APP,
		pb.PortNum_TEXT_MESSAGE_COMPRESSED_APP, pb.PortNum_NEIGHBORINFO_APP, pb.PortNum_STORE_FORWARD_APP,
		pb.PortNum_RANGE_TEST_APP, pb.PortNum_ALERT_APP, pb.PortNum_DETECTION_SENSOR_APP, pb.PortNum_PAXCOUNTER_APP:
		// Client-side ports: nothing to answer on the node.

	case pb.PortNum_ADMIN_APP:
		// Remote administration of virtual nodes isn't supported.
		if toUs && d.WantResponse {
			h.sendAckNak(id, pb.Routing_NOT_AUTHORIZED, p.From, p.Id, int(p.Channel), h.responseHopLimit(p), false)
		}
		return nil

	default:
		if toUs && d.WantResponse && d.RequestId == 0 {
			h.sendAckNak(id, pb.Routing_NO_RESPONSE, p.From, p.Id, int(p.Channel), h.responseHopLimit(p), false)
		}
	}
	return p
}

// TracerouteResult is published when a traceroute response reaches one of our identities.
type TracerouteResult struct {
	Identity   string    `json:"identity"`
	Target     string    `json:"target"`
	Route      []string  `json:"route"`
	SNRTowards []float64 `json:"snr_towards"`
	RouteBack  []string  `json:"route_back"`
	SNRBack    []float64 `json:"snr_back"`
}

func nodeIDs(n []uint32) []string {
	out := make([]string, len(n))
	for i, v := range n {
		out[i] = wire.NodeID(v)
	}
	return out
}

func snrs(v []int32) []float64 {
	out := make([]float64, len(v))
	for i, s := range v {
		out[i] = float64(s) / 4
	}
	return out
}

// appendTraceroute mirrors TraceRouteModule::alterReceivedProtobuf. snrOnly is true at the
// destination / originator, false for a relay that adds its own node number.
func (h *Host) appendTraceroute(p *pb.MeshPacket, d *pb.Data, self uint32, snrOnly bool) *pb.RouteDiscovery {
	rd := &pb.RouteDiscovery{}
	if proto.Unmarshal(d.Payload, rd) != nil {
		return nil
	}
	towards := d.RequestId == 0
	route, snr := &rd.Route, &rd.SnrTowards
	if !towards {
		route, snr = &rd.RouteBack, &rd.SnrBack
	}
	if hops := wire.HopsAway(p); hops >= 0 {
		for len(*route) < hops && len(*route) < tracerouteRouteMaxHops {
			*route = append(*route, wire.Broadcast)
		}
	}
	if len(*snr) < tracerouteRouteMaxHops {
		q := int32(math.Round(float64(p.RxSnr) * 4))
		q = max(-127, min(127, q))
		*snr = append(*snr, q)
	}
	if !snrOnly && len(*route) < tracerouteRouteMaxHops {
		*route = append(*route, self)
	}
	return rd
}

// ------------------------------------------------------------------------------------ NodeInfo

// sendNodeInfo transmits an identity's User. short uses the 60 s throttle (requests/pings).
func (h *Host) sendNodeInfo(id *Identity, to uint32, wantResponse bool, channel int, short bool) {
	now := time.Now()
	id.mu.Lock()
	limit := nodeInfoMinInterval
	if short {
		limit = nodeInfoShortInterval
	}
	if !id.lastNodeInfoTx.IsZero() && now.Sub(id.lastNodeInfoTx) < limit {
		id.mu.Unlock()
		return
	}
	id.lastNodeInfoTx = now
	id.mu.Unlock()
	u := id.UserCopy()
	payload, _ := proto.Marshal(u)
	prio := pb.MeshPacket_BACKGROUND
	if short {
		prio = pb.MeshPacket_DEFAULT
	}
	p := &pb.MeshPacket{To: to, Channel: uint32(channel), Priority: prio,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: payload,
			WantResponse: wantResponse}}}
	p.From = id.NodeNum
	p.Id = wire.RandomPacketID()
	p.HopLimit = h.Config().HopLimit
	if err := h.transmit(id, p, false); err != nil {
		h.log.Debug("nodeinfo not sent", "identity", id.NodeID(), "err", err)
	}
}

func (h *Host) replyNodeInfo(id *Identity, req *pb.MeshPacket) {
	now := time.Now()
	id.mu.Lock()
	if last, ok := id.nodeInfoReplied[req.From]; ok && now.Sub(last) < nodeInfoReplySuppress {
		id.mu.Unlock()
		return
	}
	id.nodeInfoReplied[req.From] = now
	if len(id.nodeInfoReplied) > 2048 {
		for k, v := range id.nodeInfoReplied {
			if now.Sub(v) > nodeInfoReplySuppress {
				delete(id.nodeInfoReplied, k)
			}
		}
	}
	id.mu.Unlock()
	if h.Air.ChannelUtilPercent(now) > 25 {
		return
	}
	h.sendNodeInfo(id, req.From, false, int(req.Channel), req.To != wire.Broadcast)
}

// periodicNodeInfo broadcasts each identity's NodeInfo on its interval, staggered at startup.
func (h *Host) periodicNodeInfo(now time.Time) {
	interval := h.Config().NodeInfoInterval
	for _, id := range h.Identities() {
		id.mu.Lock()
		due := id.Enabled && !id.nextNodeInfo.IsZero() && !now.Before(id.nextNodeInfo)
		if due {
			id.nextNodeInfo = now.Add(interval)
		}
		id.mu.Unlock()
		if !due || h.Air.ChannelUtilPercent(now) > 40 || !h.radioOK.Load() {
			continue
		}
		if limit := h.dutyLimit(); limit < 100 && h.Air.TxPercent(now) > limit/2 {
			continue
		}
		h.sendNodeInfo(id, wire.Broadcast, false, 0, false)
	}
}

// RequestNodeInfo asks a remote node for its User from one of our identities.
func (h *Host) RequestNodeInfo(from *Identity, to uint32) {
	h.sendNodeInfo(from, to, true, 0, true)
}

// Traceroute starts a traceroute from an identity.
func (h *Host) Traceroute(from *Identity, to uint32) error {
	now := time.Now()
	from.mu.Lock()
	if now.Sub(from.lastTraceroute) < 30*time.Second {
		from.mu.Unlock()
		return &RoutingError{pb.Routing_RATE_LIMIT_EXCEEDED}
	}
	from.lastTraceroute = now
	from.mu.Unlock()
	payload, _ := proto.Marshal(&pb.RouteDiscovery{})
	p := &pb.MeshPacket{To: to, WantAck: true,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TRACEROUTE_APP, Payload: payload,
			WantResponse: true}}}
	return h.Send(from, p)
}
