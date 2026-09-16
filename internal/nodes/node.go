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
	// follow: the host takes its settings from the node (an attached board). Otherwise the node
	// takes the host's (a hosted meshtasticd).
	follow bool

	mu    sync.Mutex
	host  *mesh.Host
	id    *mesh.Identity
	onCfg func(mesh.Config)
	owner *pb.User // names the node should carry (hosted nodes)

	// rebootWait is how long a settings change waits for the node to reboot before re-reading it.
	rebootWait time.Duration
}

// OnConfig registers fn to hear the host settings mirrored from the node after each handshake.
func (n *Node) OnConfig(fn func(mesh.Config)) {
	n.mu.Lock()
	n.onCfg = fn
	h := n.host
	n.mu.Unlock()
	// The first handshake usually finishes before anyone listens: hand over what it mirrored.
	if h != nil && n.client.Snapshot().Connected {
		fn(h.Config())
	}
}

var _ mesh.Remote = (*Node)(nil)

// Client is the node's API client.
func (n *Node) Client() *mtclient.Client { return n.client }

// Identity creates the host's identity for the node. It waits up to wait for the node to answer,
// then falls back to the node's saved state, then to a placeholder that is replaced when the node
// first answers.
func (n *Node) Identity(ctx context.Context, wait time.Duration) (*mesh.Identity, error) {
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
			n.logf("meshtastic: node at %s not answering yet (%v); using its saved state", n.addr, err)
		} else {
			n.logf("meshtastic: node at %s not answering yet (%v); it appears once it does", n.addr, err)
			st = placeholderState(n.addr)
		}
	}
	id, err := mesh.NewRemoteIdentity(n, st)
	if err != nil {
		return nil, err
	}
	id.IsRelay = true
	return id, nil
}

// Bind follows the node for host h, whose relay persona is id, until ctx ends.
func (n *Node) Bind(ctx context.Context, h *mesh.Host, id *mesh.Identity) {
	n.mu.Lock()
	n.host, n.id = h, id
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
				n.logf("meshtastic: node at %s disconnected: %v", n.addr, e.Err)
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
	st := remoteState(snap)
	n.saveState(st)
	n.mu.Lock()
	h, cur := n.host, n.id
	n.mu.Unlock()
	if cur.NodeNum != st.NodeNum {
		next, err := mesh.NewRemoteIdentity(n, st)
		if err == nil {
			err = h.SwapRemote(cur, next)
		}
		if err != nil {
			n.logf("meshtastic: node is now %s but the identity couldn't follow: %v", wire.NodeID(st.NodeNum), err)
			return
		}
		n.logf("meshtastic: node %s replaces %s", next.NodeID(), cur.NodeID())
		n.mu.Lock()
		n.id, cur = next, next
		n.mu.Unlock()
	}
	if !n.follow {
		if err := n.ApplyConfig(ctx, h.Config()); err != nil {
			n.logf("meshtastic: node %s didn't take the host's settings: %v", cur.NodeID(), err)
		}
	} else if cfg, ok := hostConfig(h.Config(), snap); ok {
		if err := h.MirrorConfig(ctx, cfg); err != nil {
			n.logf("meshtastic: host settings not updated from the node: %v", err)
		} else {
			n.mu.Lock()
			fn := n.onCfg
			n.mu.Unlock()
			if fn != nil {
				fn(h.Config())
			}
		}
	}
	h.SyncRemote(cur, st)
	n.logf("meshtastic: node %s %q on %s, firmware %s, %s %s", cur.NodeID(), st.User.GetLongName(), n.addr,
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
	return filepath.Join(n.stateDir, "attached-node.json")
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

// Kind is "board" for an attached node and "hosted" for a meshtasticd RepeaterTastic runs.
func (n *Node) Kind() string {
	if n.follow {
		return "board"
	}
	return "hosted"
}

// SetOwner sets the names a hosted node is given (and keeps) on every connect.
func (n *Node) SetOwner(long, short string) {
	n.mu.Lock()
	n.owner = &pb.User{LongName: long, ShortName: short}
	n.mu.Unlock()
}

// ErrNotReady is returned when settings can't reach a node that isn't connected.
var ErrNotReady = errors.New("the Meshtastic node isn't connected")

// ApplyConfig writes the host settings a node owns (region, preset, frequency slot, power, hop
// limit, primary channel name, role) to the node in one edit transaction, so it reboots at most
// once, then re-reads its configuration.
func (n *Node) ApplyConfig(ctx context.Context, cfg mesh.Config) error {
	s := n.client.Snapshot()
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
	n.mu.Lock()
	want := n.owner
	n.mu.Unlock()
	if want != nil && !n.follow {
		self := s.Self().GetUser()
		if (want.LongName != "" && want.LongName != self.GetLongName()) || (want.ShortName != "" && want.ShortName != self.GetShortName()) {
			u := &pb.User{LongName: self.GetLongName(), ShortName: self.GetShortName()}
			if want.LongName != "" {
				u.LongName = want.LongName
			}
			if want.ShortName != "" {
				u.ShortName = want.ShortName
			}
			msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: u}})
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return n.edit(ctx, msgs)
}

// edit applies admin messages inside begin/commit_edit_settings and refreshes the mirror.
func (n *Node) edit(ctx context.Context, msgs []*pb.AdminMessage) error {
	events, stop := n.client.Subscribe(16)
	defer stop()
	all := append([]*pb.AdminMessage{{PayloadVariant: &pb.AdminMessage_BeginEditSettings{BeginEditSettings: true}}}, msgs...)
	all = append(all, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_CommitEditSettings{CommitEditSettings: true}})
	for _, m := range all {
		if _, err := n.client.Admin(ctx, m); err != nil {
			return fmt.Errorf("meshtastic node refused the settings: %w", err)
		}
	}
	// Some changes reboot the node; the client reconnects by itself. Otherwise re-read the config.
	timer := time.NewTimer(n.rebootWait)
	defer timer.Stop()
	for {
		select {
		case e := <-events:
			if e.Kind == mtclient.Disconnected {
				return nil
			}
		case <-timer.C:
			n.client.Reconnect()
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

func newNode(addr, stateDir string, c *mtclient.Client, logf func(string, ...any), follow bool) *Node {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Node{addr: addr, stateDir: stateDir, client: c, logf: logf, follow: follow, rebootWait: 8 * time.Second}
}
