package phoneapi

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

// collector is a session's send function that records every frame; accept false drops them.
type collector struct {
	mu     sync.Mutex
	got    []*pb.FromRadio
	reject bool
}

func (c *collector) send(fr *pb.FromRadio) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reject {
		return false
	}
	c.got = append(c.got, fr)
	return true
}

func (c *collector) frames() []*pb.FromRadio {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pb.FromRadio(nil), c.got...)
}

func (c *collector) reset() {
	c.mu.Lock()
	c.got = nil
	c.mu.Unlock()
}

func newTestSession(t *testing.T, h *mesh.Host, id *mesh.Identity) (*Session, *collector) {
	t.Helper()
	c := &collector{}
	return NewSession(h, id, slog.New(slog.NewTextHandler(io.Discard, nil)), c.send), c
}

func textPacket(id, to uint32, port pb.PortNum) *pb.ToRadio {
	return &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: &pb.MeshPacket{To: to, Id: id,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: port, Payload: []byte("x")}}}}}
}

// routingErrors returns the routing error reasons the session reported for request reqID.
func routingErrors(t *testing.T, frames []*pb.FromRadio, reqID uint32) []pb.Routing_Error {
	t.Helper()
	var out []pb.Routing_Error
	for _, fr := range frames {
		d := fr.GetPacket().GetDecoded()
		if d.GetPortnum() != pb.PortNum_ROUTING_APP || d.GetRequestId() != reqID {
			continue
		}
		r := &pb.Routing{}
		if err := proto.Unmarshal(d.Payload, r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r.GetErrorReason())
	}
	return out
}

func queueStatuses(frames []*pb.FromRadio) []*pb.QueueStatus {
	var out []*pb.QueueStatus
	for _, fr := range frames {
		if qs := fr.GetQueueStatus(); qs != nil {
			out = append(out, qs)
		}
	}
	return out
}

func TestLiveTrafficQueuedUntilHandshake(t *testing.T) {
	h, id, _ := testServer(t)
	s, c := newTestSession(t, h, id)
	for i := 1; i <= 70; i++ {
		s.SendFromRadio(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{Id: uint32(i)}}})
	}
	if len(c.frames()) != 0 {
		t.Fatal("live traffic sent before the handshake")
	}
	s.startConfig(nonceOnlyNodes)
	var ids []uint32
	for _, fr := range c.frames() {
		if p := fr.GetPacket(); p != nil {
			ids = append(ids, p.Id)
		}
	}
	// Only the newest 64 are kept, in order.
	if len(ids) != 64 || ids[0] != 7 || ids[63] != 70 {
		t.Fatalf("released %d packets: first %v", len(ids), ids)
	}
	if id.ClientCount() != 1 {
		t.Fatalf("session not attached: %d clients", id.ClientCount())
	}

	// Once configured, live traffic goes straight out with increasing FromRadio ids.
	c.reset()
	s.SendFromRadio(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{Id: 99}}})
	got := c.frames()
	if len(got) != 1 || got[0].GetPacket().GetId() != 99 || got[0].Id == 0 {
		t.Fatalf("live frame %v", got)
	}

	s.Close()
	if id.ClientCount() != 0 {
		t.Fatal("closed session still attached")
	}
	c.reset()
	s.SendFromRadio(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{Id: 100}}})
	if len(c.frames()) != 0 {
		t.Fatal("closed session still sends")
	}
}

func TestSlowClientDropsFrames(t *testing.T) {
	h, id, _ := testServer(t)
	s, c := newTestSession(t, h, id)
	c.reject = true
	s.HandleToRadio(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
	c.reject = false
	s.HandleToRadio(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
	got := c.frames()
	if len(got) != 1 || got[0].Id != 2 {
		t.Fatalf("frames after a drop: %v", got)
	}
}

func TestHandleToRadioControlMessages(t *testing.T) {
	h, id, _ := testServer(t)
	s, c := newTestSession(t, h, id)

	if s.HandleToRadio(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}}) {
		t.Fatal("heartbeat disconnected")
	}
	if qs := queueStatuses(c.frames()); len(qs) != 1 || qs[0].Res != 0 || qs[0].Free != queueMax {
		t.Fatalf("heartbeat reply %v", qs)
	}

	// Nonce 1 asks the node to broadcast its node info; nothing goes back to the client.
	c.reset()
	s.HandleToRadio(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{Nonce: 1}}})
	if len(c.frames()) != 0 {
		t.Fatalf("nonce-1 heartbeat answered: %v", c.frames())
	}

	c.reset()
	s.HandleToRadio(&pb.ToRadio{PayloadVariant: &pb.ToRadio_XmodemPacket{XmodemPacket: &pb.XModem{Control: pb.XModem_SOH, Seq: 5}}})
	x := c.frames()[0].GetXmodemPacket()
	if x.GetControl() != pb.XModem_NAK || x.GetSeq() != 5 {
		t.Fatalf("xmodem reply %v", x)
	}

	if !s.HandleToRadio(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Disconnect{Disconnect: true}}) {
		t.Fatal("disconnect not reported")
	}
}

func TestEncryptedPacketFromClientRejected(t *testing.T) {
	h, id, _ := testServer(t)
	s, c := newTestSession(t, h, id)
	s.HandleToRadio(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: &pb.MeshPacket{Id: 12,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1, 2}}}}})
	qs := queueStatuses(c.frames())
	if len(qs) != 1 || qs[0].Res != 1 || qs[0].MeshPacketId != 12 {
		t.Fatalf("queue status %v", qs)
	}
}

func TestDuplicatePacketSentOnce(t *testing.T) {
	h, id, _ := testServer(t)
	s, c := newTestSession(t, h, id)
	s.HandleToRadio(textPacket(500, wire.Broadcast, pb.PortNum_POSITION_APP))
	s.HandleToRadio(textPacket(500, wire.Broadcast, pb.PortNum_POSITION_APP))
	if qs := queueStatuses(c.frames()); len(qs) != 1 {
		t.Fatalf("repeat handled again: %v", qs)
	}

	// A packet without an id gets one.
	c.reset()
	s.HandleToRadio(textPacket(0, wire.Broadcast, pb.PortNum_POSITION_APP))
	if qs := queueStatuses(c.frames()); len(qs) != 1 || qs[0].MeshPacketId == 0 {
		t.Fatalf("id-less packet: %v", qs)
	}
}

func TestSeenRecentlyPrunesOldIDs(t *testing.T) {
	s := &Session{recentIDs: map[uint32]time.Time{}}
	now := time.Now()
	for i := uint32(1); i <= 600; i++ {
		s.recentIDs[i] = now.Add(-time.Hour)
	}
	if s.seenRecently(1, now) {
		t.Fatal("an hour-old id counted as recent")
	}
	if len(s.recentIDs) != 1 {
		t.Fatalf("old ids kept: %d", len(s.recentIDs))
	}
	if !s.seenRecently(1, now.Add(time.Minute)) {
		t.Fatal("repeat within ten minutes not seen")
	}
}

func TestClientRateLimits(t *testing.T) {
	tests := []struct {
		name string
		port pb.PortNum
		gap  time.Duration
	}{
		{"text", pb.PortNum_TEXT_MESSAGE_APP, 2 * time.Second},
		{"traceroute", pb.PortNum_TRACEROUTE_APP, 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{}
			now := time.Now()
			if s.rateLimited(tc.port, now) {
				t.Fatal("first send limited")
			}
			if !s.rateLimited(tc.port, now.Add(tc.gap/2)) {
				t.Fatal("second send not limited")
			}
			if s.rateLimited(tc.port, now.Add(tc.gap+time.Millisecond)) {
				t.Fatal("send after the gap limited")
			}
			if s.rateLimited(pb.PortNum_POSITION_APP, now) {
				t.Fatal("other ports limited")
			}
		})
	}
}

func TestRateLimitedTextGetsRoutingError(t *testing.T) {
	h, id, _ := testServer(t)
	s, c := newTestSession(t, h, id)
	s.HandleToRadio(textPacket(601, wire.Broadcast, pb.PortNum_TEXT_MESSAGE_APP))
	s.HandleToRadio(textPacket(602, wire.Broadcast, pb.PortNum_TEXT_MESSAGE_APP))
	frames := c.frames()
	if errs := routingErrors(t, frames, 601); len(errs) != 0 {
		t.Fatalf("first text refused: %v", errs)
	}
	errs := routingErrors(t, frames, 602)
	if len(errs) != 1 || errs[0] != pb.Routing_RATE_LIMIT_EXCEEDED {
		t.Fatalf("second text: %v", errs)
	}
}

func TestPacketToVirtualNodeWithoutRemote(t *testing.T) {
	h, _, _ := testServer(t)
	relay := h.Relay()
	s, c := newTestSession(t, h, relay)
	// To itself: the node can't answer, so the client hears NO_INTERFACE.
	s.HandleToRadio(textPacket(700, relay.NodeNum, pb.PortNum_ADMIN_APP))
	errs := routingErrors(t, c.frames(), 700)
	if len(errs) != 1 || errs[0] != pb.Routing_NO_INTERFACE {
		t.Fatalf("routing errors %v", errs)
	}
	// To someone else the failure is only logged.
	c.reset()
	s.HandleToRadio(textPacket(701, 0x1234, pb.PortNum_POSITION_APP))
	if errs := routingErrors(t, c.frames(), 701); len(errs) != 0 {
		t.Fatalf("unexpected routing errors %v", errs)
	}
}

func TestConfigReflectsIdentitySettings(t *testing.T) {
	h, id, _ := testServer(t)
	s, _ := newTestSession(t, h, id)

	if got := s.configByType(pb.AdminMessage_POSITION_CONFIG).GetPosition().GetPositionBroadcastSecs(); got != 3*3600 {
		t.Fatalf("default position interval %d", got)
	}
	id.SetPositionInterval(600)
	if got := s.configByType(pb.AdminMessage_POSITION_CONFIG).GetPosition().GetPositionBroadcastSecs(); got != 600 {
		t.Fatalf("identity position interval %d", got)
	}

	radioHops := h.Config().HopLimit
	if got := s.configByType(pb.AdminMessage_LORA_CONFIG).GetLora().GetHopLimit(); got != radioHops {
		t.Fatalf("hop limit %d, radio %d", got, radioHops)
	}
	if err := id.SetMaxHops(1); err != nil {
		t.Fatal(err)
	}
	if got := s.configByType(pb.AdminMessage_LORA_CONFIG).GetLora().GetHopLimit(); got != 1 {
		t.Fatalf("capped hop limit %d", got)
	}
	if s.configByType(pb.AdminMessage_ConfigType(99)) != nil {
		t.Fatal("unknown config type produced a config")
	}
}

func TestPositionIntervalFromSiteConfig(t *testing.T) {
	h, id, _ := testServer(t)
	cfg := h.Config()
	cfg.Position.Interval = 20 * time.Minute
	if err := h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, h, id)
	if got := s.configByType(pb.AdminMessage_POSITION_CONFIG).GetPosition().GetPositionBroadcastSecs(); got != 1200 {
		t.Fatalf("site position interval %d", got)
	}
}

func TestMissingChannelReportedDisabled(t *testing.T) {
	h, id, _ := testServer(t)
	s, _ := newTestSession(t, h, id)
	ch := s.channel(mesh.MaxChannels)
	if ch.Index != int32(mesh.MaxChannels) || ch.Role != pb.Channel_DISABLED || ch.Settings == nil {
		t.Fatalf("channel %v", ch)
	}
}
