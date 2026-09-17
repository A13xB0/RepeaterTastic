package mqtt

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

func TestTruncateKeepsTopBits(t *testing.T) {
	lat := int32(562055731) // 56.2055731°
	got := truncate(lat, 14)
	if uint32(got)>>18 != uint32(lat)>>18 {
		t.Fatalf("top 14 bits changed: %d -> %d", lat, got)
	}
	if diff := got - lat; diff < -(1<<18) || diff > 1<<18 {
		t.Fatalf("truncated too far: %d -> %d", lat, got)
	}
	if truncate(lat, 32) != lat {
		t.Fatal("32 bits should keep the full position")
	}
}

func TestRateLimits(t *testing.T) {
	l := New(nil, Options{DownlinkPerMinute: 3, UplinkPerMinute: 5}, nil)
	count := func(b *bucket) (n int) {
		for i := 0; i < 10; i++ {
			if b.take() {
				n++
			}
		}
		return n
	}
	if n := count(&l.down); n != 3 {
		t.Fatalf("downlink allowed %d of 10 at 3/min, want 3", n)
	}
	if n := count(&l.up); n != 5 {
		t.Fatalf("uplink allowed %d of 10 at 5/min, want 5", n)
	}
}

func TestDefaultsKeepNonBridgesConservative(t *testing.T) {
	l := New(nil, Options{Mode: ModeGateway, IgnoreConsent: true, RelayHops: 3}, nil)
	if l.opt.IgnoreConsent || l.opt.RelayHops != 0 {
		t.Fatalf("gateway kept ignore_consent=%v relay_hops=%d", l.opt.IgnoreConsent, l.opt.RelayHops)
	}
	if b := New(nil, Options{Mode: ModeBridge, IgnoreConsent: true, RelayHops: 3}, nil); !b.opt.IgnoreConsent || b.opt.RelayHops != 3 {
		t.Fatal("bridge lost its settings")
	}
	if m := New(nil, Options{Mode: ModeMonitor}, nil); m.Format() != FormatJSON || m.downlinks() {
		t.Fatalf("monitor format=%s downlinks=%v", m.Format(), m.downlinks())
	}
	if New(nil, Options{UplinkChannels: []string{"LongFast"}}, nil).opt.ChannelSelection != SelectOverride {
		t.Fatal("channel lists without a policy should override")
	}
}

func TestChannelSelection(t *testing.T) {
	flagged := mesh.ChannelRef{Name: "LongFast", Uplink: true}
	listed := mesh.ChannelRef{Name: "Scotland"}
	for _, c := range []struct {
		policy          string
		flagged, listed bool
	}{
		{SelectIdentity, true, false},
		{SelectOverride, false, true},
		{SelectCombine, true, true},
	} {
		l := New(nil, Options{ChannelSelection: c.policy, UplinkChannels: []string{"Scotland"}}, nil)
		if got := l.selected(flagged, true); got != c.flagged {
			t.Errorf("%s: identity-flagged channel selected=%v", c.policy, got)
		}
		if got := l.selected(listed, true); got != c.listed {
			t.Errorf("%s: listed channel selected=%v", c.policy, got)
		}
		if l.selected(listed, false) {
			t.Errorf("%s: uplink list used for downlink", c.policy)
		}
	}
}

func TestOriginsCrossLink(t *testing.T) {
	o := NewOrigins()
	New(nil, Options{Name: "a", CrossLink: true, Origins: o}, nil)
	New(nil, Options{Name: "b", Origins: o}, nil)
	o.record(1, 10, "a", 3)
	o.record(1, 11, "b", 3)
	if hop, ok := o.crossLink(1, 10, "b"); !ok || hop != 3 {
		t.Fatalf("a → b = %d, %v; want 3, true", hop, ok)
	}
	if _, ok := o.crossLink(1, 10, "a"); ok {
		t.Fatal("a packet went back out on the connection it came from")
	}
	if _, ok := o.crossLink(1, 11, "a"); ok {
		t.Fatal("b does not allow cross_link but its packet was passed on")
	}
	if _, ok := o.crossLink(2, 10, "b"); ok {
		t.Fatal("unknown packet passed on")
	}
}

func TestJSONPacket(t *testing.T) {
	d := &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hello")}
	b := jsonPacket(&pb.MeshPacket{From: 0x11223344, To: wire.Broadcast, Id: 7, HopStart: 3, HopLimit: 1}, d, "!be77562b")
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "text" || got["sender"] != "!be77562b" || got["hops_away"] != 2.0 ||
		got["payload"].(map[string]any)["text"] != "hello" {
		t.Fatalf("json = %s", b)
	}
	if jsonPacket(&pb.MeshPacket{}, &pb.Data{Portnum: pb.PortNum_ROUTING_APP}, "!x") != nil {
		t.Fatal("routing packets should not be serialised")
	}
}

// longFastPacket builds an encrypted LongFast text packet from `from`, as a node would send it.
func longFastPacket(from, id uint32, text string, okToMQTT bool) *pb.MeshPacket {
	bf := uint32(0)
	if okToMQTT {
		bf = 1
	}
	plain, _ := proto.Marshal(&pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text), Bitfield: &bf})
	key := wire.ExpandPSK([]byte{1})
	return &pb.MeshPacket{From: from, To: wire.Broadcast, Id: id, Channel: uint32(wire.ChannelHash("LongFast", key, false)),
		HopLimit: 3, HopStart: 3, PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: wire.AESCTR(key, from, id, plain)}}
}

// testBroker is the broker named by RT_TEST_MQTT_BROKER, or an in-process fake broker without one.
func testBroker(t *testing.T) string {
	t.Helper()
	if broker := os.Getenv("RT_TEST_MQTT_BROKER"); broker != "" {
		return broker
	}
	return startFakeBroker(t).addr()
}

// linkTap stands in for the air bridge: it records the packets links bring in (mesh.LinkTap),
// which is how hosted nodes receive them.
type linkTap struct{ heard chan *pb.MeshPacket }

func newLinkTap() *linkTap { return &linkTap{heard: make(chan *pb.MeshPacket, 16)} }

func (*linkTap) Heard(radio.Frame)                          { /* air frames are not under test */ }
func (*linkTap) Transmitted([]byte, *pb.MeshPacket, uint32) { /* nor transmissions */ }
func (lt *linkTap) LinkHeard(p *pb.MeshPacket)              { lt.heard <- proto.Clone(p).(*pb.MeshPacket) }

// next returns the next packet a link brought in, or nil after timeout.
func (lt *linkTap) next(timeout time.Duration) *pb.MeshPacket {
	select {
	case p := <-lt.heard:
		return p
	case <-time.After(timeout):
		return nil
	}
}

// newTestHost makes a host with a relay persona and a "Desk" identity, which prepare (if set)
// can adjust before it's added, and an air tap recording link packets. It isn't started.
func newTestHost(t *testing.T, prepare func(desk *mesh.Identity)) (h *mesh.Host, relay, desk *mesh.Identity, tap *linkTap) {
	t.Helper()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", StateDir: t.TempDir()}, null.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	relay, _ = mesh.NewIdentity(nil, "Test Relay", "TRLY")
	relay.IsRelay = true
	desk, _ = mesh.NewIdentity(nil, "Desk", "DESK")
	for wire.LastByte(desk.NodeNum) == wire.LastByte(relay.NodeNum) { // the host needs distinct relay bytes
		desk, _ = mesh.NewIdentity(nil, "Desk", "DESK")
	}
	if prepare != nil {
		prepare(desk)
	}
	for _, id := range []*mesh.Identity{relay, desk} {
		if err := h.AddIdentity(id); err != nil {
			t.Fatal(err)
		}
	}
	tap = newLinkTap()
	h.AddAirTap(tap)
	return h, relay, desk, tap
}

// runTestHost is newTestHost, started until the test ends.
func runTestHost(t *testing.T, prepare func(desk *mesh.Identity)) (ctx context.Context, h *mesh.Host, relay, desk *mesh.Identity, tap *linkTap) {
	t.Helper()
	h, relay, desk, tap = newTestHost(t, prepare)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() { defer close(done); _ = h.Run(ctx) }()
	return ctx, h, relay, desk, tap
}

// runLink runs a connection until the test ends.
func runLink(t *testing.T, ctx context.Context, l *Link) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() { defer close(done); _ = l.Run(ctx) }()
}

// connectObserver connects an outside client, standing in for other gateways, until the test ends.
func connectObserver(t *testing.T, broker, clientID string) paho.Client {
	t.Helper()
	obs := paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + broker).SetClientID(clientID))
	if tok := obs.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer connect: %v", tok.Error())
	}
	t.Cleanup(func() { obs.Disconnect(100) })
	return obs
}

// waitUntil polls done every step until it's true (true) or timeout passes (false).
func waitUntil(timeout, step time.Duration, done func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(step)
	}
	return true
}

// Against a real broker: RT_TEST_MQTT_BROKER=127.0.0.1:1883 go test ./internal/links/mqtt
// (otherwise against the in-process fake broker).
//
// The host only logs and sniffs what it receives: a downlinked packet reaches the hosted nodes
// through the air bridge's LinkHeard, marked via MQTT and with hop limit 0 (no relay_mqtt), and
// the node DB records the sender as heard via MQTT.
func TestBrokerUplinkAndDownlink(t *testing.T) {
	broker := testBroker(t)
	ctx, h, relay, _, tap := runTestHost(t, func(desk *mesh.Identity) {
		desk.Channels[0].Settings.UplinkEnabled = true
		desk.Channels[0].Settings.DownlinkEnabled = true
	})
	link := New(h, Options{Address: broker, Root: "msh/TEST"}, nil)
	runLink(t, ctx, link)

	obs := connectObserver(t, broker, "rt-test-observer")
	up := make(chan *pb.ServiceEnvelope, 16)
	obs.Subscribe("msh/TEST/2/e/LongFast/"+relay.NodeID(), 0, func(_ paho.Client, m paho.Message) {
		env := &pb.ServiceEnvelope{}
		if proto.Unmarshal(m.Payload(), env) == nil {
			up <- env
		}
	}).WaitTimeout(5 * time.Second)

	if !waitUntil(15*time.Second, 20*time.Millisecond, func() bool { return link.Connected() && len(link.Subscriptions()) == 1 }) {
		t.Fatalf("link connected=%v subscriptions=%v", link.Connected(), link.Subscriptions())
	}

	// uplink: heard on air with OK_TO_MQTT → published; without it → not
	h.HandleReceived(longFastPacket(0x11223344, 1001, "no consent", false), []byte{0})
	h.HandleReceived(longFastPacket(0x11223344, 1002, "hello broker", true), []byte{0})
	checkUplinks(t, up, relay.NodeID())
	if p := tap.next(0); p != nil {
		t.Fatalf("an on-air packet was passed to the air bridge as a link packet: %v", p)
	}

	// downlink: another gateway's packet goes to the hosted nodes through the air bridge
	other := longFastPacket(0x55667788, 2001, "from the internet", true)
	b, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: other, ChannelId: "LongFast", GatewayId: "!0badcafe"})
	obs.Publish("msh/TEST/2/e/LongFast/!0badcafe", 0, false, b).WaitTimeout(5 * time.Second)
	p := tap.next(5 * time.Second)
	if p == nil {
		t.Fatal("downlinked packet never reached the air bridge")
	}
	checkDownlinked(t, p, 2001)
	if link.Rx.Load() != 1 {
		t.Fatalf("link Rx = %d, want 1", link.Rx.Load())
	}
	heardViaMQTT := func() bool { e, ok := h.DB.Get(0x55667788); return ok && e.ViaMQTT }
	if !waitUntil(5*time.Second, 10*time.Millisecond, heardViaMQTT) {
		t.Fatal("node DB doesn't have the sender as heard via MQTT")
	}
	if h.Counters.Relayed.Load() != 0 {
		t.Fatal("the host relayed a packet itself")
	}
}

// checkDownlinked checks a broker packet as handed to the hosted nodes: via MQTT, hop limit 0.
func checkDownlinked(t *testing.T, p *pb.MeshPacket, id uint32) {
	t.Helper()
	if p.Id != id || !p.ViaMqtt || p.HopLimit != 0 || p.TransportMechanism != pb.MeshPacket_TRANSPORT_MQTT || p.GetEncrypted() == nil {
		t.Fatalf("downlinked packet = %v", p)
	}
}

// checkUplinks watches the uplinked envelopes until 1002 arrives. Packet 1001, without
// OK_TO_MQTT, was handed over first and must never appear; 1002 must, from gateway.
func checkUplinks(t *testing.T, up <-chan *pb.ServiceEnvelope, gateway string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case env := <-up:
			switch env.GetPacket().GetId() {
			case 1001:
				t.Fatal("packet without OK_TO_MQTT was uplinked")
			case 1002:
				if env.GetChannelId() != "LongFast" || env.GetGatewayId() != gateway || env.GetPacket().RxRssi != nil {
					t.Fatalf("uplinked envelope = %v", env)
				}
				return
			}
		case <-timeout:
			t.Fatal("OK_TO_MQTT packet was not uplinked")
		}
	}
}

// Two connections on one radio: a gateway that downlinks, and a JSON monitor on another root.
// Broker packets reach the monitor only when both allow cross_link, and a gateway without
// relay_mqtt hands them to the hosted nodes with hop limit 0 so they stay off air.
func TestBrokerTwoConnections(t *testing.T) {
	broker := testBroker(t)
	ctx, h, _, desk, tap := runTestHost(t, nil)
	origins := NewOrigins()
	gw := New(h, Options{Name: "public", Address: broker, Root: "msh/TWO", DownlinkChannels: []string{"LongFast"},
		CrossLink: true, Origins: origins}, nil)
	mon := New(h, Options{Name: "logger", Address: broker, Root: "msh/MON", Mode: ModeMonitor, Gateway: desk.NodeID(),
		UplinkChannels: []string{"LongFast"}, CrossLink: true, Origins: origins}, nil)
	runLink(t, ctx, gw)
	runLink(t, ctx, mon)

	obs := connectObserver(t, broker, "rt-test-observer-2")
	seen := make(chan string, 16)
	obs.Subscribe("msh/MON/#", 0, func(_ paho.Client, m paho.Message) { seen <- m.Topic() + " " + string(m.Payload()) }).WaitTimeout(5 * time.Second)

	if !waitUntil(15*time.Second, 20*time.Millisecond, func() bool {
		return gw.Connected() && mon.Connected() && len(gw.Subscriptions()) == 1
	}) {
		t.Fatalf("gateway connected=%v subs=%v, monitor connected=%v", gw.Connected(), gw.Subscriptions(), mon.Connected())
	}
	if len(mon.Subscriptions()) != 0 {
		t.Fatal("monitor subscribed to a downlink")
	}

	b, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: longFastPacket(0x55667788, 3001, "cross", true), ChannelId: "LongFast", GatewayId: "!0badcafe"})
	obs.Publish("msh/TWO/2/e/LongFast/!0badcafe", 0, false, b).WaitTimeout(5 * time.Second)
	msg := waitForSeen(seen, `"text":"cross"`, time.Now().Add(5*time.Second))
	if want := "msh/MON/2/json/LongFast/" + desk.NodeID() + " "; !strings.HasPrefix(msg, want) {
		t.Fatalf("monitor published %q, want topic %q", msg, want)
	}
	if p := tap.next(5 * time.Second); p == nil {
		t.Fatal("broker packet never reached the air bridge")
	} else {
		checkDownlinked(t, p, 3001)
	}

	// the monitor also serialises packets heard on air
	h.HandleReceived(longFastPacket(0x11223344, 3002, "on air", true), []byte{0})
	if waitForSeen(seen, `"text":"on air"`, time.Now().Add(5*time.Second)) == "" {
		t.Fatal("on-air packet never reached the monitor as JSON")
	}
}

// waitForSeen returns the first message containing want that arrives on seen before deadline,
// or "" when none does.
func waitForSeen(seen <-chan string, want string, deadline time.Time) string {
	for {
		select {
		case msg := <-seen:
			if strings.Contains(msg, want) {
				return msg
			}
		case <-time.After(time.Until(deadline)):
			return ""
		}
	}
}
