package plugins

import (
	"encoding/json"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
	"github.com/A13xB0/RepeaterTastic/pb"
	pluginv1 "github.com/A13xB0/RepeaterTastic/pluginapi/v1"
)

func (m *Manager) radiosProto() []*pluginv1.Radio {
	out := make([]*pluginv1.Radio, 0, len(m.opt.Radios))
	for _, r := range m.opt.Radios {
		rp := r.Host.RadioParams()
		pr := &pluginv1.Radio{Id: r.ID, Name: r.Name, Region: rp.Region.Name, Preset: rp.Preset.String(),
			PresetName: rp.PresetName(), FrequencyMhz: rp.FrequencyMHz, Connected: r.Host.RadioConfigured()}
		if relay := r.Host.Relay(); relay != nil {
			pr.Relay = identityProto(r.ID, relay)
			pr.Identities = append(pr.Identities, pr.Relay)
		}
		for _, id := range r.Host.Identities() {
			if !id.IsRelay {
				pr.Identities = append(pr.Identities, identityProto(r.ID, id))
			}
		}
		out = append(out, pr)
	}
	return out
}

func identityProto(radioID string, id *mesh.Identity) *pluginv1.Identity {
	u := id.UserCopy()
	return &pluginv1.Identity{NodeId: id.NodeID(), NodeNum: id.NodeNum, LongName: u.GetLongName(), ShortName: u.GetShortName(), RadioId: radioID}
}

func packetEvent(r Radio, rec mesh.PacketRecord) *pluginv1.HostMessage {
	if rec.Mesh == nil {
		return nil
	}
	p := proto.Clone(rec.Mesh).(*pb.MeshPacket)
	p.PkiEncrypted = rec.PKI
	if p.RxTime == nil && rec.Direction == "rx" {
		t := uint32(rec.Time / 1000)
		p.RxTime = &t
	}
	relayIndex := int32(-1)
	var holders []*pluginv1.ChannelHolder
	if rec.Data != nil {
		p.PayloadVariant = &pb.MeshPacket_Decoded{Decoded: rec.Data}
		for _, h := range rec.Holders {
			holders = append(holders, &pluginv1.ChannelHolder{NodeNum: h.NodeNum, ChannelIndex: uint32(h.Index)})
			if h.Relay && relayIndex < 0 {
				p.Channel, relayIndex = uint32(h.Index), int32(h.Index)
			}
		}
	}
	b, err := proto.Marshal(p)
	if err != nil {
		return nil
	}
	ev := &pluginv1.PacketEvent{RadioId: r.ID, Direction: rec.Direction, Kind: rec.Kind, MeshPacket: b, Decoded: rec.Data != nil,
		ChannelHash: uint32(rec.ChannelHash), ChannelName: rec.Channel, TimeMs: rec.Time, RelayChannelIndex: relayIndex, Holders: holders}
	if relay := r.Host.Relay(); relay != nil {
		ev.ReporterNodeNum = relay.NodeNum
	}
	return &pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Packet{Packet: ev}}
}

// textEvent: messages to and from a radio's relay persona, once each (outgoing ones are
// published again as their status changes; only the first, "queued", goes to plugins).
func textEvent(r Radio, me mesh.MessageEvent) *pluginv1.HostMessage {
	relay := r.Host.Relay()
	if relay == nil || me.Identity != relay.NodeID() {
		return nil
	}
	msg := me.Message
	if msg.Direction == "out" && msg.Status != "queued" {
		return nil
	}
	from, _ := wire.ParseNodeID(msg.From)
	to, _ := wire.ParseNodeID(msg.To)
	return &pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Text{Text: &pluginv1.TextMessageEvent{
		RadioId: r.ID, IdentityNodeId: me.Identity, From: from, To: to, Channel: uint32(max(0, msg.Channel)),
		Direct: to != wire.Broadcast, Text: msg.Text, PacketId: msg.ID, TimeMs: msg.Time, Direction: msg.Direction}}}
}

func nodeProto(radioID string, n mesh.NodeEntry) *pluginv1.Node {
	out := &pluginv1.Node{NodeNum: n.Num, NodeId: wire.NodeID(n.Num), RadioId: radioID, LastHeardMs: n.LastHeard.UnixMilli(),
		Snr: n.SNR, Rssi: n.RSSI, HopsAway: int32(n.HopsAway), ViaMqtt: n.ViaMQTT, Local: n.Local}
	if n.LastHeard.IsZero() {
		out.LastHeardMs = 0
	}
	if n.User != nil {
		out.User, _ = proto.Marshal(n.User)
	}
	if n.Position != nil {
		out.Position, _ = proto.Marshal(n.Position)
	}
	if n.Metrics != nil {
		out.DeviceMetrics, _ = proto.Marshal(n.Metrics)
	}
	return out
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
