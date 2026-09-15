package mesh

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

const (
	MaxChannels    = 8
	backlogPackets = 64
)

// ClientSink receives client-API frames for one identity (a connected app, web chat, ...).
type ClientSink interface {
	SendFromRadio(fr *pb.FromRadio)
}

// Identity is one virtual Meshtastic node hosted on this radio.
type Identity struct {
	mu         sync.RWMutex
	NodeNum    uint32
	PrivateKey []byte
	PublicKey  []byte
	User       *pb.User
	Channels   [MaxChannels]*pb.Channel
	IsRelay    bool
	Enabled    bool
	APIBind    string
	APIPort    int
	CreatedAt  time.Time
	// ShareLimitPct is this identity's slice of the hourly duty budget (0 = host default).
	ShareLimitPct float64
	MACAddr       []byte

	sinks           map[ClientSink]struct{}
	backlog         []*pb.FromRadio
	lastNodeInfoTx  time.Time
	nextNodeInfo    time.Time
	nextPosition      time.Time
	lastPositionReply time.Time
	nodeInfoReplied map[uint32]time.Time
	lastTraceroute  time.Time
}

// NewIdentity creates an identity from a private key (nil generates one).
func NewIdentity(priv []byte, longName, shortName string) (*Identity, error) {
	if priv == nil {
		priv = wire.GeneratePrivateKey()
	}
	pub, err := wire.PublicKey(priv)
	if err != nil {
		return nil, err
	}
	num := wire.NodeNumFromPublicKey(pub)
	if num < wire.NumReserved || num == wire.Broadcast {
		return nil, errors.New("key maps to a reserved node number, pick another")
	}
	if shortName == "" {
		shortName = fmt.Sprintf("%04x", num&0xFFFF)
	}
	if longName == "" {
		longName = "Meshtastic " + shortName
	}
	mac := make([]byte, 6)
	mac[0], mac[1] = 0x02, 0x4d // locally administered
	mac[2], mac[3], mac[4], mac[5] = byte(num>>24), byte(num>>16), byte(num>>8), byte(num)
	id := &Identity{
		NodeNum:    num,
		PrivateKey: priv,
		PublicKey:  pub,
		Enabled:    true,
		CreatedAt:  time.Now(),
		MACAddr:    mac,
		User: &pb.User{
			Id:        wire.NodeID(num),
			LongName:  truncate(longName, 39),
			ShortName: truncate(shortName, 4),
			HwModel:   pb.HardwareModel_PORTDUINO,
			Role:      pb.Config_DeviceConfig_CLIENT,
			PublicKey: pub,
			Macaddr:   mac,
		},
		sinks:           map[ClientSink]struct{}{},
		nodeInfoReplied: map[uint32]time.Time{},
	}
	id.Channels[0] = &pb.Channel{Index: 0, Role: pb.Channel_PRIMARY,
		Settings: &pb.ChannelSettings{Psk: []byte{1}, ModuleSettings: &pb.ModuleSettings{}}}
	for i := 1; i < MaxChannels; i++ {
		id.Channels[i] = &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED, Settings: &pb.ChannelSettings{}}
	}
	return id, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	for len(s) > n {
		s = s[:len(s)-1]
	}
	return s
}

func (id *Identity) NodeID() string { return wire.NodeID(id.NodeNum) }

// UserCopy returns the User protobuf safe to send.
func (id *Identity) UserCopy() *pb.User {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return proto.Clone(id.User).(*pb.User)
}

// ChannelCopy returns a copy of channel i.
func (id *Identity) ChannelCopy(i int) *pb.Channel {
	id.mu.RLock()
	defer id.mu.RUnlock()
	if i < 0 || i >= MaxChannels || id.Channels[i] == nil {
		return nil
	}
	return proto.Clone(id.Channels[i]).(*pb.Channel)
}

// SetOwner updates names (used by admin set_owner and the web API).
func (id *Identity) SetOwner(longName, shortName string) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if longName != "" {
		id.User.LongName = truncate(longName, 39)
	}
	if shortName != "" {
		id.User.ShortName = truncate(shortName, 4)
	}
}

// AddSink attaches a client and flushes any backlog to it.
func (id *Identity) AddSink(s ClientSink) {
	id.mu.Lock()
	id.sinks[s] = struct{}{}
	id.mu.Unlock()
}

// FlushBacklog sends packets that arrived while no client was attached.
func (id *Identity) FlushBacklog(s ClientSink) {
	id.mu.Lock()
	b := id.backlog
	id.backlog = nil
	id.mu.Unlock()
	for _, fr := range b {
		s.SendFromRadio(fr)
	}
}

func (id *Identity) RemoveSink(s ClientSink) {
	id.mu.Lock()
	delete(id.sinks, s)
	id.mu.Unlock()
}

func (id *Identity) ClientCount() int {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return len(id.sinks)
}

// deliverToClients pushes a FromRadio to every attached client, or keeps it for later.
func (id *Identity) deliverToClients(fr *pb.FromRadio, keepIfOffline bool) {
	id.mu.Lock()
	if len(id.sinks) == 0 {
		if keepIfOffline {
			id.backlog = append(id.backlog, fr)
			if len(id.backlog) > backlogPackets {
				id.backlog = id.backlog[len(id.backlog)-backlogPackets:]
			}
		}
		id.mu.Unlock()
		return
	}
	sinks := make([]ClientSink, 0, len(id.sinks))
	for s := range id.sinks {
		sinks = append(sinks, s)
	}
	id.mu.Unlock()
	for _, s := range sinks {
		s.SendFromRadio(fr)
	}
}

// BacklogLen is the number of packets waiting for a client.
func (id *Identity) BacklogLen() int {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return len(id.backlog)
}

// --------------------------------------------------------------------------------- channels

// resolvedChannel is a channel ready for crypto.
type resolvedChannel struct {
	index int
	name  string // resolved display name used in the hash
	key   []byte
	hash  uint8
	aead  bool
}

func (id *Identity) resolvedChannels(presetDisplay string) []resolvedChannel {
	id.mu.RLock()
	defer id.mu.RUnlock()
	var out []resolvedChannel
	var primaryKey []byte
	for i, ch := range id.Channels {
		if ch == nil || ch.Settings == nil || ch.Role == pb.Channel_DISABLED {
			continue
		}
		key := wire.ExpandPSK(ch.Settings.Psk)
		if i == 0 || ch.Role == pb.Channel_PRIMARY {
			primaryKey = key
		}
		if len(ch.Settings.Psk) == 0 && ch.Role == pb.Channel_SECONDARY {
			key = primaryKey
		}
		name := ch.Settings.Name
		if name == "" {
			name = presetDisplay
		}
		out = append(out, resolvedChannel{index: i, name: name, key: key, aead: ch.Settings.UseAead,
			hash: wire.ChannelHash(name, key, ch.Settings.UseAead)})
	}
	return out
}

// ------------------------------------------------------------------------------ persistence

// IdentityRecord is the on-disk form of an identity.
type IdentityRecord struct {
	PrivateKey string   `json:"private_key"`
	LongName   string   `json:"long_name"`
	ShortName  string   `json:"short_name"`
	Role       string   `json:"role,omitempty"`
	IsRelay    bool     `json:"is_relay,omitempty"`
	Enabled    bool     `json:"enabled"`
	APIBind    string   `json:"api_bind,omitempty"`
	APIPort    int      `json:"api_port,omitempty"`
	CreatedAt  int64    `json:"created_at"`
	ShareLimit float64  `json:"share_limit_pct,omitempty"`
	Channels   []string `json:"channels"` // base64 protobuf Channel
}

func (id *Identity) Record() IdentityRecord {
	id.mu.RLock()
	defer id.mu.RUnlock()
	r := IdentityRecord{
		PrivateKey: base64.StdEncoding.EncodeToString(id.PrivateKey),
		LongName:   id.User.LongName, ShortName: id.User.ShortName, Role: id.User.Role.String(),
		IsRelay: id.IsRelay, Enabled: id.Enabled, APIBind: id.APIBind, APIPort: id.APIPort,
		CreatedAt: id.CreatedAt.UnixMilli(), ShareLimit: id.ShareLimitPct,
	}
	for _, ch := range id.Channels {
		b, _ := proto.Marshal(ch)
		r.Channels = append(r.Channels, base64.StdEncoding.EncodeToString(b))
	}
	return r
}

func IdentityFromRecord(r IdentityRecord) (*Identity, error) {
	priv, err := base64.StdEncoding.DecodeString(r.PrivateKey)
	if err != nil || len(priv) != 32 {
		return nil, fmt.Errorf("bad private key for %q", r.LongName)
	}
	id, err := NewIdentity(priv, r.LongName, r.ShortName)
	if err != nil {
		return nil, err
	}
	id.IsRelay, id.Enabled, id.APIBind, id.APIPort = r.IsRelay, r.Enabled, r.APIBind, r.APIPort
	id.ShareLimitPct = r.ShareLimit
	if v, ok := pb.Config_DeviceConfig_Role_value[r.Role]; ok {
		id.User.Role = pb.Config_DeviceConfig_Role(v)
	}
	if r.CreatedAt > 0 {
		id.CreatedAt = time.UnixMilli(r.CreatedAt)
	}
	for i, s := range r.Channels {
		if i >= MaxChannels {
			break
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			continue
		}
		ch := &pb.Channel{}
		if proto.Unmarshal(b, ch) == nil {
			ch.Index = int32(i)
			if ch.Settings == nil {
				ch.Settings = &pb.ChannelSettings{}
			}
			id.Channels[i] = ch
		}
	}
	return id, nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// SetChannel applies a client or web channel change. The primary channel's name and role are
// shared by every identity (they pick the frequency), so only its PSK and module settings change.
func (h *Host) SetChannel(id *Identity, ch *pb.Channel) error {
	if ch == nil || ch.Index < 0 || int(ch.Index) >= MaxChannels {
		return errors.New("channel index out of range")
	}
	nc := proto.Clone(ch).(*pb.Channel)
	if nc.Settings == nil {
		nc.Settings = &pb.ChannelSettings{}
	}
	if len(nc.Settings.Psk) > 32 {
		return errors.New("PSK longer than 32 bytes")
	}
	if nc.Index == 0 {
		nc.Role = pb.Channel_PRIMARY
		nc.Settings.Name = h.Config().PrimaryChannel
	} else if nc.Role == pb.Channel_PRIMARY {
		return errors.New("only channel 0 can be primary")
	}
	id.mu.Lock()
	id.Channels[nc.Index] = nc
	id.mu.Unlock()
	h.ChannelsChanged()
	h.Bus.Publish(Event{Type: "identity", Data: id.NodeID()})
	return nil
}

// SetRole changes the identity's advertised device role.
func (id *Identity) SetRole(role string) error {
	v, ok := pb.Config_DeviceConfig_Role_value[strings.ToUpper(role)]
	if !ok {
		return fmt.Errorf("unknown role %q", role)
	}
	id.mu.Lock()
	id.User.Role = pb.Config_DeviceConfig_Role(v)
	id.mu.Unlock()
	return nil
}
