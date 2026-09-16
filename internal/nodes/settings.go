// The settings RepeaterTastic gives a node: radio, role, position, telemetry, channels, owner and key.

package nodes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
	"google.golang.org/protobuf/proto"
)

// ErrNotReady is returned when settings can't reach a node that isn't connected.
var ErrNotReady = errors.New("the Meshtastic node isn't connected")

// ApplyConfig writes the settings the host keeps to the node in one edit transaction, so it
// reboots at most once, then re-reads its configuration: region, preset, frequency, power, the
// primary channel name, role, hop limit, transmitter, position and telemetry. A fresh hosted node
// is given its saved identity (key, names, channels) in the same transaction.
func (n *Node) ApplyConfig(ctx context.Context, cfg mesh.Config) error {
	n.editMu.Lock()
	defer n.editMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, n.rebootWait)
	_ = n.client.WaitReady(wctx) // back from a reboot the previous change caused
	cancel()
	s := n.client.Snapshot()
	if !s.Connected || s.Config.GetLora() == nil {
		return ErrNotReady
	}
	fresh := !n.seeded(s)
	n.mu.Lock()
	in := settingsInput{cfg: cfg, s: s, host: n.host, id: n.id, owner: n.owner, seed: n.seed, board: n.extra != nil,
		behind: n.hopsBehind, fresh: fresh}
	extra := n.extra
	n.mu.Unlock()
	in.relay = in.id == nil || in.id.IsRelay

	lora := in.lora()
	if s.Config.GetLora().GetRegion() == pb.Config_LoRaConfig_UNSET && lora.Region != pb.Config_LoRaConfig_UNSET && !in.fresh {
		// A board's first region makes its keys, and its node number moves with them at once:
		// answers to anything addressed to the old number are lost, a commit included. So the
		// region goes on its own, saved straight away; the rest follows on the next connection.
		return n.firstRegion(ctx, lora)
	}
	var msgs []*pb.AdminMessage
	if !proto.Equal(lora, s.Config.GetLora()) {
		msgs = append(msgs, setConfig(&pb.Config{PayloadVariant: &pb.Config_Lora{Lora: lora}}))
	}
	msgs = append(msgs, in.deviceMsgs()...)
	msgs = append(msgs, n.positionMsgs(s, in.host, in.id)...)
	if in.relay && mesh.NormalizeRelayRole(cfg.RelayRole) == mesh.RoleClientBase && in.host != nil {
		msgs = append(msgs, favoriteMsgs(s, in.host)...)
	}
	msgs = append(msgs, in.telemetryMsgs()...)
	msgs = append(msgs, in.channelMsgs()...)
	msgs = append(msgs, in.ownerMsgs()...)
	if extra != nil {
		msgs = mergeChannelSets(msgs, extra(s))
	}
	if in.fresh {
		// The key goes last: once the node takes it, it answers under its new number, and
		// messages to the old one are no longer its own.
		msgs = append(msgs, n.keyMsg(s))
	}
	if len(msgs) == 0 {
		return nil
	}
	return n.edit(ctx, msgs, in.fresh)
}

// settingsInput is what one ApplyConfig works from.
type settingsInput struct {
	cfg    mesh.Config
	s      mtclient.Snapshot
	host   *mesh.Host
	id     *mesh.Identity
	owner  *pb.User
	seed   *mesh.IdentityRecord
	relay  bool   // the node is the relay persona (or not yet bound)
	board  bool   // the node is a board carrying identities over MQTT
	behind uint32 // hops from the air
	fresh  bool   // the node doesn't hold its saved key yet
}

func setConfig(c *pb.Config) *pb.AdminMessage {
	return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: c}}
}

// lora is the node's LoRa config as the host wants it.
func (in settingsInput) lora() *pb.Config_LoRaConfig {
	cfg := in.cfg
	lora := proto.Clone(in.s.Config.GetLora()).(*pb.Config_LoRaConfig)
	if v, ok := pb.Config_LoRaConfig_RegionCode_value[strings.ToUpper(cfg.Region)]; ok {
		lora.Region = pb.Config_LoRaConfig_RegionCode(v)
	}
	lora.UsePreset = true
	lora.ModemPreset = cfg.Preset
	lora.ChannelNum = uint32(cfg.ChannelNum)
	lora.OverrideFrequency = float32(cfg.OverrideFreqMHz)
	lora.FrequencyOffset = float32(cfg.FreqOffsetMHz)
	lora.TxPower = int32(cfg.TxPowerDBm)
	if cfg.HopLimit != 0 {
		lora.HopLimit = cfg.HopLimit
	}
	transmits := cfg.RelayRole != mesh.RoleMonitor && cfg.RelayRole != mesh.RoleOff
	if in.behind > 0 {
		lora.HopLimit = min(lora.HopLimit+in.behind, wire.HopMax)
	}
	if !in.relay {
		if limit := in.id.MaxHops(); limit > 0 && limit < lora.HopLimit {
			lora.HopLimit = limit
		}
		transmits = transmits && in.id.Settings().Enabled
	}
	lora.TxEnabled = transmits
	lora.ConfigOkToMqtt = cfg.OKToMQTT
	// A board carries the identities over MQTT, so it must take what comes that way; identities
	// never repeat, so they take everything addressed to them.
	lora.IgnoreMqtt = in.relay && cfg.IgnoreMQTT && !in.board
	return lora
}

// deviceMsgs sets the node's role, rebroadcast mode and NodeInfo interval.
func (in settingsInput) deviceMsgs() []*pb.AdminMessage {
	dev := in.s.Config.GetDevice()
	if dev == nil {
		return nil
	}
	d := proto.Clone(dev).(*pb.Config_DeviceConfig)
	if in.relay {
		d.Role = mesh.DeviceRole(in.cfg.RelayRole, dev.GetRole())
		if mode, ok := mesh.RebroadcastMode(in.cfg.Rebroadcast); ok {
			d.RebroadcastMode = mode
		}
	} else {
		want := in.id.UserCopy().GetRole()
		if v, ok := pb.Config_DeviceConfig_Role_value[in.seedRole()]; ok {
			want = pb.Config_DeviceConfig_Role(v)
		}
		d.Role, d.RebroadcastMode = IdentityRole(want)
	}
	if secs := uint32(in.cfg.NodeInfoInterval / time.Second); secs >= 3600 {
		d.NodeInfoBroadcastSecs = secs
	}
	if proto.Equal(d, dev) {
		return nil
	}
	return []*pb.AdminMessage{setConfig(&pb.Config{PayloadVariant: &pb.Config_Device{Device: d}})}
}

// seedRole is the saved role a fresh node starts with ("" once it has its key).
func (in settingsInput) seedRole() string {
	if in.fresh && in.seed != nil {
		return in.seed.Role
	}
	return ""
}

// telemetryMsgs turns device telemetry on for the relay (on the configured interval) and off for
// identities.
func (in settingsInput) telemetryMsgs() []*pb.AdminMessage {
	tel := in.s.ModuleConfig.GetTelemetry()
	if tel == nil {
		return nil
	}
	t := proto.Clone(tel).(*pb.ModuleConfig_TelemetryConfig)
	t.DeviceTelemetryEnabled = in.relay && in.cfg.TelemetryInterval > 0
	if t.DeviceTelemetryEnabled {
		t.DeviceUpdateInterval = uint32(in.cfg.TelemetryInterval / time.Second)
	}
	if proto.Equal(t, tel) {
		return nil
	}
	return []*pb.AdminMessage{{PayloadVariant: &pb.AdminMessage_SetModuleConfig{
		SetModuleConfig: &pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Telemetry{Telemetry: t}}}}}
}

// channelMsgs gives a fresh node its saved channels, and keeps the primary channel's name with the
// radio's.
func (in settingsInput) channelMsgs() []*pb.AdminMessage {
	primary := in.cfg.PrimaryChannel
	setChannel := func(ch *pb.Channel) *pb.AdminMessage {
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: ch}}
	}
	if in.fresh && in.seed != nil {
		st, err := mesh.RecordState(*in.seed)
		if err != nil {
			return nil
		}
		msgs := make([]*pb.AdminMessage, 0, len(st.Channels))
		for i, ch := range st.Channels {
			ch = proto.Clone(ch).(*pb.Channel)
			ch.Index = int32(i)
			if i == 0 {
				ch.Role = pb.Channel_PRIMARY
				ch.Settings.Name = primary
			}
			msgs = append(msgs, setChannel(ch))
		}
		return msgs
	}
	chs := in.s.Channels
	if len(chs) == 0 || chs[0] == nil || chs[0].GetSettings().GetName() == primary {
		return nil
	}
	ch := proto.Clone(chs[0]).(*pb.Channel)
	if ch.Settings == nil {
		ch.Settings = &pb.ChannelSettings{}
	}
	ch.Settings.Name = primary
	return []*pb.AdminMessage{setChannel(ch)}
}

// ownerMsgs sets the node's names: the ones it was given, or a fresh node's saved ones.
func (in settingsInput) ownerMsgs() []*pb.AdminMessage {
	owner := in.owner
	if owner == nil && in.fresh && in.seed != nil {
		owner = &pb.User{LongName: in.seed.LongName, ShortName: in.seed.ShortName}
	}
	if owner == nil {
		return nil
	}
	self := in.s.Self().GetUser()
	u := &pb.User{LongName: self.GetLongName(), ShortName: self.GetShortName()}
	if owner.LongName != "" {
		u.LongName = owner.LongName
	}
	if owner.ShortName != "" {
		u.ShortName = owner.ShortName
	}
	if u.LongName == self.GetLongName() && u.ShortName == self.GetShortName() {
		return nil
	}
	return []*pb.AdminMessage{{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: u}}}
}

// keyMsg gives the node its saved key (clamped, as 2.8 keeps only clamped keys).
func (n *Node) keyMsg(s mtclient.Snapshot) *pb.AdminMessage {
	priv, pub := n.seedKey()
	sec := &pb.Config_SecurityConfig{}
	if old := s.Config.GetSecurity(); old != nil {
		sec = proto.Clone(old).(*pb.Config_SecurityConfig)
	}
	sec.PrivateKey, sec.PublicKey = wire.ClampPrivateKey(priv), pub
	return setConfig(&pb.Config{PayloadVariant: &pb.Config_Security{Security: sec}})
}

// mergeChannelSets appends more to msgs. A channel set for a slot msgs already sets takes that
// set's name and key, and replaces it, so one slot is written once.
func mergeChannelSets(msgs, more []*pb.AdminMessage) []*pb.AdminMessage {
	for _, m := range more {
		if ch := m.GetSetChannel(); ch != nil {
			for i, prev := range msgs {
				if p := prev.GetSetChannel(); p != nil && p.Index == ch.Index {
					ch.Settings.Name, ch.Settings.Psk, ch.Role = p.GetSettings().GetName(), p.GetSettings().GetPsk(), p.Role
					msgs = append(msgs[:i], msgs[i+1:]...)
					break
				}
			}
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// IdentityRole is the device role and rebroadcast mode a hosted identity runs with. Identities
// never repeat (the relay persona does): tracker, sensor and TAK tracker keep their role with
// rebroadcasting off; every other role becomes CLIENT_MUTE.
func IdentityRole(want pb.Config_DeviceConfig_Role) (pb.Config_DeviceConfig_Role, pb.Config_DeviceConfig_RebroadcastMode) {
	switch want {
	case pb.Config_DeviceConfig_TRACKER, pb.Config_DeviceConfig_SENSOR, pb.Config_DeviceConfig_TAK_TRACKER:
		return want, pb.Config_DeviceConfig_NONE
	}
	return pb.Config_DeviceConfig_CLIENT_MUTE, pb.Config_DeviceConfig_ALL
}

// IdentityRoleAllowed reports whether a hosted identity can take a role as it is.
func IdentityRoleAllowed(role pb.Config_DeviceConfig_Role) bool {
	r, _ := IdentityRole(role)
	return r == role
}

// positionMsgs sets the node's fixed position to the one the host gives the identity (its own, or
// the site's when shared), or removes it.
func (n *Node) positionMsgs(s mtclient.Snapshot, h *mesh.Host, id *mesh.Identity) []*pb.AdminMessage {
	cur := s.Config.GetPosition()
	if h == nil || id == nil || cur == nil {
		return nil
	}
	var msgs []*pb.AdminMessage
	pos, ok := h.PositionFor(id)
	pc := proto.Clone(cur).(*pb.Config_PositionConfig)
	pc.FixedPosition = ok
	if ok {
		pc.GpsMode = pb.Config_PositionConfig_NOT_PRESENT
		pc.PositionBroadcastSmartEnabled = false
		pc.PositionBroadcastSecs = uint32(pos.Interval / time.Second)
		if pc.PositionBroadcastSecs == 0 {
			pc.PositionBroadcastSecs = 3 * 3600
		}
	}
	if !proto.Equal(pc, cur) {
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Position{Position: pc}}}})
	}
	have := s.Self().GetPosition()
	lat, lon := int32(math.Round(pos.Latitude*1e7)), int32(math.Round(pos.Longitude*1e7))
	switch {
	case ok && (!cur.GetFixedPosition() || have.GetLatitudeI() != lat || have.GetLongitudeI() != lon || have.GetAltitude() != pos.Altitude):
		alt := pos.Altitude
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetFixedPosition{
			SetFixedPosition: &pb.Position{LatitudeI: &lat, LongitudeI: &lon, Altitude: &alt, LocationSource: pb.Position_LOC_MANUAL}}})
	case !ok && cur.GetFixedPosition():
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_RemoveFixedPosition{RemoveFixedPosition: true}})
	}
	return msgs
}

// firstRegion sets a node's region for the first time, outside an edit transaction so the node
// saves it at once, then reconnects to learn the node's new number. Configured then pushes the rest.
func (n *Node) firstRegion(ctx context.Context, lora *pb.Config_LoRaConfig) error {
	n.logf("meshtasticd: node at %s gets its first region, %s (its node number changes with its new keys)", n.addr, lora.Region)
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := n.client.Admin(actx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: lora}}}})
	cancel()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		n.logf("meshtasticd: node at %s: first region: %v (the answer is expected to go missing)", n.addr, err)
	}
	n.committed.Store(time.Now().UnixMilli())
	time.Sleep(time.Second) // let it save
	n.client.Reconnect()
	return nil
}

// edit applies admin messages inside begin/commit_edit_settings and refreshes the mirror.
// With rekey, the last message changes the node's key: the commit after it may go unanswered.
func (n *Node) edit(ctx context.Context, msgs []*pb.AdminMessage, rekey bool) error {
	events, stop := n.client.Subscribe(16)
	defer stop()
	all := append([]*pb.AdminMessage{{PayloadVariant: &pb.AdminMessage_BeginEditSettings{BeginEditSettings: true}}}, msgs...)
	all = append(all, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_CommitEditSettings{CommitEditSettings: true}})
	for i, m := range all {
		if i == len(all)-1 {
			n.committed.Store(time.Now().UnixMilli())
		}
		if _, err := n.client.Admin(ctx, m); err != nil {
			if i == len(all)-1 && (rekey || !n.client.Snapshot().Connected) {
				break // the commit's ack was lost to the reboot (or the new number) it caused
			}
			return fmt.Errorf("meshtasticd refused the settings: %w", err)
		}
	}
	// Some changes reboot the node; the client reconnects by itself. Otherwise re-read the config.
	timer := time.NewTimer(n.rebootWait)
	defer timer.Stop()
wait:
	for {
		select {
		case e := <-events:
			if e.Kind == mtclient.Disconnected {
				break wait
			}
		case <-timer.C:
			n.client.Reconnect()
			break wait
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Hand back once the node has answered again, so the next change reads fresh settings.
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		select {
		case e := <-events:
			if e.Kind == mtclient.Configured {
				return nil
			}
		case <-wctx.Done():
			return nil // it comes back on its own; the next change waits for it
		}
	}
}

// favoriteMsgs makes a client_base relay's favourites match the host's: its identities and the
// configured favourites. A favourite the relay hasn't heard of is added as a contact first, from
// what the host knows of it (a node can't favourite a stranger).
func favoriteMsgs(s mtclient.Snapshot, h *mesh.Host) []*pb.AdminMessage {
	var msgs []*pb.AdminMessage
	self := s.NodeNum()
	for _, num := range h.Config().Favorites {
		if _, known := s.Nodes[num]; known || num == self {
			continue
		}
		e, ok := h.DB.Get(num)
		if !ok || e.User == nil {
			continue // unknown here too: set once either side has heard it
		}
		msgs = append(msgs,
			&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_AddContact{AddContact: &pb.SharedContact{NodeNum: num, User: proto.Clone(e.User).(*pb.User)}}},
			&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetFavoriteNode{SetFavoriteNode: num}})
	}
	for num, info := range s.Nodes {
		if num == self || num == 0 {
			continue
		}
		want := h.IsRelayFavorite(num)
		switch {
		case want && !info.GetIsFavorite():
			msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetFavoriteNode{SetFavoriteNode: num}})
		case !want && info.GetIsFavorite():
			msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_RemoveFavoriteNode{RemoveFavoriteNode: num}})
		}
	}
	return msgs
}
