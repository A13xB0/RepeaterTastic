package phoneapi

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// adminToSelf is an admin message from the app to its own node.
func adminToSelf(id *mesh.Identity, pktID uint32, m *pb.AdminMessage) *pb.ToRadio {
	payload, _ := proto.Marshal(m)
	return &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: &pb.MeshPacket{To: id.NodeNum, Id: pktID,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ADMIN_APP, Payload: payload, WantResponse: true}}}}}
}

var (
	setRegion = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{
		PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_US}}}}}
	reboot   = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_RebootSeconds{RebootSeconds: 1}}
	getOwner = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}}
	rename   = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: &pb.User{LongName: "Renamed"}}}
)

func TestAppCantChangeNodeSettingsByDefault(t *testing.T) {
	h, id, _ := testServer(t)
	s, c := newTestSession(t, h, id)
	for i, m := range []*pb.AdminMessage{setRegion, reboot} {
		pktID := uint32(100 + i)
		s.HandleToRadio(adminToSelf(id, pktID, m))
		if errs := routingErrors(t, c.frames(), pktID); !slices.Contains(errs, pb.Routing_NOT_AUTHORIZED) {
			t.Fatalf("%s: routing %v, want NOT_AUTHORIZED", adminKind(m), errs)
		}
	}
	for i, m := range []*pb.AdminMessage{getOwner, rename} {
		pktID := uint32(200 + i)
		s.HandleToRadio(adminToSelf(id, pktID, m))
		if errs := routingErrors(t, c.frames(), pktID); slices.Contains(errs, pb.Routing_NOT_AUTHORIZED) {
			t.Fatalf("%s refused", adminKind(m))
		}
	}
}

func TestAppSettingsSwitchLetsSettingsThrough(t *testing.T) {
	h, id, _ := testServer(t)
	id.SetSettings(func(x *mesh.IdentitySettings) { x.AppSettings = true })
	s, c := newTestSession(t, h, id)
	s.HandleToRadio(adminToSelf(id, 300, setRegion))
	if errs := routingErrors(t, c.frames(), 300); slices.Contains(errs, pb.Routing_NOT_AUTHORIZED) {
		t.Fatal("set_config refused with app settings allowed")
	}
}

func TestAppAdminAllowList(t *testing.T) {
	for m, want := range map[*pb.AdminMessage]bool{
		getOwner: true, rename: true, setRegion: false, reboot: false,
		{PayloadVariant: &pb.AdminMessage_GetConfigRequest{}}:                                   true,
		{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: &pb.Channel{}}}:                true,
		{PayloadVariant: &pb.AdminMessage_SetModuleConfig{SetModuleConfig: &pb.ModuleConfig{}}}: false,
		{PayloadVariant: &pb.AdminMessage_SetFixedPosition{SetFixedPosition: &pb.Position{}}}:   false,
		{PayloadVariant: &pb.AdminMessage_FactoryResetDevice{FactoryResetDevice: 1}}:            false,
		{PayloadVariant: &pb.AdminMessage_SetFavoriteNode{SetFavoriteNode: 1}}:                  true,
	} {
		if got := appAdminAllowed(m); got != want {
			t.Errorf("%s allowed = %v, want %v", adminKind(m), got, want)
		}
	}
}
