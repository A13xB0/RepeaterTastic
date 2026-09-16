package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Node is a real Meshtastic node behind the client API standing in for one of a host's
// identities: its mesh.Remote. It keeps the identity and the host current as the node reconnects,
// and hands what the node delivers to the host.
type Node struct {
	addr     string
	stateDir string
	client   *mtclient.Client
	logf     func(string, ...any)

	mu    sync.Mutex
	host  *mesh.Host
	id    *mesh.Identity
	owner *pb.User // names the node should carry (hosted nodes)
	// seed is the saved identity a hosted node runs: its key, and the names and channels a fresh
	// node starts with. nil for a node that keeps its own identity.
	seed      *mesh.IdentityRecord
	seedTries int // key pushes that didn't take, in a row

	// rebootWait is how long a settings change waits for the node to reboot before re-reading it.
	rebootWait time.Duration
	// editMu keeps settings changes one at a time: each reads the node, edits it and waits for it
	// to come back before the next one looks.
	editMu sync.Mutex
}

var _ mesh.Remote = (*Node)(nil)

// Client is the node's API client.
func (n *Node) Client() *mtclient.Client { return n.client }

// Identity creates the host's identity for the node. It waits up to wait for the node to answer,
// then falls back to the node's saved state, then to a placeholder that is replaced when the node
// first answers.
func (n *Node) Identity(ctx context.Context, wait time.Duration) (*mesh.Identity, error) {
	if seed := n.Seed(); seed != nil {
		// The record says who the node is: no need to wait for it.
		if s := n.client.Snapshot(); s.Connected && n.seeded(s) {
			st := remoteState(s)
			n.saveState(st)
			return mesh.NewHostedIdentity(n, st, *seed)
		}
		st, err := mesh.RecordState(*seed)
		if err != nil {
			return nil, err
		}
		return mesh.NewHostedIdentity(n, st, *seed)
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	err := n.client.WaitReady(wctx)
	cancel()
	var st mesh.RemoteState
	switch {
	case err == nil:
		st = remoteState(n.client.Snapshot())
		n.saveState(st)
	default:
		var ok bool
		if st, ok = n.loadState(); ok {
			n.logf("meshtasticd: node at %s not answering yet (%v); using its saved state", n.addr, err)
		} else {
			n.logf("meshtasticd: node at %s not answering yet (%v); it appears once it does", n.addr, err)
			st = placeholderState(n.addr)
		}
	}
	return mesh.NewRemoteIdentity(n, st)
}

// SetSeed makes the node run the saved identity rec: it is given rec's key (and, while it is
// fresh, rec's names and channels) and stands for rec from then on.
func (n *Node) SetSeed(rec mesh.IdentityRecord) {
	n.mu.Lock()
	n.seed = &rec
	n.mu.Unlock()
}

// Seed is the saved identity the node runs, or nil.
func (n *Node) Seed() *mesh.IdentityRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.seed
}

// seedKey is the seed's key pair, or nils.
func (n *Node) seedKey() (priv, pub []byte) {
	seed := n.Seed()
	if seed == nil {
		return nil, nil
	}
	priv, err := seed.Key()
	if err != nil {
		return nil, nil
	}
	pub, err = wire.PublicKey(priv)
	if err != nil {
		return nil, nil
	}
	return priv, pub
}

// seeded reports whether the node already holds the seed's key (true without a seed).
func (n *Node) seeded(s mtclient.Snapshot) bool {
	_, pub := n.seedKey()
	return pub == nil || bytes.Equal(s.Config.GetSecurity().GetPublicKey(), pub)
}

// maxSeedTries is how often a node is given its key before RepeaterTastic gives up on it.
const maxSeedTries = 3

// OnAir reports whether the node may transmit: it holds its saved key (if it has one).
func (n *Node) OnAir() bool { return n.seeded(n.client.Snapshot()) }

// Bind makes id, on host h, the identity the node stands for.
func (n *Node) Bind(h *mesh.Host, id *mesh.Identity) {
	n.mu.Lock()
	n.host, n.id = h, id
	n.mu.Unlock()
}

// Run keeps the bound host current with the node (settings, identity, deliveries) until ctx ends.
func (n *Node) Run(ctx context.Context) {
	n.mu.Lock()
	h := n.host
	n.mu.Unlock()
	events, stop := n.client.Subscribe(512)
	defer stop()
	if n.client.Snapshot().Connected {
		n.configured(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			switch e.Kind {
			case mtclient.Configured:
				n.configured(ctx)
			case mtclient.Disconnected:
				n.logf("meshtasticd: node at %s disconnected: %v", n.addr, e.Err)
			case mtclient.Received:
				// A sim-radio meshtasticd hands its transmissions to the client; nobody bridges them here.
				if p := e.FromRadio.GetPacket(); p != nil && p.GetDecoded().GetPortnum() != pb.PortNum_SIMULATOR_APP {
					n.mu.Lock()
					cur := n.id
					n.mu.Unlock()
					h.RemoteReceived(cur, p)
				}
			}
		}
	}
}

// configured brings the host in line with the node after a handshake.
func (n *Node) configured(ctx context.Context) {
	snap := n.client.Snapshot()
	n.mu.Lock()
	h, cur, seed := n.host, n.id, n.seed
	n.mu.Unlock()
	if !n.seeded(snap) {
		// A fresh node (or one whose key was changed): give it the saved identity first. It comes
		// back under the saved node number. Nothing it sends goes on air meanwhile.
		n.mu.Lock()
		n.seedTries++
		tries := n.seedTries
		n.mu.Unlock()
		if tries > maxSeedTries {
			if tries == maxSeedTries+1 {
				n.logf("meshtasticd: ERROR node at %s won't keep %s's key after %d tries; it stays off air until restarted", n.addr, cur.NodeID(), maxSeedTries)
			}
			return
		}
		n.logf("meshtasticd: node at %s gets %s's key", n.addr, cur.NodeID())
		go func() {
			err := n.ApplyConfig(ctx, h.Config())
			if err == nil || ctx.Err() != nil {
				return
			}
			n.logf("meshtasticd: node at %s didn't take %s's key: %v; trying again", n.addr, cur.NodeID(), err)
			select { // a node just started may not answer yet: try again on a fresh connection
			case <-ctx.Done():
			case <-time.After(n.rebootWait):
				if !n.OnAir() {
					n.client.Reconnect()
				}
			}
		}()
		return
	}
	n.mu.Lock()
	n.seedTries = 0
	n.mu.Unlock()
	st := remoteState(snap)
	n.saveState(st)
	if cur.NodeNum != st.NodeNum {
		var next *mesh.Identity
		var err error
		if seed != nil {
			next, err = mesh.NewHostedIdentity(n, st, *seed)
		} else {
			next, err = mesh.NewRemoteIdentity(n, st)
		}
		if err == nil {
			err = h.SwapRemote(cur, next)
		}
		if err != nil {
			n.logf("meshtasticd: node is now %s but the identity couldn't follow: %v", wire.NodeID(st.NodeNum), err)
			return
		}
		n.logf("meshtasticd: node %s replaces %s", next.NodeID(), cur.NodeID())
		n.mu.Lock()
		n.id, cur = next, next
		n.mu.Unlock()
	}
	// Pushing may wait for a reboot; the event loop keeps delivering meanwhile.
	go func(id string) {
		if err := n.ApplyConfig(ctx, h.Config()); err != nil && ctx.Err() == nil {
			n.logf("meshtasticd: node %s didn't take the host's settings: %v", id, err)
		}
	}(cur.NodeID())
	h.SyncRemote(cur, st)
	if cur.Hosted() {
		if err := h.SaveIdentities(); err != nil {
			n.logf("meshtasticd: saving %s: %v", cur.NodeID(), err)
		}
	}
	n.logf("meshtasticd: node %s %q on %s, firmware %s, %s %s", cur.NodeID(), st.User.GetLongName(), n.addr,
		snap.Metadata.GetFirmwareVersion(), snap.Config.GetLora().GetRegion(), snap.Config.GetLora().GetModemPreset())
}

// ------------------------------------------------------------------------------ mesh.Remote

func (n *Node) SendPacket(p *pb.MeshPacket) (uint32, error) { return n.client.SendPacket(p) }

func (n *Node) Admin(ctx context.Context, m *pb.AdminMessage) (*pb.AdminMessage, error) {
	return n.client.Admin(ctx, m)
}

// ------------------------------------------------------------------------------ state cache

type savedState struct {
	NodeNum  uint32            `json:"node_num"`
	User     json.RawMessage   `json:"user"`
	Channels []json.RawMessage `json:"channels"`
	Address  string            `json:"address"`
}

func (n *Node) statePath() string {
	if n.stateDir == "" {
		return ""
	}
	return filepath.Join(n.stateDir, "node.json")
}

func (n *Node) saveState(st mesh.RemoteState) {
	path := n.statePath()
	if path == "" {
		return
	}
	s := savedState{NodeNum: st.NodeNum, Address: n.addr}
	s.User, _ = protojson.Marshal(st.User)
	for _, ch := range st.Channels {
		b, _ := protojson.Marshal(ch)
		s.Channels = append(s.Channels, b)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (n *Node) loadState() (mesh.RemoteState, bool) {
	path := n.statePath()
	if path == "" {
		return mesh.RemoteState{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return mesh.RemoteState{}, false
	}
	var s savedState
	if json.Unmarshal(b, &s) != nil || s.NodeNum == 0 {
		return mesh.RemoteState{}, false
	}
	st := mesh.RemoteState{NodeNum: s.NodeNum, User: &pb.User{}}
	_ = protojson.Unmarshal(s.User, st.User)
	for _, raw := range s.Channels {
		ch := &pb.Channel{}
		if protojson.Unmarshal(raw, ch) == nil {
			st.Channels = append(st.Channels, ch)
		}
	}
	return st, true
}

// placeholderState names a node that hasn't answered yet, with a stable number from its address.
func placeholderState(addr string) mesh.RemoteState {
	num := crc32.ChecksumIEEE([]byte("meshtastic:"+addr)) | 0x10000000
	if num == wire.Broadcast {
		num--
	}
	return mesh.RemoteState{NodeNum: num, User: &pb.User{LongName: "Hosted node (starting)", ShortName: "…"}}
}

// ------------------------------------------------------------------------------ conversions

func remoteState(s mtclient.Snapshot) mesh.RemoteState {
	st := mesh.RemoteState{NodeNum: s.NodeNum(), Channels: s.Channels}
	if self := s.Self(); self != nil && self.User != nil {
		st.User = proto.Clone(self.User).(*pb.User)
	} else {
		st.User = &pb.User{}
	}
	if len(st.User.PublicKey) == 0 {
		st.User.PublicKey = s.Config.GetSecurity().GetPublicKey()
	}
	if st.User.Role == pb.Config_DeviceConfig_CLIENT {
		st.User.Role = s.Config.GetDevice().GetRole()
	}
	for _, n := range s.Nodes {
		st.Nodes = append(st.Nodes, n)
	}
	return st
}

// Current is the host identity the node stands for now (nil before Bind).
func (n *Node) Current() *mesh.Identity {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.id
}

// SetOwner sets the names a hosted node is given (and keeps) on every connect.
func (n *Node) SetOwner(long, short string) {
	n.mu.Lock()
	n.owner = &pb.User{LongName: long, ShortName: short}
	n.mu.Unlock()
}

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
	n.mu.Lock()
	h, id, owner, seed := n.host, n.id, n.owner, n.seed
	n.mu.Unlock()
	relay := id == nil || id.IsRelay
	var msgs []*pb.AdminMessage
	setConfig := func(c *pb.Config) {
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: c}})
	}
	fresh := !n.seeded(s)

	lora := proto.Clone(s.Config.GetLora()).(*pb.Config_LoRaConfig)
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
	if !relay {
		if cap := id.MaxHops(); cap > 0 && cap < lora.HopLimit {
			lora.HopLimit = cap
		}
		transmits = transmits && id.Settings().Enabled
	}
	lora.TxEnabled = transmits
	lora.ConfigOkToMqtt = cfg.OKToMQTT
	if relay {
		lora.IgnoreMqtt = cfg.IgnoreMQTT
	}
	if !proto.Equal(lora, s.Config.GetLora()) {
		setConfig(&pb.Config{PayloadVariant: &pb.Config_Lora{Lora: lora}})
	}

	if dev := s.Config.GetDevice(); dev != nil {
		d := proto.Clone(dev).(*pb.Config_DeviceConfig)
		if relay {
			d.Role = mesh.DeviceRole(cfg.RelayRole, dev.GetRole())
			if mode, ok := mesh.RebroadcastMode(cfg.Rebroadcast); ok {
				d.RebroadcastMode = mode
			}
		} else {
			want := id.UserCopy().GetRole()
			if fresh && seed != nil {
				if v, ok := pb.Config_DeviceConfig_Role_value[seed.Role]; ok {
					want = pb.Config_DeviceConfig_Role(v)
				}
			}
			d.Role, d.RebroadcastMode = IdentityRole(want)
		}
		if secs := uint32(cfg.NodeInfoInterval / time.Second); secs >= 3600 {
			d.NodeInfoBroadcastSecs = secs
		}
		if !proto.Equal(d, dev) {
			setConfig(&pb.Config{PayloadVariant: &pb.Config_Device{Device: d}})
		}
	}

	msgs = append(msgs, n.positionMsgs(s, h, id)...)
	if tel := s.ModuleConfig.GetTelemetry(); tel != nil {
		t := proto.Clone(tel).(*pb.ModuleConfig_TelemetryConfig)
		t.DeviceTelemetryEnabled = relay && cfg.TelemetryInterval > 0
		if t.DeviceTelemetryEnabled {
			t.DeviceUpdateInterval = uint32(cfg.TelemetryInterval / time.Second)
		}
		if !proto.Equal(t, tel) {
			msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetModuleConfig{
				SetModuleConfig: &pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Telemetry{Telemetry: t}}}})
		}
	}

	if fresh && seed != nil {
		// The saved channels, primary name following the radio.
		if st, err := mesh.RecordState(*seed); err == nil {
			for i, ch := range st.Channels {
				ch = proto.Clone(ch).(*pb.Channel)
				ch.Index = int32(i)
				if i == 0 {
					ch.Role = pb.Channel_PRIMARY
					ch.Settings.Name = cfg.PrimaryChannel
				}
				msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: ch}})
			}
			if owner == nil {
				owner = &pb.User{LongName: seed.LongName, ShortName: seed.ShortName}
			}
		}
	} else if len(s.Channels) > 0 && s.Channels[0] != nil && s.Channels[0].GetSettings().GetName() != cfg.PrimaryChannel {
		ch := proto.Clone(s.Channels[0]).(*pb.Channel)
		if ch.Settings == nil {
			ch.Settings = &pb.ChannelSettings{}
		}
		ch.Settings.Name = cfg.PrimaryChannel
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: ch}})
	}

	if owner != nil {
		self := s.Self().GetUser()
		if (owner.LongName != "" && owner.LongName != self.GetLongName()) || (owner.ShortName != "" && owner.ShortName != self.GetShortName()) {
			u := &pb.User{LongName: self.GetLongName(), ShortName: self.GetShortName()}
			if owner.LongName != "" {
				u.LongName = owner.LongName
			}
			if owner.ShortName != "" {
				u.ShortName = owner.ShortName
			}
			msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: u}})
		}
	}
	rekey := false
	if fresh {
		// The key goes last: once the node takes it, it answers under its new number, and
		// messages to the old one are no longer its own.
		priv, pub := n.seedKey()
		sec := &pb.Config_SecurityConfig{}
		if old := s.Config.GetSecurity(); old != nil {
			sec = proto.Clone(old).(*pb.Config_SecurityConfig)
		}
		sec.PrivateKey, sec.PublicKey = wire.ClampPrivateKey(priv), pub
		setConfig(&pb.Config{PayloadVariant: &pb.Config_Security{Security: sec}})
		rekey = true
	}
	if len(msgs) == 0 {
		return nil
	}
	return n.edit(ctx, msgs, rekey)
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

// edit applies admin messages inside begin/commit_edit_settings and refreshes the mirror.
// With rekey, the last message changes the node's key: the commit after it may go unanswered.
func (n *Node) edit(ctx context.Context, msgs []*pb.AdminMessage, rekey bool) error {
	events, stop := n.client.Subscribe(16)
	defer stop()
	all := append([]*pb.AdminMessage{{PayloadVariant: &pb.AdminMessage_BeginEditSettings{BeginEditSettings: true}}}, msgs...)
	all = append(all, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_CommitEditSettings{CommitEditSettings: true}})
	for i, m := range all {
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

func newNode(addr, stateDir string, c *mtclient.Client, logf func(string, ...any)) *Node {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Node{addr: addr, stateDir: stateDir, client: c, logf: logf, rebootWait: 8 * time.Second}
}
