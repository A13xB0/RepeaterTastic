package mtclient

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

func TestMergeOneofIgnoresMismatches(t *testing.T) {
	dst := &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{HopLimit: 4}}
	want := proto.Clone(dst)
	mergeOneof(dst, &pb.User{LongName: "no oneof"})                                                               // no payload_variant
	mergeOneof(dst, &pb.Config{})                                                                                 // nothing set
	mergeOneof(dst, &pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Mqtt{Mqtt: &pb.ModuleConfig_MQTTConfig{}}}) // no mqtt field
	if !proto.Equal(dst, want) {
		t.Fatalf("dst changed: %v", dst)
	}
	src := &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{HopLimit: 6}}}
	mergeOneof(dst, src)
	src.GetLora().HopLimit = 1
	if dst.GetLora().GetHopLimit() != 6 {
		t.Fatalf("merged %v", dst)
	}
}

func TestSetChannelBounds(t *testing.T) {
	var chs []*pb.Channel
	setChannel(&chs, &pb.Channel{Index: 64})
	setChannel(&chs, &pb.Channel{Index: -1})
	if len(chs) != 0 {
		t.Fatalf("out-of-range index stored: %v", chs)
	}
	setChannel(&chs, &pb.Channel{Index: 63})
	if len(chs) != 64 || chs[63] == nil {
		t.Fatalf("index 63: len %d", len(chs))
	}
}

func TestRoutingErrorOnlyForRouting(t *testing.T) {
	b, _ := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: pb.Routing_TIMEOUT}})
	if routingError(&pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: b}) {
		t.Fatal("text message counted as a routing error")
	}
	if !routingError(&pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: b}) {
		t.Fatal("timeout not a routing error")
	}
}

func TestHandleFrameSkipsGarbage(t *testing.T) {
	c := New(Options{})
	hs := newHandshake(1)
	called := false
	if !c.handleFrame([]byte{0xff, 0xff, 0xff}, hs, func() { called = true }) || called {
		t.Fatal("garbage frame ended the session or completed the handshake")
	}
	// Completion without MyInfo is not a finished handshake.
	done, _ := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: 1}})
	if !c.handleFrame(done, hs, func() { called = true }) || called {
		t.Fatal("handshake completed without my_info")
	}
}

func TestClonePreservesNil(t *testing.T) {
	var m *pb.User
	if clone(m) != nil {
		t.Fatal("nil clone not nil")
	}
	u := &pb.User{LongName: "a"}
	if c := clone(u); c == u || c.LongName != "a" {
		t.Fatal("clone")
	}
}

func TestIsAdminGet(t *testing.T) {
	cases := []struct {
		m    *pb.AdminMessage
		want bool
	}{
		{&pb.AdminMessage{}, false},
		{&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}}, true},
		{&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerResponse{GetOwnerResponse: &pb.User{}}}, false},
		{&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: &pb.User{}}}, false},
	}
	for i, tc := range cases {
		if got := isAdminGet(tc.m); got != tc.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
}

type failWriter struct{ n int }

func (w *failWriter) Write(p []byte) (int, error) {
	if w.n == 0 {
		return 0, errors.New("write failed")
	}
	w.n--
	return len(p), nil
}

func TestRequestConfigWriteErrors(t *testing.T) {
	c := New(Options{})
	if err := c.requestConfig(&failWriter{}, 1); err == nil {
		t.Fatal("wakeup write error ignored")
	}
	if err := c.requestConfig(&failWriter{n: 1}, 1); err == nil {
		t.Fatal("want_config write error ignored")
	}
	var buf bytes.Buffer
	if err := c.requestConfig(&buf, 7); err != nil || !bytes.HasPrefix(buf.Bytes(), wakeup) {
		t.Fatalf("request config %x, %v", buf.Bytes(), err)
	}
}

func TestNextFrameTruncated(t *testing.T) {
	cases := map[string][]byte{
		"after start1": {start1},
		"in length":    {start1, start2, 0},
		"in payload":   {start1, start2, 0, 4, 1, 2},
	}
	for name, in := range cases {
		_, ok, err := nextFrame(bufio.NewReader(bytes.NewReader(in)))
		if ok || !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
		}
	}
}

func TestReadFramesStops(t *testing.T) {
	var buf bytes.Buffer
	for range 3 {
		f, _ := AppendFrame(nil, []byte{9})
		buf.Write(f)
	}
	n := 0
	if err := ReadFrames(&buf, func([]byte) bool { n++; return n < 2 }); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
