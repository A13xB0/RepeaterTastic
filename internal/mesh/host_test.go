package mesh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func TestRelayRoles(t *testing.T) {
	keep := pb.Config_DeviceConfig_TRACKER
	for _, c := range []struct {
		in    string
		valid bool
		want  pb.Config_DeviceConfig_Role
	}{
		{" Client ", true, pb.Config_DeviceConfig_CLIENT},
		{"client_base", true, pb.Config_DeviceConfig_CLIENT_BASE},
		{"MUTE", true, pb.Config_DeviceConfig_CLIENT_MUTE},
		{"router", true, pb.Config_DeviceConfig_ROUTER},
		{"router_late", true, pb.Config_DeviceConfig_ROUTER_LATE},
		{"monitor", true, keep},
		{"off", true, keep},
		{"repeater", false, keep},
	} {
		if got := ValidRelayRole(c.in); got != c.valid {
			t.Errorf("ValidRelayRole(%q) = %v", c.in, got)
		}
		if got := DeviceRole(c.in, keep); got != c.want {
			t.Errorf("DeviceRole(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRebroadcastMode(t *testing.T) {
	for name, want := range map[string]pb.Config_DeviceConfig_RebroadcastMode{
		"": pb.Config_DeviceConfig_ALL, "LOCAL_ONLY": pb.Config_DeviceConfig_LOCAL_ONLY, "none": pb.Config_DeviceConfig_NONE,
	} {
		if got, ok := RebroadcastMode(name); !ok || got != want {
			t.Errorf("RebroadcastMode(%q) = %v, %v", name, got, ok)
		}
	}
	if _, ok := RebroadcastMode("some"); ok {
		t.Error("unknown mode accepted")
	}
}

func TestHostAccessors(t *testing.T) {
	h, r, _ := virtualHost(t, Config{RelayRole: "router"})
	if h.Radio() != r || h.RadioID() != "main" || h.Started().IsZero() || h.RadioConfigured() || h.QueueLen() != 0 {
		t.Fatal("fresh host accessors wrong")
	}
	if h.Relay().UserCopy().Role != pb.Config_DeviceConfig_ROUTER {
		t.Fatal("relay persona didn't take the router role")
	}
	if err := h.SetRelayRole("pilot"); err == nil {
		t.Fatal("unknown relay role accepted")
	}
	if !h.Transmits() {
		t.Fatal("router doesn't transmit")
	}
	for _, role := range []string{RoleMonitor, RoleOff} {
		if err := h.SetRelayRole(role); err != nil || h.Transmits() {
			t.Fatalf("%s: err %v, transmits %v", role, err, h.Transmits())
		}
	}
	g := &fakeGate{}
	h.SetTxGate(g)
	if h.txGate() != g {
		t.Fatal("gate not set")
	}
	h.SetTxGate(nil)
	if h.txGate() != nil {
		t.Fatal("gate not removed")
	}
	if _, err := NewHost(Config{Region: "MARS"}, r, nil); err == nil {
		t.Fatal("unknown region accepted")
	}
}

func TestHostIdentities(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	if err := h.AddIdentity(relay); err == nil {
		t.Fatal("duplicate identity added")
	}
	clash, _ := NewRemoteIdentity(&fakeRemote{}, RemoteState{NodeNum: relay.NodeNum ^ 0xff00})
	if err := h.AddIdentity(clash); err == nil {
		t.Fatal("identity sharing a last byte added")
	}
	second, _ := NewRemoteIdentity(&fakeRemote{}, RemoteState{NodeNum: relay.NodeNum + 2})
	second.IsRelay = true
	if err := h.AddIdentity(second); err == nil {
		t.Fatal("second relay persona added")
	}
	a := addVirtual(t, h, "A")
	time.Sleep(time.Millisecond)
	b := addVirtual(t, h, "B")
	ids := h.Identities()
	if len(ids) != 3 || ids[0] != relay || ids[1] != a || ids[2] != b {
		t.Fatal("identities not relay first, then by age")
	}
	if err := h.RemoveIdentity(relay.NodeNum); err == nil {
		t.Fatal("relay persona removed")
	}
	if err := h.RemoveIdentity(0x1234); err == nil {
		t.Fatal("unknown identity removed")
	}
	if err := h.RemoveIdentity(a.NodeNum); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.DB.Get(a.NodeNum); ok || h.Identity(a.NodeNum) != nil {
		t.Fatal("removed identity left behind")
	}
}

type fakeApplier struct {
	mu   sync.Mutex
	got  []Config
	fail error
}

func (f *fakeApplier) ApplyConfig(_ context.Context, cfg Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, cfg)
	return f.fail
}

func TestUpdateConfig(t *testing.T) {
	h, r, relay := virtualHost(t, Config{RadioID: "north"})
	ctx := context.Background()
	ok, bad := &fakeApplier{}, &fakeApplier{fail: errors.New("rebooting")}
	h.AddConfigApplier(ok)
	cfg := h.Config()
	cfg.PrimaryChannel, cfg.RadioID, cfg.HopLimit, cfg.NodeInfoInterval = "Mast", "", 0, 0
	if err := h.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	got := h.Config()
	if got.RadioID != "north" || got.HopLimit != defaultHopLimit || got.NodeInfoInterval != 3*time.Hour ||
		relay.ChannelCopy(0).GetSettings().GetName() != "Mast" || len(ok.got) != 1 || ok.got[0].PrimaryChannel != "Mast" {
		t.Fatalf("config after update = %+v", got)
	}
	if r.configureCount() != 0 { // EU_868 LongFast has one slot: the name doesn't move the frequency
		t.Fatalf("radio retuned %d times for a new channel name", r.configureCount())
	}

	h.AddConfigApplier(bad)
	if err := h.UpdateConfig(ctx, cfg); err == nil || !errors.Is(err, bad.fail) {
		t.Fatalf("failing applier: %v", err)
	}
	h.RemoveConfigApplier(bad)
	h.RemoveConfigApplier(&fakeApplier{}) // not registered
	if err := h.PushConfig(ctx); err != nil {
		t.Fatal(err)
	}

	cfg.Region = "MARS"
	if err := h.UpdateConfig(ctx, cfg); err == nil {
		t.Fatal("unknown region accepted")
	}
	cfg.Region, cfg.Preset = "EU_868", pb.Config_LoRaConfig_SHORT_FAST
	r.set(func(r *fakeRadio) { r.configureErr = errors.New("modem gone") })
	if err := h.UpdateConfig(ctx, cfg); err == nil || r.configureCount() != 1 {
		t.Fatalf("radio rejection not reported: %v", err)
	}
	if h.Config().Preset != pb.Config_LoRaConfig_SHORT_FAST {
		t.Fatal("settings not kept when the radio rejected them")
	}
}

func TestDutyLimit(t *testing.T) {
	h, _, _ := virtualHost(t, Config{})
	region := h.dutyLimit()
	if region <= 0 || region >= 100 {
		t.Fatalf("EU_868 duty cycle = %v", region)
	}
	cfg := h.Config()
	cfg.DutyCyclePct = 3
	_ = h.UpdateConfig(context.Background(), cfg)
	if h.dutyLimit() != 3 {
		t.Fatal("configured duty cycle ignored")
	}
	cfg.OverrideDutyCycle = true
	_ = h.UpdateConfig(context.Background(), cfg)
	if h.dutyLimit() != 100 {
		t.Fatal("override ignored")
	}
}

// encryptedItem is a queued packet from origin on the host's primary channel.
func encryptedItem(t *testing.T, h *Host, from *Identity, id uint32, relay bool) *txItem {
	t.Helper()
	hash, key, _, _ := h.ChannelKey(h.Relay(), 0)
	d := &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("on air")}
	plain, _ := proto.Marshal(d)
	src := from.NodeNum
	p := &pb.MeshPacket{From: src, To: wire.Broadcast, Id: id, Channel: uint32(hash), HopLimit: 3, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: wire.AESCTR(key, src, id, plain)}}
	it := &txItem{key: pktKey{src, id}, pkt: p, due: time.Now(), relay: relay}
	if !relay {
		it.origin, it.plain = src, d
	}
	return it
}

func queueMessage(h *Host, id *Identity, pid uint32) {
	h.Messages.Add(id.NodeNum, &Message{ID: pid, Direction: "out", Status: "queued", To: "!ffffffff"})
}

func messageStatus(h *Host, id *Identity, pid uint32) string {
	for _, m := range h.Messages.List(id.NodeNum, id.NodeID(), "", 0, 100) {
		if m.ID == pid {
			return m.Status + ":" + m.Error
		}
	}
	return ""
}

type plainLink struct {
	mu    sync.Mutex
	plain []*pb.Data
}

func (l *plainLink) Name() string              { return "plain" }
func (l *plainLink) SendPacket(*pb.MeshPacket) {}
func (l *plainLink) SendPacketPlain(_ *pb.MeshPacket, d *pb.Data) {
	l.mu.Lock()
	l.plain = append(l.plain, d)
	l.mu.Unlock()
}

type countLink struct{ n int }

func (l *countLink) Name() string              { return "count" }
func (l *countLink) SendPacket(*pb.MeshPacket) { l.n++ }

func TestTransmitOwnPacket(t *testing.T) {
	h, r, relay := virtualHost(t, Config{})
	tap := &recordingTap{}
	h.AddAirTap(tap)
	pl, cl := &plainLink{}, &countLink{}
	h.AddLink(pl)
	h.AddLink(cl)
	it := encryptedItem(t, h, relay, 100, false)
	queueMessage(h, relay, 100)

	if !h.transmitNext(context.Background(), it) {
		t.Fatal("transmitNext stopped")
	}
	if len(r.sentFrames()) != 1 || h.Counters.Tx.Load() != 1 || h.Counters.Relayed.Load() != 0 {
		t.Fatal("frame not sent")
	}
	if _, tx, _ := tap.counts(); tx != 1 {
		t.Fatal("tap not told")
	}
	if len(pl.plain) != 1 || pl.plain[0].GetPortnum() != pb.PortNum_TEXT_MESSAGE_APP || cl.n != 1 {
		t.Fatal("links not given the packet")
	}
	if s := messageStatus(h, relay, 100); s != "sent:" {
		t.Fatalf("message status %q", s)
	}
	recs := h.Packets.List(1, 0, nil)
	if len(recs) != 1 || recs[0].Kind != "ours" || recs[0].Summary != "on air" || recs[0].DecodedBy != relay.NodeID() {
		t.Fatalf("record = %+v", recs)
	}
	if tx, _ := h.Air.HourTotals(time.Now()); tx <= 0 {
		t.Fatal("airtime not counted")
	}
}

func TestTransmitRelay(t *testing.T) {
	h, r, _ := virtualHost(t, Config{})
	stranger := &Identity{NodeNum: 0x0abc0123}
	it := encryptedItem(t, h, stranger, 101, true)
	h.transmitNext(context.Background(), it)
	recs := h.Packets.List(1, 0, nil)
	if len(r.sentFrames()) != 1 || h.Counters.Relayed.Load() != 1 || len(recs) != 1 ||
		recs[0].Kind != "relayed" || recs[0].Summary != "on air" {
		t.Fatalf("relay record = %+v", recs)
	}
}

func TestTransmitDutyCycle(t *testing.T) {
	h, r, relay := virtualHost(t, Config{DutyCyclePct: 1})
	h.Air.AddTx(time.Now(), 3600000, 0) // the whole hour
	it := encryptedItem(t, h, relay, 102, false)
	queueMessage(h, relay, 102)
	h.transmitNext(context.Background(), it)
	if len(r.sentFrames()) != 0 || h.Counters.DroppedDuty.Load() != 1 {
		t.Fatal("packet over the duty cycle sent")
	}
	if s := messageStatus(h, relay, 102); s != "failed:DUTY_CYCLE_LIMIT" {
		t.Fatalf("message status %q", s)
	}
	h.transmitNext(context.Background(), encryptedItem(t, h, relay, 103, true))
	if h.Counters.DroppedDuty.Load() != 2 {
		t.Fatal("relay over the duty cycle not dropped")
	}
}

func TestTransmitOffAir(t *testing.T) {
	h, r, relay := virtualHost(t, Config{RelayRole: RoleMonitor})
	it := encryptedItem(t, h, relay, 104, false)
	queueMessage(h, relay, 104)
	h.transmitNext(context.Background(), it)
	h.transmitNext(context.Background(), encryptedItem(t, h, relay, 105, true))
	if len(r.sentFrames()) != 0 {
		t.Fatal("monitor mode transmitted")
	}
	if s := messageStatus(h, relay, 104); s != "failed:NO_INTERFACE" {
		t.Fatalf("message status %q", s)
	}
}

func TestTransmitChannelBusy(t *testing.T) {
	h, r, relay := virtualHost(t, Config{})
	r.set(func(r *fakeRadio) { r.busy = true })
	it := encryptedItem(t, h, relay, 106, false)
	start := time.Now()
	h.transmitNext(context.Background(), it)
	if len(r.sentFrames()) != 0 || it.attempts != 1 || !h.txq.Contains(it.key) || !it.due.After(start) {
		t.Fatal("busy channel didn't defer the packet")
	}
	h.txq.Cancel(it.key, true)
	it.attempts = 12
	h.transmitNext(context.Background(), it)
	if len(r.sentFrames()) != 1 {
		t.Fatal("packet deferred forever")
	}
	r.set(func(r *fakeRadio) { r.busyErr = errors.New("no LBT") })
	h.transmitNext(context.Background(), encryptedItem(t, h, relay, 107, false))
	if len(r.sentFrames()) != 2 {
		t.Fatal("channel-busy error blocked the send")
	}
}

func TestTransmitFailures(t *testing.T) {
	h, r, relay := virtualHost(t, Config{})
	decoded := &txItem{pkt: &pb.MeshPacket{From: relay.NodeNum, Id: 108,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}}
	if !h.transmitNext(context.Background(), decoded) || len(r.sentFrames()) != 0 {
		t.Fatal("unencrypted packet sent")
	}
	r.set(func(r *fakeRadio) { r.sendErr = radio.ErrTxFailed })
	h.transmitNext(context.Background(), encryptedItem(t, h, relay, 109, false))
	if h.Counters.TxFailed.Load() != 1 || h.Counters.Tx.Load() != 0 {
		t.Fatal("send failure not counted")
	}
}

// fakeGate is a TxGate with a set result; before runs while the turn is being acquired.
type fakeGate struct {
	err      error
	before   func()
	acquired int
	released int
}

func (g *fakeGate) Acquire(context.Context, *Host) (func(), error) {
	g.acquired++
	if g.before != nil {
		g.before()
	}
	if g.err != nil {
		return nil, g.err
	}
	return func() { g.released++ }, nil
}

func TestTransmitSiteGate(t *testing.T) {
	h, r, relay := virtualHost(t, Config{})
	ctx := context.Background()
	g := &fakeGate{}
	h.SetTxGate(g)
	h.transmitNext(ctx, encryptedItem(t, h, relay, 110, false))
	if len(r.sentFrames()) != 1 || g.acquired != 1 || g.released != 1 {
		t.Fatalf("gated send: sent %d, gate %+v", len(r.sentFrames()), g)
	}

	g.err = ErrSiteDutyCycle
	queueMessage(h, relay, 111)
	if !h.transmitNext(ctx, encryptedItem(t, h, relay, 111, false)) || h.Counters.DroppedDuty.Load() != 1 {
		t.Fatal("site duty cycle didn't drop the packet")
	}
	if s := messageStatus(h, relay, 111); s != "failed:DUTY_CYCLE_LIMIT" {
		t.Fatalf("message status %q", s)
	}

	g.err = context.Canceled
	if h.transmitNext(ctx, encryptedItem(t, h, relay, 112, false)) {
		t.Fatal("cancelled wait for the site's turn didn't stop the loop")
	}

	g.err = nil
	g.before = func() { _ = h.SetRelayRole(RoleOff) }
	if !h.transmitNext(ctx, encryptedItem(t, h, relay, 113, false)) || len(r.sentFrames()) != 1 || g.released != 2 {
		t.Fatalf("switched off while waiting: sent %d, gate %+v", len(r.sentFrames()), g)
	}
}

func TestQueueHostedAndLogInternal(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	if err := h.QueueHosted(&pb.MeshPacket{PayloadVariant: &pb.MeshPacket_Decoded{}}, nil, 1); !errors.Is(err, wire.ErrNotEncrypted) {
		t.Fatalf("decoded packet queued: %v", err)
	}
	relayed := encryptedItem(t, h, &Identity{NodeNum: 0x0abc0123}, 200, true)
	if err := h.QueueHosted(relayed.pkt, nil, 0x0abc0999); err != nil { // another hosted node relays it
		t.Fatal(err)
	}
	if !h.hist.WeRelayed(relayed.key) || h.QueueLen() != 1 {
		t.Fatal("relay not marked or not queued")
	}
	for i := uint32(1); h.QueueLen() < 64; i++ {
		if err := h.QueueHosted(encryptedItem(t, h, relay, 300+i, false).pkt, nil, relay.NodeNum); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.QueueHosted(encryptedItem(t, h, relay, 999, false).pkt, nil, relay.NodeNum); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue: %v", err)
	}

	queueMessage(h, relay, 301)
	if n := h.DropOutgoing(relay.NodeNum, "moving"); n != 1 || h.QueueLen() != 1 {
		t.Fatalf("dropped %d, queue %d", n, h.QueueLen())
	}
	if s := messageStatus(h, relay, 301); s != "failed:moving" {
		t.Fatalf("message status %q", s)
	}

	p := &pb.MeshPacket{From: relay.NodeNum, To: 0x1234, Id: 5}
	h.LogInternal(p, nil)
	h.LogInternal(p, &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("next door")})
	recs := h.Packets.List(2, 0, nil)
	if len(recs) != 2 || recs[0].Summary != "next door" || recs[0].Port != "TEXT_MESSAGE_APP" ||
		!recs[1].PKI || recs[1].Kind != "local" || recs[1].Transport != "internal" {
		t.Fatalf("internal records = %+v", recs)
	}
}

func TestMonitorFailsQueued(t *testing.T) {
	h, _, relay := virtualHost(t, Config{})
	if err := h.QueueHosted(encryptedItem(t, h, relay, 400, false).pkt, nil, relay.NodeNum); err != nil {
		t.Fatal(err)
	}
	queueMessage(h, relay, 400)
	if err := h.SetRelayRole(RoleOff); err != nil {
		t.Fatal(err)
	}
	if s := messageStatus(h, relay, 400); s != "failed:the radio is off" || h.QueueLen() != 0 {
		t.Fatalf("message status %q, queue %d", s, h.QueueLen())
	}
}

func TestRunRequiresRelay(t *testing.T) {
	h, err := NewHost(Config{Region: "EU_868"}, newFakeRadio(), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("ran without a relay persona")
	}
}

// runHost runs h until the returned stop func is called.
func runHost(t *testing.T, h *Host) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			t.Fatal("Run didn't stop")
			return nil
		}
	}
}

func TestRunLoops(t *testing.T) {
	dir := t.TempDir()
	h, r, relay := virtualHost(t, Config{StateDir: dir})
	tap := &recordingTap{}
	h.AddAirTap(tap)
	stop := runHost(t, h)

	waitFor(t, "radio configured", h.RadioConfigured)
	// A frame from the queue goes on air.
	if err := h.QueueHosted(encryptedItem(t, h, relay, 500, false).pkt, nil, relay.NodeNum); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.sentCh:
	case <-time.After(2 * time.Second):
		t.Fatal("queued frame not transmitted")
	}
	// Frames from the radio reach the taps and the receive pipeline; junk is counted.
	heard := encryptedItem(t, h, &Identity{NodeNum: 0x0abc0123}, 501, true)
	frame, _ := wire.EncodeFrame(heard.pkt)
	r.frames <- radio.Frame{Data: frame, RSSI: -100, SNR: 2, At: time.Now()}
	r.frames <- radio.Frame{Data: []byte{1, 2}, At: time.Now()}
	waitFor(t, "frames handled", func() bool { return h.Counters.RxBad.Load() == 1 && h.Counters.Rx.Load() == 1 })
	if heardN, _, _ := tap.counts(); heardN != 2 {
		t.Fatalf("tap heard %d frames", heardN)
	}
	queueMessage(h, relay, 9) // something to save

	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("run ended with %v", err)
	}
	for _, f := range []string{nodeDBFile, messagesFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not saved: %v", f, err)
		}
	}

	// A restart loads the saved state and keeps its own entries local.
	h2, _, _ := virtualHost(t, Config{StateDir: dir})
	stop2 := runHost(t, h2)
	waitFor(t, "state loaded", func() bool { _, ok := h2.DB.Get(0x0abc0123); return ok })
	_ = stop2()
	if s := h2.Messages.List(relay.NodeNum, relay.NodeID(), "", 0, 10); len(s) != 1 || s[0].Status != "failed" {
		t.Fatalf("restored messages = %+v", s)
	}
}

func TestRunRadioOff(t *testing.T) {
	h, r, _ := virtualHost(t, Config{RelayRole: RoleOff})
	tap := &recordingTap{}
	h.AddAirTap(tap)
	stop := runHost(t, h)
	r.frames <- radio.Frame{Data: []byte{1, 2}, At: time.Now()}
	waitFor(t, "frame airtime", func() bool { _, rx := h.Air.HourTotals(time.Now()); return rx > 0 })
	close(r.frames) // the rx loop ends with the radio
	_ = stop()
	if heard, _, _ := tap.counts(); heard != 0 || h.Counters.RxBad.Load() != 0 {
		t.Fatal("a radio that is off handled a frame")
	}
}

func TestRunBadState(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{nodeDBFile, messagesFile} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, r, _ := virtualHost(t, Config{StateDir: dir})
	r.set(func(r *fakeRadio) { r.configureErr = errors.New("no modem") })
	stop := runHost(t, h)
	waitFor(t, "configure attempt", func() bool { return r.configureCount() > 0 })
	_ = stop()
	if h.RadioConfigured() {
		t.Fatal("radio reported configured")
	}
}
