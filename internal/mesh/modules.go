package mesh

import (
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

// nodeInfoRequestInterval limits how often an identity asks for a node's NodeInfo.
const nodeInfoRequestInterval = 60 * time.Second

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

// RequestNodeInfo sends an identity's User to a node (or everyone), asking for theirs back.
func (h *Host) RequestNodeInfo(from *Identity, to uint32) {
	now := time.Now()
	from.mu.Lock()
	if !from.lastNodeInfoTx.IsZero() && now.Sub(from.lastNodeInfoTx) < nodeInfoRequestInterval {
		from.mu.Unlock()
		return
	}
	from.lastNodeInfoTx = now
	from.mu.Unlock()
	u := from.UserCopy()
	u.HwModel = h.Hardware()
	payload, _ := proto.Marshal(u)
	p := &pb.MeshPacket{To: to, Priority: pb.MeshPacket_DEFAULT,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: payload,
			WantResponse: true}}}
	if err := h.Send(from, p); err != nil {
		h.log.Debug("nodeinfo not sent", "identity", from.NodeID(), "err", err)
	}
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
