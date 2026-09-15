// Package mesh is the Meshtastic stack shared by every virtual node on one radio: receive
// pipeline, duplicate cache, relaying, reliable delivery, and local routing between identities.
//
// Behaviour follows meshtastic/firmware src/mesh (Router, FloodingRouter, NextHopRouter,
// ReliableRouter) at 2.8.1, adapted so N identities share one radio and one relay persona.
package mesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/pb"
	"github.com/A13xB0/RepeaterTastic/internal/phy"
	"github.com/A13xB0/RepeaterTastic/internal/radio"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

// Relay roles for the host's relay persona.
const (
	RoleClient = "client"
	RoleRouter = "router"
	RoleMute   = "mute"
)

const (
	numReliableRetx         = 3
	numReliableUnicastRetry = 5
	defaultHopLimit         = 3
)

// Config is the host-wide mesh configuration.
type Config struct {
	Region            string
	Preset            phy.Preset
	PrimaryChannel    string // shared by all identities; "" = preset name
	ChannelNum        int
	OverrideFreqMHz   float64
	FreqOffsetMHz     float64
	TxPowerDBm        int
	HopLimit          uint32
	RelayRole         string
	DutyCyclePct      float64 // 0 = region default
	OverrideDutyCycle bool
	NodeInfoInterval  time.Duration
	// TelemetryInterval is how often the relay persona broadcasts DeviceMetrics; 0 = off.
	TelemetryInterval time.Duration
	LocalDMOverRF     bool
	StateDir          string
	// RadioID names this radio when a site runs several ("main" for the first).
	RadioID string
	// OKToMQTT sets the OK_TO_MQTT bit on packets our identities originate: consent for
	// other nodes' MQTT gateways to uplink them (LoRaConfig.config_ok_to_mqtt).
	OKToMQTT bool
	// IgnoreMQTT stops the relay persona rebroadcasting packets that arrived via MQTT
	// (LoRaConfig.ignore_mqtt), so broker traffic never costs this radio's airtime.
	IgnoreMQTT bool
	// HwModel is the hardware identities advertise; UNSET = the modem's own board.
	HwModel pb.HardwareModel
	// Position is the site's fixed location, broadcast and answered on request.
	Position FixedPosition
}

// Counters are cumulative statistics.
type Counters struct {
	Rx, RxDupe, RxUndecryptable, RxBad, Tx, TxFailed, Relayed, RelayCancelled, AckOK, AckFail, DroppedDuty atomic.Uint64
}

// TxGate coordinates transmissions between the radios of one site. Acquire blocks until
// this host may key up and returns a release func to call when the transmission is over.
// It returns ErrSiteDutyCycle when the site-wide airtime budget is spent.
type TxGate interface {
	Acquire(ctx context.Context, h *Host) (release func(), err error)
}

// ErrSiteDutyCycle is returned by a TxGate when the site's shared airtime budget is used up.
var ErrSiteDutyCycle = errors.New("site duty cycle limit reached")

// Link is an extra packet interface (UDP multicast, host link, MQTT) carrying encrypted MeshPackets.
type Link interface {
	Name() string
	SendPacket(p *pb.MeshPacket)
}

// ChannelLink is a Link that also wants the channel packets received, first sighting only,
// with the channel they decoded on and their decoded payload (the MQTT uplink). Packets
// injected by a link arrive too, marked via_mqtt; the link decides whether to pass them on.
type ChannelLink interface {
	Link
	ChannelPacketHeard(p *pb.MeshPacket, ch ChannelRef, data *pb.Data)
}

// PlainLink is a Link that also wants our own packets' decoded payload when it is known.
type PlainLink interface {
	Link
	SendPacketPlain(p *pb.MeshPacket, data *pb.Data)
}

// ChannelRef describes a channel shared by one or more identities on this host.
type ChannelRef struct {
	Name     string // display name, e.g. "LongFast"
	Hash     uint8
	Uplink   bool // some identity holding this channel has uplink enabled
	Downlink bool // some identity holding this channel has downlink enabled
	OKToMQTT bool // the packet's sender allowed MQTT uplink (only set for heard packets)
	// PublicKey: the channel has no key or a well-known default key, so anyone can read it.
	PublicKey bool
}

type pendingTx struct {
	pkt       *pb.MeshPacket // encrypted, as sent
	origin    *Identity
	remaining int
	next      time.Time
	broadcast bool
	text      bool
	index     int      // channel index the packet was sent on (for a fallback on another radio)
	plain     *pb.Data // payload, for the packet log on retransmits
}

type chanMember struct {
	id    *Identity
	index int
}

type chanGroup struct {
	hash    uint8
	key     []byte
	aead    bool
	name    string
	members []chanMember
}

// Host runs the shared stack for all identities on one radio.
type Host struct {
	log   *slog.Logger
	radio radio.Radio

	cfgMu sync.RWMutex
	cfg   Config
	rp    phy.RadioParams

	mu    sync.RWMutex
	ids   map[uint32]*Identity
	relay *Identity

	chanMu    sync.Mutex
	chanCache map[uint8][]*chanGroup

	DB       *NodeDB
	Air      *Airtime
	Bus      *Bus
	Packets  *PacketLog
	Messages *MessageStore
	Counters Counters

	hist *History
	txq  *TxQueue

	pmu     sync.Mutex
	pending map[pktKey]*pendingTx

	linkMu sync.RWMutex
	links  []Link

	started       time.Time
	nextTelemetry time.Time // run loop only

	fed          *Federation // nil unless the site joins its radios (experimental)
	guestMu      sync.Mutex
	guestList    []*Identity
	guestGen     uint64
	guestOK      bool
	guestTimers  map[uint32]*guestTimer // run loop only
	stateDir     string
	radioOK      atomic.Bool
	nodeInfoAsks sync.Map // uint32 → time.Time

	gateMu sync.RWMutex
	gate   TxGate
}

// NewHost validates the PHY and prepares a host. Call AddIdentity for each node, then Run.
func NewHost(cfg Config, r radio.Radio, log *slog.Logger) (*Host, error) {
	if cfg.HopLimit == 0 || cfg.HopLimit > wire.HopMax {
		cfg.HopLimit = defaultHopLimit
	}
	if cfg.RelayRole == "" {
		cfg.RelayRole = RoleClient
	}
	if cfg.NodeInfoInterval == 0 {
		cfg.NodeInfoInterval = 3 * time.Hour
	}
	rp, err := phy.Resolve(phy.Options{Region: cfg.Region, Preset: cfg.Preset, PrimaryChannelName: cfg.PrimaryChannel,
		ChannelNum: cfg.ChannelNum, OverrideFreqMHz: cfg.OverrideFreqMHz, FreqOffsetMHz: cfg.FreqOffsetMHz,
		TxPowerDBm: cfg.TxPowerDBm})
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Host{
		log: log, radio: r, cfg: cfg, rp: rp,
		ids:       map[uint32]*Identity{},
		chanCache: nil,
		DB:        NewNodeDB(),
		Air:       NewAirtime(),
		Bus:       NewBus(),
		Packets:   NewPacketLog(5000),
		Messages:  NewMessageStore(1000),
		hist:      NewHistory(4096, 30*time.Minute),
		txq:       NewTxQueue(64),
		pending:   map[pktKey]*pendingTx{},
		started:   time.Now(),
		stateDir:  cfg.StateDir,
	}, nil
}

func (h *Host) Config() Config {
	h.cfgMu.RLock()
	defer h.cfgMu.RUnlock()
	return h.cfg
}

func (h *Host) RadioParams() phy.RadioParams {
	h.cfgMu.RLock()
	defer h.cfgMu.RUnlock()
	return h.rp
}

func (h *Host) Radio() radio.Radio { return h.radio }

// RadioID is the site-unique name of this host's radio.
func (h *Host) RadioID() string {
	if id := h.Config().RadioID; id != "" {
		return id
	}
	return "main"
}

// SetTxGate installs the site's transmit coordinator; nil removes it.
func (h *Host) SetTxGate(g TxGate) {
	h.gateMu.Lock()
	h.gate = g
	h.gateMu.Unlock()
}

func (h *Host) txGate() TxGate {
	h.gateMu.RLock()
	defer h.gateMu.RUnlock()
	return h.gate
}
func (h *Host) Started() time.Time { return h.started }

// SetRelayRole changes the relay persona role at runtime.
func (h *Host) SetRelayRole(role string) error {
	switch role {
	case RoleClient, RoleRouter, RoleMute:
	default:
		return fmt.Errorf("unknown relay role %q", role)
	}
	h.cfgMu.Lock()
	h.cfg.RelayRole = role
	h.cfgMu.Unlock()
	if r := h.Relay(); r != nil {
		r.mu.Lock()
		switch role {
		case RoleRouter:
			r.User.Role = pb.Config_DeviceConfig_ROUTER
		case RoleMute:
			r.User.Role = pb.Config_DeviceConfig_CLIENT_MUTE
		default:
			r.User.Role = pb.Config_DeviceConfig_CLIENT
		}
		r.mu.Unlock()
	}
	return nil
}

func (h *Host) AddLink(l Link) {
	h.linkMu.Lock()
	h.links = append(h.links, l)
	h.linkMu.Unlock()
}

// ------------------------------------------------------------------------------- identities

// AddIdentity registers a node. The first identity marked IsRelay becomes the relay persona.
func (h *Host) AddIdentity(id *Identity) error {
	h.mu.Lock()
	if _, dup := h.ids[id.NodeNum]; dup {
		h.mu.Unlock()
		return fmt.Errorf("identity %s already exists", id.NodeID())
	}
	for _, other := range h.ids {
		if wire.LastByte(other.NodeNum) == wire.LastByte(id.NodeNum) {
			h.mu.Unlock()
			return fmt.Errorf("identity %s shares its last byte with %s; generate another key", id.NodeID(), other.NodeID())
		}
	}
	if id.IsRelay {
		if h.relay != nil {
			h.mu.Unlock()
			return errors.New("a relay persona already exists")
		}
		h.relay = id
	}
	// The primary channel name is shared: it picks the frequency.
	primary := h.Config().PrimaryChannel
	id.mu.Lock()
	if id.Channels[0] != nil && id.Channels[0].Settings != nil {
		id.Channels[0].Settings.Name = primary
		id.Channels[0].Role = pb.Channel_PRIMARY
	}
	id.nextNodeInfo = time.Now().Add(30*time.Second + time.Duration(len(h.ids))*20*time.Second)
	id.mu.Unlock()
	h.ids[id.NodeNum] = id
	h.mu.Unlock()
	if id.IsRelay {
		_ = h.SetRelayRole(h.Config().RelayRole)
	}
	h.DB.Update(id.NodeNum, func(e *NodeEntry) {
		e.Local = true
		e.User = id.UserCopy()
		e.HopsAway = 0
		e.LastHeard = time.Now()
	})
	h.ChannelsChanged()
	h.Bus.Publish(Event{Type: "identity", Data: id.NodeID()})
	return nil
}

// RemoveIdentity deletes a node (not the relay persona).
func (h *Host) RemoveIdentity(num uint32) error {
	h.mu.Lock()
	id, ok := h.ids[num]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("no identity %s", wire.NodeID(num))
	}
	if id.IsRelay {
		h.mu.Unlock()
		return errors.New("the relay persona can't be removed")
	}
	delete(h.ids, num)
	h.mu.Unlock()
	h.DB.Delete(num)
	h.ChannelsChanged()
	h.Bus.Publish(Event{Type: "identity", Data: wire.NodeID(num)})
	return nil
}

func (h *Host) Identity(num uint32) *Identity {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ids[num]
}

func (h *Host) Relay() *Identity {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.relay
}

// Identities returns all identities, relay persona first, then by creation time.
func (h *Host) Identities() []*Identity {
	h.mu.RLock()
	out := make([]*Identity, 0, len(h.ids))
	for _, id := range h.ids {
		out = append(out, id)
	}
	h.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsRelay != out[j].IsRelay {
			return out[i].IsRelay
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (h *Host) presetDisplay() string { return phy.Presets[h.RadioParams().Preset].Display }

// ChannelsChanged must be called after any identity's channels change.
func (h *Host) ChannelsChanged() {
	h.chanMu.Lock()
	h.chanCache = nil
	h.chanMu.Unlock()
	if h.fed != nil {
		h.fed.gen.Add(1) // other radios' guest lists may include this radio's identities
	}
}

// Channels lists every distinct channel held by this host's identities.
func (h *Host) Channels() []ChannelRef {
	h.channelGroups(0) // build the cache
	h.chanMu.Lock()
	defer h.chanMu.Unlock()
	var out []ChannelRef
	for _, gs := range h.chanCache {
		for _, g := range gs {
			out = append(out, g.ref())
		}
	}
	return out
}

// ChannelsByHash lists this host's channels with that hash (usually one).
func (h *Host) ChannelsByHash(hash uint8) []ChannelRef {
	var out []ChannelRef
	for _, g := range h.channelGroups(hash) {
		out = append(out, g.ref())
	}
	return out
}

func (g *chanGroup) ref() ChannelRef {
	r := ChannelRef{Name: g.name, Hash: g.hash, PublicKey: wire.IsPublicKey(g.key)}
	for _, m := range g.members {
		if ch := m.id.ChannelCopy(m.index); ch != nil && ch.GetSettings() != nil {
			r.Uplink = r.Uplink || ch.GetSettings().GetUplinkEnabled()
			r.Downlink = r.Downlink || ch.GetSettings().GetDownlinkEnabled()
		}
	}
	return r
}

func (h *Host) channelGroups(hash uint8) []*chanGroup {
	h.chanMu.Lock()
	defer h.chanMu.Unlock()
	if h.chanCache == nil {
		h.chanCache = map[uint8][]*chanGroup{}
		display := h.presetDisplay()
		for _, id := range append(h.Identities(), h.guests()...) {
			for _, rc := range id.resolvedChannels(display) {
				if !h.slotOnThisRadio(id, rc.index) {
					continue // that slot is on another radio
				}
				var g *chanGroup
				for _, x := range h.chanCache[rc.hash] {
					if string(x.key) == string(rc.key) && x.aead == rc.aead && x.name == rc.name {
						g = x
						break
					}
				}
				if g == nil {
					g = &chanGroup{hash: rc.hash, key: rc.key, aead: rc.aead, name: rc.name}
					h.chanCache[rc.hash] = append(h.chanCache[rc.hash], g)
				}
				g.members = append(g.members, chanMember{id: id, index: rc.index})
			}
		}
	}
	return h.chanCache[hash]
}

// ----------------------------------------------------------------------------------------- run

// Run configures the radio and runs the stack until ctx ends.
func (h *Host) Run(ctx context.Context) error {
	if h.Relay() == nil {
		return errors.New("no relay persona configured")
	}
	if h.stateDir != "" {
		if err := h.DB.Load(filepath.Join(h.stateDir, "nodedb.json")); err != nil {
			h.log.Warn("node DB not loaded", "err", err)
		}
		if err := h.Messages.Load(filepath.Join(h.stateDir, "messages.json")); err != nil {
			h.log.Warn("messages not loaded", "err", err)
		}
		for _, id := range h.Identities() { // re-assert local entries over stale saved ones
			h.DB.Update(id.NodeNum, func(e *NodeEntry) { e.Local = true; e.User = id.UserCopy(); e.HopsAway = 0 })
		}
	}
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); h.configureLoop(ctx) }()
	go func() { defer wg.Done(); h.rxLoop(ctx) }()
	go func() { defer wg.Done(); h.txLoop(ctx) }()
	go func() { defer wg.Done(); h.timerLoop(ctx) }()
	wg.Wait()
	if h.stateDir != "" {
		_ = h.DB.Save(filepath.Join(h.stateDir, "nodedb.json"))
		_ = h.Messages.Save(filepath.Join(h.stateDir, "messages.json"))
	}
	return ctx.Err()
}

func (h *Host) radioConfig() radio.Config {
	rp := h.RadioParams()
	return radio.Config{FrequencyHz: rp.FrequencyHz(), BandwidthHz: rp.BwHz(), SF: uint8(rp.SF), CR: uint8(rp.CR),
		SyncWord: rp.SyncWord, Preamble: uint16(rp.Preamble), TxPowerDBm: int8(rp.TxPowerDBm)}
}

func (h *Host) configureLoop(ctx context.Context) {
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := h.radio.Configure(cctx, h.radioConfig())
		cancel()
		if err == nil {
			rp := h.RadioParams()
			h.radioOK.Store(true)
			h.log.Info("radio configured", "freq_mhz", rp.FrequencyMHz, "preset", rp.PresetName(), "sf", rp.SF,
				"bw_khz", rp.BwKHz, "sync", fmt.Sprintf("0x%02x", rp.SyncWord), "power_dbm", rp.TxPowerDBm)
			return
		}
		h.log.Error("radio configuration failed, retrying in 10s", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (h *Host) rxLoop(ctx context.Context) {
	frames := h.radio.Frames()
	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-frames:
			if !ok {
				return
			}
			h.Air.AddRx(f.At, h.RadioParams().AirtimeMs(len(f.Data)))
			p := wire.DecodeFrame(f.Data, int32(f.RSSI), f.SNR)
			if p == nil {
				h.Counters.RxBad.Add(1)
				continue
			}
			h.HandleReceived(p, f.Data)
		}
	}
}

func (h *Host) timerLoop(ctx context.Context) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	lastSave := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(lastSave) > time.Minute {
				h.RecordOwnPositions()
			}
			h.doRetransmissions(now)
			h.periodicNodeInfo(now)
			h.periodicPosition(now)
			h.periodicTelemetry(now)
			h.periodicGuests(now)
			if h.stateDir != "" && now.Sub(lastSave) > time.Minute {
				lastSave = now
				if err := h.DB.Save(filepath.Join(h.stateDir, "nodedb.json")); err != nil {
					h.log.Warn("saving node DB", "err", err)
				}
				if err := h.Messages.Save(filepath.Join(h.stateDir, "messages.json")); err != nil {
					h.log.Warn("saving messages", "err", err)
				}
			}
		}
	}
}

func (h *Host) dutyLimit() float64 {
	c := h.Config()
	if c.OverrideDutyCycle {
		return 100
	}
	if c.DutyCyclePct > 0 {
		return c.DutyCyclePct
	}
	return h.RadioParams().Region.DutyCyclePct
}

func (h *Host) txLoop(ctx context.Context) {
	for {
		it, err := h.txq.Next(ctx)
		if err != nil {
			return
		}
		now := time.Now()
		rp := h.RadioParams()
		if limit := h.dutyLimit(); limit < 100 && h.Air.TxPercent(now) >= limit {
			h.Counters.DroppedDuty.Add(1)
			if !it.relay {
				if o := h.Identity(it.origin); o != nil {
					h.nakLocal(o, it.pkt.Id, pb.Routing_DUTY_CYCLE_LIMIT)
				}
			}
			h.log.Warn("duty cycle limit reached, dropping packet", "id", it.pkt.Id, "relay", it.relay)
			continue
		}
		if busy, err := h.radio.ChannelBusy(ctx); err == nil && busy && it.attempts < 12 {
			it.attempts++
			it.due = now.Add(time.Duration(phy.OwnTxDelayMs(h.Air.ChannelUtilPercent(now), rp.SlotTimeMs())+rp.SlotTimeMs()) * time.Millisecond)
			h.txq.Enqueue(it)
			continue
		}
		frame, err := wire.EncodeFrame(it.pkt)
		if err != nil {
			h.log.Error("encoding frame", "err", err)
			continue
		}
		release := func() {}
		if g := h.txGate(); g != nil {
			rel, gerr := g.Acquire(ctx, h)
			if errors.Is(gerr, ErrSiteDutyCycle) {
				h.Counters.DroppedDuty.Add(1)
				if !it.relay {
					if o := h.Identity(it.origin); o != nil {
						h.nakLocal(o, it.pkt.Id, pb.Routing_DUTY_CYCLE_LIMIT)
					}
				}
				h.log.Warn("site duty cycle limit reached, dropping packet", "id", it.pkt.Id, "relay", it.relay)
				continue
			}
			if gerr != nil {
				return // context cancelled while waiting for another radio
			}
			release = rel
		}
		sctx, cancel := context.WithTimeout(ctx, time.Duration(rp.AirtimeMs(len(frame))*2+5000)*time.Millisecond)
		err = h.radio.Send(sctx, frame)
		cancel()
		release()
		if err != nil {
			h.Counters.TxFailed.Add(1)
			h.log.Warn("transmit failed", "id", it.pkt.Id, "err", err)
			continue
		}
		ms := rp.AirtimeMs(len(frame))
		h.Air.AddTx(time.Now(), ms, it.origin)
		h.Counters.Tx.Add(1)
		kind := "ours"
		if it.relay {
			kind = "relayed"
			h.Counters.Relayed.Add(1)
		}
		rec := h.baseRecord(it.pkt, frame, "tx", kind)
		rec.AirtimeMs = ms
		if it.plain != nil {
			rec.Port, rec.PKI = it.plain.Portnum.String(), it.pkt.PkiEncrypted
			rec.Summary, rec.Payload = summarize(it.plain), payloadJSON(it.plain)
		} else if dec := h.decode(it.pkt); dec.ok {
			h.fillRecordFromDecoded(&rec, it.pkt, dec) // a relayed packet on a channel we hold
		}
		if o := h.identityAny(it.origin); o != nil {
			rec.DecodedBy = o.NodeID()
			if m, ok := h.storeFor(o).SetStatus(o.NodeNum, it.pkt.Id, "sent", ""); ok {
				h.publishMessage(o, m)
			}
		}
		h.publishPacket(rec)
		h.linkMu.RLock()
		for _, l := range h.links {
			if pl, ok := l.(PlainLink); ok {
				pl.SendPacketPlain(it.pkt, it.plain)
			} else {
				l.SendPacket(it.pkt)
			}
		}
		h.linkMu.RUnlock()
	}
}

// MessageEvent is published when a chat message is added or changes status.
type MessageEvent struct {
	Identity string  `json:"identity"`
	Message  Message `json:"message"`
}

// SaveIdentities writes all identities to the state dir.
func (h *Host) SaveIdentities() error {
	if h.stateDir == "" {
		return nil
	}
	var recs []IdentityRecord
	for _, id := range h.Identities() {
		recs = append(recs, id.Record())
	}
	return writeJSONAtomic(filepath.Join(h.stateDir, "identities.json"), recs)
}

// LoadIdentityRecords reads identities saved by SaveIdentities.
func LoadIdentityRecords(stateDir string) ([]IdentityRecord, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "identities.json"))
	if err != nil {
		return nil, err
	}
	var recs []IdentityRecord
	return recs, jsonUnmarshal(b, &recs)
}

func clonePacket(p *pb.MeshPacket) *pb.MeshPacket { return proto.Clone(p).(*pb.MeshPacket) }

// UpdateConfig applies a new host configuration at runtime. When the PHY changes (region, preset,
// primary channel, frequency, power) the radio is retuned and every identity's primary channel
// name follows.
func (h *Host) UpdateConfig(ctx context.Context, cfg Config) error {
	if cfg.HopLimit == 0 || cfg.HopLimit > wire.HopMax {
		cfg.HopLimit = defaultHopLimit
	}
	if cfg.NodeInfoInterval == 0 {
		cfg.NodeInfoInterval = 3 * time.Hour
	}
	rp, err := phy.Resolve(phy.Options{Region: cfg.Region, Preset: cfg.Preset, PrimaryChannelName: cfg.PrimaryChannel,
		ChannelNum: cfg.ChannelNum, OverrideFreqMHz: cfg.OverrideFreqMHz, FreqOffsetMHz: cfg.FreqOffsetMHz,
		TxPowerDBm: cfg.TxPowerDBm})
	if err != nil {
		return err
	}
	h.cfgMu.Lock()
	old := h.rp
	cfg.StateDir = h.stateDir // fixed at start
	if cfg.RadioID == "" {
		cfg.RadioID = h.cfg.RadioID
	}
	h.cfg = cfg
	h.rp = rp
	h.cfgMu.Unlock()
	_ = h.SetRelayRole(cfg.RelayRole)
	for _, id := range h.Identities() {
		id.mu.Lock()
		if id.Channels[0] != nil && id.Channels[0].Settings != nil {
			id.Channels[0].Settings.Name = cfg.PrimaryChannel
		}
		id.mu.Unlock()
	}
	h.ChannelsChanged()
	if old.FrequencyHz() != rp.FrequencyHz() || old.SF != rp.SF || old.BwKHz != rp.BwKHz || old.CR != rp.CR ||
		old.TxPowerDBm != rp.TxPowerDBm || old.Preamble != rp.Preamble {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := h.radio.Configure(cctx, h.radioConfig()); err != nil {
			return fmt.Errorf("settings saved but the radio rejected them: %w", err)
		}
		h.log.Info("radio retuned", "freq_mhz", rp.FrequencyMHz, "preset", rp.PresetName())
	}
	return nil
}

// RadioConfigured reports whether the modem accepted our PHY settings.
func (h *Host) RadioConfigured() bool { return h.radioOK.Load() }

// QueueLen is the number of packets waiting to transmit.
func (h *Host) QueueLen() int { return h.txq.Len() }

// DropOutgoing cancels an identity's queued transmissions and pending retries (before it moves
// to another radio). Its unsent messages are marked failed so they can be sent again.
func (h *Host) DropOutgoing(num uint32, reason string) int {
	ids := h.txq.DropOrigin(num)
	h.pmu.Lock()
	for k, p := range h.pending {
		if p.origin != nil && p.origin.NodeNum == num {
			ids = append(ids, p.pkt.GetId())
			delete(h.pending, k)
		}
	}
	h.pmu.Unlock()
	failed := 0
	for _, pid := range ids {
		if m, ok := h.Messages.SetStatus(num, pid, "failed", reason); ok {
			failed++
			h.Bus.Publish(Event{Type: "message", Data: MessageEvent{Identity: wire.NodeID(num), Message: m}})
		}
	}
	return failed
}
