package mtclient_test

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
)

const otherNum = 0x0badcafe

type result struct {
	p   *pb.MeshPacket
	err error
}

// startRequest runs c.Request in the background and waits until the fake node has the packet.
func startRequest(t *testing.T, f *mtclienttest.Node, c *mtclient.Client, p *pb.MeshPacket) <-chan result {
	t.Helper()
	p.Id = mtclient.NewPacketID()
	out := make(chan result, 1)
	go func() {
		r, err := c.Request(context.Background(), p)
		out <- result{r, err}
	}()
	waitFor(t, "request packet", func() bool { return sentID(f, p.Id) })
	return out
}

func sentID(f *mtclienttest.Node, id uint32) bool {
	for _, p := range f.Packets() {
		if p.Id == id {
			return true
		}
	}
	return false
}

// reply delivers a decoded packet answering request id.
func reply(f *mtclienttest.Node, id uint32, port pb.PortNum, m proto.Message) {
	b, _ := proto.Marshal(m)
	f.Deliver(&pb.MeshPacket{From: otherNum, To: fakeNum, Id: mtclient.NewPacketID(),
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: port, Payload: b, RequestId: id}}})
}

func routing(reason pb.Routing_Error) *pb.Routing {
	return &pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}}
}

func awaitResult(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("request did not return")
		return result{}
	}
}

func decodedTo(to uint32, wantResponse bool) *pb.MeshPacket {
	return &pb.MeshPacket{To: to, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{
		Portnum: pb.PortNum_TRACEROUTE_APP, WantResponse: wantResponse}}}
}

func TestRequestWantResponseSkipsAck(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	p := decodedTo(otherNum, true)
	ch := startRequest(t, f, c, p)
	reply(f, p.Id, pb.PortNum_ROUTING_APP, routing(pb.Routing_NONE))
	reply(f, p.Id, pb.PortNum_TRACEROUTE_APP, &pb.RouteDiscovery{Route: []uint32{1}})
	r := awaitResult(t, ch)
	if r.err != nil || r.p.GetDecoded().GetPortnum() != pb.PortNum_TRACEROUTE_APP {
		t.Fatalf("answer %v, %v", r.p, r.err)
	}
}

func TestRequestRoutingErrorAnswers(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	p := decodedTo(otherNum, true)
	ch := startRequest(t, f, c, p)
	reply(f, p.Id, pb.PortNum_ROUTING_APP, routing(pb.Routing_NO_ROUTE))
	r := awaitResult(t, ch)
	if r.err != nil || r.p.GetDecoded().GetPortnum() != pb.PortNum_ROUTING_APP {
		t.Fatalf("answer %v, %v", r.p, r.err)
	}
}

func TestRequestAckAnswersWithoutWantResponse(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	p := decodedTo(otherNum, false)
	ch := startRequest(t, f, c, p)
	// An unrelated reply (no waiter) is ignored.
	reply(f, p.Id+1, pb.PortNum_ROUTING_APP, routing(pb.Routing_NONE))
	reply(f, p.Id, pb.PortNum_ROUTING_APP, routing(pb.Routing_NONE))
	if r := awaitResult(t, ch); r.err != nil || r.p.GetDecoded().GetRequestId() != p.Id {
		t.Fatalf("answer %v, %v", r.p, r.err)
	}
}

func TestRequestEndsWhenClientCloses(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	ch := startRequest(t, f, c, decodedTo(otherNum, true))
	c.Close()
	if r := awaitResult(t, ch); !errors.Is(r.err, mtclient.ErrNotConnected) {
		t.Fatalf("err = %v", r.err)
	}
}

func TestRequestWhileDisconnected(t *testing.T) {
	c := mtclient.New(mtclient.Options{Address: "x"})
	if _, err := c.Request(context.Background(), decodedTo(otherNum, false)); !errors.Is(err, mtclient.ErrNotConnected) {
		t.Fatalf("err = %v", err)
	}
}

func TestSendPacketTooBig(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	p := &pb.MeshPacket{To: otherNum, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Payload: make([]byte, 600)}}}
	if _, err := c.SendPacket(p); err == nil {
		t.Fatal("oversize packet sent")
	}
}

func TestSendPacketToSelfKeepsZeroHopLimit(t *testing.T) {
	f := newFakeNode()
	f.Update(func(s *mtclienttest.State) { s.Config.Lora.HopLimit = 0 })
	c := startFake(t, f, nil)
	self := &pb.MeshPacket{To: fakeNum, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP}}}
	other := &pb.MeshPacket{To: otherNum, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP}}}
	if _, err := c.SendPacket(self); err != nil || self.HopLimit != 0 {
		t.Fatalf("self packet hop %d, %v", self.HopLimit, err)
	}
	// The node has no hop limit configured: the firmware default of 3 is used.
	if _, err := c.SendPacket(other); err != nil || other.HopLimit != 3 {
		t.Fatalf("other packet hop %d, %v", other.HopLimit, err)
	}
}

// adminAnswer runs an admin request to the node whose answer the test supplies.
func adminAnswer(t *testing.T, port pb.PortNum, payload []byte) error {
	t.Helper()
	f := newFakeNode()
	c := startFake(t, f, nil)
	errc := make(chan error, 1)
	// get_ requests without a case in the fake are left unanswered.
	go func() {
		_, err := c.Admin(context.Background(), &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetDeviceMetadataRequest{GetDeviceMetadataRequest: true}})
		errc <- err
	}()
	var id uint32
	waitFor(t, "admin packet", func() bool {
		for _, tr := range f.Received() {
			if p := tr.GetPacket(); p.GetDecoded().GetPortnum() == pb.PortNum_ADMIN_APP {
				id = p.Id
				return true
			}
		}
		return false
	})
	f.Deliver(&pb.MeshPacket{From: fakeNum, To: fakeNum, Id: 99,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: port, Payload: payload, RequestId: id}}})
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("admin did not return")
		return nil
	}
}

func TestAdminBadAnswers(t *testing.T) {
	garbage := []byte{0xff, 0xff, 0xff}
	cases := []struct {
		name    string
		port    pb.PortNum
		payload []byte
		want    string
	}{
		{"unexpected port", pb.PortNum_TEXT_MESSAGE_APP, []byte("hi"), "unexpected answer"},
		{"bad admin payload", pb.PortNum_ADMIN_APP, garbage, "cannot parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := adminAnswer(t, tc.port, tc.payload)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReceivedUpdatesMirror(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	ev, stop := c.Subscribe(32)
	defer stop()
	pushes := []*pb.FromRadio{
		{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: &pb.NodeInfo{Num: 0x42, User: &pb.User{LongName: "New"}}}},
		{PayloadVariant: &pb.FromRadio_Channel{Channel: &pb.Channel{Index: 3, Role: pb.Channel_SECONDARY}}},
		{PayloadVariant: &pb.FromRadio_ModuleConfig{ModuleConfig: &pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Mqtt{Mqtt: &pb.ModuleConfig_MQTTConfig{Address: "broker"}}}}},
		{PayloadVariant: &pb.FromRadio_Metadata{Metadata: &pb.DeviceMetadata{FirmwareVersion: "2.8.1"}}},
		{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: &pb.MyNodeInfo{MyNodeNum: fakeNum, RebootCount: 9}}},
		{PayloadVariant: &pb.FromRadio_QueueStatus{QueueStatus: &pb.QueueStatus{Free: 1}}},
	}
	for _, fr := range pushes {
		f.Push(fr)
		waitEvent(t, ev, mtclient.Received)
	}
	s := c.Snapshot()
	if s.Nodes[0x42].GetUser().GetLongName() != "New" || len(s.Channels) != 4 || s.Channels[2] != nil ||
		s.Channels[3].GetRole() != pb.Channel_SECONDARY {
		t.Fatalf("nodes/channels %v %v", s.Nodes, s.Channels)
	}
	if s.ModuleConfig.GetMqtt().GetAddress() != "broker" || s.Metadata.GetFirmwareVersion() != "2.8.1" || s.MyInfo.GetRebootCount() != 9 {
		t.Fatalf("snapshot %+v", s)
	}
}

func deliverFrom(f *mtclienttest.Node, from uint32, port pb.PortNum, m proto.Message) {
	b, _ := proto.Marshal(m)
	f.Deliver(&pb.MeshPacket{From: from, To: fakeNum, Id: mtclient.NewPacketID(),
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: port, Payload: b}}})
}

func TestReceivedPositionAndTelemetry(t *testing.T) {
	f := newFakeNode()
	c := startFake(t, f, nil)
	ev, stop := c.Subscribe(32)
	defer stop()
	const stranger = 0x77
	deliverFrom(f, stranger, pb.PortNum_POSITION_APP, &pb.Position{LatitudeI: proto.Int32(575000000)})
	deliverFrom(f, stranger, pb.PortNum_POSITION_APP, &pb.Position{Altitude: proto.Int32(5)}) // no fix: ignored
	deliverFrom(f, stranger, pb.PortNum_TELEMETRY_APP, &pb.Telemetry{Variant: &pb.Telemetry_DeviceMetrics{DeviceMetrics: &pb.DeviceMetrics{BatteryLevel: proto.Uint32(80)}}})
	deliverFrom(f, stranger, pb.PortNum_TELEMETRY_APP, &pb.Telemetry{Variant: &pb.Telemetry_EnvironmentMetrics{EnvironmentMetrics: &pb.EnvironmentMetrics{}}})
	// Encrypted packets and packets without a sender don't touch the node DB.
	f.Deliver(&pb.MeshPacket{From: 0x78, PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1}}})
	deliverFrom(f, 0, pb.PortNum_POSITION_APP, &pb.Position{LatitudeI: proto.Int32(1)})
	for range 6 {
		waitEvent(t, ev, mtclient.Received)
	}
	s := c.Snapshot()
	n := s.Nodes[stranger]
	if n.GetPosition().GetLatitudeI() != 575000000 || n.GetDeviceMetrics().GetBatteryLevel() != 80 || n.HopsAway != nil {
		t.Fatalf("node %v", n)
	}
	if _, ok := s.Nodes[0x78]; ok {
		t.Fatal("encrypted packet created a node entry")
	}
	if _, ok := s.Nodes[0]; ok {
		t.Fatal("sender 0 created a node entry")
	}
}

func TestHeartbeats(t *testing.T) {
	f := newFakeNode()
	startFake(t, f, func(o *mtclient.Options) { o.Heartbeat = 5 * time.Millisecond })
	waitFor(t, "heartbeat", func() bool {
		for _, tr := range f.Received() {
			if tr.GetHeartbeat() != nil {
				return true
			}
		}
		return false
	})
}

func TestStartOverTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	f := newFakeNode()
	go f.Serve(l)
	c := mtclient.New(mtclient.Options{Address: l.Addr().String()})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().NodeNum() != fakeNum {
		t.Fatal("wrong node")
	}
}

func TestStartBadAddress(t *testing.T) {
	c := mtclient.New(mtclient.Options{Address: "host:99999"})
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("bad address accepted")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close of an unstarted client: %v", err)
	}
}

func TestStartSerialMissingDevice(t *testing.T) {
	dev := "/dev/" + filepath.Base(t.TempDir()) + "-missing"
	c := mtclient.New(mtclient.Options{Address: dev, ReconnectInterval: time.Hour})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Wait(ctx); err == nil {
		t.Fatal("opening a missing serial device succeeded")
	}
}

func TestWaitHonoursContext(t *testing.T) {
	f := newFakeNode()
	f.SetSilent(true)
	c := mtclient.New(mtclient.Options{Address: "fake", Dial: f.Dial})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait: %v", err)
	}
	if err := c.WaitReady(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait ready: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// A dial that finishes after Close must not leave the connection open.
func TestDialCompletingAfterClose(t *testing.T) {
	host, node := net.Pipe()
	defer node.Close()
	dialed := make(chan struct{})
	c := mtclient.New(mtclient.Options{Address: "x", Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
		close(dialed)
		<-ctx.Done()
		return host, nil
	}})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-dialed
	c.Close()
	if _, err := host.Write([]byte{1}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("connection still open: %v", err)
	}
}

func TestSubscribeCancelTwice(t *testing.T) {
	c := mtclient.New(mtclient.Options{Address: "x"})
	ch, stop := c.Subscribe(1)
	stop()
	stop()
	if _, ok := <-ch; ok {
		t.Fatal("channel still open")
	}
	s := c.Snapshot()
	if s.Connected || s.NodeNum() != 0 || s.MyInfo != nil || s.Self() != nil {
		t.Fatalf("empty snapshot %+v", s)
	}
}
