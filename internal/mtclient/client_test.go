package mtclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/pb"
)

// fakeNode answers the client API the way the firmware does, closely enough for the client:
// the config handshake, admin gets and sets, and packets pushed by the test.
type fakeNode struct {
	num uint32

	mu      sync.Mutex
	conn    net.Conn
	dials   int
	toRadio []*pb.ToRadio
	owner   *pb.User
	noise   bool // write console text between frames, as a serial board does
	silent  bool // never finish the handshake
}

func newFakeNode() *fakeNode {
	return &fakeNode{num: 0x1ee7a001, owner: &pb.User{Id: "!1ee7a001", LongName: "Fake", ShortName: "FAKE"}}
}

func (f *fakeNode) dial(context.Context) (io.ReadWriteCloser, error) {
	host, node := net.Pipe()
	f.mu.Lock()
	f.dials++
	f.conn = node
	f.mu.Unlock()
	go f.serve(node)
	return host, nil
}

func (f *fakeNode) send(conn io.Writer, fr *pb.FromRadio) {
	b, _ := proto.Marshal(fr)
	frame, _ := appendFrame(nil, b)
	f.mu.Lock()
	noise := f.noise
	f.mu.Unlock()
	if noise {
		frame = append([]byte("INFO | 12:00:00 [Router] \x94 console text\r\n"), frame...)
	}
	_, _ = conn.Write(frame)
}

// push sends a FromRadio on the current connection.
func (f *fakeNode) push(fr *pb.FromRadio) {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	f.send(conn, fr)
}

func (f *fakeNode) drop() {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	conn.Close()
}

func (f *fakeNode) received() []*pb.ToRadio {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.ToRadio(nil), f.toRadio...)
}

func (f *fakeNode) serve(conn net.Conn) {
	_ = readFrames(conn, func(b []byte) bool {
		tr := &pb.ToRadio{}
		if proto.Unmarshal(b, tr) != nil {
			return true
		}
		f.mu.Lock()
		f.toRadio = append(f.toRadio, tr)
		silent := f.silent
		owner := proto.Clone(f.owner).(*pb.User)
		f.mu.Unlock()
		switch v := tr.PayloadVariant.(type) {
		case *pb.ToRadio_WantConfigId:
			if silent {
				return true
			}
			for _, fr := range f.handshake(v.WantConfigId, owner) {
				f.send(conn, fr)
			}
		case *pb.ToRadio_Packet:
			f.answer(conn, v.Packet)
		}
		return true
	})
	conn.Close()
}

func (f *fakeNode) handshake(nonce uint32, owner *pb.User) []*pb.FromRadio {
	fr := func(v any) *pb.FromRadio {
		switch x := v.(type) {
		case *pb.MyNodeInfo:
			return &pb.FromRadio{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: x}}
		case *pb.DeviceMetadata:
			return &pb.FromRadio{PayloadVariant: &pb.FromRadio_Metadata{Metadata: x}}
		case *pb.NodeInfo:
			return &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: x}}
		case *pb.Channel:
			return &pb.FromRadio{PayloadVariant: &pb.FromRadio_Channel{Channel: x}}
		case *pb.Config:
			return &pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: x}}
		case *pb.ModuleConfig:
			return &pb.FromRadio{PayloadVariant: &pb.FromRadio_ModuleConfig{ModuleConfig: x}}
		}
		panic(v)
	}
	return []*pb.FromRadio{
		// A completion for somebody else's request comes first and must be ignored.
		{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce + 1}},
		fr(&pb.MyNodeInfo{MyNodeNum: f.num}),
		fr(&pb.DeviceMetadata{FirmwareVersion: "2.8.0.fake"}),
		fr(&pb.NodeInfo{Num: f.num, User: owner}),
		fr(&pb.NodeInfo{Num: 0x0badcafe, User: &pb.User{LongName: "Other"}}),
		fr(&pb.Channel{Index: 0, Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{Psk: []byte{1}}}),
		fr(&pb.Channel{Index: 1, Role: pb.Channel_DISABLED}),
		fr(&pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, HopLimit: 5}}}),
		fr(&pb.Config{PayloadVariant: &pb.Config_Device{Device: &pb.Config_DeviceConfig{Role: pb.Config_DeviceConfig_ROUTER}}}),
		fr(&pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Mqtt{Mqtt: &pb.ModuleConfig_MQTTConfig{Enabled: true}}}),
		{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce}},
	}
}

// answer replies to admin packets addressed to the node: a get_owner response, or a routing ack.
func (f *fakeNode) answer(conn io.Writer, p *pb.MeshPacket) {
	d := p.GetDecoded()
	if p.To != f.num || d.GetPortnum() != pb.PortNum_ADMIN_APP {
		return
	}
	var am pb.AdminMessage
	if proto.Unmarshal(d.GetPayload(), &am) != nil {
		return
	}
	ack := func(reason pb.Routing_Error) {
		b, _ := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}})
		f.send(conn, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: f.num, To: f.num, Id: NewPacketID(),
			PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: b, RequestId: p.Id}}}}})
	}
	switch v := am.PayloadVariant.(type) {
	case *pb.AdminMessage_GetOwnerRequest:
		ack(pb.Routing_NONE) // the firmware acks first; the response must still be awaited
		f.mu.Lock()
		owner := proto.Clone(f.owner).(*pb.User)
		f.mu.Unlock()
		b, _ := proto.Marshal(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerResponse{GetOwnerResponse: owner}})
		f.send(conn, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: f.num, To: f.num, Id: NewPacketID(),
			PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ADMIN_APP, Payload: b, RequestId: p.Id}}}}})
	case *pb.AdminMessage_SetOwner:
		f.mu.Lock()
		f.owner = v.SetOwner
		f.mu.Unlock()
		ack(pb.Routing_NONE)
	case *pb.AdminMessage_RebootSeconds:
		ack(pb.Routing_NOT_AUTHORIZED)
	}
}

func startFake(t *testing.T, f *fakeNode, opt func(*Options)) *Client {
	t.Helper()
	o := Options{Address: "fake", Dial: f.dial, ReconnectInterval: 10 * time.Millisecond, RequestTimeout: 2 * time.Second, Logf: t.Logf}
	if opt != nil {
		opt(&o)
	}
	c := New(o)
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
	f.noise = true
	c := startFake(t, f, nil)
	s := c.Snapshot()
	if !s.Connected || s.NodeNum() != f.num || s.Metadata.GetFirmwareVersion() != "2.8.0.fake" {
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
	// The wake-up bytes come before the first frame.
	f.mu.Lock()
	first := f.toRadio[0]
	f.mu.Unlock()
	if first.GetWantConfigId() == 0 {
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
		for _, tr := range f.received() {
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
	for _, tr := range f.received() {
		if p := tr.GetPacket(); p != nil && (p.To != f.num || p.HopLimit != 0) {
			t.Fatalf("admin packet %v", p)
		}
	}
}

func TestReconnect(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	ev, stop := c.Subscribe(16)
	defer stop()
	f.mu.Lock()
	f.owner = &pb.User{LongName: "After reboot"}
	f.mu.Unlock()
	f.drop()
	waitEvent(t, ev, Disconnected)
	if err := c.Send(&pb.ToRadio{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("send while down: %v", err)
	}
	waitEvent(t, ev, Configured)
	s := c.Snapshot()
	if s.Reconnects != 1 || s.Self().GetUser().GetLongName() != "After reboot" {
		t.Fatalf("after reconnect %+v", s)
	}

	// A Rebooted frame ends the session too, and so does Reconnect.
	f.push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
	waitEvent(t, ev, Disconnected)
	waitEvent(t, ev, Configured)
	c.Reconnect()
	waitEvent(t, ev, Disconnected)
	waitEvent(t, ev, Configured)
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
	f.push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: 0x0badcafe, To: 0xffffffff, Id: 7,
		RxTime: proto.Uint32(1000), RxSnr: 4.5, HopLimit: 2, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: user}}}}})
	e := waitEvent(t, ev, Received)
	if e.FromRadio.GetPacket().GetId() != 7 {
		t.Fatalf("event %v", e)
	}
	n := c.Snapshot().Nodes[0x0badcafe]
	if n.GetUser().GetLongName() != "Other renamed" || n.GetLastHeard() != 1000 || n.GetSnr() != 4.5 || n.GetHopsAway() != 1 {
		t.Fatalf("node %v", n)
	}
	f.push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{HopLimit: 7}}}}})
	waitEvent(t, ev, Received)
	if c.Snapshot().Config.GetLora().GetHopLimit() != 7 || c.Snapshot().Config.GetDevice().GetRole() != pb.Config_DeviceConfig_ROUTER {
		t.Fatal("config update not merged")
	}
}

func TestConfigTimeout(t *testing.T) {
	f := newFakeNode()
	f.silent = true
	c := New(Options{Address: "fake", Dial: f.dial, ConfigTimeout: 50 * time.Millisecond, ReconnectInterval: 10 * time.Millisecond})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Wait(ctx); err == nil || !bytes.Contains([]byte(err.Error()), []byte("no config")) {
		t.Fatalf("first attempt: %v", err)
	}
	f.mu.Lock()
	f.silent = false
	f.mu.Unlock()
	if err := c.WaitReady(ctx); err != nil {
		t.Fatalf("after the node answers: %v", err)
	}
}

func TestDialFailure(t *testing.T) {
	c := New(Options{Address: "x", Dial: func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("no such device") }})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Wait(ctx); err == nil || err.Error() != "no such device" {
		t.Fatalf("wait: %v", err)
	}
	c.Close()
	if err := c.WaitReady(context.Background()); !errors.Is(err, ErrNotConnected) {
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
		if got, err := TCPAddress(in); err != nil || got != want {
			t.Errorf("TCPAddress(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := TCPAddress(" "); err == nil {
		t.Error("empty address accepted")
	}
	if !IsSerial("/dev/ttyACM0") || IsSerial("192.168.1.5") {
		t.Error("IsSerial")
	}
}

func TestFraming(t *testing.T) {
	if _, err := appendFrame(nil, make([]byte, MaxFrame+1)); err == nil {
		t.Fatal("oversize frame accepted")
	}
	var buf bytes.Buffer
	buf.Write([]byte{0x94, 0x94, 0xC3, 0x00, 0x01, 0xAA}) // a repeated start byte
	buf.Write([]byte{0x94, 0xC3, 0x02, 0x01})             // oversize: skipped
	good, _ := appendFrame(nil, []byte{1, 2, 3})
	buf.Write(good)
	var got [][]byte
	_ = readFrames(&buf, func(b []byte) bool { got = append(got, b); return true })
	if len(got) < 2 || !bytes.Equal(got[0], []byte{0xAA}) || !bytes.Equal(got[len(got)-1], []byte{1, 2, 3}) {
		t.Fatalf("frames %x", got)
	}
}

func waitEvent(t *testing.T, ev <-chan Event, k EventKind) Event {
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
