package mesh

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

const stranger = 0x0abc0123

// hdr is a test packet's addressing.
type hdr struct{ from, to, id uint32 }

// onChannel encrypts d on the identity's channel slot.
func onChannel(t *testing.T, h *Host, holder *Identity, slot int, a hdr, d *pb.Data) *pb.MeshPacket {
	t.Helper()
	from, to, id := a.from, a.to, a.id
	hash, key, aead, ok := h.ChannelKey(holder, slot)
	if !ok {
		t.Fatalf("no channel %d", slot)
	}
	plain, _ := proto.Marshal(d)
	enc := wire.AESCTR(key, from, id, plain)
	if aead {
		var err error
		if enc, err = wire.AEADEncrypt(key, from, to, id, plain); err != nil {
			t.Fatal(err)
		}
	}
	return &pb.MeshPacket{From: from, To: to, Id: id, Channel: uint32(hash), HopLimit: 3, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: enc}}
}

// hear passes p through the radio path and returns the packet log's newest record.
func hear(t *testing.T, h *Host, p *pb.MeshPacket) PacketRecord {
	t.Helper()
	frame, err := wire.EncodeFrame(p)
	if err != nil {
		t.Fatal(err)
	}
	h.HandleReceived(wire.DecodeFrame(frame, -90, 4), frame)
	recs := h.Packets.List(1, 0, nil)
	if len(recs) != 1 {
		t.Fatal("nothing logged")
	}
	return recs[0]
}

func textData(s string) *pb.Data {
	return &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(s)}
}

// pkiPacket is a DM from sender (priv) to the identity.
func pkiPacket(t *testing.T, priv []byte, from uint32, to *Identity, id uint32, d *pb.Data) *pb.MeshPacket {
	t.Helper()
	plain, _ := proto.Marshal(d)
	enc, err := wire.PKIEncrypt(priv, to.PublicKey, from, id, plain, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &pb.MeshPacket{From: from, To: to.NodeNum, Id: id, HopLimit: 3, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: enc}}
}

func TestReceiveKinds(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	for _, c := range []struct {
		name string
		pkt  func() *pb.MeshPacket
		kind string
	}{
		{"bad hops", func() *pb.MeshPacket {
			p := onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 1}, textData("x"))
			p.HopStart = 2
			return p
		}, "bad"},
		{"echo", func() *pb.MeshPacket {
			return onChannel(t, h, relay, 0, hdr{relay.NodeNum, wire.Broadcast, 2}, textData("me"))
		}, "echo"},
		{"unknown channel", func() *pb.MeshPacket {
			p := onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 3}, textData("x"))
			p.Channel++
			return p
		}, "undecryptable"},
		{"pre-2.3", func() *pb.MeshPacket {
			p := onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 4}, textData("old"))
			p.HopStart = 0
			return p
		}, "legacy"},
		{"legacy DM", func() *pb.MeshPacket {
			return onChannel(t, h, relay, 0, hdr{stranger, relay.NodeNum, 5}, textData("psst"))
		}, "undecryptable"},
		{"DM from an unknown key", func() *pb.MeshPacket {
			priv := wire.GeneratePrivateKey()
			return pkiPacket(t, priv, stranger, relay, 6, textData("who am I"))
		}, "undecryptable"},
	} {
		if r := hear(t, h, c.pkt()); r.Kind != c.kind {
			t.Errorf("%s: kind %q, want %q", c.name, r.Kind, c.kind)
		}
	}
	if r := h.Packets.List(5, 0, func(r *PacketRecord) bool { return r.Kind == "echo" }); len(r) != 1 || r[0].Summary != "me" {
		t.Errorf("echo not described: %+v", r)
	}
	if h.Counters.RxBad.Load() != 1 || h.Counters.RxUndecryptable.Load() != 3 {
		t.Errorf("counters bad %d, undecryptable %d", h.Counters.RxBad.Load(), h.Counters.RxUndecryptable.Load())
	}
}

func TestReceivePKI(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	priv := wire.GeneratePrivateKey()
	pub, _ := wire.PublicKey(priv)
	h.DB.SetUser(stranger, &pb.User{LongName: "Sender", PublicKey: pub})

	r := hear(t, h, pkiPacket(t, priv, stranger, relay, 10, textData("secret")))
	if r.Kind != "delivered" || r.Channel != "PKI" || !r.PKI || r.Summary != "secret" || r.DecodedBy != relay.NodeID() {
		t.Fatalf("PKI record = %+v", r)
	}
	// Authenticated but carrying no usable payload.
	if r := hear(t, h, pkiPacket(t, priv, stranger, relay, 11, &pb.Data{Payload: []byte("no port")})); r.Kind != "undecryptable" {
		t.Fatalf("empty PKI payload logged as %q", r.Kind)
	}
	// Failing authentication.
	bad := pkiPacket(t, priv, stranger, relay, 12, textData("tampered"))
	bad.GetEncrypted()[0] ^= 0xff
	if r := hear(t, h, bad); r.Kind != "undecryptable" {
		t.Fatalf("tampered PKI logged as %q", r.Kind)
	}

	// A DM between two of the host's identities comes back as an echo the host can read.
	other := addVirtual(t, h, "Other")
	echo := hear(t, h, pkiPacket(t, other.PrivateKey, other.NodeNum, relay, 13, textData("next door")))
	if echo.Kind != "echo" || echo.Summary != "next door" || echo.Channel != "PKI" {
		t.Fatalf("local DM echo = %+v", echo)
	}
}

func TestReceiveAEADChannel(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	psk := make([]byte, 16)
	psk[0] = 9
	if err := h.SetChannel(relay, &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY,
		Settings: &pb.ChannelSettings{Name: "Sealed", Psk: psk, UseAead: true}}); err != nil {
		t.Fatal(err)
	}
	r := hear(t, h, onChannel(t, h, relay, 1, hdr{stranger, wire.Broadcast, 20}, textData("sealed")))
	if r.Kind != "delivered" || r.Channel != "Sealed" || r.Summary != "sealed" {
		t.Fatalf("AEAD record = %+v", r)
	}
	p := onChannel(t, h, relay, 1, hdr{stranger, wire.Broadcast, 21}, textData("forged"))
	p.GetEncrypted()[0] ^= 0xff
	if r := hear(t, h, p); r.Kind != "undecryptable" {
		t.Fatalf("forged AEAD packet logged as %q", r.Kind)
	}
}

func TestReceiveSniffs(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	events, stop := h.Bus.Subscribe(64)
	defer stop()

	pos, _ := proto.Marshal(&pb.Position{LatitudeI: proto.Int32(561234567), LongitudeI: proto.Int32(-31234567)})
	r := hear(t, h, onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 30}, &pb.Data{Portnum: pb.PortNum_POSITION_APP, Payload: pos}))
	if r.Summary != "56.12346, -3.12346" || r.Payload["latitude_i"] == nil {
		t.Fatalf("position record = %+v", r)
	}
	if e := nextEvent(t, events, "node"); e.Data != wire.NodeID(stranger) {
		t.Fatalf("node event = %+v", e)
	}
	e, _ := h.DB.Get(stranger)
	if e.Position.GetLatitudeI() != 561234567 || e.Position.Time == 0 {
		t.Fatalf("sniffed position = %v", e.Position)
	}

	empty, _ := proto.Marshal(&pb.Position{})
	hear(t, h, onChannel(t, h, relay, 0, hdr{0x0abc0456, wire.Broadcast, 31}, &pb.Data{Portnum: pb.PortNum_POSITION_APP, Payload: empty}))
	if e, _ := h.DB.Get(0x0abc0456); e.Position != nil {
		t.Fatal("position request stored as a position")
	}

	tel, _ := proto.Marshal(&pb.Telemetry{Variant: &pb.Telemetry_DeviceMetrics{DeviceMetrics: &pb.DeviceMetrics{BatteryLevel: proto.Uint32(55)}}})
	hear(t, h, onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 32}, &pb.Data{Portnum: pb.PortNum_TELEMETRY_APP, Payload: tel}))
	if e, _ := h.DB.Get(stranger); e.Metrics.GetBatteryLevel() != 55 {
		t.Fatalf("sniffed metrics = %v", e.Metrics)
	}
}

func TestReceiveRoutingSniff(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	const asker = 0x0abc0777
	neighbour := uint32(0x0abc0888)
	if wire.LastByte(relay.NodeNum) == wire.LastByte(neighbour) {
		neighbour++ // the relay's own byte would make the neighbour ambiguous
	}
	orig := pktKey{asker, 0x5000}
	h.hist.MarkTx(orig, 3, 0, time.Now())
	h.DB.Update(neighbour, func(*NodeEntry) {})
	h.txq.Enqueue(&txItem{key: orig, pkt: &pb.MeshPacket{Id: orig.ID}, due: time.Now().Add(time.Hour), relay: true})

	reply := onChannel(t, h, relay, 0, hdr{stranger, asker, 40}, &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP,
		Payload: []byte("answer"), RequestId: orig.ID})
	reply.RelayNode = uint32(wire.LastByte(neighbour))
	hear(t, h, reply)
	if e, _ := h.DB.Get(stranger); e.NextHop != wire.LastByte(neighbour) {
		t.Fatalf("next hop = %x", e.NextHop)
	}
	if h.txq.Contains(orig) {
		t.Fatal("relay of an answered request not cancelled")
	}
}

func queueRelay(h *Host, k pktKey, hopLimit uint32) {
	h.txq.Enqueue(&txItem{key: k, pkt: &pb.MeshPacket{From: k.From, Id: k.ID, HopLimit: hopLimit},
		due: time.Now().Add(time.Hour), relay: true})
}

func TestReceiveDuplicates(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	now := time.Now()

	// A copy with more hops left replaces a queued relay with fewer.
	up := pktKey{stranger, 50}
	h.hist.Observe(up, 1, 0, 0, 0, now)
	queueRelay(h, up, 1)
	p := onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 50}, textData("x"))
	p.HopLimit = 2
	if r := hear(t, h, p); r.Kind != "dup" || r.Summary != "x" {
		t.Fatalf("upgraded copy = %+v", r)
	}
	if h.txq.Contains(up) {
		t.Fatal("lower-hop relay kept")
	}

	// Someone else relayed first: our waiting relay stands down.
	same := pktKey{stranger, 51}
	h.hist.Observe(same, 3, 0, 0, 0, now)
	queueRelay(h, same, 3)
	hear(t, h, onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 51}, textData("y")))
	if h.txq.Contains(same) || h.Counters.RelayCancelled.Load() != 1 || h.Counters.RxDupe.Load() != 2 {
		t.Fatal("duplicate didn't cancel the relay")
	}

	// We were the chosen next hop: keep relaying.
	next := pktKey{stranger, 52}
	h.hist.Observe(next, 3, 0, wire.LastByte(relay.NodeNum), 0, now)
	queueRelay(h, next, 3)
	hear(t, h, onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 52}, textData("z")))
	if !h.txq.Contains(next) {
		t.Fatal("next-hop relay cancelled")
	}
}

func TestRouterKeepsRelays(t *testing.T) {
	h, _, relay := virtualHost(t, Config{RelayRole: RoleRouter})
	k := pktKey{stranger, 60}
	h.hist.Observe(k, 3, 0, 0, 0, time.Now())
	queueRelay(h, k, 3)
	hear(t, h, onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 60}, textData("r")))
	if !h.txq.Contains(k) {
		t.Fatal("router cancelled its relay")
	}
}

func TestPacketCopiesAndTaps(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	other := addVirtual(t, h, "Other")
	tap := &recordingTap{}
	h.AddAirTap(tap)
	h.PacketCopies.Store(true)
	events, stop := h.Bus.Subscribe(16)
	defer stop()

	p := onChannel(t, h, relay, 0, hdr{stranger, wire.Broadcast, 70}, textData("copy"))
	h.HandleReceived(p, nil) // from a link
	rec := nextEvent(t, events, "packet").Data.(PacketRecord)
	if rec.Mesh == nil || rec.Data.GetPortnum() != pb.PortNum_TEXT_MESSAGE_APP || len(rec.Holders) != 2 {
		t.Fatalf("plugin copy = %+v", rec)
	}
	if rec.Mesh == p || rec.Holders[0].NodeNum != relay.NodeNum || !rec.Holders[0].Relay || rec.Holders[1].NodeNum != other.NodeNum {
		t.Fatalf("copy shares the packet or misses a holder: %+v", rec.Holders)
	}
	if _, _, link := tap.counts(); link != 1 {
		t.Fatalf("link tap told %d times", link)
	}
	if logged := h.Packets.List(1, 0, nil)[0]; logged.Mesh != nil || logged.Size != wire.HeaderLen+len(p.GetEncrypted()) {
		t.Fatalf("logged record = %+v", logged)
	}
}

func TestReceiveWithoutRelay(t *testing.T) {
	h, err := NewHost(Config{Region: "EU_868"}, newFakeRadio(), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	id := addVirtual(t, h, "Solo")
	if h.relayLastByte() != 0 {
		t.Fatal("relay byte without a relay")
	}
	if r := hear(t, h, onChannel(t, h, id, 0, hdr{stranger, wire.Broadcast, 80}, textData("hi"))); r.Kind != "delivered" {
		t.Fatalf("record = %+v", r)
	}
}

func TestSummaries(t *testing.T) {
	marshal := func(m proto.Message) []byte { b, _ := proto.Marshal(m); return b }
	route := marshal(&pb.RouteDiscovery{Route: []uint32{1, 2}, RouteBack: []uint32{3}})
	bad := []byte{0xff}
	for _, c := range []struct {
		d    *pb.Data
		want string
	}{
		{nil, ""},
		{&pb.Data{Portnum: pb.PortNum_POSITION_APP, Payload: marshal(&pb.Position{})}, "position request"},
		{&pb.Data{Portnum: pb.PortNum_ROUTING_APP, RequestId: 0xab, Payload: marshal(&pb.Routing{
			Variant: &pb.Routing_ErrorReason{ErrorReason: pb.Routing_NO_ROUTE}})}, "NAK NO_ROUTE for 000000ab"},
		{&pb.Data{Portnum: pb.PortNum_ROUTING_APP, Payload: bad}, ""},
		{&pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: bad}, ""},
		{&pb.Data{Portnum: pb.PortNum_TELEMETRY_APP, Payload: marshal(&pb.Telemetry{Variant: &pb.Telemetry_DeviceMetrics{
			DeviceMetrics: &pb.DeviceMetrics{BatteryLevel: proto.Uint32(90), Voltage: proto.Float32(4.1)}}})},
			"battery 90% 4.10V ch 0.0% air 0.0%"},
		{&pb.Data{Portnum: pb.PortNum_TELEMETRY_APP, Payload: marshal(&pb.Telemetry{Variant: &pb.Telemetry_EnvironmentMetrics{
			EnvironmentMetrics: &pb.EnvironmentMetrics{Temperature: proto.Float32(12.5)}}})}, "12.5°C 0% 0hPa"},
		{&pb.Data{Portnum: pb.PortNum_TELEMETRY_APP, Payload: marshal(&pb.Telemetry{})}, ""},
		{&pb.Data{Portnum: pb.PortNum_TELEMETRY_APP, Payload: bad}, ""},
		{&pb.Data{Portnum: pb.PortNum_TRACEROUTE_APP, Payload: route}, "traceroute, 2 hops so far"},
		{&pb.Data{Portnum: pb.PortNum_TRACEROUTE_APP, Payload: route, RequestId: 1}, "traceroute reply, 2 hops out, 1 back"},
		{&pb.Data{Portnum: pb.PortNum_TRACEROUTE_APP, Payload: bad}, ""},
		{&pb.Data{Portnum: pb.PortNum_PRIVATE_APP}, ""},
	} {
		if got := summarize(c.d); got != c.want {
			t.Errorf("summarize(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestPayloadJSON(t *testing.T) {
	way, _ := proto.Marshal(&pb.Waypoint{Name: "Cairn"})
	if m := payloadJSON(&pb.Data{Portnum: pb.PortNum_WAYPOINT_APP, Payload: way, RequestId: 9}); m["name"] != "Cairn" || m["_request_id"] != uint32(9) {
		t.Fatalf("waypoint = %v", m)
	}
	if m := payloadJSON(&pb.Data{Portnum: pb.PortNum_PRIVATE_APP, Payload: []byte{0xbe, 0xef}, RequestId: 3}); m["payload_hex"] != "beef" || m["request_id"] != uint32(3) {
		t.Fatalf("private = %v", m)
	}
	for _, port := range []pb.PortNum{pb.PortNum_NEIGHBORINFO_APP, pb.PortNum_ROUTING_APP, pb.PortNum_NODEINFO_APP} {
		if m := payloadJSON(&pb.Data{Portnum: port, Payload: []byte{0xff}}); m != nil {
			t.Errorf("%v with a bad payload = %v", port, m)
		}
	}
	if m := payloadJSON(&pb.Data{Portnum: pb.PortNum_TELEMETRY_APP}); m == nil || len(m) != 0 {
		t.Errorf("empty telemetry = %v", m)
	}
}

func TestPositions(t *testing.T) {
	site := FixedPosition{Latitude: 56, Longitude: -3, PrecisionBits: 16, Interval: 2 * time.Hour}
	h, _, relay := virtualHost(t, Config{Position: site})
	other := addVirtual(t, h, "Other")
	own := addVirtual(t, h, "Own")
	if err := own.SetFixedPosition(&IdentityPosition{Latitude: 10, Longitude: 20, Altitude: 5}); err != nil {
		t.Fatal(err)
	}
	own.SetPositionInterval(3600)

	if p, ok := h.PositionFor(relay); !ok || p != site {
		t.Fatalf("relay position = %+v, %v", p, ok)
	}
	if _, ok := h.PositionFor(other); ok {
		t.Fatal("site position given to a non-relay identity")
	}
	p, ok := h.PositionFor(own)
	if !ok || p.Latitude != 10 || p.Altitude != 5 || p.PrecisionBits != 16 || p.interval() != time.Hour {
		t.Fatalf("own position = %+v, %v", p, ok)
	}
	relay.SetPositionInterval(1800)
	if p, _ := h.PositionFor(relay); p.Interval != 30*time.Minute {
		t.Fatalf("relay interval = %v", p.Interval)
	}

	h.RecordOwnPositions()
	for id, want := range map[*Identity]bool{relay: true, other: false, own: true} {
		if e, _ := h.DB.Get(id.NodeNum); (e.Position != nil) != want {
			t.Errorf("%s position recorded = %v", id.User.LongName, e.Position)
		}
	}

	cfg := h.Config()
	cfg.Position.AllIdentities = true
	if err := h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.PositionFor(other); !ok {
		t.Fatal("all-identities position not given")
	}
	if (FixedPosition{}).Set() || !site.Set() || (FixedPosition{}).interval() != positionDefaultInterval || site.interval() != 2*time.Hour {
		t.Fatal("Set/interval wrong")
	}
}

func TestHardware(t *testing.T) {
	h, r, _ := virtualHost(t, Config{})
	r.set(func(r *fakeRadio) { r.name = "Heltec Wireless Tracker" })
	if got := h.Hardware(); got != pb.HardwareModel_HELTEC_WIRELESS_TRACKER {
		t.Fatalf("hardware = %v", got)
	}
	cfg := h.Config()
	cfg.HwModel = pb.HardwareModel_RAK4631
	if err := h.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := h.Hardware(); got != pb.HardwareModel_RAK4631 {
		t.Fatalf("configured hardware = %v", got)
	}
	for name, want := range map[string]pb.HardwareModel{
		"XIAO ESP32S3": pb.HardwareModel_SEEED_XIAO_S3, "LilyGo T-Echo": pb.HardwareModel_T_ECHO,
		"TBeam": pb.HardwareModel_TBEAM,
	} {
		if got := hardwareFromModem(name); got != want {
			t.Errorf("hardwareFromModem(%q) = %v", name, got)
		}
	}
}

func TestNodeInfoRequestsAndAcks(t *testing.T) {
	h, id, fr := remoteHost(t)
	h.RequestNodeInfo(id, 0x0badcafe)
	h.RequestNodeInfo(id, 0x0badcafe) // rate limited
	if n := len(fr.packets()); n != 1 {
		t.Fatalf("%d NodeInfo requests sent", n)
	}
	virtual := addVirtual(t, h, "Virtual")
	h.RequestNodeInfo(virtual, wire.Broadcast) // no node: logged, not sent
	if n := len(fr.packets()); n != 1 {
		t.Fatalf("virtual identity sent through the node: %d", n)
	}

	pid, _ := h.SendText(id, 0x0badcafe, 0, "ack me", true)
	h.failMessage(id, pid, pb.Routing_NONE)
	if s := messageStatus(h, id, pid); s != "acked:" {
		t.Fatalf("status %q", s)
	}
}

func TestSwapAndSyncEdges(t *testing.T) {
	h, id, fr := remoteHost(t)
	board, _ := NewRemoteIdentity(fr, RemoteState{NodeNum: 0x0abc0998})
	if err := h.AddIdentity(board); err != nil {
		t.Fatal(err)
	}
	virtual := addVirtual(t, h, "Virtual")
	h.SyncRemote(virtual, RemoteState{NodeNum: virtual.NodeNum, User: &pb.User{LongName: "Nope"}})
	if virtual.UserCopy().GetLongName() != "Virtual" {
		t.Fatal("virtual identity synced from a node")
	}
	loose, _ := NewRemoteIdentity(fr, RemoteState{NodeNum: 0x0abc0999})
	if err := h.SwapRemote(loose, loose); err == nil {
		t.Fatal("swapped an identity the host doesn't have")
	}
	if err := h.SwapRemote(id, board); err == nil {
		t.Fatal("swapped onto an existing identity")
	}
}

func TestRunStopsWhenGateCancels(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	h.SetTxGate(&fakeGate{err: context.Canceled})
	stop := runHost(t, h)
	if err := h.QueueHosted(encryptedItem(t, h, relay, 1, false).pkt, nil, relay.NodeNum); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "packet taken", func() bool { return h.QueueLen() == 0 })
	_ = stop()
	if h.Counters.Tx.Load() != 0 {
		t.Fatal("transmitted without the site's turn")
	}
}
