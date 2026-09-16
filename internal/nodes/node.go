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

	mu    sync.Mutex
	host  *mesh.Host
	id    *mesh.Identity
	owner *pb.User // names the node should carry (hosted nodes)

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

// ApplyConfig writes the host settings a node owns (region, preset, frequency slot, power, hop
// limit, primary channel name, role) to the node in one edit transaction, so it reboots at most
// once, then re-reads its configuration.
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
		d := proto.Clone(dev).(*pb.Config_DeviceConfig)
		d.Role = mesh.DeviceRole(cfg.RelayRole, dev.GetRole())
		if mode, ok := mesh.RebroadcastMode(cfg.Rebroadcast); ok {
			d.RebroadcastMode = mode
		}
		if !proto.Equal(d, dev) {
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
	if want != nil {
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
	for i, m := range all {
		if _, err := n.client.Admin(ctx, m); err != nil {
			if i == len(all)-1 && !n.client.Snapshot().Connected {
				break // the commit's ack was lost to the reboot it caused
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
