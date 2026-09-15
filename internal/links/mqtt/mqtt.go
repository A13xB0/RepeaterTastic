// Package mqtt is a Meshtastic MQTT gateway for one radio: the relay persona is the gateway
// node, and packets travel as ServiceEnvelope protobufs on <root>/2/e/<channel>/<!gateway>,
// the same topics and payloads the firmware uses.
//
// Uplink (mesh → broker): channel packets heard on air whose sender set OK_TO_MQTT, and
// our own identities' channel packets, for channels with uplink enabled. Direct messages
// (PKI) are never uplinked. Payloads stay encrypted with the channel key.
//
// Downlink (broker → mesh): encrypted packets on channels with downlink enabled, rate
// limited, marked via_mqtt and handed to the host like a received packet. The host
// delivers them to our identities; the relay persona only rebroadcasts them when
// relay_mqtt is set, so by default broker traffic never uses airtime.
//
// Map reports (optional): the relay persona's MapReport on <root>/2/map/, with its
// position truncated to position_precision bits.
package mqtt

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
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

// Options configures a link.
type Options struct {
	Address           string // host:port
	Username          string
	Password          string
	TLS               bool
	Root              string // "" = msh/<region>
	DownlinkPerMinute int    // 0 = 30
	FirmwareVersion   string // reported in map reports

	MapReport         bool
	MapInterval       time.Duration // 0 = 1h, minimum 15m
	PositionPrecision int           // bits kept, 0 = 14
	Latitude          float64
	Longitude         float64
	Altitude          int
}

// Link is one radio's broker connection.
type Link struct {
	host *mesh.Host
	opt  Options
	log  *slog.Logger

	client    paho.Client
	connected atomic.Bool

	Rx, Tx, Dropped atomic.Uint64

	subMu      sync.Mutex
	subscribed map[string]bool // channel names with an active downlink subscription

	rateMu sync.Mutex
	tokens float64
	last   time.Time
}

// New prepares a link; Run connects it.
func New(h *mesh.Host, opt Options, log *slog.Logger) *Link {
	if opt.DownlinkPerMinute <= 0 {
		opt.DownlinkPerMinute = 30
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
	if log == nil {
		log = slog.Default()
	}
	return &Link{host: h, opt: opt, log: log.With("link", "mqtt"), subscribed: map[string]bool{},
		tokens: float64(opt.DownlinkPerMinute), last: time.Now()}
}

func (l *Link) Name() string    { return "mqtt" }
func (l *Link) Connected() bool { return l.connected.Load() }

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
	return out
}

func (l *Link) gatewayID() string {
	if r := l.host.Relay(); r != nil {
		return r.NodeID()
	}
	return ""
}

// Run connects, keeps subscriptions in step with the channels' downlink flags and sends map
// reports until ctx ends.
func (l *Link) Run(ctx context.Context) error {
	scheme := "tcp"
	if l.opt.TLS {
		scheme = "ssl"
	}
	gw := strings.TrimPrefix(l.gatewayID(), "!")
	opts := paho.NewClientOptions().
		AddBroker(scheme + "://" + l.opt.Address).
		SetClientID(fmt.Sprintf("repeatertastic-%s-%s", l.host.RadioID(), gw)).
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
			l.log.Info("MQTT connected", "broker", l.opt.Address, "root", l.Root(), "gateway", l.gatewayID())
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

// ChannelPacketHeard uplinks a packet heard on air (mesh.ChannelLink).
func (l *Link) ChannelPacketHeard(p *pb.MeshPacket, ch mesh.ChannelRef) {
	if !ch.Uplink || !ch.OKToMQTT {
		return
	}
	l.publish(p, ch.Name)
}

// SendPacket uplinks our own identities' channel packets after they go on air (mesh.Link).
// Relayed packets were already uplinked when they were heard.
func (l *Link) SendPacket(p *pb.MeshPacket) {
	if p.GetEncrypted() == nil || p.PkiEncrypted || l.host.Identity(p.From) == nil {
		return
	}
	for _, ch := range l.host.ChannelsByHash(uint8(p.Channel)) {
		if ch.Uplink {
			l.publish(p, ch.Name)
			return
		}
	}
}

func (l *Link) publish(p *pb.MeshPacket, channel string) {
	if !l.Connected() || p.GetEncrypted() == nil || channel == "" {
		return
	}
	cp := proto.Clone(p).(*pb.MeshPacket)
	cp.RxSnr, cp.RxRssi, cp.RxTime = 0, nil, nil
	b, err := proto.Marshal(&pb.ServiceEnvelope{Packet: cp, ChannelId: channel, GatewayId: l.gatewayID()})
	if err != nil {
		return
	}
	topic := l.Root() + "/2/e/" + channel + "/" + l.gatewayID()
	l.client.Publish(topic, 0, false, b)
	l.Tx.Add(1)
}

// syncSubscriptions subscribes to every channel with downlink enabled and drops the rest.
func (l *Link) syncSubscriptions() {
	if l.client == nil || !l.Connected() {
		return
	}
	want := map[string]bool{}
	for _, ch := range l.host.Channels() {
		if ch.Downlink && ch.Name != "" && !strings.ContainsAny(ch.Name, "+#/") {
			want[ch.Name] = true
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
	if env.GetGatewayId() == l.gatewayID() {
		return // our own uplink coming back
	}
	p := env.Packet
	// Like the firmware, only encrypted packets from a real node are taken from a broker.
	if p.GetEncrypted() == nil || p.From == 0 || env.GetChannelId() != channel {
		l.Dropped.Add(1)
		return
	}
	if !l.allow() {
		l.Dropped.Add(1)
		return
	}
	p.RxSnr, p.RxRssi, p.RxTime = 0, nil, nil
	p.ViaMqtt = true
	p.TransportMechanism = pb.MeshPacket_TRANSPORT_MQTT
	l.Rx.Add(1)
	l.host.HandleReceived(p, nil)
}

// allow is a token bucket of DownlinkPerMinute packets.
func (l *Link) allow() bool {
	l.rateMu.Lock()
	defer l.rateMu.Unlock()
	now := time.Now()
	rate := float64(l.opt.DownlinkPerMinute)
	l.tokens = math.Min(rate, l.tokens+now.Sub(l.last).Minutes()*rate)
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

func (l *Link) publishMapReport() {
	relay := l.host.Relay()
	if relay == nil || !l.Connected() {
		return
	}
	u := relay.UserCopy()
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
	pkt := &pb.MeshPacket{From: relay.NodeNum, To: wire.Broadcast, Id: binary.LittleEndian.Uint32(id[:]),
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_MAP_REPORT_APP, Payload: payload}}}
	b, err := proto.Marshal(&pb.ServiceEnvelope{Packet: pkt, ChannelId: rp.PresetName(), GatewayId: relay.NodeID()})
	if err != nil {
		return
	}
	l.client.Publish(l.Root()+"/2/map/", 0, false, b)
	l.Tx.Add(1)
	l.log.Info("MQTT map report published", "precision_bits", l.opt.PositionPrecision)
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
