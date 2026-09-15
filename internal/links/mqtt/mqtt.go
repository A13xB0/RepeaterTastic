// Package mqtt connects a radio to Meshtastic MQTT brokers. A radio can have several
// connections; each speaks as one gateway identity (the relay persona by default) and uses
// the firmware's topics and payloads: ServiceEnvelope protobufs on <root>/2/e/<channel>/<!gw>,
// JSON on <root>/2/json/<channel>/<!gw> and map reports on <root>/2/map/.
//
// Modes:
//   - gateway: uplink and downlink. Consent (OK_TO_MQTT) is respected, JSON is only published
//     for channels anyone can read, broker packets are rebroadcast at most zero-hop.
//   - uplink_only: gateway without the downlink.
//   - map_only: map reports only.
//   - monitor: uplink only, JSON by default, for dashboards and loggers.
//   - bridge: joins sites or private meshes. May uplink without consent, publish JSON of
//     private channels, rebroadcast with more hops. Needs bridge_acknowledged in the config.
//
// Uplink (mesh → broker): channel packets heard on air and our own identities' channel
// packets, on the connection's uplink channels. Direct messages (PKI) are never uplinked.
//
// Downlink (broker → mesh): encrypted packets on the connection's downlink channels, rate
// limited, marked via_mqtt and handed to the host like a received packet. They are only
// rebroadcast on air when the connection has relay_mqtt set.
//
// Packets that came from one connection are only passed to another when both allow
// cross_link; the host's duplicate filter stops a packet looping back in.
package mqtt

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

// Modes, formats and channel selection policies (as in the config).
const (
	ModeGateway    = "gateway"
	ModeUplinkOnly = "uplink_only"
	ModeMapOnly    = "map_only"
	ModeMonitor    = "monitor"
	ModeBridge     = "bridge"

	FormatEncrypted = "encrypted"
	FormatJSON      = "json"
	FormatBoth      = "both"

	SelectOverride = "override"
	SelectCombine  = "combine"
	SelectIdentity = "identity"
)

// Options configures a connection.
type Options struct {
	Name              string // unique per radio, e.g. "mqtt"
	Address           string // host:port
	Username          string
	Password          string
	TLS               bool
	Root              string // "" = msh/<region>
	Mode              string // "" = gateway
	Gateway           string // "" or "relay" = relay persona, or a node id
	Format            string // "" = encrypted (json for monitor)
	UplinkChannels    []string
	DownlinkChannels  []string
	ChannelSelection  string // "" = identity without channel lists, override with them
	IgnoreConsent     bool   // bridge only
	RelayMQTT         bool   // broker packets may be rebroadcast on air
	RelayHops         int    // hops a rebroadcast broker packet may travel (bridge only; otherwise 0)
	CrossLink         bool
	DownlinkPerMinute int    // 0 = 30
	UplinkPerMinute   int    // 0 = 120
	FirmwareVersion   string // reported in map reports

	MapReport         bool
	MapInterval       time.Duration // 0 = 1h, minimum 15m
	PositionPrecision int           // bits kept, 0 = 14
	Latitude          float64
	Longitude         float64
	Altitude          int

	// Origins is shared by a radio's connections to track which one a broker packet came from.
	Origins *Origins
	// Uplinked is shared by every connection on the site, so a packet heard on two radios is
	// published to a broker and root once.
	Uplinked *Uplinked
}

// Link is one broker connection.
type Link struct {
	host *mesh.Host
	opt  Options
	log  *slog.Logger

	client    paho.Client
	connected atomic.Bool

	Rx, Tx, Dropped atomic.Uint64

	subMu      sync.Mutex
	subscribed map[string]bool // channel names with an active downlink subscription

	down, up bucket
}

// New prepares a connection; Run connects it.
func New(h *mesh.Host, opt Options, log *slog.Logger) *Link {
	if opt.Name == "" {
		opt.Name = "mqtt"
	}
	if opt.Mode == "" {
		opt.Mode = ModeGateway
	}
	if opt.Format == "" {
		opt.Format = FormatEncrypted
		if opt.Mode == ModeMonitor {
			opt.Format = FormatJSON
		}
	}
	if opt.ChannelSelection == "" {
		opt.ChannelSelection = SelectIdentity
		if len(opt.UplinkChannels) > 0 || len(opt.DownlinkChannels) > 0 {
			opt.ChannelSelection = SelectOverride
		}
	}
	if opt.Mode != ModeBridge {
		opt.IgnoreConsent, opt.RelayHops = false, 0
	}
	if opt.DownlinkPerMinute <= 0 {
		opt.DownlinkPerMinute = 30
	}
	if opt.UplinkPerMinute <= 0 {
		opt.UplinkPerMinute = 120
	}
	if opt.MapInterval <= 0 {
		opt.MapInterval = time.Hour
	}
	if opt.MapInterval < 15*time.Minute {
		opt.MapInterval = 15 * time.Minute
	}
	if opt.PositionPrecision <= 0 {
		opt.PositionPrecision = 14
	}
	if opt.Origins == nil {
		opt.Origins = NewOrigins()
	}
	opt.Origins.register(opt.Name, opt.CrossLink)
	if log == nil {
		log = slog.Default()
	}
	return &Link{host: h, opt: opt, log: log.With("link", "mqtt", "connection", opt.Name), subscribed: map[string]bool{},
		down: newBucket(opt.DownlinkPerMinute), up: newBucket(opt.UplinkPerMinute)}
}

func (l *Link) Name() string    { return "mqtt:" + l.opt.Name }
func (l *Link) Connected() bool { return l.connected.Load() }

// Connection is the connection's configured name.
func (l *Link) Connection() string { return l.opt.Name }

// Mode is the effective mode; Format the effective payload format.
func (l *Link) Mode() string   { return l.opt.Mode }
func (l *Link) Format() string { return l.opt.Format }

// Root is the topic prefix in use.
func (l *Link) Root() string {
	if l.opt.Root != "" {
		return strings.TrimSuffix(l.opt.Root, "/")
	}
	return "msh/" + l.host.RadioParams().Region.Name
}

// Broker is the broker address, for status pages.
func (l *Link) Broker() string { return l.opt.Address }

// Subscriptions lists the channels currently downlinked.
func (l *Link) Subscriptions() []string {
	l.subMu.Lock()
	defer l.subMu.Unlock()
	out := make([]string, 0, len(l.subscribed))
	for ch := range l.subscribed {
		out = append(out, ch)
	}
	slices.Sort(out)
	return out
}

// UplinkChannels lists the host channels this connection currently uplinks.
func (l *Link) UplinkChannels() []string {
	var out []string
	if !l.uplinks() {
		return out
	}
	for _, ch := range l.host.Channels() {
		if l.selected(ch, true) && !slices.Contains(out, ch.Name) {
			out = append(out, ch.Name)
		}
	}
	slices.Sort(out)
	return out
}

// GatewayIdentity is the identity this connection speaks as: the configured one, or the
// relay persona when that is unset or no longer exists.
func (l *Link) GatewayIdentity() *mesh.Identity {
	if g := strings.TrimSpace(l.opt.Gateway); g != "" && g != "relay" {
		if n, err := strconv.ParseUint(strings.TrimPrefix(g, "!"), 16, 32); err == nil {
			if id := l.host.Identity(uint32(n)); id != nil {
				return id
			}
		}
	}
	return l.host.Relay()
}

func (l *Link) gatewayID() string {
	if id := l.GatewayIdentity(); id != nil {
		return id.NodeID()
	}
	return ""
}

func (l *Link) uplinks() bool   { return l.opt.Mode != ModeMapOnly }
func (l *Link) downlinks() bool { return l.opt.Mode == ModeGateway || l.opt.Mode == ModeBridge }

// selected reports whether a channel is carried in one direction under the selection policy.
func (l *Link) selected(ch mesh.ChannelRef, uplink bool) bool {
	listed, flag := slices.Contains(l.opt.DownlinkChannels, ch.Name), ch.Downlink
	if uplink {
		listed, flag = slices.Contains(l.opt.UplinkChannels, ch.Name), ch.Uplink
	}
	switch l.opt.ChannelSelection {
	case SelectOverride:
		return listed
	case SelectCombine:
		return listed || flag
	}
	return flag
}

// Run connects, keeps subscriptions in step with the channel selection and sends map reports
// until ctx ends.
func (l *Link) Run(ctx context.Context) error {
	scheme := "tcp"
	if l.opt.TLS {
		scheme = "ssl"
	}
	gw := strings.TrimPrefix(l.gatewayID(), "!")
	opts := paho.NewClientOptions().
		AddBroker(scheme + "://" + l.opt.Address).
		SetClientID(fmt.Sprintf("repeatertastic-%s-%s-%s", l.host.RadioID(), gw, l.opt.Name)).
		SetUsername(l.opt.Username).
		SetPassword(l.opt.Password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10 * time.Second).
		SetMaxReconnectInterval(5 * time.Minute).
		SetKeepAlive(60 * time.Second).
		SetOnConnectHandler(func(paho.Client) {
			l.connected.Store(true)
			l.subMu.Lock()
			l.subscribed = map[string]bool{} // a clean session drops them all
			l.subMu.Unlock()
			l.log.Info("MQTT connected", "broker", l.opt.Address, "root", l.Root(), "gateway", l.gatewayID(), "mode", l.opt.Mode)
			l.syncSubscriptions()
		}).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			l.connected.Store(false)
			l.log.Warn("MQTT connection lost", "err", err)
		})
	if l.opt.TLS {
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	l.client = paho.NewClient(opts)
	l.client.Connect() // retries in the background (SetConnectRetry)
	l.host.AddLink(l)

	subTick := time.NewTicker(30 * time.Second)
	defer subTick.Stop()
	var mapTick <-chan time.Time
	if l.opt.MapReport {
		t := time.NewTicker(l.opt.MapInterval)
		defer t.Stop()
		mapTick = t.C
		go func() { // first report shortly after connecting
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
				l.publishMapReport()
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			l.client.Disconnect(250)
			l.connected.Store(false)
			return ctx.Err()
		case <-subTick.C:
			l.syncSubscriptions()
		case <-mapTick:
			l.publishMapReport()
		}
	}
}

// ChannelPacketHeard uplinks a packet received on a channel we hold (mesh.ChannelLink): heard
// on air, or injected by another connection when both allow cross_link.
func (l *Link) ChannelPacketHeard(p *pb.MeshPacket, ch mesh.ChannelRef, data *pb.Data) {
	if !l.uplinks() || !l.selected(ch, true) {
		return
	}
	if p.ViaMqtt {
		hop, ok := l.opt.Origins.crossLink(p.From, p.Id, l.opt.Name)
		if !ok || !l.opt.CrossLink {
			return
		}
		p = proto.Clone(p).(*pb.MeshPacket)
		p.ViaMqtt, p.HopLimit, p.TransportMechanism = false, hop, pb.MeshPacket_TRANSPORT_LORA
	}
	if !ch.OKToMQTT && !l.opt.IgnoreConsent {
		return
	}
	l.publish(p, ch, data)
}

// SendPacket uplinks our own identities' channel packets after they go on air (mesh.Link).
// Relayed packets were already uplinked when they were heard.
func (l *Link) SendPacket(p *pb.MeshPacket) { l.SendPacketPlain(p, nil) }

// SendPacketPlain is SendPacket with the decoded payload, for JSON (mesh.PlainLink).
func (l *Link) SendPacketPlain(p *pb.MeshPacket, data *pb.Data) {
	if !l.uplinks() || p.GetEncrypted() == nil || p.PkiEncrypted || l.host.Identity(p.From) == nil {
		return
	}
	for _, ch := range l.host.ChannelsByHash(uint8(p.Channel)) {
		if l.selected(ch, true) {
			l.publish(p, ch, data)
			return
		}
	}
}

func (l *Link) publish(p *pb.MeshPacket, ch mesh.ChannelRef, data *pb.Data) {
	if !l.Connected() || ch.Name == "" {
		return
	}
	if !l.opt.Uplinked.first(l.opt.Address+"|"+l.Root(), p.From, p.Id) {
		return // another radio of this site already published it here
	}
	gw := l.gatewayID()
	sent := false
	if l.opt.Format != FormatJSON && p.GetEncrypted() != nil {
		if !l.up.take() {
			l.Dropped.Add(1)
			return
		}
		sent = true
		cp := proto.Clone(p).(*pb.MeshPacket)
		cp.RxSnr, cp.RxRssi, cp.RxTime = 0, nil, nil
		if b, err := proto.Marshal(&pb.ServiceEnvelope{Packet: cp, ChannelId: ch.Name, GatewayId: gw}); err == nil {
			l.client.Publish(l.Root()+"/2/e/"+ch.Name+"/"+gw, 0, false, b)
			l.Tx.Add(1)
		}
	}
	// JSON is plaintext: only for channels anyone could read, unless this is a bridge.
	if l.opt.Format != FormatEncrypted && data != nil && (ch.PublicKey || l.opt.Mode == ModeBridge) {
		b := jsonPacket(p, data, gw)
		if b == nil {
			return
		}
		if !sent && !l.up.take() {
			l.Dropped.Add(1)
			return
		}
		l.client.Publish(l.Root()+"/2/json/"+ch.Name+"/"+gw, 0, false, b)
		l.Tx.Add(1)
	}
}

// syncSubscriptions subscribes to every downlink channel and drops the rest.
func (l *Link) syncSubscriptions() {
	if l.client == nil || !l.Connected() {
		return
	}
	want := map[string]bool{}
	if l.downlinks() {
		for _, ch := range l.host.Channels() {
			if l.selected(ch, false) && ch.Name != "" && !strings.ContainsAny(ch.Name, "+#/") {
				want[ch.Name] = true
			}
		}
	}
	l.subMu.Lock()
	defer l.subMu.Unlock()
	for ch := range want {
		if l.subscribed[ch] {
			continue
		}
		topic := l.Root() + "/2/e/" + ch + "/+"
		name := ch
		tok := l.client.Subscribe(topic, 0, func(_ paho.Client, m paho.Message) { l.onMessage(name, m.Payload()) })
		if tok.WaitTimeout(10*time.Second) && tok.Error() == nil {
			l.subscribed[ch] = true
			l.log.Info("MQTT downlink subscribed", "topic", topic)
		}
	}
	for ch := range l.subscribed {
		if !want[ch] {
			l.client.Unsubscribe(l.Root() + "/2/e/" + ch + "/+")
			delete(l.subscribed, ch)
			l.log.Info("MQTT downlink unsubscribed", "channel", ch)
		}
	}
}

func (l *Link) onMessage(channel string, payload []byte) {
	env := &pb.ServiceEnvelope{}
	if proto.Unmarshal(payload, env) != nil || env.Packet == nil {
		l.Dropped.Add(1)
		return
	}
	p := env.Packet
	if l.ours(env.GetGatewayId()) || l.host.SiteIdentity(p.From) {
		return // our own uplink (from any of our connections) coming back
	}
	// Like the firmware, only encrypted packets from a real node are taken from a broker.
	if p.GetEncrypted() == nil || p.From == 0 || env.GetChannelId() != channel {
		l.Dropped.Add(1)
		return
	}
	if !l.down.take() {
		l.Dropped.Add(1)
		return
	}
	l.opt.Origins.record(p.From, p.Id, l.opt.Name, p.HopLimit)
	p.RxSnr, p.RxRssi, p.RxTime = 0, nil, nil
	p.ViaMqtt = true
	p.TransportMechanism = pb.MeshPacket_TRANSPORT_MQTT
	// The relay persona decrements the hop limit and never relays at zero: 0 keeps the packet
	// off air, relay_hops+1 lets it travel that many hops after the rebroadcast.
	if !l.opt.RelayMQTT {
		p.HopLimit = 0
	} else if max := uint32(l.opt.RelayHops) + 1; p.HopLimit > max {
		p.HopLimit = max
	}
	l.Rx.Add(1)
	l.host.HandleReceived(p, nil)
}

// ours reports whether a gateway id is one of this radio's identities.
func (l *Link) ours(gatewayID string) bool {
	n, err := strconv.ParseUint(strings.TrimPrefix(gatewayID, "!"), 16, 32)
	return err == nil && l.host.SiteIdentity(uint32(n)) // any radio of this site, not just this one
}

func (l *Link) publishMapReport() {
	gw := l.GatewayIdentity()
	if gw == nil || !l.Connected() {
		return
	}
	u := gw.UserCopy()
	rp := l.host.RadioParams()
	region := pb.Config_LoRaConfig_RegionCode(pb.Config_LoRaConfig_RegionCode_value[rp.Region.Name])
	online := 0
	for _, id := range l.host.Identities() {
		if id.Enabled {
			online++
		}
	}
	lat, lon, alt := l.opt.Latitude, l.opt.Longitude, l.opt.Altitude
	if lat == 0 && lon == 0 { // no map-report position of its own: use the site position
		pos := l.host.Config().Position
		lat, lon, alt = pos.Latitude, pos.Longitude, int(pos.Altitude)
	}
	report := &pb.MapReport{
		LongName: u.LongName, ShortName: u.ShortName, Role: u.Role, HwModel: l.host.Hardware(),
		FirmwareVersion: l.opt.FirmwareVersion, Region: region, ModemPreset: rp.Preset,
		HasDefaultChannel: l.host.Config().PrimaryChannel == "",
		LatitudeI:         truncate(int32(math.Round(lat*1e7)), l.opt.PositionPrecision),
		LongitudeI:        truncate(int32(math.Round(lon*1e7)), l.opt.PositionPrecision),
		Altitude:          int32(alt), PositionPrecision: uint32(l.opt.PositionPrecision),
		NumOnlineLocalNodes: uint32(online), HasOptedReportLocation: true,
	}
	payload, err := proto.Marshal(report)
	if err != nil {
		return
	}
	var id [4]byte
	_, _ = rand.Read(id[:])
	pkt := &pb.MeshPacket{From: gw.NodeNum, To: wire.Broadcast, Id: binary.LittleEndian.Uint32(id[:]),
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_MAP_REPORT_APP, Payload: payload}}}
	b, err := proto.Marshal(&pb.ServiceEnvelope{Packet: pkt, ChannelId: rp.PresetName(), GatewayId: gw.NodeID()})
	if err != nil {
		return
	}
	l.client.Publish(l.Root()+"/2/map/", 0, false, b)
	l.Tx.Add(1)
	l.log.Info("MQTT map report published", "gateway", gw.NodeID(), "precision_bits", l.opt.PositionPrecision)
}

// truncate keeps the top `bits` bits of a coordinate and centres it in the dropped range,
// as the firmware does for reduced-precision positions.
func truncate(v int32, bits int) int32 {
	if bits >= 32 {
		return v
	}
	mask := uint32(math.MaxUint32) << (32 - bits)
	return int32(uint32(v)&mask + (1 << (31 - bits)))
}

// bucket is a token bucket refilled at perMinute.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	tokens float64
	last   time.Time
}

func newBucket(perMinute int) bucket {
	return bucket{rate: float64(perMinute), tokens: float64(perMinute), last: time.Now()}
}

func (b *bucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = math.Min(b.rate, b.tokens+now.Sub(b.last).Minutes()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Origins remembers which connection each broker packet arrived on, so a radio's connections
// only pass packets between them when both allow it.
type Origins struct {
	mu    sync.Mutex
	cross map[string]bool // connection name → cross_link
	seen  map[uint64]origin
}

type origin struct {
	link     string
	hopLimit uint32
	at       time.Time
}

const originTTL = 10 * time.Minute

func NewOrigins() *Origins {
	return &Origins{cross: map[string]bool{}, seen: map[uint64]origin{}}
}

func (o *Origins) register(name string, crossLink bool) {
	o.mu.Lock()
	o.cross[name] = crossLink
	o.mu.Unlock()
}

func (o *Origins) record(from, id uint32, link string, hopLimit uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if len(o.seen) > 4096 {
		for k, v := range o.seen {
			if now.Sub(v.at) > originTTL {
				delete(o.seen, k)
			}
		}
	}
	o.seen[uint64(from)<<32|uint64(id)] = origin{link: link, hopLimit: hopLimit, at: now}
}

// crossLink reports whether a broker packet may go out on connection to: it came from another
// connection of this radio that allows cross_link. It also returns the packet's original hop limit.
func (o *Origins) crossLink(from, id uint32, to string) (uint32, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.seen[uint64(from)<<32|uint64(id)]
	if !ok || v.link == to || time.Since(v.at) > originTTL || !o.cross[v.link] {
		return 0, false
	}
	return v.hopLimit, true
}

// Uplinked remembers packets published per broker and root across a site's radios.
type Uplinked struct {
	mu   sync.Mutex
	seen map[upKey]time.Time
}

type upKey struct {
	dest     string
	from, id uint32
}

func NewUplinked() *Uplinked { return &Uplinked{seen: map[upKey]time.Time{}} }

// first reports whether this is the first publish of a packet to dest (nil: always).
func (u *Uplinked) first(dest string, from, id uint32) bool {
	if u == nil || id == 0 {
		return true
	}
	now := time.Now()
	k := upKey{dest, from, id}
	u.mu.Lock()
	defer u.mu.Unlock()
	if at, ok := u.seen[k]; ok && now.Sub(at) < originTTL {
		return false
	}
	u.seen[k] = now
	if len(u.seen) > 8192 {
		for key, at := range u.seen {
			if now.Sub(at) > originTTL {
				delete(u.seen, key)
			}
		}
	}
	return true
}
