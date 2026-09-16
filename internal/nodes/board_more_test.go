package nodes

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

func TestOpenBoardOverTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fake := mtclienttest.New(boardNum)
	go fake.Serve(ln)
	b, err := OpenBoard(context.Background(), ln.Addr().String(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	eventually(t, "board connected", func() bool { return b.Stats(context.Background()).Connected })
	info := b.Info()
	if info.Name != "Meshtastic HELTEC V3" || info.Device != ln.Addr().String() || info.Driver != BoardDriver {
		t.Fatalf("info %+v", info)
	}
	if err := b.Configure(context.Background(), radio.Config{}); err != nil {
		t.Fatal(err)
	}
	if busy, err := b.ChannelBusy(context.Background()); busy || err != nil {
		t.Fatalf("busy %v, %v", busy, err)
	}
	if b.Node().Client() == nil {
		t.Fatal("no client")
	}
}

func TestOpenBoardBadAddress(t *testing.T) {
	if _, err := OpenBoard(context.Background(), "board:99999", "", nil); err == nil {
		t.Fatal("bad address accepted")
	}
}

func TestBoardInfoBeforeConnecting(t *testing.T) {
	c := mtclient.New(mtclient.Options{Address: "/dev/ttyACM0"}) // never started: the board is away
	b := &BoardRadio{node: newNode("/dev/ttyACM0", "", c, nil), device: "/dev/ttyACM0"}
	if info := b.Info(); info.Name != "Meshtastic node" || info.Firmware != "" {
		t.Fatalf("info %+v", info)
	}
	if st := b.Stats(context.Background()); st.Connected || st.TxPackets != 0 {
		t.Fatalf("stats %+v", st)
	}
	if err := b.Send(context.Background(), []byte{1, 2}); err == nil || !strings.Contains(err.Error(), "not a Meshtastic frame") {
		t.Fatalf("garbage frame: %v", err)
	}
	frame, _ := wire.EncodeFrame(&pb.MeshPacket{From: 1, To: wire.Broadcast, Channel: 8, PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1}}})
	if err := b.Send(context.Background(), frame); !errors.Is(err, mtclient.ErrNotConnected) {
		t.Fatalf("send while away: %v", err)
	}
	if b.Stats(context.Background()).Errors != 1 {
		t.Fatal("failed send not counted")
	}
}

func TestBoardSendUnknownChannel(t *testing.T) {
	b := startTestBoard(t, mtclienttest.New(boardNum))
	frame, _ := wire.EncodeFrame(&pb.MeshPacket{From: 1, To: wire.Broadcast, Channel: 200, HopLimit: 3,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1}}})
	if err := b.Send(context.Background(), frame); err == nil || !strings.Contains(err.Error(), "no channel with hash 200") {
		t.Fatalf("err = %v", err)
	}
}

// pkiFrame is a direct message frame to num.
func pkiFrame(t *testing.T, to, id uint32, hop uint32) []byte {
	t.Helper()
	f, err := wire.EncodeFrame(&pb.MeshPacket{From: 0x1234abcd, To: to, Id: id, HopLimit: hop, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte("pki ciphertext and tag here....")}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func addedContacts(fake *mtclienttest.Node) map[uint32]bool {
	out := map[uint32]bool{}
	for _, m := range fake.Admins() {
		if c := m.GetAddContact(); c != nil {
			out[c.NodeNum] = true
		}
	}
	return out
}

func TestBoardIntroducesDirectMessageTargets(t *testing.T) {
	const keyed, unknown, keyless, stranger = 0x0000aaaa, 0x0000bbbb, 0x0000cccc, 0x0000dddd
	fake := mtclienttest.New(boardNum)
	fake.Update(func(s *mtclienttest.State) {
		s.Others = []*pb.NodeInfo{{Num: keyed, User: &pb.User{PublicKey: make([]byte, 32)}}, {Num: keyless}}
	})
	b := startTestBoard(t, fake)
	// Without a contact source nothing is introduced.
	if err := b.Send(context.Background(), pkiFrame(t, unknown, 1, 3)); err != nil {
		t.Fatal(err)
	}
	b.SetContacts(func(num uint32) *pb.User {
		if num == stranger {
			return &pb.User{LongName: "short key", PublicKey: []byte{1}}
		}
		return &pb.User{LongName: "known here", PublicKey: make([]byte, 32)}
	})
	for i, to := range []uint32{keyed, unknown, keyless, stranger} {
		if err := b.Send(context.Background(), pkiFrame(t, to, uint32(10+i), 3)); err != nil {
			t.Fatal(err)
		}
	}
	got := addedContacts(fake)
	if !got[unknown] || !got[keyless] || got[keyed] || got[stranger] {
		t.Fatalf("contacts added %v", got)
	}
	// Every DM is echoed; take the echoes before the board closes.
	if echoes := collectEchoes(t, b, 5); echoes[1] == nil || echoes[13] == nil {
		t.Fatalf("echoes %v", echoes)
	}
}

func TestBoardDoesNotEchoWhenItWontRepeat(t *testing.T) {
	fake := mtclienttest.New(boardNum)
	fake.Update(func(s *mtclienttest.State) { s.Config.Device.Role = pb.Config_DeviceConfig_CLIENT_MUTE })
	b := startTestBoard(t, fake)
	if err := b.Send(context.Background(), pkiFrame(t, 0x0000aaaa, 1, 3)); err != nil {
		t.Fatal(err)
	}
	fake.Update(func(s *mtclienttest.State) { s.Config.Device.Role = pb.Config_DeviceConfig_ROUTER })
	b.node.client.Reconnect()
	eventually(t, "router again", func() bool {
		return b.node.client.Snapshot().Config.GetDevice().GetRole() == pb.Config_DeviceConfig_ROUTER
	})
	// No hops left: nothing to repeat. To the board itself: it doesn't repeat its own.
	for i, f := range [][]byte{pkiFrame(t, 0x0000aaaa, 2, 0), pkiFrame(t, boardNum, 3, 3)} {
		if err := b.Send(context.Background(), f); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	select {
	case f := <-b.Frames():
		t.Fatalf("unexpected echo %x", f.Data)
	case <-time.After(echoDelay + 200*time.Millisecond):
	}
	if st := b.Stats(context.Background()); st.TxPackets != 3 || st.RxPackets != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func pushUplink(fake *mtclienttest.Node, data []byte) {
	fake.Push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_MqttClientProxyMessage{MqttClientProxyMessage: &pb.MqttClientProxyMessage{
		Topic: "msh/EU_868/2/e/LongFast/!0b0a4d01", PayloadVariant: &pb.MqttClientProxyMessage_Data{Data: data}}}})
}

func TestBoardSkipsUplinksItCantUse(t *testing.T) {
	fake := mtclienttest.New(boardNum)
	b := startTestBoard(t, fake)
	decoded, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: &pb.MeshPacket{From: 5, Id: 1,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP}}}})
	noSender, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: &pb.MeshPacket{Id: 2,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1}}}})
	tooBig, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: &pb.MeshPacket{From: 5, Id: 3,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: make([]byte, wire.MaxPayload+1)}}})
	good, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: &pb.MeshPacket{From: 5, Id: 4, HopLimit: 1,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1}}}})
	for _, d := range [][]byte{{0xff, 0xff}, decoded, noSender, tooBig, good} {
		pushUplink(fake, d)
	}
	select {
	case f := <-b.Frames():
		if p := wire.DecodeFrame(f.Data, 0, 0); p.GetId() != 4 {
			t.Fatalf("frame %v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("good uplink not delivered")
	}
	if st := b.Stats(context.Background()); st.RxPackets != 1 || st.Errors != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestBoardDropsUplinksWhenTheHostIsBehind(t *testing.T) {
	fake := mtclienttest.New(boardNum)
	b := startTestBoard(t, fake)
	env, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: &pb.MeshPacket{From: 5, Id: 9,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1}}}})
	n := cap(b.frames) + 3
	for range n {
		pushUplink(fake, env)
	}
	eventually(t, "all uplinks handled", func() bool {
		st := b.Stats(context.Background())
		return st.RxPackets == uint32(n)
	})
	if st := b.Stats(context.Background()); st.Errors != 3 {
		t.Fatalf("stats %+v", st)
	}
}

func TestBoardFramesCloseWithTheBoard(t *testing.T) {
	b := startTestBoard(t, mtclienttest.New(boardNum))
	b.Close()
	select {
	case _, ok := <-b.Frames():
		if ok {
			t.Fatal("frame after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("frames not closed")
	}
}

func TestBoardSettingsEdgeCases(t *testing.T) {
	s := mtclient.Snapshot{
		Channels: []*pb.Channel{
			nil,
			{Index: 1, Role: pb.Channel_SECONDARY},
			{Index: 2, Role: pb.Channel_DISABLED},
			{Index: 3, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{UplinkEnabled: true, DownlinkEnabled: true}},
		},
	}
	msgs := BoardSettings(s)
	if len(msgs) != 1 || msgs[0].GetSetChannel().GetIndex() != 1 || !msgs[0].GetSetChannel().GetSettings().GetUplinkEnabled() {
		t.Fatalf("no module config, one channel to fix: %v", msgs)
	}
	s.ModuleConfig = &pb.LocalModuleConfig{}
	msgs = BoardSettings(s)
	if len(msgs) != 2 || !msgs[0].GetSetModuleConfig().GetMqtt().GetProxyToClientEnabled() {
		t.Fatalf("module config without mqtt: %v", msgs)
	}
	s.ModuleConfig.Mqtt = proto.Clone(msgs[0].GetSetModuleConfig().GetMqtt()).(*pb.ModuleConfig_MQTTConfig)
	if msgs = BoardSettings(s); len(msgs) != 1 {
		t.Fatalf("mqtt already right: %v", msgs)
	}
	var n Node
	n.SetExtraSettings(BoardSettings)
	n.PrepareChannel(nil)
	disabled := &pb.Channel{Role: pb.Channel_DISABLED}
	n.PrepareChannel(disabled)
	bare := &pb.Channel{Role: pb.Channel_SECONDARY}
	n.PrepareChannel(bare)
	if disabled.Settings != nil || !bare.GetSettings().GetDownlinkEnabled() {
		t.Fatalf("prepared %v %v", disabled, bare)
	}
}
