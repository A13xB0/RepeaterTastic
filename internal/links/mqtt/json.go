package mqtt

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/pb"
)

// jsonPacket renders a decoded packet in the firmware's MQTT JSON shape
// (MeshPacketSerializer), or nil for port numbers the firmware doesn't serialise.
func jsonPacket(p *pb.MeshPacket, d *pb.Data, gateway string) []byte {
	var typ string
	var payload any
	switch d.Portnum {
	case pb.PortNum_TEXT_MESSAGE_APP:
		typ, payload = "text", map[string]any{"text": string(d.Payload)}
	case pb.PortNum_NODEINFO_APP:
		u := &pb.User{}
		if proto.Unmarshal(d.Payload, u) != nil {
			return nil
		}
		typ, payload = "nodeinfo", map[string]any{"id": u.GetId(), "longname": u.GetLongName(), "shortname": u.GetShortName(),
			"hardware": int32(u.GetHwModel()), "role": int32(u.GetRole())}
	case pb.PortNum_POSITION_APP:
		pos := &pb.Position{}
		if proto.Unmarshal(d.Payload, pos) != nil {
			return nil
		}
		m := map[string]any{"latitude_i": pos.GetLatitudeI(), "longitude_i": pos.GetLongitudeI(), "altitude": pos.GetAltitude(),
			"time": pos.GetTime(), "precision_bits": pos.GetPrecisionBits()}
		if pos.GetSatsInView() > 0 {
			m["sats_in_view"] = pos.GetSatsInView()
		}
		typ, payload = "position", m
	case pb.PortNum_TELEMETRY_APP:
		t := &pb.Telemetry{}
		if proto.Unmarshal(d.Payload, t) != nil {
			return nil
		}
		m := map[string]any{}
		if dm := t.GetDeviceMetrics(); dm != nil {
			m["battery_level"], m["voltage"], m["channel_utilization"] = dm.GetBatteryLevel(), dm.GetVoltage(), dm.GetChannelUtilization()
			m["air_util_tx"], m["uptime_seconds"] = dm.GetAirUtilTx(), dm.GetUptimeSeconds()
		} else if em := t.GetEnvironmentMetrics(); em != nil {
			m["temperature"], m["relative_humidity"], m["barometric_pressure"] = em.GetTemperature(), em.GetRelativeHumidity(), em.GetBarometricPressure()
			m["gas_resistance"], m["voltage"], m["current"] = em.GetGasResistance(), em.GetVoltage(), em.GetCurrent()
		} else {
			return nil
		}
		typ, payload = "telemetry", m
	case pb.PortNum_WAYPOINT_APP:
		w := &pb.Waypoint{}
		if proto.Unmarshal(d.Payload, w) != nil {
			return nil
		}
		typ, payload = "waypoint", map[string]any{"id": w.GetId(), "name": w.GetName(), "description": w.GetDescription(),
			"expire": w.GetExpire(), "locked_to": w.GetLockedTo(), "latitude_i": w.GetLatitudeI(), "longitude_i": w.GetLongitudeI()}
	default:
		return nil
	}
	hopsAway := 0
	if p.HopStart >= p.HopLimit {
		hopsAway = int(p.HopStart - p.HopLimit)
	}
	ts := time.Now().Unix()
	if p.RxTime != nil && *p.RxTime > 0 {
		ts = int64(*p.RxTime)
	}
	out := map[string]any{"channel": p.Channel, "from": p.From, "to": p.To, "id": p.Id, "hop_start": p.HopStart,
		"hops_away": hopsAway, "sender": gateway, "timestamp": ts, "type": typ, "payload": payload}
	if p.RxSnr != 0 {
		out["snr"] = p.RxSnr
	}
	if p.RxRssi != nil {
		out["rssi"] = *p.RxRssi
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(fmt.Sprintf("mqtt json: %v", err)) // only plain values above
	}
	return b
}
