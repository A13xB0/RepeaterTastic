package mqtt

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/pb"
)

func marshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJSONPayloads(t *testing.T) {
	bad := []byte{0xff, 0xff, 0xff}
	for _, c := range []struct {
		name     string
		port     pb.PortNum
		payload  []byte
		wantType string
		key      string
		want     any // payload[key]; nil = no payload expected
	}{
		{"nodeinfo", pb.PortNum_NODEINFO_APP,
			marshal(t, &pb.User{Id: "!11223344", LongName: "Hill", ShortName: "HILL", HwModel: pb.HardwareModel_HELTEC_V3}),
			"nodeinfo", "longname", "Hill"},
		{"bad nodeinfo", pb.PortNum_NODEINFO_APP, bad, "nodeinfo", "", nil},
		{"position", pb.PortNum_POSITION_APP,
			marshal(t, &pb.Position{LatitudeI: proto.Int32(561234567), SatsInView: 7}),
			"position", "sats_in_view", uint32(7)},
		{"position without sats", pb.PortNum_POSITION_APP,
			marshal(t, &pb.Position{LatitudeI: proto.Int32(561234567)}),
			"position", "latitude_i", int32(561234567)},
		{"bad position", pb.PortNum_POSITION_APP, bad, "position", "", nil},
		{"device telemetry", pb.PortNum_TELEMETRY_APP,
			marshal(t, &pb.Telemetry{Variant: &pb.Telemetry_DeviceMetrics{DeviceMetrics: &pb.DeviceMetrics{BatteryLevel: proto.Uint32(88)}}}),
			"telemetry", "battery_level", uint32(88)},
		{"environment telemetry", pb.PortNum_TELEMETRY_APP,
			marshal(t, &pb.Telemetry{Variant: &pb.Telemetry_EnvironmentMetrics{EnvironmentMetrics: &pb.EnvironmentMetrics{Temperature: proto.Float32(12.5)}}}),
			"telemetry", "temperature", float32(12.5)},
		{"other telemetry", pb.PortNum_TELEMETRY_APP,
			marshal(t, &pb.Telemetry{Variant: &pb.Telemetry_PowerMetrics{PowerMetrics: &pb.PowerMetrics{}}}),
			"telemetry", "", nil},
		{"bad telemetry", pb.PortNum_TELEMETRY_APP, bad, "telemetry", "", nil},
		{"waypoint", pb.PortNum_WAYPOINT_APP, marshal(t, &pb.Waypoint{Id: 9, Name: "Cairn"}), "waypoint", "name", "Cairn"},
		{"bad waypoint", pb.PortNum_WAYPOINT_APP, bad, "waypoint", "", nil},
		{"text", pb.PortNum_TEXT_MESSAGE_APP, []byte("hi"), "text", "text", "hi"},
	} {
		typ, payload := jsonPayload(&pb.Data{Portnum: c.port, Payload: c.payload})
		if c.want == nil {
			if payload != nil {
				t.Errorf("%s: payload %v, want none", c.name, payload)
			}
			continue
		}
		if typ != c.wantType || payload[c.key] != c.want {
			t.Errorf("%s: type %q payload %v; want %q with %s=%v", c.name, typ, payload, c.wantType, c.key, c.want)
		}
	}
}
