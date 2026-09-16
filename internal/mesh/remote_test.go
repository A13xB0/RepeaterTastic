package mesh

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

type fakeRemote struct {
	mu   sync.Mutex
	sent []*pb.MeshPacket
	fail bool
}

func (f *fakeRemote) SendPacket(p *pb.MeshPacket) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return 0, errors.New("node gone")
	}
	f.sent = append(f.sent, proto.Clone(p).(*pb.MeshPacket))
	return p.Id, nil
}

func (f *fakeRemote) Admin(context.Context, *pb.AdminMessage) (*pb.AdminMessage, error) {
	return nil, nil
}

func (f *fakeRemote) packets() []*pb.MeshPacket {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.MeshPacket(nil), f.sent...)
}

const remoteNum = 0x6330e73c

func remoteHost(t *testing.T) (*Host, *Identity, *fakeRemote) {
	t.Helper()
	h, err := NewHost(Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, NodeInfoInterval: time.Hour},
		null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{}
	id, err := NewRemoteIdentity(fr, RemoteState{NodeNum: remoteNum,
		User: &pb.User{LongName: "Board", ShortName: "BRD", PublicKey: make([]byte, 32), Role: pb.Config_DeviceConfig_ROUTER},
		Channels: []*pb.Channel{{Index: 0, Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{Psk: []byte{1}}},
			{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "Ops", Psk: make([]byte, 16)}}},
		Nodes: []*pb.NodeInfo{{Num: remoteNum}, {Num: 0x0badcafe, User: &pb.User{LongName: "Neighbour"}, Snr: 7.5,
			LastHeard: uint32(time.Now().Unix()), HopsAway: proto.Uint32(1), Position: &pb.Position{LatitudeI: proto.Int32(1)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id.IsRelay = true
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	return h, id, fr
}

func TestRemoteIdentity(t *testing.T) {
	h, id, _ := remoteHost(t)
	if id.PrivateKey != nil || id.Remote() == nil || h.Relay() != id {
		t.Fatal("remote identity not the relay")
	}
	if u := id.UserCopy(); u.GetId() != "!6330e73c" || u.GetLongName() != "Board" {
		t.Fatalf("user %v", u)
	}
	if ch := id.ChannelCopy(1); ch.GetSettings().GetName() != "Ops" || id.ChannelCopy(5).GetRole() != pb.Channel_DISABLED {
		t.Fatal("channels not taken from the node")
	}
	if _, err := NewRemoteIdentity(&fakeRemote{}, RemoteState{NodeNum: 1}); err == nil {
		t.Fatal("reserved node number accepted")
	}
	if err := h.SaveIdentities(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteSync(t *testing.T) {
	h, id, _ := remoteHost(t)
	h.SyncRemote(id, RemoteState{NodeNum: remoteNum, User: &pb.User{LongName: "Renamed"},
		Nodes: []*pb.NodeInfo{{Num: 0x0badcafe, User: &pb.User{LongName: "Neighbour"}, Snr: 7.5,
			LastHeard: uint32(time.Now().Unix()), HopsAway: proto.Uint32(1), Position: &pb.Position{LatitudeI: proto.Int32(1)}}}})
	if id.UserCopy().GetLongName() != "Renamed" {
		t.Fatal("user not synced")
	}
	e, ok := h.DB.Get(0x0badcafe)
	if !ok || e.User.GetLongName() != "Neighbour" || e.SNR != 7.5 || e.HopsAway != 1 || e.Position == nil {
		t.Fatalf("node DB entry %+v", e)
	}
	if self, _ := h.DB.Get(remoteNum); !self.Local || self.User.GetLongName() != "Renamed" {
		t.Fatalf("own entry %+v", self)
	}
}

func TestRemoteSend(t *testing.T) {
	h, id, fr := remoteHost(t)
	pid, err := h.SendText(id, 0xffffffff, 1, "hello", true)
	if err != nil {
		t.Fatal(err)
	}
	sent := fr.packets()
	if len(sent) != 1 || sent[0].From != 0 || sent[0].Id != pid || sent[0].Channel != 1 || text(sent[0]) != "hello" || sent[0].HopLimit == 0 {
		t.Fatalf("sent %v", sent)
	}
	msgs := h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 10)
	if len(msgs) != 1 || msgs[0].Status != "queued" {
		t.Fatalf("messages %+v", msgs)
	}
	// The node's ACK marks it delivered.
	ack, _ := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: pb.Routing_NONE}})
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: remoteNum, Id: 99,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: ack, RequestId: pid}}})
	if m := h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 10); m[0].Status != "acked" {
		t.Fatalf("after ack %+v", m)
	}
	// A node that went away fails the message instead of leaving it queued.
	fr.mu.Lock()
	fr.fail = true
	fr.mu.Unlock()
	pid2, err := h.SendText(id, 0x0badcafe, 0, "gone?", true)
	if err == nil {
		t.Fatal("send to a missing node succeeded")
	}
	for _, m := range h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 10) {
		if m.ID == pid2 && m.Status != "failed" {
			t.Fatalf("message %+v", m)
		}
	}
	// NodeInfo and traceroute requests go to the node too.
	fr.mu.Lock()
	fr.fail = false
	fr.mu.Unlock()
	h.RequestNodeInfo(id, 0x0badcafe)
	if err := h.Traceroute(id, 0x0badcafe); err != nil {
		t.Fatal(err)
	}
	ports := map[pb.PortNum]bool{}
	for _, p := range fr.packets() {
		ports[p.GetDecoded().GetPortnum()] = true
	}
	if !ports[pb.PortNum_NODEINFO_APP] || !ports[pb.PortNum_TRACEROUTE_APP] {
		t.Fatalf("ports sent %v", ports)
	}
}

func TestRemoteReceived(t *testing.T) {
	h, id, fr := remoteHost(t)
	s := newSink()
	id.AddSink(s)
	events, stop := h.Bus.Subscribe(64)
	defer stop()
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: remoteNum, Id: 7, Channel: 0, PkiEncrypted: true,
		RxSnr: 5, RxRssi: proto.Int32(-80), HopLimit: 3, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hi board")}}})
	p := s.waitPacket(t, time.Second, func(p *pb.MeshPacket) bool { return text(p) == "hi board" })
	if p.RxTime == nil {
		t.Fatal("delivered packet has no rx_time")
	}
	msgs := h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 10)
	if len(msgs) != 1 || msgs[0].Direction != "in" || !msgs[0].PKI || msgs[0].RSSI != -80 {
		t.Fatalf("messages %+v", msgs)
	}
	rec := nextEvent(t, events, "packet").Data.(PacketRecord)
	if rec.Kind != "delivered" || rec.Channel != "PKI" || rec.Summary == "" {
		t.Fatalf("record %+v", rec)
	}

	// A traceroute answer is published as a result; the node already appended the route.
	rd, _ := proto.Marshal(&pb.RouteDiscovery{Route: []uint32{0x12345678}, SnrTowards: []int32{20, 24}})
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: remoteNum, Id: 8,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TRACEROUTE_APP, Payload: rd, RequestId: 5}}})
	if tr := nextEvent(t, events, "traceroute").Data.(TracerouteResult); len(tr.Route) != 1 || tr.Route[0] != "!12345678" {
		t.Fatalf("traceroute result %+v", tr)
	}

	// The host never answers for the node, and ignores frames a link injects.
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: remoteNum, Id: 9,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, WantResponse: true}}})
	h.HandleReceived(&pb.MeshPacket{From: 0x0badcafe, To: 0xffffffff, Id: 10, HopStart: 3, HopLimit: 3,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1, 2, 3}}}, nil)
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: 0xffffffff, Id: 11,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1, 2, 3}}})
	if n := len(fr.packets()); n != 0 {
		t.Fatalf("host sent %d packets for the node", n)
	}
}

func TestSwapRemote(t *testing.T) {
	h, id, fr := remoteHost(t)
	s := newSink()
	id.AddSink(s)
	id.APIPort = 4428
	next, err := NewRemoteIdentity(fr, RemoteState{NodeNum: 0x12345678, User: &pb.User{LongName: "Real"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SwapRemote(id, next); err != nil {
		t.Fatal(err)
	}
	if h.Relay() != next || h.Identity(remoteNum) != nil || !next.IsRelay || next.APIPort != 4428 || next.ClientCount() != 1 {
		t.Fatal("swap didn't carry the identity over")
	}
	if _, ok := h.DB.Get(remoteNum); ok {
		t.Fatal("old number left in the node DB")
	}
	virtual, _ := NewIdentity(nil, "V", "")
	if err := h.SwapRemote(virtual, next); err == nil {
		t.Fatal("swapped a virtual identity")
	}
}

func TestClientBaseFavorites(t *testing.T) {
	h, err := NewHost(Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, RelayRole: RoleClientBase, Favorites: []uint32{0x11112222}},
		null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := NewIdentity(nil, "Desk", "DESK")
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		from, to uint32
		want     bool
	}{
		{0x11112222, wire.Broadcast, true},  // from a favourite
		{0x33334444, id.NodeNum, true},      // to our own identity
		{0x33334444, wire.Broadcast, false}, // a stranger's broadcast
	} {
		if got := h.relaysAsRouter(&pb.MeshPacket{From: c.from, To: c.to}); got != c.want {
			t.Errorf("%08x -> %08x: router priority %v, want %v", c.from, c.to, got, c.want)
		}
	}
	cfg := h.Config()
	cfg.RelayRole = RoleClient
	if err := h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if h.relaysAsRouter(&pb.MeshPacket{From: 0x11112222, To: wire.Broadcast}) {
		t.Error("favourites only matter to client_base")
	}
}
