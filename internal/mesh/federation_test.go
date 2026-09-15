package mesh

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/radio/sim"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

func TestDropOutgoing(t *testing.T) {
	h, err := NewHost(Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST}, sim.NewHub(1).Attach("x", 8), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	relay, _ := NewIdentity(nil, "relay", "")
	relay.IsRelay = true
	_ = h.AddIdentity(relay)
	id, _ := NewIdentity(nil, "desk", "")
	_ = h.AddIdentity(id)
	pid, err := h.SendText(id, wire.Broadcast, 0, "stuck", false) // not running: stays queued
	if err != nil {
		t.Fatal(err)
	}
	if n := h.DropOutgoing(id.NodeNum, "moved"); n != 1 || h.QueueLen() != 0 {
		t.Fatalf("dropped %d, queue %d", n, h.QueueLen())
	}
	if m := h.Messages.List(id.NodeNum, id.NodeID(), "ch:0", 0, 10); len(m) != 1 || m[0].ID != pid || m[0].Status != "failed" {
		t.Fatalf("messages = %+v", m)
	}
}

// siteHost is a host on a preset with a relay persona and the given identities.
func siteHost(t *testing.T, ctx context.Context, hub *sim.Hub, radio string, preset pb.Config_LoRaConfig_ModemPreset, names ...string) *testHost {
	t.Helper()
	h, err := NewHost(Config{Region: "EU_868", Preset: preset, NodeInfoInterval: time.Hour, RadioID: radio},
		hub.Attach(radio, 64), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	th := &testHost{Host: h}
	mk := func(n string, relay bool) *Identity {
		for {
			id, err := NewIdentity(nil, n, "")
			if err != nil {
				continue
			}
			id.IsRelay = relay
			if err := h.AddIdentity(id); err != nil {
				continue
			}
			id.nextNodeInfo = time.Time{}
			return id
		}
	}
	mk(radio+" relay", true)
	for _, n := range names {
		th.ids = append(th.ids, mk(n, false))
	}
	go func() { _ = h.Run(ctx) }()
	return th
}

// One identity on a LongFast radio, attached to a MediumFast radio: hears MediumFast traffic, DMs a
// MediumFast-only node over the MediumFast radio, and drops back to one radio when switched off.
func TestMultiRadioIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0.02)
	lf := siteHost(t, ctx, hub, "lf", pb.Config_LoRaConfig_LONG_FAST, "Alex")
	mf := siteHost(t, ctx, hub, "mf", pb.Config_LoRaConfig_MEDIUM_FAST)
	remote := siteHost(t, ctx, hub, "far", pb.Config_LoRaConfig_MEDIUM_FAST, "Rory") // a MediumFast node elsewhere
	alex, rory := lf.ids[0], remote.ids[0]
	alexSink, rorySink := newSink(), newSink()
	alex.AddSink(alexSink)
	rory.AddSink(rorySink)

	fed := NewFederation(lf.Host, mf.Host)
	fed.SetEnabled(true)
	onMediumFast(t, lf.Host, alex, 1)
	time.Sleep(100 * time.Millisecond)
	if got := lf.RadiosOf(alex); len(got) != 2 || got[1] != "mf" {
		t.Fatalf("radios of Alex = %v", got)
	}

	// MediumFast broadcast reaches Alex through its MediumFast slot, once, recorded in lf's store
	if _, err := remote.SendText(rory, wire.Broadcast, 0, "hello mediumfast", false); err != nil {
		t.Fatal(err)
	}
	alexSink.waitPacket(t, 5*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "hello mediumfast" })
	msgs := lf.Messages.List(alex.NodeNum, alex.NodeID(), "ch:1", 0, 10)
	if len(msgs) != 1 || msgs[0].Radio != "mf" {
		t.Fatalf("lf store = %+v", msgs)
	}
	if n := len(lf.Messages.List(alex.NodeNum, alex.NodeID(), "ch:0", 0, 10)); n != 0 {
		t.Fatalf("MediumFast traffic landed on Alex's LongFast primary (%d)", n)
	}
	if n := len(mf.Messages.List(alex.NodeNum, alex.NodeID(), "ch:1", 0, 10)); n != 0 {
		t.Fatalf("message landed in the guest radio's store too (%d)", n)
	}

	// Rory is only heard on mf: a DM goes out there and is ACKed
	remote.RequestNodeInfo(rory, alex.NodeNum)
	deadline := time.Now().Add(8 * time.Second)
	lastAsk := time.Now()
	for mf.peerKey(rory.NodeNum) == nil || remote.peerKey(alex.NodeNum) == nil {
		if time.Since(lastAsk) > 2*time.Second { // a single request can be lost on the simulated air
			remote.broadcastNodeInfo(rory)
			mf.broadcastNodeInfo(alex)
			lastAsk = time.Now()
		}
		if time.Now().After(deadline) {
			t.Fatalf("keys not exchanged over the MediumFast radio: mf has rory=%v, remote has alex=%v, mf tx=%d rx=%d undecryptable=%d, lf tx=%d",
				mf.peerKey(rory.NodeNum) != nil, remote.peerKey(alex.NodeNum) != nil, mf.Counters.Tx.Load(), mf.Counters.Rx.Load(), mf.Counters.RxUndecryptable.Load(), lf.Counters.Tx.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if radios, reason := lf.RoutePreview(alex, rory.NodeNum, 0); len(radios) != 1 || radios[0] != "mf" {
		t.Fatalf("route preview = %v (%s)", radios, reason)
	}
	pid, err := lf.SendText(alex, rory.NodeNum, 0, "dm over mf", true)
	if err != nil {
		t.Fatal(err)
	}
	rorySink.waitPacket(t, 8*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "dm over mf" })
	alexSink.waitPacket(t, 8*time.Second, isAckFor(pid))
	deadline = time.Now().Add(3 * time.Second)
	for {
		m := lf.Messages.List(alex.NodeNum, alex.NodeID(), "dm:"+rory.NodeID(), 0, 10)
		if len(m) == 1 && m[0].Status == "acked" && m[0].Radio == "mf" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dm status = %+v", m)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mf.isSiteIdentity(alex.NodeNum) != true || mf.perhapsRelay(&pb.MeshPacket{From: alex.NodeNum, To: wire.Broadcast, Id: 9, HopLimit: 3}, decodeResult{}) {
		t.Fatal("the MediumFast relay would rebroadcast one of the site's own identities")
	}

	// switched off: MediumFast traffic no longer reaches Alex
	fed.SetEnabled(false)
	if _, err := remote.SendText(rory, wire.Broadcast, 0, "after off", false); err != nil {
		t.Fatal(err)
	}
	select {
	case fr := <-waitText(alexSink, "after off"):
		t.Fatalf("delivered after the switch was turned off: %v", fr)
	case <-time.After(1500 * time.Millisecond):
	}
	if radios, _ := lf.RoutePreview(alex, rory.NodeNum, 0); len(radios) != 1 || radios[0] != "lf" {
		t.Fatalf("route with the switch off = %v", radios)
	}
}

func waitText(s *sink, want string) <-chan *pb.MeshPacket {
	out := make(chan *pb.MeshPacket, 1)
	go func() {
		for fr := range s.ch {
			if p := fr.GetPacket(); p != nil && text(p) == want {
				out <- p
				return
			}
		}
	}()
	return out
}

// onMediumFast puts the MediumFast primary (by name, default key) in one of an identity's slots on
// the "mf" radio.
func onMediumFast(t *testing.T, home *Host, id *Identity, slot int) {
	t.Helper()
	if err := home.SetChannel(id, &pb.Channel{Index: int32(slot), Role: pb.Channel_SECONDARY,
		Settings: &pb.ChannelSettings{Name: "MediumFast", Psk: []byte{1}}}); err != nil {
		t.Fatal(err)
	}
	id.SetSlotRadio(slot, "mf")
	home.fed.Changed()
}

func TestMultiRadioDefaultsAndSighting(t *testing.T) {
	mr := &MultiRadio{Channels: map[int]string{2: "mf"}}
	if mr.SlotRadio(0, "lf") != "lf" || mr.SlotRadio(1, "lf") != "lf" || mr.SlotRadio(2, "lf") != "mf" {
		t.Fatal("slot radios: default for unset slots, slot 0 always the default")
	}
	mr.DefaultRadio = "ls"
	if mr.SlotRadio(0, "lf") != "ls" || mr.SlotRadio(1, "lf") != "ls" || mr.SlotRadio(2, "lf") != "mf" {
		t.Fatal("slot 0 and unset slots follow the default radio")
	}

	// phase 3 listen/send becomes one radio per slot, with extra listening radios as copied slots
	id, _ := NewIdentity(nil, "legacy", "")
	id.Channels[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "Scotland", Psk: []byte{1}}}
	id.multiRadio = &MultiRadio{Radios: []string{"mf"}, Listen: map[int][]string{1: {"lf", "mf"}}, Send: map[int]string{1: "lf"}, DM: "home"}
	if lost := id.convertLegacy(); lost != 0 {
		t.Fatalf("lost %d", lost)
	}
	m := id.multiRadio
	copySlot := -1
	for k := 2; k < MaxChannels; k++ {
		if c := id.Channels[k]; c != nil && c.GetSettings().GetName() == "Scotland" {
			copySlot = k
		}
	}
	if m.Channels[1] != "lf" || copySlot < 0 || m.Channels[copySlot] != "mf" || m.DM != DMDefault || m.Listen != nil {
		t.Fatalf("converted = %+v, copy in slot %d", m, copySlot)
	}

	now := time.Now()
	a, _ := NewHost(Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, RadioID: "a"}, sim.NewHub(1).Attach("a", 4), nil)
	b, _ := NewHost(Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_MEDIUM_FAST, RadioID: "b"}, sim.NewHub(1).Attach("b", 4), nil)
	a.DB.Update(7, func(e *NodeEntry) { e.LastHeard, e.HopsAway, e.SNR = now.Add(-time.Minute), 2, 9 })
	b.DB.Update(7, func(e *NodeEntry) { e.LastHeard, e.HopsAway, e.SNR = now.Add(-10*time.Minute), 0, -3 })
	if best, _ := bestSighting([]*Host{a, b}, 7, now); best != b {
		t.Fatal("fewer hops should win over a fresher sighting")
	}
	b.DB.Update(7, func(e *NodeEntry) { e.LastHeard = now.Add(-25 * time.Hour) })
	if best, _ := bestSighting([]*Host{a, b}, 7, now); best != a {
		t.Fatal("a sighting older than a day should be ignored")
	}
}

// A DM that exhausted its retries on the home radio gets one more try on the radio where the
// destination was heard; a second failure there is final.
func TestMultiRadioFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := sim.NewHub(0.02)
	lf := siteHost(t, ctx, hub, "lf", pb.Config_LoRaConfig_LONG_FAST, "Alex")
	mf := siteHost(t, ctx, hub, "mf", pb.Config_LoRaConfig_MEDIUM_FAST)
	remote := siteHost(t, ctx, hub, "far", pb.Config_LoRaConfig_MEDIUM_FAST, "Zed")
	alex, zed := lf.ids[0], remote.ids[0]
	zedSink := newSink()
	zed.AddSink(zedSink)
	fed := NewFederation(lf.Host, mf.Host)
	fed.SetEnabled(true)
	onMediumFast(t, lf.Host, alex, 1)
	mr := alex.MultiRadio()
	mr.DM, mr.Fallback = DMDefault, true
	alex.SetMultiRadio(mr)
	fed.Changed()
	time.Sleep(100 * time.Millisecond)

	deadline := time.Now().Add(10 * time.Second)
	var lastAnnounce time.Time
	for mf.peerKey(zed.NodeNum) == nil || remote.peerKey(alex.NodeNum) == nil {
		if time.Since(lastAnnounce) > 1500*time.Millisecond { // the simulated air loses packets now and then
			remote.broadcastNodeInfo(zed)
			mf.broadcastNodeInfo(alex) // Alex announced on mf, so Zed can read its PKI DMs
			lastAnnounce = time.Now()
		}
		if time.Now().After(deadline) {
			t.Fatal("mf never heard Zed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if radios, _ := lf.RoutePreview(alex, zed.NodeNum, 0); radios[0] != "lf" {
		t.Fatalf("DM policy default should route to lf, got %v", radios)
	}

	const pid = 0x0badf00d
	now := time.Now()
	lf.Messages.Add(alex.NodeNum, &Message{ID: pid, From: alex.NodeID(), To: zed.NodeID(), Text: "fallback", Direction: "out", Status: "sent", PKI: true})
	k := pktKey{alex.NodeNum, pid}
	lf.pmu.Lock()
	lf.pending[k] = &pendingTx{pkt: &pb.MeshPacket{From: alex.NodeNum, To: zed.NodeNum, Id: pid, PkiEncrypted: true}, origin: alex,
		remaining: 0, text: true, next: now, plain: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("fallback")}}
	lf.pmu.Unlock()
	lf.doRetransmissions(now.Add(time.Second))

	zedSink.waitPacket(t, 8*time.Second, func(p *pb.MeshPacket) bool { return text(p) == "fallback" && p.Id == pid })
	if m := lf.Messages.List(alex.NodeNum, alex.NodeID(), "dm:"+zed.NodeID(), 0, 10); len(m) != 1 || m[0].Status == "failed" {
		t.Fatalf("message after fallback = %+v", m)
	}
	// the same packet exhausting on mf too doesn't bounce back to lf
	mf.pmu.Lock()
	mf.pending[k] = &pendingTx{pkt: &pb.MeshPacket{From: alex.NodeNum, To: zed.NodeNum, Id: pid, PkiEncrypted: true}, origin: alex,
		remaining: 0, text: true, next: now, plain: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("fallback")}}
	mf.pmu.Unlock()
	if mf.tryFallback(mf.pending[k], k) {
		t.Fatal("fell back a second time")
	}
}
