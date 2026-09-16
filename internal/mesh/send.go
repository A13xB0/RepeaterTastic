package mesh

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// RoutingError is a send failure the client API reports as a Routing NAK.
type RoutingError struct{ Reason pb.Routing_Error }

func (e *RoutingError) Error() string { return "routing: " + e.Reason.String() }

// ErrNotRunning is returned for a send from an identity with no node behind it.
var ErrNotRunning = errors.New("the identity's node isn't running")

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Send originates a decoded packet from an identity (client app, web UI or a plugin): its node
// (meshtasticd) encrypts, transmits and retries it. p.From is overwritten; zero Id, HopLimit and
// To get defaults.
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
	if d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
		m := &Message{ID: p.Id, From: from.NodeID(), To: wire.NodeID(p.To), Channel: int(p.Channel), Text: string(d.Payload),
			Time: time.Now().UnixMilli(), Direction: "out", Status: "queued", PKI: p.To != wire.Broadcast}
		h.Messages.Add(from.NodeNum, m)
		h.publishMessage(from, *m)
	}
	r := from.Remote()
	if r == nil {
		h.failMessage(from, p.Id, pb.Routing_NO_INTERFACE)
		return ErrNotRunning
	}
	return h.sendRemote(from, r, p)
}

// failMessage marks one of an identity's messages failed (or acked, for Routing_NONE).
func (h *Host) failMessage(id *Identity, reqID uint32, reason pb.Routing_Error) {
	status := "failed"
	if reason == pb.Routing_NONE {
		status = "acked"
	}
	if m, ok := h.Messages.SetStatus(id.NodeNum, reqID, status, errString(reason)); ok {
		h.publishMessage(id, m)
	}
}

func errString(r pb.Routing_Error) string {
	if r == pb.Routing_NONE {
		return ""
	}
	return r.String()
}

// publishMessage announces a new or updated message, where the identity's chat views listen.
func (h *Host) publishMessage(id *Identity, m Message) {
	h.Bus.Publish(Event{Type: "message", Data: MessageEvent{Identity: id.NodeID(), Message: m}})
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
