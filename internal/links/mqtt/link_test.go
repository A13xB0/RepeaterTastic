package mqtt

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// ------------------------------------------------------------------------------ fake client

// doneToken is an already-completed paho token.
type doneToken struct{ err error }

func (doneToken) Wait() bool                     { return true }
func (doneToken) WaitTimeout(time.Duration) bool { return true }
func (doneToken) Done() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}
func (t doneToken) Error() error { return t.err }

type published struct {
	topic   string
	payload []byte
}

// fakeClient records what a Link publishes and subscribes to. Methods the link doesn't use are
// left to the embedded (nil) interface.
type fakeClient struct {
	paho.Client

	mu       sync.Mutex
	pubs     []published
	subs     map[string]paho.MessageHandler
	unsubs   []string
	subError error
}

func newFakeClient() *fakeClient { return &fakeClient{subs: map[string]paho.MessageHandler{}} }

func (c *fakeClient) Publish(topic string, _ byte, _ bool, payload any) paho.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pubs = append(c.pubs, published{topic, payload.([]byte)})
	return doneToken{}
}

func (c *fakeClient) Subscribe(topic string, _ byte, h paho.MessageHandler) paho.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subError == nil {
		c.subs[topic] = h
	}
	return doneToken{c.subError}
}

func (c *fakeClient) Unsubscribe(topics ...string) paho.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range topics {
		delete(c.subs, t)
	}
	c.unsubs = append(c.unsubs, topics...)
	return doneToken{}
}

// take returns and clears the recorded publishes.
func (c *fakeClient) take() []published {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.pubs
	c.pubs = nil
	return out
}

func (c *fakeClient) topics() []string {
	var out []string
	for _, p := range c.take() {
		out = append(out, p.topic)
	}
	return out
}

// connectedLink makes a link on h with a fake client that counts as connected.
func connectedLink(h *mesh.Host, opt Options) (*Link, *fakeClient) {
	l := New(h, opt, nil)
	c := newFakeClient()
	l.client = c
	l.connected.Store(true)
	return l, c
}

// fakeMessage is a received broker message.
type fakeMessage struct {
	paho.Message
	payload []byte
}

func (m fakeMessage) Payload() []byte { return m.payload }

// longFast is the default channel as the host reports it.
func longFast(h *mesh.Host, t *testing.T) mesh.ChannelRef {
	t.Helper()
	for _, ch := range h.Channels() {
		if ch.Name == "LongFast" {
			return ch
		}
	}
	t.Fatal("host has no LongFast channel")
	return mesh.ChannelRef{}
}

func textData(s string) *pb.Data {
	return &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(s)}
}

// ------------------------------------------------------------------------------------ tests

func TestAccessors(t *testing.T) {
	h, _, _, _ := newTestHost(t, nil)
	l := New(h, Options{Name: "pub", Address: "broker:1883", Mode: ModeUplinkOnly}, nil)
	if l.Name() != "mqtt:pub" || l.Connection() != "pub" || l.Mode() != ModeUplinkOnly || l.Broker() != "broker:1883" {
		t.Fatalf("name=%s connection=%s mode=%s broker=%s", l.Name(), l.Connection(), l.Mode(), l.Broker())
	}
	if l.Connected() || l.Root() != "msh/EU_868" {
		t.Fatalf("connected=%v root=%s", l.Connected(), l.Root())
	}
	if r := New(h, Options{Root: "custom/root/"}, nil).Root(); r != "custom/root" {
		t.Fatalf("root = %s", r)
	}
	if New(h, Options{MapInterval: time.Minute}, nil).opt.MapInterval != 15*time.Minute {
		t.Fatal("map interval below 15 minutes was kept")
	}
}

func TestGatewayIdentity(t *testing.T) {
	h, relay, desk, _ := newTestHost(t, nil)
	for _, c := range []struct {
		gateway string
		want    *mesh.Identity
	}{
		{"", relay},
		{"relay", relay},
		{desk.NodeID(), desk},
		{strings.TrimPrefix(desk.NodeID(), "!"), desk},
		{"!00000001", relay}, // no such identity
		{"not hex", relay},
	} {
		l := New(h, Options{Gateway: c.gateway}, nil)
		if got := l.GatewayIdentity(); got != c.want {
			t.Errorf("gateway %q: got %v, want %s", c.gateway, got, c.want.NodeID())
		}
		if l.gatewayID() != c.want.NodeID() {
			t.Errorf("gateway %q: id %s", c.gateway, l.gatewayID())
		}
	}

	bare, err := mesh.NewHost(mesh.Config{Region: "EU_868"}, null.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id := New(bare, Options{}, nil).gatewayID(); id != "" {
		t.Fatalf("host without identities has gateway %q", id)
	}
}

func TestUplinkChannels(t *testing.T) {
	h, _, _, _ := newTestHost(t, func(desk *mesh.Identity) { desk.Channels[0].Settings.UplinkEnabled = true })
	if got := New(h, Options{}, nil).UplinkChannels(); !slices.Equal(got, []string{"LongFast"}) {
		t.Fatalf("identity selection uplinks %v", got)
	}
	if got := New(h, Options{Mode: ModeMapOnly}, nil).UplinkChannels(); len(got) != 0 {
		t.Fatalf("map_only uplinks %v", got)
	}
	if got := New(h, Options{UplinkChannels: []string{"Other"}}, nil).UplinkChannels(); len(got) != 0 {
		t.Fatalf("override to an unknown channel uplinks %v", got)
	}
}

func TestChannelPacketHeardConsent(t *testing.T) {
	h, _, _, _ := newTestHost(t, nil)
	ch := longFast(h, t)
	ch.Uplink = true
	for _, c := range []struct {
		name    string
		opt     Options
		consent bool
		want    int
	}{
		{"gateway with consent", Options{}, true, 1},
		{"gateway without consent", Options{}, false, 0},
		{"gateway can't ignore consent", Options{IgnoreConsent: true}, false, 0},
		{"bridge ignoring consent", Options{Mode: ModeBridge, IgnoreConsent: true}, false, 1},
		{"map_only", Options{Mode: ModeMapOnly}, true, 0},
		{"channel not selected", Options{UplinkChannels: []string{"Other"}}, true, 0},
	} {
		l, fc := connectedLink(h, c.opt)
		ref := ch
		ref.OKToMQTT = c.consent
		l.ChannelPacketHeard(longFastPacket(0x11223344, 1, "hi", c.consent), ref, textData("hi"))
		if got := len(fc.take()); got != c.want {
			t.Errorf("%s: %d publishes, want %d", c.name, got, c.want)
		}
	}
}

func TestPublishEnvelope(t *testing.T) {
	h, relay, _, _ := newTestHost(t, nil)
	ch := longFast(h, t)
	ch.Uplink, ch.OKToMQTT = true, true
	l, fc := connectedLink(h, Options{Root: "msh/T"})
	p := longFastPacket(0x11223344, 5, "hi", true)
	rssi := int32(-90)
	p.RxSnr, p.RxRssi = 5.5, &rssi
	l.ChannelPacketHeard(p, ch, textData("hi"))

	pubs := fc.take()
	if len(pubs) != 1 || pubs[0].topic != "msh/T/2/e/LongFast/"+relay.NodeID() {
		t.Fatalf("publishes %v", pubs)
	}
	env := &pb.ServiceEnvelope{}
	if err := proto.Unmarshal(pubs[0].payload, env); err != nil {
		t.Fatal(err)
	}
	if env.ChannelId != "LongFast" || env.GatewayId != relay.NodeID() || env.Packet.Id != 5 ||
		env.Packet.RxSnr != 0 || env.Packet.RxRssi != nil {
		t.Fatalf("envelope %v", env)
	}
	if p.RxRssi == nil {
		t.Fatal("publishing changed the caller's packet")
	}
	if l.Tx.Load() != 1 {
		t.Fatalf("Tx = %d", l.Tx.Load())
	}
}

func TestPublishJSONFormats(t *testing.T) {
	h, _, _, _ := newTestHost(t, nil)
	public := longFast(h, t)
	public.Uplink, public.OKToMQTT = true, true
	private := public
	private.PublicKey = false
	for _, c := range []struct {
		name string
		opt  Options
		ch   mesh.ChannelRef
		data *pb.Data
		want []string
	}{
		{"json on a public channel", Options{Format: FormatJSON}, public, textData("x"), []string{"/2/json/"}},
		{"json on a private channel", Options{Format: FormatJSON}, private, textData("x"), nil},
		{"bridge json on a private channel", Options{Mode: ModeBridge, Format: FormatJSON}, private, textData("x"), []string{"/2/json/"}},
		{"both", Options{Format: FormatBoth}, public, textData("x"), []string{"/2/e/", "/2/json/"}},
		{"both without payload", Options{Format: FormatBoth}, public, nil, []string{"/2/e/"}},
		{"json of an unserialised port", Options{Format: FormatJSON}, public, &pb.Data{Portnum: pb.PortNum_ROUTING_APP}, nil},
	} {
		l, fc := connectedLink(h, c.opt)
		l.ChannelPacketHeard(longFastPacket(0x11223344, 9, "x", true), c.ch, c.data)
		got := fc.topics()
		if len(got) != len(c.want) {
			t.Errorf("%s: published %v, want %v", c.name, got, c.want)
			continue
		}
		for i, w := range c.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: topic %s, want %s", c.name, got[i], w)
			}
		}
	}
}

func TestPublishSkips(t *testing.T) {
	h, _, _, _ := newTestHost(t, nil)
	ch := longFast(h, t)
	ch.Uplink, ch.OKToMQTT = true, true
	l, fc := connectedLink(h, Options{})

	unnamed := ch
	unnamed.Name = ""
	l.publish(longFastPacket(1, 1, "x", true), unnamed, nil)
	l.connected.Store(false)
	l.publish(longFastPacket(1, 2, "x", true), ch, nil)
	if got := fc.take(); len(got) != 0 {
		t.Fatalf("published %v while disconnected or without a channel name", got)
	}
}

func TestUplinkRateLimitAndSiteDedup(t *testing.T) {
	h, _, _, _ := newTestHost(t, nil)
	ch := longFast(h, t)
	ch.Uplink, ch.OKToMQTT = true, true
	up := NewUplinked()
	a, fa := connectedLink(h, Options{Name: "a", Address: "b:1", UplinkPerMinute: 1, Uplinked: up})
	b, fb := connectedLink(h, Options{Name: "b", Address: "b:1", Uplinked: up})

	a.ChannelPacketHeard(longFastPacket(1, 100, "x", true), ch, nil)
	b.ChannelPacketHeard(longFastPacket(1, 100, "x", true), ch, nil) // same broker and root: once only
	a.ChannelPacketHeard(longFastPacket(1, 101, "x", true), ch, nil) // over a's rate
	if len(fa.take()) != 1 || len(fb.take()) != 0 {
		t.Fatal("packet published twice to one broker and root")
	}
	if a.Dropped.Load() != 1 {
		t.Fatalf("a dropped %d, want 1", a.Dropped.Load())
	}

	j, fj := connectedLink(h, Options{Format: FormatJSON, UplinkPerMinute: 1})
	j.ChannelPacketHeard(longFastPacket(1, 200, "x", true), ch, textData("x"))
	j.ChannelPacketHeard(longFastPacket(1, 201, "x", true), ch, textData("x"))
	if len(fj.take()) != 1 || j.Dropped.Load() != 1 {
		t.Fatalf("json rate limit: dropped %d", j.Dropped.Load())
	}
}

func TestUplinkedFirst(t *testing.T) {
	var none *Uplinked
	if !none.first("d", 1, 1) {
		t.Fatal("nil Uplinked must allow everything")
	}
	u := NewUplinked()
	for range 2 {
		if !u.first("d", 1, 0) {
			t.Fatal("packets without an id must always go out")
		}
	}
	if !u.first("d", 1, 1) || u.first("d", 1, 1) || !u.first("e", 1, 1) {
		t.Fatal("dedup per destination broken")
	}
	old := time.Now().Add(-2 * originTTL)
	u.seen[upKey{"d", 2, 2}] = old
	if !u.first("d", 2, 2) {
		t.Fatal("expired entry blocked a publish")
	}
	for i := range uint32(8200) {
		u.seen[upKey{"old", i, i + 1}] = old
	}
	u.first("d", 3, 3)
	if len(u.seen) > 10 {
		t.Fatalf("%d entries left after pruning", len(u.seen))
	}
}

func TestOriginsPrune(t *testing.T) {
	o := NewOrigins()
	o.register("a", true)
	old := time.Now().Add(-2 * originTTL)
	for i := range uint64(4100) {
		o.seen[i] = origin{link: "a", at: old}
	}
	o.record(1, 1, "a", 2)
	if len(o.seen) != 1 {
		t.Fatalf("%d entries left after pruning", len(o.seen))
	}
	o.seen[uint64(1)<<32|2] = origin{link: "a", at: old}
	if _, ok := o.crossLink(1, 2, "b"); ok {
		t.Fatal("expired origin passed on")
	}
}

func TestCrossLinkUplink(t *testing.T) {
	h, _, _, _ := newTestHost(t, nil)
	ch := longFast(h, t)
	ch.Uplink, ch.OKToMQTT = true, true
	origins := NewOrigins()
	New(h, Options{Name: "src", CrossLink: true, Origins: origins}, nil)
	origins.record(0x11223344, 7, "src", 2)

	p := longFastPacket(0x11223344, 7, "x", true)
	p.ViaMqtt, p.HopLimit, p.TransportMechanism = true, 0, pb.MeshPacket_TRANSPORT_MQTT

	closed, fc := connectedLink(h, Options{Name: "closed", Origins: origins})
	closed.ChannelPacketHeard(p, ch, nil)
	if len(fc.take()) != 0 {
		t.Fatal("connection without cross_link passed a broker packet on")
	}

	open, fo := connectedLink(h, Options{Name: "open", CrossLink: true, Origins: origins})
	open.ChannelPacketHeard(p, ch, nil)
	pubs := fo.take()
	if len(pubs) != 1 {
		t.Fatalf("cross-linked publishes %v", pubs)
	}
	env := &pb.ServiceEnvelope{}
	_ = proto.Unmarshal(pubs[0].payload, env)
	if env.Packet.ViaMqtt || env.Packet.HopLimit != 2 || env.Packet.TransportMechanism != pb.MeshPacket_TRANSPORT_LORA {
		t.Fatalf("cross-linked packet %v", env.Packet)
	}
	if !p.ViaMqtt {
		t.Fatal("cross-linking changed the caller's packet")
	}

	unknown := proto.Clone(p).(*pb.MeshPacket)
	unknown.Id = 8
	open.ChannelPacketHeard(unknown, ch, nil)
	if len(fo.take()) != 0 {
		t.Fatal("broker packet of unknown origin passed on")
	}
}

func TestSendPacketPlain(t *testing.T) {
	h, _, desk, _ := newTestHost(t, func(desk *mesh.Identity) { desk.Channels[0].Settings.UplinkEnabled = true })
	own := func() *pb.MeshPacket { return longFastPacket(desk.NodeNum, 1, "x", true) }
	pki := own()
	pki.PkiEncrypted = true
	decoded := own()
	decoded.PayloadVariant = &pb.MeshPacket_Decoded{Decoded: textData("x")}
	otherHash := own()
	otherHash.Channel++
	for _, c := range []struct {
		name string
		opt  Options
		p    *pb.MeshPacket
		want int
	}{
		{"own channel packet", Options{}, own(), 1},
		{"someone else's packet", Options{}, longFastPacket(0x11223344, 1, "x", true), 0},
		{"PKI", Options{}, pki, 0},
		{"not encrypted", Options{}, decoded, 0},
		{"unknown channel hash", Options{}, otherHash, 0},
		{"channel not selected", Options{UplinkChannels: []string{"Other"}}, own(), 0},
		{"map_only", Options{Mode: ModeMapOnly}, own(), 0},
	} {
		l, fc := connectedLink(h, c.opt)
		l.SendPacket(c.p)
		if got := len(fc.take()); got != c.want {
			t.Errorf("%s: %d publishes, want %d", c.name, got, c.want)
		}
	}
}

// secondaryChannel gives desk a downlinked secondary channel with this name.
func secondaryChannel(name string) func(*mesh.Identity) {
	return func(desk *mesh.Identity) {
		desk.Channels[0].Settings.DownlinkEnabled = true
		desk.Channels[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY,
			Settings: &pb.ChannelSettings{Name: name, Psk: []byte{2}, DownlinkEnabled: true}}
	}
}

func TestSyncSubscriptions(t *testing.T) {
	h, _, desk, _ := newTestHost(t, secondaryChannel("bad/name"))
	l, fc := connectedLink(h, Options{Root: "msh/T"})

	l.syncSubscriptions()
	if got := l.Subscriptions(); !slices.Equal(got, []string{"LongFast"}) {
		t.Fatalf("subscriptions %v (a name with a topic separator must be left out)", got)
	}
	if _, ok := fc.subs["msh/T/2/e/LongFast/+"]; !ok {
		t.Fatalf("client subscriptions %v", fc.subs)
	}

	desk.Channels[0].Settings.DownlinkEnabled = false
	h.ChannelsChanged()
	l.syncSubscriptions()
	if len(l.Subscriptions()) != 0 || !slices.Equal(fc.unsubs, []string{"msh/T/2/e/LongFast/+"}) {
		t.Fatalf("after disabling downlink: subscriptions %v, unsubscribed %v", l.Subscriptions(), fc.unsubs)
	}

	mon, fm := connectedLink(h, Options{Mode: ModeMonitor, DownlinkChannels: []string{"LongFast"}})
	mon.syncSubscriptions()
	if len(fm.subs) != 0 {
		t.Fatal("monitor subscribed to a downlink")
	}
}

func TestSyncSubscriptionsFailures(t *testing.T) {
	h, _, _, _ := newTestHost(t, secondaryChannel("Second"))
	l, fc := connectedLink(h, Options{})
	fc.subError = errors.New("not authorised")
	l.syncSubscriptions()
	if len(l.Subscriptions()) != 0 {
		t.Fatalf("failed subscriptions recorded: %v", l.Subscriptions())
	}

	l.connected.Store(false)
	fc.subError = nil
	l.syncSubscriptions()
	if len(fc.subs) != 0 {
		t.Fatal("subscribed while disconnected")
	}
	New(h, Options{}, nil).syncSubscriptions() // no client yet: nothing to do
}

func TestSubscriptionDeliversToHost(t *testing.T) {
	h, _, _, tap := newTestHost(t, secondaryChannel("Second"))
	l, fc := connectedLink(h, Options{Root: "msh/T"})
	l.syncSubscriptions()
	handler := fc.subs["msh/T/2/e/LongFast/+"]
	if handler == nil {
		t.Fatal("no LongFast subscription")
	}
	b, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: longFastPacket(0x55667788, 1, "x", true), ChannelId: "LongFast", GatewayId: "!0badcafe"})
	handler(nil, fakeMessage{payload: b})
	if p := tap.next(time.Second); p == nil {
		t.Fatal("subscribed message not handed to the host")
	}
}

// envelope marshals a broker envelope for onMessage.
func envelope(p *pb.MeshPacket, channel, gateway string) []byte {
	b, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: p, ChannelId: channel, GatewayId: gateway})
	return b
}

func TestOnMessageRejects(t *testing.T) {
	h, relay, desk, tap := newTestHost(t, nil)
	decoded := longFastPacket(0x55667788, 1, "x", true)
	decoded.PayloadVariant = &pb.MeshPacket_Decoded{Decoded: textData("x")}
	for _, c := range []struct {
		name    string
		payload []byte
		dropped bool
	}{
		{"garbage", []byte{0xff, 0xff, 0xff}, true},
		{"no packet", envelope(nil, "LongFast", "!0badcafe"), true},
		{"our own gateway", envelope(longFastPacket(0x55667788, 1, "x", true), "LongFast", relay.NodeID()), false},
		{"our own identity", envelope(longFastPacket(desk.NodeNum, 1, "x", true), "LongFast", "!0badcafe"), false},
		{"not encrypted", envelope(decoded, "LongFast", "!0badcafe"), true},
		{"from node 0", envelope(longFastPacket(0, 1, "x", true), "LongFast", "!0badcafe"), true},
		{"wrong channel", envelope(longFastPacket(0x55667788, 1, "x", true), "Other", "!0badcafe"), true},
	} {
		l := New(h, Options{}, nil)
		l.onMessage("LongFast", c.payload)
		if got := l.Dropped.Load() == 1; got != c.dropped || l.Rx.Load() != 0 {
			t.Errorf("%s: dropped=%d rx=%d", c.name, l.Dropped.Load(), l.Rx.Load())
		}
	}
	if p := tap.next(0); p != nil {
		t.Fatalf("rejected message reached the host: %v", p)
	}
}

func TestOnMessageHopLimits(t *testing.T) {
	h, _, _, tap := newTestHost(t, nil)
	for i, c := range []struct {
		name string
		opt  Options
		want uint32
	}{
		{"no relay_mqtt keeps it off air", Options{}, 0},
		{"relay_mqtt on a gateway: one rebroadcast", Options{RelayMQTT: true}, 1},
		{"bridge with relay_hops", Options{Mode: ModeBridge, RelayMQTT: true, RelayHops: 1}, 2},
		{"bridge within relay_hops", Options{Mode: ModeBridge, RelayMQTT: true, RelayHops: 5}, 3},
	} {
		origins := NewOrigins()
		l := New(h, Options{Name: "x", Origins: origins, RelayMQTT: c.opt.RelayMQTT, RelayHops: c.opt.RelayHops, Mode: c.opt.Mode}, nil)
		id := uint32(100 + i)
		l.onMessage("LongFast", envelope(longFastPacket(0x55667788, id, "x", true), "LongFast", "!0badcafe"))
		p := tap.next(time.Second)
		if p == nil || p.HopLimit != c.want || !p.ViaMqtt || p.TransportMechanism != pb.MeshPacket_TRANSPORT_MQTT {
			t.Errorf("%s: got %v, want hop limit %d", c.name, p, c.want)
			continue
		}
		if _, ok := origins.seen[uint64(0x55667788)<<32|uint64(id)]; !ok || l.Rx.Load() != 1 {
			t.Errorf("%s: origin not recorded or rx=%d", c.name, l.Rx.Load())
		}
	}
}

func TestOnMessageRateLimit(t *testing.T) {
	h, _, _, tap := newTestHost(t, nil)
	l := New(h, Options{DownlinkPerMinute: 1}, nil)
	l.onMessage("LongFast", envelope(longFastPacket(0x55667788, 1, "x", true), "LongFast", "!0badcafe"))
	l.onMessage("LongFast", envelope(longFastPacket(0x55667788, 2, "x", true), "LongFast", "!0badcafe"))
	if l.Rx.Load() != 1 || l.Dropped.Load() != 1 {
		t.Fatalf("rx=%d dropped=%d, want 1 and 1", l.Rx.Load(), l.Dropped.Load())
	}
	if tap.next(time.Second) == nil || tap.next(0) != nil {
		t.Fatal("want exactly one packet handed to the host")
	}
}

// mapReport decodes a published map report.
func mapReport(t *testing.T, pub published) (*pb.ServiceEnvelope, *pb.MapReport) {
	t.Helper()
	env := &pb.ServiceEnvelope{}
	if err := proto.Unmarshal(pub.payload, env); err != nil {
		t.Fatal(err)
	}
	d := env.GetPacket().GetDecoded()
	if d.GetPortnum() != pb.PortNum_MAP_REPORT_APP {
		t.Fatalf("map report packet %v", env.Packet)
	}
	r := &pb.MapReport{}
	if err := proto.Unmarshal(d.Payload, r); err != nil {
		t.Fatal(err)
	}
	return env, r
}

func TestPublishMapReport(t *testing.T) {
	h, _, desk, _ := newTestHost(t, nil)
	desk.Enabled = true
	l, fc := connectedLink(h, Options{Root: "msh/T", Gateway: desk.NodeID(), FirmwareVersion: "2.7.0",
		Latitude: 56.2055731, Longitude: -3.1, Altitude: 120, PositionPrecision: 16})
	l.publishMapReport()
	pubs := fc.take()
	if len(pubs) != 1 || pubs[0].topic != "msh/T/2/map/" {
		t.Fatalf("publishes %v", pubs)
	}
	env, r := mapReport(t, pubs[0])
	if env.GatewayId != desk.NodeID() || env.ChannelId != "LongFast" || env.Packet.From != desk.NodeNum || env.Packet.To != wire.Broadcast {
		t.Fatalf("envelope %v", env)
	}
	if r.LongName != "Desk" || r.FirmwareVersion != "2.7.0" || r.Region != pb.Config_LoRaConfig_EU_868 ||
		!r.HasDefaultChannel || r.PositionPrecision != 16 || r.Altitude != 120 || r.NumOnlineLocalNodes == 0 {
		t.Fatalf("report %v", r)
	}
	if r.LatitudeI != truncate(562055731, 16) || r.LongitudeI != truncate(-31000000, 16) {
		t.Fatalf("position %d,%d not truncated to 16 bits", r.LatitudeI, r.LongitudeI)
	}
}

func TestPublishMapReportSitePosition(t *testing.T) {
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Position: mesh.FixedPosition{Latitude: 55.5, Longitude: -4.25, Altitude: 30}}, null.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	l, fc := connectedLink(h, Options{PositionPrecision: 32})
	l.publishMapReport()
	if len(fc.take()) != 0 {
		t.Fatal("map report published without a gateway identity")
	}

	relay, _ := mesh.NewIdentity(nil, "Relay", "RLY")
	relay.IsRelay = true
	if err := h.AddIdentity(relay); err != nil {
		t.Fatal(err)
	}
	l.connected.Store(false)
	l.publishMapReport()
	if len(fc.take()) != 0 {
		t.Fatal("map report published while disconnected")
	}
	l.connected.Store(true)
	l.publishMapReport()
	pubs := fc.take()
	if len(pubs) != 1 {
		t.Fatalf("publishes %v", pubs)
	}
	if _, r := mapReport(t, pubs[0]); r.LatitudeI != 555000000 || r.LongitudeI != -42500000 || r.Altitude != 30 {
		t.Fatalf("report position %d,%d alt %d; want the site's", r.LatitudeI, r.LongitudeI, r.Altitude)
	}
}

func TestRunConnectsAndStops(t *testing.T) {
	broker := startFakeBroker(t)
	h, _, _, _ := newTestHost(t, func(desk *mesh.Identity) { desk.Channels[0].Settings.DownlinkEnabled = true })
	l := New(h, Options{Address: broker.addr(), MapReport: true}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	if !waitUntil(5*time.Second, 10*time.Millisecond, func() bool { return l.Connected() && len(l.Subscriptions()) == 1 }) {
		t.Fatalf("connected=%v subscriptions=%v", l.Connected(), l.Subscriptions())
	}
	// A dropped connection is re-established (paho retries at once) and the downlink resubscribed.
	before := broker.connections()
	broker.dropAll()
	if !waitUntil(5*time.Second, 10*time.Millisecond, func() bool {
		return broker.connections() > before && l.Connected() && len(l.Subscriptions()) == 1
	}) {
		t.Fatalf("after a drop: connects %d, connected=%v subscriptions=%v", broker.connections(), l.Connected(), l.Subscriptions())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't stop")
	}
}

func TestRunTLSStops(t *testing.T) {
	h, _, _, _ := newTestHost(t, nil)
	l := New(h, Options{Address: "127.0.0.1:1", TLS: true}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := l.Run(ctx); !errors.Is(err, context.DeadlineExceeded) || l.Connected() {
		t.Fatalf("Run = %v, connected %v", err, l.Connected())
	}
}

func TestJSONPacketFields(t *testing.T) {
	rx, rssi := uint32(1700000000), int32(-101)
	p := &pb.MeshPacket{From: 1, To: 2, Id: 3, Channel: 8, HopStart: 1, HopLimit: 3, RxTime: &rx, RxSnr: 4.5, RxRssi: &rssi}
	var got map[string]any
	if err := json.Unmarshal(jsonPacket(p, textData("t"), "!gw"), &got); err != nil {
		t.Fatal(err)
	}
	if got["hops_away"] != 0.0 || got["timestamp"] != 1700000000.0 || got["snr"] != 4.5 || got["rssi"] != -101.0 || got["channel"] != 8.0 {
		t.Fatalf("json %v", got)
	}
}
