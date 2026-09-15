package phoneapi

import (
	"io"
	"log/slog"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/pb"
)

// Each identity is configured from its own app: role, hop limit and fixed position are the
// identity's; the shared radio's settings are not changed from a client.
func TestClientConfiguresItsIdentity(t *testing.T) {
	h, id, _ := testServer(t)
	s := NewSession(h, id, slog.New(slog.NewTextHandler(io.Discard, nil)), func(*pb.FromRadio) bool { return true })
	admin := func(m *pb.AdminMessage) {
		t.Helper()
		b, _ := proto.Marshal(m)
		s.handleAdmin(&pb.MeshPacket{From: id.NodeNum, To: id.NodeNum, Id: 1,
			PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ADMIN_APP, Payload: b}}})
	}
	radioBefore := h.RadioParams()

	admin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Device{
		Device: &pb.Config_DeviceConfig{Role: pb.Config_DeviceConfig_CLIENT_MUTE}}}}})
	if id.UserCopy().Role != pb.Config_DeviceConfig_CLIENT_MUTE {
		t.Errorf("role not applied: %v", id.UserCopy().Role)
	}

	admin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Lora{
		Lora: &pb.Config_LoRaConfig{HopLimit: 2, UsePreset: true, ModemPreset: pb.Config_LoRaConfig_SHORT_FAST, Region: radioBefore.Region.Code}}}}})
	if id.MaxHops() != 2 {
		t.Errorf("hop limit cap = %d, want 2", id.MaxHops())
	}
	if h.RadioParams().Preset != radioBefore.Preset {
		t.Error("a client retuned the shared radio")
	}
	if got := s.configByType(pb.AdminMessage_LORA_CONFIG).GetLora().GetHopLimit(); got != 2 {
		t.Errorf("app sees hop limit %d, want 2", got)
	}

	lat, lon, alt := int32(562055731), int32(-31618580), int32(95)
	admin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetFixedPosition{SetFixedPosition: &pb.Position{
		LatitudeI: &lat, LongitudeI: &lon, Altitude: &alt}}})
	if p, ok := id.FixedPosition(); !ok || p.Altitude != 95 || p.Latitude < 56.2 || p.Latitude > 56.21 {
		t.Fatalf("fixed position = %+v %v", p, ok)
	}
	if !s.configByType(pb.AdminMessage_POSITION_CONFIG).GetPosition().GetFixedPosition() {
		t.Error("app doesn't see the fixed position")
	}
	if e, _ := h.DB.Get(id.NodeNum); e.Position == nil || e.Position.GetAltitude() != 95 {
		t.Errorf("own node DB position = %v", e.Position)
	}

	admin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_RemoveFixedPosition{RemoveFixedPosition: true}})
	if _, ok := id.FixedPosition(); ok {
		t.Error("fixed position not removed")
	}
}
