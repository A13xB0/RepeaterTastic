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

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
	"github.com/A13xB0/RepeaterTastic/pb"
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

// Against a real broker: RT_TEST_MQTT_BROKER=127.0.0.1:1883 go test ./internal/links/mqtt
func TestBrokerUplinkAndDownlink(t *testing.T) {
	broker := os.Getenv("RT_TEST_MQTT_BROKER")
	if broker == "" {
		t.Skip("set RT_TEST_MQTT_BROKER=host:port to run against a broker")
	}
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", StateDir: t.TempDir(), IgnoreMQTT: true}, null.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	relay, _ := mesh.NewIdentity(nil, "Test Relay", "TRLY")
	relay.IsRelay = true
	desk, _ := mesh.NewIdentity(nil, "Desk", "DESK")
	desk.Channels[0].Settings.UplinkEnabled = true
	desk.Channels[0].Settings.DownlinkEnabled = true
	for _, id := range []*mesh.Identity{relay, desk} {
		if err := h.AddIdentity(id); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	link := New(h, Options{Address: broker, Root: "msh/TEST"}, nil)
	go func() { _ = link.Run(ctx) }()

	// an outside client standing in for other gateways
	obs := paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + broker).SetClientID("rt-test-observer"))
	if tok := obs.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer connect: %v", tok.Error())
	}
	defer obs.Disconnect(100)
	up := make(chan *pb.ServiceEnvelope, 4)
	obs.Subscribe("msh/TEST/2/e/LongFast/"+relay.NodeID(), 0, func(_ paho.Client, m paho.Message) {
		env := &pb.ServiceEnvelope{}
		if proto.Unmarshal(m.Payload(), env) == nil {
			up <- env
		}
	}).WaitTimeout(5 * time.Second)

	deadline := time.Now().Add(15 * time.Second)
	for !(link.Connected() && len(link.Subscriptions()) == 1) {
		if time.Now().After(deadline) {
			t.Fatalf("link connected=%v subscriptions=%v", link.Connected(), link.Subscriptions())
		}
		time.Sleep(100 * time.Millisecond)
	}

	// uplink: heard on air with OK_TO_MQTT → published; without it → not
	h.HandleReceived(longFastPacket(0x11223344, 1001, "no consent", false), nil)
	h.HandleReceived(longFastPacket(0x11223344, 1002, "hello broker", true), nil)
	// Our own identities' channel packets (e.g. the NodeInfo we send the unknown sender) go
	// up too; the packet without OK_TO_MQTT must never appear.
	sawConsented := false
	timeout := time.After(3 * time.Second)
collect:
	for {
		select {
		case env := <-up:
			switch env.GetPacket().GetId() {
			case 1001:
				t.Fatal("packet without OK_TO_MQTT was uplinked")
			case 1002:
				if env.GetChannelId() != "LongFast" || env.GetGatewayId() != relay.NodeID() {
					t.Fatalf("uplinked envelope = %v", env)
				}
				sawConsented = true
			}
		case <-timeout:
			break collect
		}
	}
	if !sawConsented {
		t.Fatal("OK_TO_MQTT packet was not uplinked")
	}

	// downlink: another gateway's packet reaches our identity, marked via MQTT
	time.Sleep(2 * time.Second) // let the relay finish rebroadcasting the on-air packets above
	relayedBefore := h.Counters.Relayed.Load()
	other := longFastPacket(0x55667788, 2001, "from the internet", true)
	b, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: other, ChannelId: "LongFast", GatewayId: "!0badcafe"})
	obs.Publish("msh/TEST/2/e/LongFast/!0badcafe", 0, false, b).WaitTimeout(5 * time.Second)
	deadline = time.Now().Add(5 * time.Second)
	for link.Rx.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("downlinked packet never arrived")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var got bool
	for time.Now().Before(deadline) && !got {
		for _, m := range h.Messages.Window(desk.NodeNum, 0) {
			if m.Text == "from the internet" {
				got = true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !got {
		t.Fatal("downlinked message not delivered to the identity as via_mqtt")
	}
	time.Sleep(2 * time.Second)
	if h.Counters.Relayed.Load() != relayedBefore {
		t.Fatal("relay persona rebroadcast an MQTT packet with IgnoreMQTT set")
	}
}

// Two connections on one radio: a gateway that downlinks, and a JSON monitor on another root.
// Broker packets reach the monitor only when both allow cross_link, and a gateway without
// relay_mqtt keeps them off air even when another connection allows relaying.
func TestBrokerTwoConnections(t *testing.T) {
	broker := os.Getenv("RT_TEST_MQTT_BROKER")
	if broker == "" {
		t.Skip("set RT_TEST_MQTT_BROKER=host:port to run against a broker")
	}
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", StateDir: t.TempDir(), IgnoreMQTT: false}, null.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	relay, _ := mesh.NewIdentity(nil, "Test Relay", "TRLY")
	relay.IsRelay = true
	desk, _ := mesh.NewIdentity(nil, "Desk", "DESK")
	for _, id := range []*mesh.Identity{relay, desk} {
		if err := h.AddIdentity(id); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	origins := NewOrigins()
	gw := New(h, Options{Name: "public", Address: broker, Root: "msh/TWO", DownlinkChannels: []string{"LongFast"},
		CrossLink: true, Origins: origins}, nil)
	mon := New(h, Options{Name: "logger", Address: broker, Root: "msh/MON", Mode: ModeMonitor, Gateway: desk.NodeID(),
		UplinkChannels: []string{"LongFast"}, CrossLink: true, Origins: origins}, nil)
	go func() { _ = gw.Run(ctx) }()
	go func() { _ = mon.Run(ctx) }()

	obs := paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + broker).SetClientID("rt-test-observer-2"))
	if tok := obs.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer connect: %v", tok.Error())
	}
	defer obs.Disconnect(100)
	seen := make(chan string, 16)
	obs.Subscribe("msh/MON/#", 0, func(_ paho.Client, m paho.Message) { seen <- m.Topic() + " " + string(m.Payload()) }).WaitTimeout(5 * time.Second)

	deadline := time.Now().Add(15 * time.Second)
	for !(gw.Connected() && mon.Connected() && len(gw.Subscriptions()) == 1) {
		if time.Now().After(deadline) {
			t.Fatalf("gateway connected=%v subs=%v, monitor connected=%v", gw.Connected(), gw.Subscriptions(), mon.Connected())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(mon.Subscriptions()) != 0 {
		t.Fatal("monitor subscribed to a downlink")
	}

	relayedBefore := h.Counters.Relayed.Load()
	b, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: longFastPacket(0x55667788, 3001, "cross", true), ChannelId: "LongFast", GatewayId: "!0badcafe"})
	obs.Publish("msh/TWO/2/e/LongFast/!0badcafe", 0, false, b).WaitTimeout(5 * time.Second)
	select {
	case msg := <-seen:
		want := "msh/MON/2/json/LongFast/" + desk.NodeID() + " "
		if !strings.HasPrefix(msg, want) || !strings.Contains(msg, `"text":"cross"`) {
			t.Fatalf("monitor published %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cross-linked packet never reached the monitor")
	}
	time.Sleep(2 * time.Second)
	if h.Counters.Relayed.Load() != relayedBefore {
		t.Fatal("a packet from a connection without relay_mqtt was rebroadcast on air")
	}

	// the monitor also serialises packets heard on air
	h.HandleReceived(longFastPacket(0x11223344, 3002, "on air", true), nil)
	deadline = time.Now().Add(5 * time.Second)
	for {
		select {
		case msg := <-seen:
			if strings.Contains(msg, `"text":"on air"`) {
				return
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("on-air packet never reached the monitor as JSON")
		}
	}
}
