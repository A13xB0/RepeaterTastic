package phoneapi

import (
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

// handleAdmin serves AdminMessages a client sends to its own node. Radio settings belong to the
// host and are reported but not changed; names, secondary channels and node flags are per identity.
func (s *Session) handleAdmin(p *pb.MeshPacket) {
	req := &pb.AdminMessage{}
	if err := proto.Unmarshal(p.GetDecoded().Payload, req); err != nil {
		return
	}
	var resp *pb.AdminMessage
	changed := false
	switch v := req.PayloadVariant.(type) {
	case *pb.AdminMessage_GetOwnerRequest:
		resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerResponse{GetOwnerResponse: s.id.UserCopy()}}
	case *pb.AdminMessage_GetChannelRequest:
		idx := int(v.GetChannelRequest) - 1
		resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelResponse{GetChannelResponse: s.channel(idx)}}
	case *pb.AdminMessage_GetConfigRequest:
		if c := s.configByType(v.GetConfigRequest); c != nil {
			resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigResponse{GetConfigResponse: c}}
		}
	case *pb.AdminMessage_GetModuleConfigRequest:
		if m := moduleConfigByType(v.GetModuleConfigRequest); m != nil {
			resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetModuleConfigResponse{GetModuleConfigResponse: m}}
		}
	case *pb.AdminMessage_GetDeviceMetadataRequest:
		resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetDeviceMetadataResponse{GetDeviceMetadataResponse: s.metadata()}}
	case *pb.AdminMessage_GetCannedMessageModuleMessagesRequest:
		resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetCannedMessageModuleMessagesResponse{}}
	case *pb.AdminMessage_GetRingtoneRequest:
		resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetRingtoneResponse{}}
	case *pb.AdminMessage_GetDeviceConnectionStatusRequest:
		resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetDeviceConnectionStatusResponse{
			GetDeviceConnectionStatusResponse: &pb.DeviceConnectionStatus{
				Ethernet: &pb.EthernetConnectionStatus{Status: &pb.NetworkConnectionStatus{IsConnected: true}}}}}
	case *pb.AdminMessage_GetUiConfigRequest:
		resp = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetUiConfigResponse{GetUiConfigResponse: &pb.DeviceUIConfig{}}}

	case *pb.AdminMessage_SetOwner:
		s.id.SetOwner(v.SetOwner.GetLongName(), v.SetOwner.GetShortName())
		s.host.DB.Update(s.id.NodeNum, func(e *mesh.NodeEntry) { e.User = s.id.UserCopy() })
		changed = true
		s.host.RequestNodeInfo(s.id, wire.Broadcast)
	case *pb.AdminMessage_SetChannel:
		if err := s.host.SetChannel(s.id, v.SetChannel); err != nil {
			s.log.Info("channel change refused", "identity", s.id.NodeID(), "err", err)
		} else {
			changed = true
		}
	case *pb.AdminMessage_SetConfig:
		changed = s.applyClientConfig(v.SetConfig)
	case *pb.AdminMessage_SetModuleConfig:
		s.log.Info("ignoring set_module_config from client", "identity", s.id.NodeID())
	case *pb.AdminMessage_RemoveByNodenum:
		s.host.DB.Delete(v.RemoveByNodenum)
	case *pb.AdminMessage_SetFavoriteNode:
		s.host.DB.Update(v.SetFavoriteNode, func(e *mesh.NodeEntry) { e.Favorite = true })
	case *pb.AdminMessage_RemoveFavoriteNode:
		s.host.DB.Update(v.RemoveFavoriteNode, func(e *mesh.NodeEntry) { e.Favorite = false })
	case *pb.AdminMessage_SetIgnoredNode:
		s.host.DB.Update(v.SetIgnoredNode, func(e *mesh.NodeEntry) { e.Ignored = true })
	case *pb.AdminMessage_RemoveIgnoredNode:
		s.host.DB.Update(v.RemoveIgnoredNode, func(e *mesh.NodeEntry) { e.Ignored = false })
	case *pb.AdminMessage_AddContact:
		if c := v.AddContact; c != nil && c.User != nil && c.NodeNum != 0 {
			s.host.DB.SetUser(c.NodeNum, c.User)
		}
	case *pb.AdminMessage_SetFixedPosition:
		if pos := v.SetFixedPosition; pos != nil {
			p := &mesh.IdentityPosition{Latitude: float64(pos.GetLatitudeI()) / 1e7, Longitude: float64(pos.GetLongitudeI()) / 1e7,
				Altitude: pos.GetAltitude()}
			if err := s.id.SetFixedPosition(p); err != nil {
				s.log.Info("fixed position refused", "identity", s.id.NodeID(), "err", err)
			} else {
				changed = true
				s.host.RecordOwnPositions()
				s.log.Info("fixed position set from client", "identity", s.id.NodeID())
			}
		}
	case *pb.AdminMessage_RemoveFixedPosition:
		_ = s.id.SetFixedPosition(nil)
		changed = true
		s.host.RecordOwnPositions()
	case *pb.AdminMessage_BeginEditSettings, *pb.AdminMessage_CommitEditSettings, *pb.AdminMessage_SetTimeOnly,
		*pb.AdminMessage_StoreUiConfig:
		// accepted, nothing to do
	case *pb.AdminMessage_RebootSeconds, *pb.AdminMessage_ShutdownSeconds, *pb.AdminMessage_FactoryResetDevice,
		*pb.AdminMessage_FactoryResetConfig, *pb.AdminMessage_NodedbReset, *pb.AdminMessage_RebootOtaSeconds:
		s.log.Info("ignoring reboot/reset request for a virtual node", "identity", s.id.NodeID())
	}
	if changed {
		if err := s.host.SaveIdentities(); err != nil {
			s.log.Warn("saving identities", "err", err)
		}
	}
	if resp == nil || !p.GetDecoded().WantResponse {
		return
	}
	resp.SessionPasskey = s.passkey
	payload, _ := proto.Marshal(resp)
	rx := uint32(time.Now().Unix())
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{
		From: s.id.NodeNum, To: s.id.NodeNum, Id: wire.RandomPacketID(), Channel: p.Channel, RxTime: &rx,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ADMIN_APP, Payload: payload,
			RequestId: p.Id}}}}})
}

func configName(c *pb.Config) string {
	if c == nil || c.PayloadVariant == nil {
		return ""
	}
	return string(c.ProtoReflect().WhichOneof(c.ProtoReflect().Descriptor().Oneofs().ByName("payload_variant")).Name())
}

// applyClientConfig applies the parts of a set_config that belong to this identity: its device
// role, its hop limit cap and its position settings. Everything that would retune the shared
// radio (region, preset, power, frequency) is refused, since other identities use it too.
func (s *Session) applyClientConfig(c *pb.Config) (changed bool) {
	switch v := c.GetPayloadVariant().(type) {
	case *pb.Config_Device:
		if role := v.Device.GetRole(); role != s.id.UserCopy().Role {
			if err := s.id.SetRole(role.String()); err == nil {
				changed = true
				s.host.DB.Update(s.id.NodeNum, func(e *mesh.NodeEntry) { e.User = s.id.UserCopy() })
				s.log.Info("role set from client", "identity", s.id.NodeID(), "role", role.String())
			}
		}
	case *pb.Config_Lora:
		lora := v.Lora
		radio := s.host.Config().HopLimit
		limit := lora.GetHopLimit()
		if limit >= radio {
			limit = 0 // at or above the radio's own limit: no cap
		}
		if limit != s.id.MaxHops() {
			if err := s.id.SetMaxHops(limit); err == nil {
				changed = true
				s.log.Info("hop limit set from client", "identity", s.id.NodeID(), "hop_limit", lora.GetHopLimit())
			}
		}
		rp := s.host.RadioParams()
		if lora.GetRegion() != rp.Region.Code || (lora.GetUsePreset() && lora.GetModemPreset() != rp.Preset) {
			s.log.Info("ignoring radio settings from client; the radio is shared by every identity",
				"identity", s.id.NodeID())
		}
	case *pb.Config_Position:
		pc := v.Position
		if secs := pc.GetPositionBroadcastSecs(); secs != s.id.PositionInterval() {
			if secs != 0 && secs < 1800 {
				secs = 1800
			}
			s.id.SetPositionInterval(secs)
			changed = true
		}
		if !pc.GetFixedPosition() {
			if _, has := s.id.FixedPosition(); has {
				_ = s.id.SetFixedPosition(nil)
				changed = true
				s.host.RecordOwnPositions()
			}
		}
	default:
		s.log.Info("ignoring set_config from client; managed by the host", "identity", s.id.NodeID(), "section", configName(c))
	}
	return changed
}
