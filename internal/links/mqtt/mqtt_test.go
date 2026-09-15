package mqtt

import (
	"context"
	"os"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
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

func TestDownlinkRateLimit(t *testing.T) {
	l := New(nil, Options{DownlinkPerMinute: 3}, nil)
	n := 0
	for i := 0; i < 10; i++ {
		if l.allow() {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("allowed %d of 10 at 3/min, want 3", n)
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
