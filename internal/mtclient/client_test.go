package mtclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const fakeNum = 0x1ee7a001

func newFakeNode() *mtclienttest.Node {
	n := mtclienttest.New(fakeNum)
	n.Update(func(s *mtclienttest.State) {
		s.Config.Lora.HopLimit = 5
		s.Config.Device.Role = pb.Config_DeviceConfig_ROUTER
		s.Modules.Mqtt.Enabled = true
		s.Others = append(s.Others, &pb.NodeInfo{Num: 0x0badcafe, User: &pb.User{LongName: "Other"}})
	})
	return n
}

func startFake(t *testing.T, f *mtclienttest.Node, opt func(*mtclient.Options)) *mtclient.Client {
	t.Helper()
	o := mtclient.Options{Address: "fake", Dial: f.Dial, ReconnectInterval: 10 * time.Millisecond, RequestTimeout: 2 * time.Second, Logf: t.Logf}
	if opt != nil {
		opt(&o)
	}
	c := mtclient.New(o)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHandshake(t *testing.T) {
	f := newFakeNode()
	f.SetNoise(true)
	c := startFake(t, f, nil)
	s := c.Snapshot()
	if !s.Connected || s.NodeNum() != fakeNum || s.Metadata.GetFirmwareVersion() != "2.8.0.fake" {
		t.Fatalf("snapshot %+v", s)
	}
	if s.Self().GetUser().GetLongName() != "Fake" || len(s.Nodes) != 2 {
		t.Fatalf("nodes %v", s.Nodes)
	}
	if s.Config.GetLora().GetHopLimit() != 5 || s.Config.GetDevice().GetRole() != pb.Config_DeviceConfig_ROUTER || !s.ModuleConfig.GetMqtt().GetEnabled() {
		t.Fatalf("config %v / %v", s.Config, s.ModuleConfig)
	}
	if len(s.Channels) != 2 || s.Channels[0].GetRole() != pb.Channel_PRIMARY {
		t.Fatalf("channels %v", s.Channels)
	}
	// The snapshot is a copy.
	s.Config.Lora.HopLimit = 1
	if c.Snapshot().Config.GetLora().GetHopLimit() != 5 {
		t.Fatal("snapshot shares state with the client")
	}
	if first := f.Received()[0]; first.GetWantConfigId() == 0 {
		t.Fatalf("first frame %v", first)
	}
}

func TestSendDefaultsHopLimit(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	if _, err := c.SendPacket(&pb.MeshPacket{To: 0xffffffff, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "packet", func() bool {
		for _, tr := range f.Received() {
			if p := tr.GetPacket(); p != nil && p.To == 0xffffffff {
				if p.HopLimit != 5 || p.Id == 0 {
					t.Fatalf("packet %v", p)
				}
				return true
			}
		}
		return false
	})
}

func TestAdmin(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	ctx := context.Background()
	r, err := c.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}})
	if err != nil || r.GetGetOwnerResponse().GetLongName() != "Fake" {
		t.Fatalf("get owner: %v %v", r, err)
	}
	r, err = c.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: &pb.User{LongName: "Renamed"}}})
	if err != nil || r != nil {
		t.Fatalf("set owner: %v %v", r, err)
	}
	if _, err := c.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_RebootSeconds{RebootSeconds: 1}}); err == nil {
		t.Fatal("refused admin message succeeded")
	}
	// Nothing answers a factory reset in the fake: the request times out.
	if _, err := c.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_FactoryResetDevice{FactoryResetDevice: 1}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unanswered admin: %v", err)
	}
	for _, tr := range f.Received() {
		if p := tr.GetPacket(); p != nil && (p.To != fakeNum || p.HopLimit != 0) {
			t.Fatalf("admin packet %v", p)
		}
	}
}

func TestReconnect(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	ev, stop := c.Subscribe(16)
	defer stop()
	f.Update(func(s *mtclienttest.State) { s.Owner = &pb.User{LongName: "After reboot"} })
	f.Drop()
	waitEvent(t, ev, mtclient.Disconnected)
	if err := c.Send(&pb.ToRadio{}); !errors.Is(err, mtclient.ErrNotConnected) {
		t.Fatalf("send while down: %v", err)
	}
	waitEvent(t, ev, mtclient.Configured)
	s := c.Snapshot()
	if s.Reconnects != 1 || s.Self().GetUser().GetLongName() != "After reboot" {
		t.Fatalf("after reconnect %+v", s)
	}

	// A Rebooted frame ends the session too, and so does Reconnect.
	f.Push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
	waitEvent(t, ev, mtclient.Disconnected)
	waitEvent(t, ev, mtclient.Configured)
	c.Reconnect()
	waitEvent(t, ev, mtclient.Disconnected)
	waitEvent(t, ev, mtclient.Configured)
	if c.Snapshot().Reconnects != 3 {
		t.Fatalf("reconnects %d", c.Snapshot().Reconnects)
	}
}

func TestReceived(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	ev, stop := c.Subscribe(16)
	defer stop()
	user, _ := proto.Marshal(&pb.User{LongName: "Other renamed"})
	f.Push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: 0x0badcafe, To: 0xffffffff, Id: 7,
		RxTime: proto.Uint32(1000), RxSnr: 4.5, HopLimit: 2, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: user}}}}})
	e := waitEvent(t, ev, mtclient.Received)
	if e.FromRadio.GetPacket().GetId() != 7 {
		t.Fatalf("event %v", e)
	}
	n := c.Snapshot().Nodes[0x0badcafe]
	if n.GetUser().GetLongName() != "Other renamed" || n.GetLastHeard() != 1000 || n.GetSnr() != 4.5 || n.GetHopsAway() != 1 {
		t.Fatalf("node %v", n)
	}
	f.Push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{HopLimit: 7}}}}})
	waitEvent(t, ev, mtclient.Received)
	if c.Snapshot().Config.GetLora().GetHopLimit() != 7 || c.Snapshot().Config.GetDevice().GetRole() != pb.Config_DeviceConfig_ROUTER {
		t.Fatal("config update not merged")
	}
}

func TestConfigTimeout(t *testing.T) {
	f := newFakeNode()
	f.SetSilent(true)
	c := mtclient.New(mtclient.Options{Address: "fake", Dial: f.Dial, ConfigTimeout: 50 * time.Millisecond, ReconnectInterval: 10 * time.Millisecond})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Wait(ctx); err == nil || !bytes.Contains([]byte(err.Error()), []byte("no config")) {
		t.Fatalf("first attempt: %v", err)
	}
	f.SetSilent(false)
	if err := c.WaitReady(ctx); err != nil {
		t.Fatalf("after the node answers: %v", err)
	}
}

func TestDialFailure(t *testing.T) {
	c := mtclient.New(mtclient.Options{Address: "x", Dial: func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("no such device") }})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Wait(ctx); err == nil || err.Error() != "no such device" {
		t.Fatalf("wait: %v", err)
	}
	c.Close()
	if err := c.WaitReady(context.Background()); !errors.Is(err, mtclient.ErrNotConnected) {
		t.Fatalf("wait after close: %v", err)
	}
}

func TestAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1":      "127.0.0.1:4403",
		"pi.local:4410":  "pi.local:4410",
		"meshtasticd":    "meshtasticd:4403",
		"fe80::1":        "[fe80::1]:4403",
		"[fe80::1]:4404": "[fe80::1]:4404",
	} {
		if got, err := mtclient.TCPAddress(in); err != nil || got != want {
			t.Errorf("mtclient.TCPAddress(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{" ", "a:b:c:d", "pi.local:0", "pi.local:99999", ":4403"} {
		if _, err := mtclient.TCPAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !mtclient.IsSerial("/dev/ttyACM0") || mtclient.IsSerial("192.168.1.5") {
		t.Error("IsSerial")
	}
}

func TestFraming(t *testing.T) {
	if _, err := mtclient.AppendFrame(nil, make([]byte, mtclient.MaxFrame+1)); err == nil {
		t.Fatal("oversize frame accepted")
	}
	var buf bytes.Buffer
	buf.Write([]byte{0x94, 0x94, 0xC3, 0x00, 0x01, 0xAA}) // a repeated start byte
	buf.Write([]byte{0x94, 0xC3, 0x02, 0x01})             // oversize: skipped
	good, _ := mtclient.AppendFrame(nil, []byte{1, 2, 3})
	buf.Write(good)
	var got [][]byte
	_ = mtclient.ReadFrames(&buf, func(b []byte) bool { got = append(got, b); return true })
	if len(got) < 2 || !bytes.Equal(got[0], []byte{0xAA}) || !bytes.Equal(got[len(got)-1], []byte{1, 2, 3}) {
		t.Fatalf("frames %x", got)
	}
}

func waitEvent(t *testing.T, ev <-chan mtclient.Event, k mtclient.EventKind) mtclient.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-ev:
			if e.Kind == k {
				return e
			}
		case <-timeout:
			t.Fatalf("no event %d", k)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
