package mesh

import (
	"errors"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

// nopTap stands in for the air bridge: with it the host logs what the radio hears.
type nopTap struct{}

func (nopTap) Heard(radio.Frame)                          {}
func (nopTap) Transmitted([]byte, *pb.MeshPacket, uint32) {}

type chanLink struct {
	mu    sync.Mutex
	heard []string
}

func (l *chanLink) Name() string              { return "test" }
func (l *chanLink) SendPacket(*pb.MeshPacket) {}
func (l *chanLink) ChannelPacketHeard(_ *pb.MeshPacket, ch ChannelRef, d *pb.Data) {
	l.mu.Lock()
	l.heard = append(l.heard, ch.Name+":"+string(d.Payload))
	l.mu.Unlock()
}

func TestHandleReceivedLogsAndSniffs(t *testing.T) {
	h, relay, _ := remoteHost(t)
	h.AddAirTap(nopTap{})
	link := &chanLink{}
	h.AddLink(link)
	_, key, _, ok := h.ChannelKey(relay, 1)
	if !ok {
		t.Fatal("no key for the Ops channel")
	}
	hash, _, _, _ := h.ChannelKey(relay, 1)

	const from = 0x0badf00d
	user, _ := proto.Marshal(&pb.User{Id: wire.NodeID(from), LongName: "Stranger", ShortName: "STR"})
	for _, c := range []struct {
		id   uint32
		data *pb.Data
		kind string
	}{
		{1, &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: user, Bitfield: proto.Uint32(0)}, "delivered"},
		{2, &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hello"), Bitfield: proto.Uint32(1)}, "delivered"},
		{2, &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hello"), Bitfield: proto.Uint32(1)}, "dup"},
	} {
		plain, _ := proto.Marshal(c.data)
		p := &pb.MeshPacket{From: from, To: wire.Broadcast, Id: c.id, Channel: uint32(hash), HopLimit: 3, HopStart: 3,
			PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: wire.AESCTR(key, from, c.id, plain)}}
		frame, err := wire.EncodeFrame(p)
		if err != nil {
			t.Fatal(err)
		}
		h.HandleReceived(wire.DecodeFrame(frame, -90, 5), frame)
		recs := h.Packets.List(1, 0, nil)
		if len(recs) != 1 || recs[0].Kind != c.kind {
			t.Fatalf("packet %d logged as %+v, want %s", c.id, recs, c.kind)
		}
	}
	if e, ok := h.DB.Get(from); !ok || e.User.GetLongName() != "Stranger" {
		t.Fatalf("node DB = %+v", e)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if len(link.heard) != 2 || link.heard[1] != "Ops:hello" {
		t.Fatalf("link heard %v", link.heard)
	}
	if n := h.Counters.Tx.Load(); n != 0 {
		t.Fatalf("the host transmitted %d packets itself", n)
	}
}

func TestSendNeedsANode(t *testing.T) {
	h, _, _ := remoteHost(t)
	id, _ := NewIdentity(nil, "Loose", "LSE")
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	pid, err := h.SendText(id, wire.Broadcast, 0, "anyone?", false)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v", err)
	}
	msgs := h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 10)
	if len(msgs) != 1 || msgs[0].ID != pid || msgs[0].Status != "failed" {
		t.Fatalf("messages = %+v", msgs)
	}
}
