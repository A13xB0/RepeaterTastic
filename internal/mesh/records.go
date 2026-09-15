package mesh

import (
	"encoding/hex"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

func (h *Host) baseRecord(p *pb.MeshPacket, raw []byte, direction, kind string) PacketRecord {
	r := PacketRecord{Direction: direction, Kind: kind, ID: p.Id, From: wire.NodeID(p.From), To: wire.NodeID(p.To),
		ChannelHash: uint8(p.Channel), HopLimit: p.HopLimit, HopStart: p.HopStart, WantAck: p.WantAck, ViaMQTT: p.ViaMqtt,
		NextHop: p.NextHop, RelayNode: p.RelayNode, SNR: p.RxSnr, PKI: p.PkiEncrypted,
		Transport: strings.TrimPrefix(strings.ToLower(p.TransportMechanism.String()), "transport_")}
	if p.RxRssi != nil {
		r.RSSI = *p.RxRssi
	}
	if raw != nil {
		r.Raw = hex.EncodeToString(raw)
		r.Size = len(raw)
	} else {
		r.Size = wire.HeaderLen + len(p.GetEncrypted())
	}
	r.AirtimeMs = h.RadioParams().AirtimeMs(r.Size)
	if gs := h.channelGroups(uint8(p.Channel)); len(gs) > 0 && !(p.Channel == 0 && h.Identity(p.To) != nil) {
		r.Channel = gs[0].name
	}
	return r
}

func (h *Host) fillRecordFromDecoded(r *PacketRecord, p *pb.MeshPacket, dec decodeResult) {
	r.Port = dec.data.Portnum.String()
	r.PKI = dec.pki
	if dec.group != nil {
		r.Channel = dec.group.name
	} else if dec.pki {
		r.Channel = "PKI"
	}
	if len(dec.deliveries) > 0 {
		r.DecodedBy = dec.deliveries[0].id.NodeID()
	} else if dec.group != nil && len(dec.group.members) > 0 {
		r.DecodedBy = dec.group.members[0].id.NodeID()
	}
	r.Summary = summarize(dec.data)
	r.Payload = payloadJSON(dec.data)
}

func (h *Host) publishPacket(r PacketRecord) {
	r = h.Packets.Add(r)
	h.Bus.Publish(Event{Type: "packet", Data: r})
}

// summarize is a one-line human description of a decoded payload.
func summarize(d *pb.Data) string {
	if d == nil {
		return ""
	}
	switch d.Portnum {
	case pb.PortNum_TEXT_MESSAGE_APP:
		return string(d.Payload)
	case pb.PortNum_NODEINFO_APP:
		u := &pb.User{}
		if proto.Unmarshal(d.Payload, u) == nil {
			return fmt.Sprintf("%s (%s) %s", u.LongName, u.ShortName, u.HwModel)
		}
	case pb.PortNum_POSITION_APP:
		pos := &pb.Position{}
		if proto.Unmarshal(d.Payload, pos) == nil && (pos.GetLatitudeI() != 0 || pos.GetLongitudeI() != 0) {
			return fmt.Sprintf("%.5f, %.5f", float64(pos.GetLatitudeI())/1e7, float64(pos.GetLongitudeI())/1e7)
		}
		return "position request"
	case pb.PortNum_ROUTING_APP:
		rt := &pb.Routing{}
		if proto.Unmarshal(d.Payload, rt) == nil {
			if rt.GetErrorReason() == pb.Routing_NONE {
				return fmt.Sprintf("ACK for %08x", d.RequestId)
			}
			return fmt.Sprintf("NAK %s for %08x", rt.GetErrorReason(), d.RequestId)
		}
	case pb.PortNum_TELEMETRY_APP:
		t := &pb.Telemetry{}
		if proto.Unmarshal(d.Payload, t) == nil {
			if m := t.GetDeviceMetrics(); m != nil {
				return fmt.Sprintf("battery %d%% %.2fV ch %.1f%% air %.1f%%", m.GetBatteryLevel(), m.GetVoltage(),
					m.GetChannelUtilization(), m.GetAirUtilTx())
			}
			if m := t.GetEnvironmentMetrics(); m != nil {
				return fmt.Sprintf("%.1f°C %.0f%% %.0fhPa", m.GetTemperature(), m.GetRelativeHumidity(), m.GetBarometricPressure())
			}
		}
	case pb.PortNum_TRACEROUTE_APP:
		rd := &pb.RouteDiscovery{}
		if proto.Unmarshal(d.Payload, rd) == nil {
			if d.RequestId != 0 {
				return fmt.Sprintf("traceroute reply, %d hops out, %d back", len(rd.Route), len(rd.RouteBack))
			}
			return fmt.Sprintf("traceroute, %d hops so far", len(rd.Route))
		}
	}
	return ""
}

var protoJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}

// payloadJSON decodes well-known payloads for the packet detail view.
func payloadJSON(d *pb.Data) map[string]any {
	var m proto.Message
	switch d.Portnum {
	case pb.PortNum_TEXT_MESSAGE_APP:
		return map[string]any{"text": string(d.Payload)}
	case pb.PortNum_NODEINFO_APP:
		m = &pb.User{}
	case pb.PortNum_POSITION_APP:
		m = &pb.Position{}
	case pb.PortNum_ROUTING_APP:
		m = &pb.Routing{}
	case pb.PortNum_TELEMETRY_APP:
		m = &pb.Telemetry{}
	case pb.PortNum_TRACEROUTE_APP:
		m = &pb.RouteDiscovery{}
	case pb.PortNum_NEIGHBORINFO_APP:
		m = &pb.NeighborInfo{}
	case pb.PortNum_WAYPOINT_APP:
		m = &pb.Waypoint{}
	default:
		return map[string]any{"payload_hex": hex.EncodeToString(d.Payload), "request_id": d.RequestId}
	}
	if proto.Unmarshal(d.Payload, m) != nil {
		return nil
	}
	b, err := protoJSON.Marshal(m)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if jsonUnmarshal(b, &out) != nil {
		return nil
	}
	if d.RequestId != 0 {
		out["_request_id"] = d.RequestId
	}
	return out
}
