// Package nodes connects RepeaterTastic to real Meshtastic nodes: boards running Meshtastic
// firmware (attached nodes) and, later, meshtasticd instances it runs itself (hosted nodes).
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Driver is the radio.driver value for a board running Meshtastic firmware.
const Driver = "meshtastic"

// Attached is a board running Meshtastic firmware (over USB serial), or a meshtasticd on the
// network, standing in for a radio. The host it serves has one identity: the node itself. The
// node does all the radio work; Attached keeps the host's view of it current.
type Attached struct {
	addr     string
	stateDir string
	client   *mtclient.Client
	logf     func(string, ...any)

	mu     sync.Mutex
	host   *mesh.Host
	id     *mesh.Identity
	frames chan radio.Frame
	closed bool
	onCfg  func(mesh.Config)

	// rebootWait is how long a settings change waits for the node to reboot before re-reading it.
	rebootWait time.Duration
}

// OnConfig registers fn to hear the host settings mirrored from the node after each handshake.
func (a *Attached) OnConfig(fn func(mesh.Config)) {
	a.mu.Lock()
	a.onCfg = fn
	h := a.host
	a.mu.Unlock()
	// The first handshake usually finishes before anyone listens: hand over what it mirrored.
	if h != nil && a.client.Snapshot().Connected {
		fn(h.Config())
	}
}

var (
	_ radio.Radio = (*Attached)(nil)
	_ mesh.Remote = (*Attached)(nil)
)

// OpenAttached starts connecting to the node at addr (a serial device or host[:port]). stateDir
// keeps the node's last state so the identity exists while the node is away.
func OpenAttached(ctx context.Context, addr, stateDir string, logf func(string, ...any)) (*Attached, error) {
	return OpenAttachedWith(ctx, mtclient.Options{Address: addr}, stateDir, logf)
}

// OpenAttachedWith is OpenAttached with full client options (tests dial a fake node).
func OpenAttachedWith(ctx context.Context, o mtclient.Options, stateDir string, logf func(string, ...any)) (*Attached, error) {
	addr := o.Address
	if strings.TrimSpace(addr) == "" {
		return nil, errors.New("meshtastic: no device: give a serial port or host[:port]")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	o.Logf = logf
	a := &Attached{addr: addr, stateDir: stateDir, logf: logf, frames: make(chan radio.Frame), rebootWait: 8 * time.Second}
	a.client = mtclient.New(o)
	if err := a.client.Start(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

// Client is the node's API client.
func (a *Attached) Client() *mtclient.Client { return a.client }

// Identity creates the host's identity for the node. It waits up to wait for the node to answer,
// then falls back to the node's saved state, then to a placeholder that is replaced when the node
// first answers.
func (a *Attached) Identity(ctx context.Context, wait time.Duration) (*mesh.Identity, error) {
	wctx, cancel := context.WithTimeout(ctx, wait)
	err := a.client.WaitReady(wctx)
	cancel()
	var st mesh.RemoteState
	switch {
	case err == nil:
		st = remoteState(a.client.Snapshot())
		a.saveState(st)
	default:
		var ok bool
		if st, ok = a.loadState(); ok {
			a.logf("meshtastic: node at %s not answering yet (%v); using its saved state", a.addr, err)
		} else {
			a.logf("meshtastic: node at %s not answering yet (%v); it appears once it does", a.addr, err)
			st = placeholderState(a.addr)
		}
	}
	id, err := mesh.NewRemoteIdentity(a, st)
	if err != nil {
		return nil, err
	}
	id.IsRelay = true
	return id, nil
}

// Bind follows the node for host h, whose relay persona is id, until ctx ends.
func (a *Attached) Bind(ctx context.Context, h *mesh.Host, id *mesh.Identity) {
	a.mu.Lock()
	a.host, a.id = h, id
	a.mu.Unlock()
	events, stop := a.client.Subscribe(512)
	defer stop()
	if a.client.Snapshot().Connected {
		a.configured(ctx)
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
				a.configured(ctx)
			case mtclient.Disconnected:
				a.logf("meshtastic: node at %s disconnected: %v", a.addr, e.Err)
			case mtclient.Received:
				// A sim-radio meshtasticd hands its transmissions to the client; nobody bridges them here.
				if p := e.FromRadio.GetPacket(); p != nil && p.GetDecoded().GetPortnum() != pb.PortNum_SIMULATOR_APP {
					a.mu.Lock()
					cur := a.id
					a.mu.Unlock()
					h.RemoteReceived(cur, p)
				}
			}
		}
	}
}

// configured brings the host in line with the node after a handshake.
func (a *Attached) configured(ctx context.Context) {
	snap := a.client.Snapshot()
	st := remoteState(snap)
	a.saveState(st)
	a.mu.Lock()
	h, cur := a.host, a.id
	a.mu.Unlock()
	if cur.NodeNum != st.NodeNum {
		next, err := mesh.NewRemoteIdentity(a, st)
		if err == nil {
			err = h.SwapRemote(cur, next)
		}
		if err != nil {
			a.logf("meshtastic: node is now %s but the identity couldn't follow: %v", wire.NodeID(st.NodeNum), err)
			return
		}
		a.logf("meshtastic: node %s replaces %s", next.NodeID(), cur.NodeID())
		a.mu.Lock()
		a.id, cur = next, next
		a.mu.Unlock()
	}
	if cfg, ok := hostConfig(h.Config(), snap); ok {
		if err := h.MirrorConfig(ctx, cfg); err != nil {
			a.logf("meshtastic: host settings not updated from the node: %v", err)
		} else {
			a.mu.Lock()
			fn := a.onCfg
			a.mu.Unlock()
			if fn != nil {
				fn(h.Config())
			}
		}
	}
	h.SyncRemote(cur, st)
	a.logf("meshtastic: node %s %q on %s, firmware %s, %s %s", cur.NodeID(), st.User.GetLongName(), a.addr,
		snap.Metadata.GetFirmwareVersion(), snap.Config.GetLora().GetRegion(), snap.Config.GetLora().GetModemPreset())
}

// ------------------------------------------------------------------------------ mesh.Remote

func (a *Attached) SendPacket(p *pb.MeshPacket) (uint32, error) { return a.client.SendPacket(p) }

func (a *Attached) Admin(ctx context.Context, m *pb.AdminMessage) (*pb.AdminMessage, error) {
	return a.client.Admin(ctx, m)
}

// ------------------------------------------------------------------------------ radio.Radio

// Configure accepts anything: the node owns its PHY, and settings reach it through ApplyConfig.
func (a *Attached) Configure(context.Context, radio.Config) error { return nil }

// Send is never used: the host hands packets to the node, not frames to a modem.
func (a *Attached) Send(context.Context, []byte) error { return radio.ErrUnsupported }

func (a *Attached) Frames() <-chan radio.Frame { return a.frames }

func (a *Attached) ChannelBusy(context.Context) (bool, error) { return false, nil }

func (a *Attached) Info() radio.Info {
	s := a.client.Snapshot()
	name := s.Self().GetUser().GetLongName()
	if name == "" {
		name = "Meshtastic node"
	}
	fw := s.Metadata.GetFirmwareVersion()
	if fw != "" {
		fw = "Meshtastic " + fw
	}
	return radio.Info{Driver: Driver, Device: a.addr, Firmware: fw, Name: name}
}

func (a *Attached) Stats(context.Context) radio.Stats {
	s := a.client.Snapshot()
	return radio.Stats{Connected: s.Connected, Reconnects: s.Reconnects}
}

func (a *Attached) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	err := a.client.Close()
	close(a.frames)
	return err
}

// ------------------------------------------------------------------------------ state cache

type savedState struct {
	NodeNum  uint32            `json:"node_num"`
	User     json.RawMessage   `json:"user"`
	Channels []json.RawMessage `json:"channels"`
	Address  string            `json:"address"`
}

func (a *Attached) statePath() string {
	if a.stateDir == "" {
		return ""
	}
	return filepath.Join(a.stateDir, "attached-node.json")
}

func (a *Attached) saveState(st mesh.RemoteState) {
	path := a.statePath()
	if path == "" {
		return
	}
	s := savedState{NodeNum: st.NodeNum, Address: a.addr}
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

func (a *Attached) loadState() (mesh.RemoteState, bool) {
	path := a.statePath()
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
	return mesh.RemoteState{NodeNum: num, User: &pb.User{LongName: "Meshtastic node (connecting)", ShortName: "…"}}
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

// hostConfig mirrors the node's LoRa and device settings into the host's configuration, so the
// GUI shows what the node runs.
func hostConfig(cur mesh.Config, s mtclient.Snapshot) (mesh.Config, bool) {
	lora := s.Config.GetLora()
	if lora == nil {
		return cur, false
	}
	cfg := cur
	if r := lora.GetRegion(); r != pb.Config_LoRaConfig_UNSET {
		cfg.Region = r.String()
	}
	if lora.GetUsePreset() {
		cfg.Preset = lora.GetModemPreset()
	}
	if lora.GetHopLimit() != 0 {
		cfg.HopLimit = lora.GetHopLimit()
	}
	cfg.ChannelNum = int(lora.GetChannelNum())
	cfg.OverrideFreqMHz = float64(lora.GetOverrideFrequency())
	cfg.FreqOffsetMHz = float64(lora.GetFrequencyOffset())
	cfg.TxPowerDBm = int(lora.GetTxPower())
	if len(s.Channels) > 0 && s.Channels[0] != nil {
		cfg.PrimaryChannel = s.Channels[0].GetSettings().GetName()
	}
	cfg.RelayRole = RelayRole(s.Config.GetDevice().GetRole(), lora.GetTxEnabled())
	return cfg, true
}

// RelayRole maps a node's device role onto the host's relay roles.
func RelayRole(role pb.Config_DeviceConfig_Role, txEnabled bool) string {
	if !txEnabled {
		return mesh.RoleMonitor
	}
	switch role {
	case pb.Config_DeviceConfig_ROUTER, pb.Config_DeviceConfig_ROUTER_LATE, pb.Config_DeviceConfig_REPEATER:
		return mesh.RoleRouter
	case pb.Config_DeviceConfig_CLIENT_MUTE:
		return mesh.RoleMute
	}
	return mesh.RoleClient
}

// ErrNotReady is returned when settings can't reach a node that isn't connected.
var ErrNotReady = errors.New("the Meshtastic node isn't connected")

// ApplyConfig writes the host settings a node owns (region, preset, frequency slot, power, hop
// limit, primary channel name, role) to the node in one edit transaction, so it reboots at most
// once, then re-reads its configuration.
func (a *Attached) ApplyConfig(ctx context.Context, cfg mesh.Config) error {
	s := a.client.Snapshot()
	if !s.Connected || s.Config.GetLora() == nil {
		return ErrNotReady
	}
	var msgs []*pb.AdminMessage
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
	lora.TxEnabled = cfg.RelayRole != mesh.RoleMonitor && cfg.RelayRole != mesh.RoleOff
	if !proto.Equal(lora, s.Config.GetLora()) {
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: lora}}}})
	}
	if dev := s.Config.GetDevice(); dev != nil {
		role := deviceRole(cfg.RelayRole, dev.GetRole())
		if role != dev.GetRole() {
			d := proto.Clone(dev).(*pb.Config_DeviceConfig)
			d.Role = role
			msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Device{Device: d}}}})
		}
	}
	if len(s.Channels) > 0 && s.Channels[0] != nil && s.Channels[0].GetSettings().GetName() != cfg.PrimaryChannel {
		ch := proto.Clone(s.Channels[0]).(*pb.Channel)
		if ch.Settings == nil {
			ch.Settings = &pb.ChannelSettings{}
		}
		ch.Settings.Name = cfg.PrimaryChannel
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: ch}})
	}
	if len(msgs) == 0 {
		return nil
	}
	return a.edit(ctx, msgs)
}

// edit applies admin messages inside begin/commit_edit_settings and refreshes the mirror.
func (a *Attached) edit(ctx context.Context, msgs []*pb.AdminMessage) error {
	events, stop := a.client.Subscribe(16)
	defer stop()
	all := append([]*pb.AdminMessage{{PayloadVariant: &pb.AdminMessage_BeginEditSettings{BeginEditSettings: true}}}, msgs...)
	all = append(all, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_CommitEditSettings{CommitEditSettings: true}})
	for _, m := range all {
		if _, err := a.client.Admin(ctx, m); err != nil {
			return fmt.Errorf("meshtastic node refused the settings: %w", err)
		}
	}
	// Some changes reboot the node; the client reconnects by itself. Otherwise re-read the config.
	timer := time.NewTimer(a.rebootWait)
	defer timer.Stop()
	for {
		select {
		case e := <-events:
			if e.Kind == mtclient.Disconnected {
				return nil
			}
		case <-timer.C:
			a.client.Reconnect()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// deviceRole picks the node role for a host relay role, keeping the node's own choice where the
// host role doesn't say otherwise (a ROUTER_LATE stays one while the host asks for router).
func deviceRole(relay string, cur pb.Config_DeviceConfig_Role) pb.Config_DeviceConfig_Role {
	switch relay {
	case mesh.RoleRouter:
		if RelayRole(cur, true) == mesh.RoleRouter {
			return cur
		}
		return pb.Config_DeviceConfig_ROUTER
	case mesh.RoleMute:
		return pb.Config_DeviceConfig_CLIENT_MUTE
	case mesh.RoleClient:
		if RelayRole(cur, true) == mesh.RoleClient {
			return cur
		}
		return pb.Config_DeviceConfig_CLIENT
	}
	return cur // monitor and off only switch the transmitter off
}
