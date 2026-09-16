package mesh

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// addVirtual adds a virtual identity whose last byte doesn't clash with the host's others.
func addVirtual(t *testing.T, h *Host, name string) *Identity {
	t.Helper()
	for range 50 {
		id, err := NewIdentity(nil, name, "")
		if err != nil {
			continue
		}
		if h.AddIdentity(id) == nil {
			return id
		}
	}
	t.Fatal("no usable identity key")
	return nil
}

func TestNewIdentityDefaults(t *testing.T) {
	id, err := NewIdentity(nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(id.User.ShortName) != 4 || id.User.LongName != "Meshtastic "+id.User.ShortName || !id.Enabled {
		t.Fatalf("defaults = %v", id.User)
	}
	if _, err := NewIdentity([]byte{1, 2}, "x", "y"); err == nil {
		t.Fatal("short private key accepted")
	}
	id.SetOwner("  A very long name that goes on and on and on forever  ", "LONGER")
	if u := id.UserCopy(); len(u.LongName) != 39 || u.ShortName != "LONG" {
		t.Fatalf("owner = %q / %q", u.LongName, u.ShortName)
	}
	id.SetOwner("", "")
	if id.UserCopy().ShortName != "LONG" {
		t.Fatal("empty names overwrote the owner")
	}
	if id.ChannelCopy(-1) != nil || id.ChannelCopy(MaxChannels) != nil {
		t.Fatal("channel out of range returned")
	}
}

func TestIdentityBacklog(t *testing.T) {
	id, _ := NewIdentity(nil, "B", "B")
	fr := func(n uint32) *pb.FromRadio { return &pb.FromRadio{Id: n} }
	id.deliverToClients(fr(0), false)
	if id.BacklogLen() != 0 {
		t.Fatal("unkept frame backlogged")
	}
	for i := uint32(1); i <= backlogPackets+6; i++ {
		id.deliverToClients(fr(i), true)
	}
	if id.BacklogLen() != backlogPackets {
		t.Fatalf("backlog = %d", id.BacklogLen())
	}
	s := newSink()
	id.AddSink(s)
	id.FlushBacklog(s)
	if id.BacklogLen() != 0 || len(s.ch) != backlogPackets || (<-s.ch).Id != 7 {
		t.Fatal("backlog not flushed oldest first")
	}
	for len(s.ch) > 0 {
		<-s.ch
	}
	id.deliverToClients(fr(99), true)
	if (<-s.ch).Id != 99 {
		t.Fatal("frame not delivered to the client")
	}
	id.RemoveSink(s)
	if id.ClientCount() != 0 {
		t.Fatal("sink not removed")
	}
}

func TestIdentityRecordRoundTrip(t *testing.T) {
	id, _ := NewIdentity(nil, "Saved", "SVD")
	id.IsRelay, id.APIBind, id.APIPort, id.ShareLimitPct = true, "0.0.0.0", 4403, 12.5
	if err := id.SetRole("router"); err != nil {
		t.Fatal(err)
	}
	if err := id.SetMaxHops(5); err != nil || id.MaxHops() != 5 {
		t.Fatal("max hops not set")
	}
	if err := id.SetFixedPosition(&IdentityPosition{Latitude: 56, Longitude: -3, Altitude: 100}); err != nil {
		t.Fatal(err)
	}
	id.SetPositionInterval(900)
	id.Channels[2] = &pb.Channel{Index: 2, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "Ops", Psk: []byte{2}}}

	rec := id.Record()
	if rec.NodeNum() != id.NodeNum || rec.Role != "ROUTER" || len(rec.Channels) != MaxChannels {
		t.Fatalf("record = %+v", rec)
	}
	back, err := IdentityFromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if back.NodeNum != id.NodeNum || !back.IsRelay || back.APIPort != 4403 || back.ShareLimitPct != 12.5 ||
		back.User.Role != pb.Config_DeviceConfig_ROUTER || back.MaxHops() != 5 || back.PositionInterval() != 900 ||
		back.ChannelCopy(2).GetSettings().GetName() != "Ops" || back.CreatedAt.UnixMilli() != id.CreatedAt.UnixMilli() {
		t.Fatalf("restored identity differs: %+v", back)
	}
	if p, ok := back.FixedPosition(); !ok || p.Latitude != 56 || p.Altitude != 100 {
		t.Fatalf("position = %+v, %v", p, ok)
	}
}

func TestIdentityFromOddRecord(t *testing.T) {
	id, _ := NewIdentity(nil, "Odd", "ODD")
	rec := id.Record()
	noSettings, _ := proto.Marshal(&pb.Channel{Role: pb.Channel_SECONDARY})
	rec.Channels = []string{rec.Channels[0], "***", base64.StdEncoding.EncodeToString(noSettings)}
	for range MaxChannels {
		rec.Channels = append(rec.Channels, rec.Channels[0])
	}
	rec.Role = "NOT_A_ROLE"
	back, err := IdentityFromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if back.ChannelCopy(1).GetRole() != pb.Channel_DISABLED || back.ChannelCopy(2).GetSettings() == nil ||
		back.User.Role != pb.Config_DeviceConfig_CLIENT {
		t.Fatalf("odd record = %+v", back.Channels)
	}

	for _, key := range []string{"!!", base64.StdEncoding.EncodeToString([]byte{1, 2, 3})} {
		bad := IdentityRecord{PrivateKey: key, LongName: "bad"}
		if _, err := bad.Key(); err == nil {
			t.Errorf("key %q accepted", key)
		}
		if bad.NodeNum() != 0 {
			t.Errorf("node number for key %q", key)
		}
		if _, err := IdentityFromRecord(bad); err == nil {
			t.Errorf("identity from key %q", key)
		}
	}
}

func TestIdentitySettersReject(t *testing.T) {
	id, _ := NewIdentity(nil, "S", "S")
	if err := id.SetRole("pilot"); err == nil {
		t.Error("unknown role accepted")
	}
	if err := id.SetMaxHops(wire.HopMax + 1); err == nil {
		t.Error("hop limit above max accepted")
	}
	for _, p := range []IdentityPosition{{Latitude: 91}, {Longitude: -181}, {}} {
		if err := id.SetFixedPosition(&p); err == nil {
			t.Errorf("position %+v accepted", p)
		}
	}
	if err := id.SetFixedPosition(&IdentityPosition{Latitude: 1, Longitude: 1}); err != nil {
		t.Fatal(err)
	}
	if err := id.SetFixedPosition(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := id.FixedPosition(); ok {
		t.Error("position not cleared")
	}
	id.SetSettings(func(s *IdentitySettings) { s.Enabled, s.APIPort, s.APIBind, s.ShareLimitPct = false, 1, "lo", 3 })
	if s := id.Settings(); s.Enabled || s.APIPort != 1 || s.APIBind != "lo" || s.ShareLimitPct != 3 {
		t.Errorf("settings = %+v", s)
	}
}

func TestSetChannel(t *testing.T) {
	h, _, relay := virtualHost(t, Config{PrimaryChannel: "Mast"})
	for _, ch := range []*pb.Channel{
		nil,
		{Index: -1},
		{Index: MaxChannels},
		{Index: 1, Settings: &pb.ChannelSettings{Psk: make([]byte, 33)}},
		{Index: 1, Role: pb.Channel_PRIMARY},
	} {
		if err := h.SetChannel(relay, ch); err == nil {
			t.Errorf("channel %v accepted", ch)
		}
	}
	if err := h.SetChannel(relay, &pb.Channel{Index: 0, Role: pb.Channel_SECONDARY,
		Settings: &pb.ChannelSettings{Name: "Mine", Psk: []byte{3}}}); err != nil {
		t.Fatal(err)
	}
	if ch := relay.ChannelCopy(0); ch.Role != pb.Channel_PRIMARY || ch.Settings.Name != "Mast" || ch.Settings.Psk[0] != 3 {
		t.Fatalf("primary = %v", ch)
	}
	// A secondary without a key shares the primary's.
	if err := h.SetChannel(relay, &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY}); err != nil {
		t.Fatal(err)
	}
	_, k0, _, _ := h.ChannelKey(relay, 0)
	_, k1, _, ok := h.ChannelKey(relay, 1)
	if !ok || string(k0) != string(k1) {
		t.Fatal("keyless secondary didn't take the primary key")
	}
	if _, _, _, ok := h.ChannelKey(relay, 5); ok {
		t.Fatal("key for a disabled channel")
	}
	hash, _, _, _ := h.ChannelKey(relay, 0)
	if refs := h.ChannelsByHash(hash); len(refs) == 0 || refs[0].Name != "Mast" {
		t.Fatalf("channels by hash = %+v", refs)
	}
	names := map[string]bool{}
	for _, c := range h.Channels() {
		names[c.Name] = true
	}
	if !names["Mast"] || !names["LongFast"] || len(names) != 2 { // the unnamed secondary takes the preset name
		t.Fatalf("channels = %v", names)
	}
}

func testRecord(t *testing.T) IdentityRecord {
	t.Helper()
	var id *Identity
	for id == nil || wire.LastByte(id.NodeNum) == wire.LastByte(remoteNum) {
		var err error
		if id, err = NewIdentity(nil, "Hosted", "HST"); err != nil {
			t.Fatal(err)
		}
	}
	id.APIPort, id.HopLimit = 4500, 2
	return id.Record()
}

func TestHostedIdentity(t *testing.T) {
	rec := testRecord(t)
	st, err := RecordState(rec)
	if err != nil {
		t.Fatal(err)
	}
	if st.NodeNum != rec.NodeNum() || st.User.GetLongName() != "Hosted" || len(st.Channels) != MaxChannels {
		t.Fatalf("record state = %+v", st)
	}
	id, err := NewHostedIdentity(&fakeRemote{}, st, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Hosted() || id.APIPort != 4500 || id.MaxHops() != 2 || id.UserCopy().GetLongName() != "Hosted" {
		t.Fatalf("hosted identity = %+v", id)
	}
	plain, _ := NewRemoteIdentity(&fakeRemote{}, st)
	if plain.Hosted() {
		t.Fatal("identity without a key reported hosted")
	}

	bad := rec
	bad.PrivateKey = "x"
	if _, err := RecordState(bad); err == nil {
		t.Error("record state from a bad key")
	}
	if _, err := NewHostedIdentity(&fakeRemote{}, st, bad); err == nil {
		t.Error("hosted identity from a bad key")
	}
	if _, err := NewHostedIdentity(nil, st, rec); err == nil {
		t.Error("hosted identity without a node")
	}
	if blank, _ := NewRemoteIdentity(&fakeRemote{}, RemoteState{NodeNum: 0x1234}); blank.UserCopy().GetId() != "!00001234" {
		t.Error("remote identity without a user has no id")
	}
}

func TestSaveIdentities(t *testing.T) {
	dir := t.TempDir()
	h, err := NewHost(Config{Region: "EU_868", StateDir: dir}, newFakeRadio(), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	rec := testRecord(t)
	st, _ := RecordState(rec)
	hosted, _ := NewHostedIdentity(&fakeRemote{}, st, rec)
	board, _ := NewRemoteIdentity(&fakeRemote{}, RemoteState{NodeNum: hosted.NodeNum + 2}) // a different last byte
	for _, id := range []*Identity{hosted, board} {
		if err := h.AddIdentity(id); err != nil {
			t.Fatal(err)
		}
	}
	kept := testRecord(t)
	h.KeepRecord(kept)
	if err := h.SaveIdentities(); err != nil {
		t.Fatal(err)
	}
	recs, err := LoadIdentityRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].PrivateKey != rec.PrivateKey || recs[1].PrivateKey != kept.PrivateKey {
		t.Fatalf("saved records = %+v", recs)
	}
	if _, err := LoadIdentityRecords(t.TempDir()); err == nil {
		t.Fatal("loaded records from an empty dir")
	}
	if err := os.WriteFile(dir+"/identities.json", []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentityRecords(dir); err == nil {
		t.Fatal("loaded bad JSON")
	}
}

type fakeHoster struct {
	mu       sync.Mutex
	hosted   []IdentityRecord
	unhosted []uint32
}

func (f *fakeHoster) HostIdentity(_ context.Context, h *Host, rec IdentityRecord) (*Identity, error) {
	f.mu.Lock()
	f.hosted = append(f.hosted, rec)
	f.mu.Unlock()
	st, err := RecordState(rec)
	if err != nil {
		return nil, err
	}
	id, err := NewHostedIdentity(&fakeRemote{}, st, rec)
	if err != nil {
		return nil, err
	}
	return id, h.AddIdentity(id)
}

func (f *fakeHoster) Unhost(id *Identity) {
	f.mu.Lock()
	f.unhosted = append(f.unhosted, id.NodeNum)
	f.mu.Unlock()
}

func TestHoster(t *testing.T) {
	h, relay, _ := remoteHost(t)
	if _, err := h.AddRecord(context.Background(), testRecord(t)); !errors.Is(err, ErrNoHoster) {
		t.Fatalf("add without a hoster: %v", err)
	}
	hs := &fakeHoster{}
	h.SetHoster(hs)
	if h.Hoster() != hs {
		t.Fatal("hoster not set")
	}
	id, err := h.AddRecord(context.Background(), testRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	if h.Identity(id.NodeNum) != id {
		t.Fatal("hosted identity not added")
	}
	if err := h.DropIdentity(relay.NodeNum); err == nil {
		t.Fatal("dropped the relay persona")
	}
	if err := h.DropIdentity(id.NodeNum); err != nil {
		t.Fatal(err)
	}
	if h.Identity(id.NodeNum) != nil || len(hs.unhosted) != 1 || hs.unhosted[0] != id.NodeNum {
		t.Fatalf("drop left %v, unhosted %v", h.Identity(id.NodeNum), hs.unhosted)
	}
	if err := h.DropIdentity(id.NodeNum); err == nil {
		t.Fatal("dropped an unknown identity")
	}
	virtual := addVirtual(t, h, "Virtual")
	if err := h.DropIdentity(virtual.NodeNum); err != nil || len(hs.unhosted) != 1 {
		t.Fatalf("virtual drop: %v, unhosted %v", err, hs.unhosted)
	}
}

func TestSite(t *testing.T) {
	a, _, relayA := virtualHost(t, Config{})
	b, _, _ := virtualHost(t, Config{RadioID: "second"})
	const node = 0x55667788
	if got := a.Sightings(node); len(got) != 0 {
		t.Fatalf("sightings of an unknown node = %+v", got)
	}
	a.DB.Update(node, func(e *NodeEntry) { e.LastHeard = t0; e.SNR = 1 })
	b.DB.Update(node, func(e *NodeEntry) { e.LastHeard = t0.Add(time.Minute); e.SNR = 2; e.ViaMQTT = true })
	if got := a.Sightings(node); len(got) != 1 || got[0].Radio != "main" {
		t.Fatalf("lone sightings = %+v", got)
	}
	if b.SiteIdentity(relayA.NodeNum) {
		t.Fatal("other radio's identity known before joining")
	}
	JoinSite(a, b)
	if !b.SiteIdentity(relayA.NodeNum) || !a.SiteIdentity(relayA.NodeNum) || a.SiteIdentity(node) {
		t.Fatal("site identities wrong")
	}
	got := a.Sightings(node)
	if len(got) != 2 || got[0].Radio != "second" || !got[0].ViaMQTT || got[1].Radio != "main" {
		t.Fatalf("site sightings = %+v", got)
	}
}

func TestRemoteReceivedEdges(t *testing.T) {
	h, id, _ := remoteHost(t)
	events, stop := h.Bus.Subscribe(64)
	defer stop()
	virtual, _ := NewIdentity(nil, "V", "V")
	h.RemoteReceived(virtual, &pb.MeshPacket{From: 5})
	h.RemoteReceived(id, nil)
	if n := h.Counters.Rx.Load(); n != 0 {
		t.Fatalf("rx counted %d for ignored packets", n)
	}

	// Opaque packets are logged as undecryptable.
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: wire.Broadcast, Id: 1,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1}}})
	if r := nextEvent(t, events, "packet").Data.(PacketRecord); r.Kind != "undecryptable" {
		t.Fatalf("opaque record = %+v", r)
	}

	// The node's own client traffic is logged as local; channel text is stored.
	h.RemoteReceived(id, &pb.MeshPacket{From: remoteNum, To: wire.Broadcast, Id: 2, Channel: 1,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("mine")}}})
	if r := nextEvent(t, events, "packet").Data.(PacketRecord); r.Kind != "local" || r.Channel != "Ops" {
		t.Fatalf("own record = %+v", r)
	}
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: wire.Broadcast, Id: 3, Channel: 6,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hey")}}})
	if r := nextEvent(t, events, "packet").Data.(PacketRecord); r.Channel != "" {
		t.Fatalf("unknown channel named %q", r.Channel)
	}
	msgs := h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 10)
	if len(msgs) != 1 || msgs[0].To != "!ffffffff" || msgs[0].Text != "hey" {
		t.Fatalf("messages = %+v", msgs)
	}

	// A bad traceroute payload publishes nothing.
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: remoteNum, Id: 4,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TRACEROUTE_APP, Payload: []byte{0xff}, RequestId: 1}}})
	nextEvent(t, events, "packet")
	select {
	case e := <-events:
		if e.Type == "traceroute" {
			t.Fatal("traceroute published for a bad payload")
		}
	default:
	}
}

func TestRemoteNAK(t *testing.T) {
	h, id, _ := remoteHost(t)
	pid, err := h.SendText(id, 0x0badcafe, 0, "dm", true)
	if err != nil {
		t.Fatal(err)
	}
	nak, _ := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: pb.Routing_MAX_RETRANSMIT}})
	h.RemoteReceived(id, &pb.MeshPacket{From: 0x0badcafe, To: remoteNum, Id: 5,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: nak, RequestId: pid}}})
	m := h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 10)
	if m[0].Status != "failed" || m[0].Error != "MAX_RETRANSMIT" || h.Counters.AckFail.Load() != 1 {
		t.Fatalf("after NAK %+v", m)
	}
	recs := h.Packets.List(10, 0, func(r *PacketRecord) bool { return r.Direction == "tx" })
	if len(recs) != 1 || recs[0].Channel != "PKI" || recs[0].Kind != "sent" {
		t.Fatalf("sent record = %+v", recs)
	}
}

func TestSendErrors(t *testing.T) {
	h, id, fr := remoteHost(t)
	if err := h.Send(id, &pb.MeshPacket{}); err == nil {
		t.Error("packet without payload sent")
	}
	err := h.Send(id, &pb.MeshPacket{Channel: MaxChannels, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}})
	var re *RoutingError
	if !errors.As(err, &re) || re.Reason != pb.Routing_NO_CHANNEL || !strings.Contains(err.Error(), "NO_CHANNEL") {
		t.Errorf("bad channel: %v", err)
	}
	if _, err := h.SendText(id, 1, 0, "", false); err == nil {
		t.Error("empty text sent")
	}
	if _, err := h.SendText(id, 1, 0, strings.Repeat("x", 201), false); err == nil {
		t.Error("long text sent")
	}
	if err := h.sendRemote(id, fr, &pb.MeshPacket{}); err == nil {
		t.Error("encrypted packet handed to a node")
	}
	// Non-text sends that fail don't touch the message store.
	fr.mu.Lock()
	fr.fail = true
	fr.mu.Unlock()
	if err := h.Send(id, &pb.MeshPacket{PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_POSITION_APP}}}); err == nil {
		t.Error("send to a missing node succeeded")
	}
	if err := h.Traceroute(id, 5); !errors.As(err, &re) || re.Reason != pb.Routing_NO_INTERFACE {
		t.Errorf("traceroute to a missing node: %v", err)
	}
	if err := h.Traceroute(id, 5); !errors.As(err, &re) || re.Reason != pb.Routing_RATE_LIMIT_EXCEEDED {
		t.Errorf("traceroute not rate limited: %v", err)
	}
}

func TestSendDefaultsAndHostedLogging(t *testing.T) {
	h, id, fr := remoteHost(t)
	h.AddAirTap(nopTap{}) // with an air bridge the host logs frames as it transmits them
	p := &pb.MeshPacket{HopLimit: 99, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_POSITION_APP}}}
	if err := h.Send(id, p); err != nil {
		t.Fatal(err)
	}
	if p.Id == 0 || p.To != wire.Broadcast || p.HopLimit != h.Config().HopLimit || len(fr.packets()) != 1 {
		t.Fatalf("defaults = %v", p)
	}
	if n := len(h.Packets.List(10, 0, nil)); n != 0 {
		t.Fatalf("%d records logged before transmission", n)
	}
}
