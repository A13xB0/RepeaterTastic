package nodes

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const boardNum = 0x0b0a4d01

func startTestBoard(t *testing.T, fake *mtclienttest.Node) *BoardRadio {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := mtclient.New(mtclient.Options{Address: "board", Dial: fake.Dial, ReconnectInterval: 10 * time.Millisecond, RequestTimeout: 2 * time.Second})
	b, err := startBoard(ctx, c, "/dev/ttyACM9", t.TempDir(), testLogf(t))
	if err != nil {
		t.Fatal(err)
	}
	b.node.rebootWait = 100 * time.Millisecond
	t.Cleanup(func() { b.Close() })
	wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
	defer wcancel()
	if err := c.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBoardIsTheRelayAndCarriesOthers(t *testing.T) {
	fake := mtclienttest.New(boardNum)
	fake.Update(func(s *mtclienttest.State) { s.Config.Lora.IgnoreMqtt = true })
	b := startTestBoard(t, fake)

	// The board is the relay; its settings and its MQTT proxy are written in one go.
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, RelayRole: mesh.RoleRouter, HopLimit: 3,
		PrimaryChannel: "Scot", IgnoreMQTT: true}, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := b.Node().Identity(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	id.IsRelay = true
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	h.AddConfigApplier(b.Node())
	b.Node().Bind(h, id)
	if err := h.PushConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "board settings", func() bool {
		c, m, chs := fake.Config(), fake.Modules().GetMqtt(), fake.Channels()
		ch0 := chs[0].GetSettings()
		return c.Device.Role == pb.Config_DeviceConfig_ROUTER && !c.Lora.IgnoreMqtt && m.GetEnabled() && m.GetProxyToClientEnabled() &&
			m.GetEncryptionEnabled() && m.GetAddress() == "127.0.0.1" && ch0.GetUplinkEnabled() && ch0.GetDownlinkEnabled() &&
			ch0.GetName() == "Scot" && string(ch0.GetPsk()) == "\x01"
	})
	if info := b.Info(); info.Driver != BoardDriver || info.Firmware != "Meshtastic 2.8.0.fake" {
		t.Fatalf("info %+v", info)
	}
	eventually(t, "mirror after the edit", func() bool { return b.node.client.Snapshot().Channels[0].GetSettings().GetName() == "Scot" })

	// A frame from another node goes to the board as a downlink on the channel with its hash.
	key := wire.ExpandPSK([]byte{1})
	hash := wire.ChannelHash("Scot", key, false)
	data, _ := proto.Marshal(&pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("behind the board")})
	pkt := &pb.MeshPacket{From: 0x1234abcd, To: wire.Broadcast, Id: 77, HopLimit: 4, HopStart: 4, Channel: uint32(hash),
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: wire.AESCTR(key, 0x1234abcd, 77, data)}}
	frame, err := wire.EncodeFrame(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	dm := &pb.MeshPacket{From: 0x1234abcd, To: 0x55667788, Id: 78, HopLimit: 4, HopStart: 4,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte("pki ciphertext and tag here....")}}
	frame, _ = wire.EncodeFrame(dm)
	if err := b.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	var envs []*pb.ServiceEnvelope
	eventually(t, "downlinks", func() bool {
		envs = envs[:0]
		for _, tr := range fake.Received() {
			if m := tr.GetMqttClientProxyMessage(); m != nil {
				var e pb.ServiceEnvelope
				if proto.Unmarshal(m.GetData(), &e) == nil {
					envs = append(envs, &e)
				}
			}
		}
		return len(envs) == 2
	})
	if envs[0].ChannelId != "Scot" || envs[0].GatewayId != "!1234abcd" || envs[0].Packet.GetId() != 77 ||
		envs[0].Packet.GetHopLimit() != 4 || string(envs[0].Packet.GetEncrypted()) != string(pkt.GetEncrypted()) {
		t.Fatalf("channel downlink %v", envs[0])
	}
	if envs[1].ChannelId != "PKI" || envs[1].Packet.GetTo() != 0x55667788 {
		t.Fatalf("PKI downlink %v", envs[1])
	}

	// The board (a router here) repeats both: the sender hears that, a hop lower.
	echoes := map[uint32]*pb.MeshPacket{}
	for len(echoes) < 2 {
		select {
		case f := <-b.Frames():
			p := wire.DecodeFrame(f.Data, int32(f.RSSI), f.SNR)
			echoes[p.GetId()] = p
		case <-time.After(2 * time.Second):
			t.Fatalf("echoes of the board's repeat: %v", echoes)
		}
	}
	if e := echoes[77]; e == nil || e.HopLimit != 3 || e.RelayNode != boardNum&0xff || echoes[78] == nil {
		t.Fatalf("echoes %v", echoes)
	}

	// What the board hears comes back as a received frame with its RSSI and SNR.
	heard := proto.Clone(pkt).(*pb.MeshPacket)
	heard.From, heard.Id, heard.HopLimit, heard.RxRssi, heard.RxSnr = 0x0badf00d, 99, 2, proto.Int32(-97), -3.5
	env, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: heard, ChannelId: "Scot", GatewayId: "!0b0a4d01"})
	fake.Push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_MqttClientProxyMessage{MqttClientProxyMessage: &pb.MqttClientProxyMessage{
		Topic: "msh/EU_868/2/e/Scot/!0b0a4d01", PayloadVariant: &pb.MqttClientProxyMessage_Data{Data: env}}}})
	select {
	case f := <-b.Frames():
		p := wire.DecodeFrame(f.Data, int32(f.RSSI), f.SNR)
		if p == nil || p.From != 0x0badf00d || p.Id != 99 || p.HopLimit != 2 || f.RSSI != -97 || f.SNR != -3.5 {
			t.Fatalf("heard %v %+v", p, f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame from the board's uplink")
	}
}

func TestHostedNodesBehindABoardGetAHopMore(t *testing.T) {
	fake := mtclienttest.New(hostedNum)
	r := startNode(t, fake, t.TempDir(), 2*time.Second)
	r.id.IsRelay = false
	r.n.SetHopsBehind(1)
	if err := r.n.ApplyConfig(context.Background(), r.h.Config()); err != nil {
		t.Fatal(err)
	}
	if hl := fake.Config().Lora.HopLimit; hl != 5 {
		t.Fatalf("hop limit %d, want the radio's 4 plus 1", hl)
	}
}

func TestBoardChannelsKeepTheProxy(t *testing.T) {
	b := &BoardRadio{node: newNode("x", "", nil, nil)}
	ch := &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "Ops"}}
	plain := proto.Clone(ch).(*pb.Channel)
	newNode("y", "", nil, nil).PrepareChannel(plain)
	if plain.Settings.UplinkEnabled {
		t.Fatal("a node that isn't a board gets its channels as they are")
	}
	b.node.SetExtraSettings(BoardSettings)
	b.node.PrepareChannel(ch)
	if !ch.Settings.UplinkEnabled || !ch.Settings.DownlinkEnabled {
		t.Fatalf("board channel %v", ch)
	}
}
