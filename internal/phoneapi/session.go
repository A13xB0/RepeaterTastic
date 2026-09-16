// Package phoneapi serves the Meshtastic client API (the protocol the Android/iOS apps, the Python
// CLI and the web client speak) for one virtual node. Mirrors firmware src/mesh/PhoneAPI.cpp.
package phoneapi

import (
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const (
	nonceOnlyConfig = 69420
	nonceOnlyNodes  = 69421

	// FirmwareVersion is what we report; apps gate features on it.
	FirmwareVersion = "2.8.1.rptrtst"
	minAppVersion   = 30200
	queueMax        = 16
)

// Session is one connected client of one identity. Output is delivered through send.
type Session struct {
	host *mesh.Host
	id   *mesh.Identity
	log  *slog.Logger
	send func(*pb.FromRadio) bool

	mu          sync.Mutex
	nextID      uint32
	configDone  bool
	attached    bool
	closed      bool
	lastText    time.Time
	lastTrace   time.Time
	recentIDs   map[uint32]time.Time
	pendingLive []*pb.FromRadio
}

// NewSession creates a session; send must not block for long (drop and return false if full).
func NewSession(h *mesh.Host, id *mesh.Identity, log *slog.Logger, send func(*pb.FromRadio) bool) *Session {
	return &Session{host: h, id: id, log: log, send: send, recentIDs: map[uint32]time.Time{}}
}

// SendFromRadio implements mesh.ClientSink: live packets for this identity.
func (s *Session) SendFromRadio(fr *pb.FromRadio) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if !s.configDone {
		// Firmware queues live traffic until the handshake completes.
		s.pendingLive = append(s.pendingLive, fr)
		if len(s.pendingLive) > 64 {
			s.pendingLive = s.pendingLive[1:]
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.emit(fr)
}

func (s *Session) emit(fr *pb.FromRadio) {
	out := proto.Clone(fr).(*pb.FromRadio)
	s.mu.Lock()
	s.nextID++
	out.Id = s.nextID
	s.mu.Unlock()
	if !s.send(out) {
		s.log.Debug("client too slow, dropped frame", "identity", s.id.NodeID())
	}
}

// Close detaches the session from its identity.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	attached := s.attached
	s.mu.Unlock()
	if attached {
		s.id.RemoveSink(s)
	}
}

// HandleToRadio processes one decoded ToRadio from the client.
func (s *Session) HandleToRadio(tr *pb.ToRadio) (disconnect bool) {
	switch v := tr.PayloadVariant.(type) {
	case *pb.ToRadio_WantConfigId:
		s.startConfig(v.WantConfigId)
	case *pb.ToRadio_Packet:
		s.handlePacket(v.Packet)
	case *pb.ToRadio_Heartbeat:
		if v.Heartbeat.GetNonce() == 1 {
			s.host.RequestNodeInfo(s.id, wire.Broadcast)
		} else {
			s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_QueueStatus{QueueStatus: s.queueStatus(0, 0)}})
		}
	case *pb.ToRadio_Disconnect:
		return true
	case *pb.ToRadio_XmodemPacket:
		s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_XmodemPacket{
			XmodemPacket: &pb.XModem{Control: pb.XModem_NAK, Seq: v.XmodemPacket.GetSeq()}}})
	}
	return false
}

func (s *Session) queueStatus(res int32, packetID uint32) *pb.QueueStatus {
	return &pb.QueueStatus{Res: res, Free: queueMax, Maxlen: queueMax, MeshPacketId: packetID}
}

// ------------------------------------------------------------------------------------ handshake

func (s *Session) startConfig(nonce uint32) {
	s.mu.Lock()
	s.configDone = false
	s.mu.Unlock()

	// A nodes-only request (the Android app's Stage 2) goes straight to the other nodes, as the
	// firmware does: the app treats any my_info as the start of a new handshake, so re-sending
	// it here made the app ignore this stage's config_complete and give up after 12 s.
	if nonce != nonceOnlyNodes {
		s.emitConfig()
	}
	if nonce != nonceOnlyConfig {
		s.emitNodes()
	}
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce}})
	s.finishConfig()
}

// emitConfig sends the handshake's own-node and configuration stage, in the firmware's order.
func (s *Session) emitConfig() {
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: s.myInfo()}})
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_DeviceuiConfig{DeviceuiConfig: &pb.DeviceUIConfig{}}})
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: s.ownNodeInfo()}})
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Metadata{Metadata: s.metadata()}})
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_RegionPresets{RegionPresets: regionPresetMap()}})
	for i := 0; i < mesh.MaxChannels; i++ {
		s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Channel{Channel: s.channel(i)}})
	}
	for _, c := range s.allConfigs() {
		s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: c}})
	}
	for _, m := range allModuleConfigs() {
		s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ModuleConfig{ModuleConfig: m}})
	}
}

// emitNodes sends the other nodes the client should know about; local ones show as zero hops.
func (s *Session) emitNodes() {
	for _, e := range s.host.DB.Snapshot() {
		if e.Num == s.id.NodeNum || (e.User == nil && e.LastHeard.IsZero()) {
			continue
		}
		ni := e.NodeInfo()
		if e.Local {
			h := uint32(0)
			ni.HopsAway = &h
		}
		s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: ni}})
	}
}

// finishConfig marks the handshake done, attaches the session on its first handshake and
// releases the live traffic queued meanwhile.
func (s *Session) finishConfig() {
	s.mu.Lock()
	s.configDone = true
	pending := s.pendingLive
	s.pendingLive = nil
	first := !s.attached
	s.attached = true
	s.mu.Unlock()
	if first {
		s.id.AddSink(s)
		s.id.FlushBacklog(s)
	}
	for _, fr := range pending {
		s.emit(fr)
	}
}

func (s *Session) myInfo() *pb.MyNodeInfo {
	devID := make([]byte, 16)
	copy(devID, s.id.PublicKey)
	return &pb.MyNodeInfo{
		MyNodeNum:     s.id.NodeNum,
		RebootCount:   0,
		MinAppVersion: minAppVersion,
		DeviceId:      devID,
		PioEnv:        "repeatertastic",
		NodedbCount:   uint32(len(s.host.DB.Snapshot())),
	}
}

func (s *Session) ownNodeInfo() *pb.NodeInfo {
	e, _ := s.host.DB.Get(s.id.NodeNum)
	e.Num = s.id.NodeNum
	e.User = s.id.UserCopy()
	e.User.HwModel = s.host.Hardware()
	e.LastHeard = time.Now()
	ni := e.NodeInfo()
	ni.HopsAway = nil
	ni.IsFavorite = true
	return ni
}

func (s *Session) metadata() *pb.DeviceMetadata {
	return &pb.DeviceMetadata{
		FirmwareVersion:    FirmwareVersion,
		DeviceStateVersion: 24,
		CanShutdown:        false,
		HasWifi:            false,
		HasBluetooth:       false,
		HasEthernet:        true,
		Role:               s.id.UserCopy().Role,
		PositionFlags:      811,
		HwModel:            s.host.Hardware(),
		HasRemoteHardware:  false,
		HasPKC:             true,
	}
}

func (s *Session) channel(i int) *pb.Channel {
	ch := s.id.ChannelCopy(i)
	if ch == nil {
		ch = &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED, Settings: &pb.ChannelSettings{}}
	}
	return ch
}

// configByType builds one Config variant (device, lora, ...).
func (s *Session) configByType(t pb.AdminMessage_ConfigType) *pb.Config {
	hc := s.host.Config()
	rp := s.host.RadioParams()
	u := s.id.UserCopy()
	switch t {
	case pb.AdminMessage_DEVICE_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Device{Device: &pb.Config_DeviceConfig{
			Role: u.Role, RebroadcastMode: pb.Config_DeviceConfig_ALL,
			NodeInfoBroadcastSecs: uint32(hc.NodeInfoInterval / time.Second)}}}
	case pb.AdminMessage_POSITION_CONFIG:
		_, fixed := s.id.FixedPosition()
		secs := s.id.PositionInterval()
		if secs == 0 {
			secs = 3 * 3600
			if iv := hc.Position.Interval; iv > 0 {
				secs = uint32(iv / time.Second)
			}
		}
		return &pb.Config{PayloadVariant: &pb.Config_Position{Position: &pb.Config_PositionConfig{
			GpsMode: pb.Config_PositionConfig_NOT_PRESENT, PositionBroadcastSecs: secs, FixedPosition: fixed}}}
	case pb.AdminMessage_POWER_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Power{Power: &pb.Config_PowerConfig{}}}
	case pb.AdminMessage_NETWORK_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Network{Network: &pb.Config_NetworkConfig{EthEnabled: true}}}
	case pb.AdminMessage_DISPLAY_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Display{Display: &pb.Config_DisplayConfig{}}}
	case pb.AdminMessage_LORA_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{
			UsePreset: true, ModemPreset: rp.Preset, Region: rp.Region.Code, HopLimit: s.hopLimit(hc.HopLimit), TxEnabled: true,
			TxPower: int32(rp.TxPowerDBm), ChannelNum: uint32(rp.Slot + 1), OverrideDutyCycle: hc.OverrideDutyCycle,
			Bandwidth: uint32(rp.BwKHz), SpreadFactor: uint32(rp.SF), CodingRate: uint32(rp.CR),
			OverrideFrequency: float32(hc.OverrideFreqMHz), FrequencyOffset: float32(hc.FreqOffsetMHz)}}}
	case pb.AdminMessage_BLUETOOTH_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Bluetooth{Bluetooth: &pb.Config_BluetoothConfig{Enabled: false}}}
	case pb.AdminMessage_SECURITY_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{
			PublicKey: s.id.PublicKey, PrivateKey: s.id.PrivateKey, SerialEnabled: true}}}
	case pb.AdminMessage_SESSIONKEY_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_Sessionkey{Sessionkey: &pb.Config_SessionkeyConfig{}}}
	case pb.AdminMessage_DEVICEUI_CONFIG:
		return &pb.Config{PayloadVariant: &pb.Config_DeviceUi{DeviceUi: &pb.DeviceUIConfig{}}}
	}
	return nil
}

func (s *Session) allConfigs() []*pb.Config {
	var out []*pb.Config
	for t := pb.AdminMessage_DEVICE_CONFIG; t <= pb.AdminMessage_DEVICEUI_CONFIG; t++ {
		if c := s.configByType(t); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// allModuleConfigs emits every ModuleConfig variant with defaults, in field order like the firmware.
func allModuleConfigs() []*pb.ModuleConfig {
	var out []*pb.ModuleConfig
	md := (&pb.ModuleConfig{}).ProtoReflect().Descriptor()
	oneof := md.Oneofs().ByName("payload_variant")
	fields := oneof.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		m := &pb.ModuleConfig{}
		rm := m.ProtoReflect()
		rm.Set(f, protoreflect.ValueOfMessage(rm.NewField(f).Message()))
		out = append(out, m)
	}
	return out
}

func regionPresetMap() *pb.LoRaRegionPresetMap {
	m := &pb.LoRaRegionPresetMap{}
	groupIdx := map[string]int{}
	for _, r := range phy.Regions {
		var presets []pb.Config_LoRaConfig_ModemPreset
		switch r.Name {
		case "EU_866":
			presets = []pb.Config_LoRaConfig_ModemPreset{pb.Config_LoRaConfig_LITE_FAST, pb.Config_LoRaConfig_LITE_SLOW}
		case "EU_N_868":
			presets = []pb.Config_LoRaConfig_ModemPreset{pb.Config_LoRaConfig_NARROW_FAST, pb.Config_LoRaConfig_NARROW_SLOW}
		case "EU_868":
			presets = []pb.Config_LoRaConfig_ModemPreset{pb.Config_LoRaConfig_LONG_FAST, pb.Config_LoRaConfig_LONG_SLOW,
				pb.Config_LoRaConfig_MEDIUM_SLOW, pb.Config_LoRaConfig_MEDIUM_FAST, pb.Config_LoRaConfig_SHORT_SLOW,
				pb.Config_LoRaConfig_SHORT_FAST, pb.Config_LoRaConfig_LONG_MODERATE}
		default:
			presets = []pb.Config_LoRaConfig_ModemPreset{pb.Config_LoRaConfig_LONG_FAST, pb.Config_LoRaConfig_LONG_SLOW,
				pb.Config_LoRaConfig_MEDIUM_SLOW, pb.Config_LoRaConfig_MEDIUM_FAST, pb.Config_LoRaConfig_SHORT_SLOW,
				pb.Config_LoRaConfig_SHORT_FAST, pb.Config_LoRaConfig_LONG_MODERATE, pb.Config_LoRaConfig_SHORT_TURBO,
				pb.Config_LoRaConfig_LONG_TURBO, pb.Config_LoRaConfig_MEDIUM_TURBO}
		}
		key := ""
		for _, p := range presets {
			key += p.String() + ","
		}
		gi, ok := groupIdx[key]
		if !ok {
			gi = len(m.Groups)
			groupIdx[key] = gi
			m.Groups = append(m.Groups, &pb.LoRaPresetGroup{Presets: presets, DefaultPreset: presets[0]})
		}
		m.RegionGroups = append(m.RegionGroups, &pb.LoRaRegionPresets{Region: r.Code, GroupIndex: uint32(gi)})
	}
	return m
}

// ---------------------------------------------------------------------------------- packets

func (s *Session) handlePacket(p *pb.MeshPacket) {
	d := p.GetDecoded()
	if d == nil {
		s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_QueueStatus{QueueStatus: s.queueStatus(1, p.Id)}})
		return
	}
	now := time.Now()
	s.mu.Lock()
	if p.Id != 0 && s.seenRecently(p.Id, now) {
		s.mu.Unlock()
		return
	}
	rateLimited := s.rateLimited(d.Portnum, now)
	s.mu.Unlock()

	if p.Id == 0 {
		p.Id = wire.RandomPacketID()
	}
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_QueueStatus{QueueStatus: s.queueStatus(0, p.Id)}})
	if rateLimited {
		s.routingToClient(p.Id, pb.Routing_RATE_LIMIT_EXCEEDED)
		return
	}
	s.forward(p)
}

// seenRecently reports a packet id the client already sent in the last ten minutes, so a repeat
// isn't sent twice, and otherwise records it. The caller holds s.mu.
func (s *Session) seenRecently(id uint32, now time.Time) bool {
	if t, ok := s.recentIDs[id]; ok && now.Sub(t) < 10*time.Minute {
		return true
	}
	s.recentIDs[id] = now
	if len(s.recentIDs) > 512 {
		for k, v := range s.recentIDs {
			if now.Sub(v) > 10*time.Minute {
				delete(s.recentIDs, k)
			}
		}
	}
	return false
}

// rateLimited applies the firmware's per-client limits on texts and traceroutes, recording the
// send when it's allowed. The caller holds s.mu.
func (s *Session) rateLimited(port pb.PortNum, now time.Time) bool {
	limited := false
	switch port {
	case pb.PortNum_TEXT_MESSAGE_APP:
		limited = now.Sub(s.lastText) < 2*time.Second && !s.lastText.IsZero()
		if !limited {
			s.lastText = now
		}
	case pb.PortNum_TRACEROUTE_APP:
		limited = now.Sub(s.lastTrace) < 30*time.Second && !s.lastTrace.IsZero()
		if !limited {
			s.lastTrace = now
		}
	}
	return limited
}

// forward sends a client's packet from the identity.
func (s *Session) forward(p *pb.MeshPacket) {
	if p.To == s.id.NodeNum {
		// The node answers its own admin messages and requests; the replies come back to every
		// client of the identity with this packet's id.
		if err := s.host.Send(s.id, p); err != nil {
			s.routingToClient(p.Id, pb.Routing_NO_INTERFACE)
		}
		return
	}
	if err := s.host.Send(s.id, p); err != nil {
		s.log.Debug("client packet not sent", "identity", s.id.NodeID(), "err", err)
	}
}

func (s *Session) routingToClient(reqID uint32, reason pb.Routing_Error) {
	payload, _ := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}})
	rx := uint32(time.Now().Unix())
	s.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{
		From: s.id.NodeNum, To: s.id.NodeNum, Id: wire.RandomPacketID(), RxTime: &rx, Priority: pb.MeshPacket_ACK,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: payload,
			RequestId: reqID}}}}})
}

// hopLimit is what the client sees as the node's hop limit: the identity's cap when it has one.
func (s *Session) hopLimit(radio uint32) uint32 {
	if limit := s.id.MaxHops(); limit > 0 && limit < radio {
		return limit
	}
	return radio
}
