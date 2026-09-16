package nodes

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/sim"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const (
	personaNum   = 0x5a5a0001
	neighbourNum = 0x0badcafe
	longFastHash = 8
)

type airRig struct {
	ctx         context.Context
	air         *LoRaAir
	personaNode *Node
	h           *mesh.Host
	node        *mtclienttest.Node
	persona     *mesh.Identity
	ops         *mesh.Identity
	far         *sim.Radio
}

// newAirRig runs a host on a sim radio whose LoRa air has one joined node (a fake hosted persona)
// and one virtual identity, plus a second radio ("far") that stands for the rest of the mesh.
func newAirRig(t *testing.T) *airRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub := sim.NewHub(0.01)
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, RelayRole: mesh.RoleClient, NodeInfoInterval: time.Hour},
		hub.Attach("mast", 64), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	node := mtclienttest.New(personaNum)
	c := mtclient.New(mtclient.Options{Address: "fake", Dial: node.Dial, ReconnectInterval: 10 * time.Millisecond})
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := c.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	n := newNode("fake", "", c, testLogf(t))
	persona, err := mesh.NewRemoteIdentity(n, remoteState(c.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	persona.IsRelay = true
	if err := h.AddIdentity(persona); err != nil {
		t.Fatal(err)
	}
	var ops *mesh.Identity
	for ops == nil {
		id, err := mesh.NewIdentity(nil, "Ops", "OPS")
		if err == nil && h.AddIdentity(id) == nil {
			ops = id
		}
	}
	n.Bind(h, persona)
	air := NewLoRaAir(h, testLogf(t))
	if air.Relay() != nil {
		t.Fatal("a LoRa air brings no relay")
	}
	air.Join(ctx, n)
	go func() { _ = h.Run(ctx) }()

	far := hub.Attach("far", 64)
	rp := h.RadioParams()
	if err := far.Configure(ctx, radio.Config{FrequencyHz: rp.FrequencyHz(), BandwidthHz: rp.BwHz(), SF: uint8(rp.SF), CR: uint8(rp.CR),
		SyncWord: rp.SyncWord, Preamble: uint16(rp.Preamble), TxPowerDBm: int8(rp.TxPowerDBm)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "air bridge registered", func() bool { return len(air.snapshot()) == 1 })
	return &airRig{ctx: ctx, air: air, personaNode: n, h: h, node: node, persona: persona, ops: ops, far: far}
}

func channelPacket(from, id uint32, hop uint32, text string) *pb.MeshPacket {
	plain, _ := proto.Marshal(&pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text)})
	return &pb.MeshPacket{From: from, To: wire.Broadcast, Id: id, HopLimit: hop, HopStart: 3, Channel: longFastHash,
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: wire.AESCTR(wire.DefaultPSK, from, id, plain)}}
}

// farFrame waits for a frame on the far radio and decodes its channel text.
func (r *airRig) farFrame(t *testing.T, match func(*pb.MeshPacket) bool) *pb.MeshPacket {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case f := <-r.far.Frames():
			p := wire.DecodeFrame(f.Data, 0, 0)
			if p != nil && match(p) {
				return p
			}
		case <-timeout:
			t.Fatal("no frame on the far radio")
		}
	}
}

func farText(p *pb.MeshPacket) string {
	var d pb.Data
	if proto.Unmarshal(wire.AESCTR(wire.DefaultPSK, p.From, p.Id, p.GetEncrypted()), &d) != nil {
		return ""
	}
	return string(d.Payload)
}

// injected returns the envelopes the fake node received, matching fn.
func (r *airRig) injected(fn func(*pb.MeshPacket, *pb.Compressed) bool) []*pb.MeshPacket {
	var out []*pb.MeshPacket
	for _, p := range r.node.Packets() {
		if p.GetDecoded().GetPortnum() != pb.PortNum_SIMULATOR_APP {
			continue
		}
		var c pb.Compressed
		if proto.Unmarshal(p.GetDecoded().GetPayload(), &c) == nil && fn(p, &c) {
			out = append(out, p)
		}
	}
	return out
}

func simTX(p *pb.MeshPacket, port pb.PortNum, payload []byte) *pb.FromRadio {
	cb, _ := proto.Marshal(&pb.Compressed{Portnum: port, Data: payload})
	q := proto.Clone(p).(*pb.MeshPacket)
	d := &pb.Data{}
	if p.GetDecoded() != nil {
		d = proto.Clone(p.GetDecoded()).(*pb.Data)
	}
	d.Portnum, d.Payload = pb.PortNum_SIMULATOR_APP, cb
	q.PayloadVariant = &pb.MeshPacket_Decoded{Decoded: d}
	return &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: q}}
}

func TestAirHostedTransmits(t *testing.T) {
	r := newAirRig(t)
	// The persona says something on its primary channel: re-encrypted, on air, logged as sent.
	r.node.Push(simTX(&pb.MeshPacket{From: personaNum, To: wire.Broadcast, Id: 1001, HopLimit: 3, HopStart: 3, Channel: 0,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Bitfield: proto.Uint32(1)}}}, pb.PortNum_TEXT_MESSAGE_APP, []byte("from the persona")))
	p := r.farFrame(t, func(p *pb.MeshPacket) bool { return p.Id == 1001 })
	if p.From != personaNum || p.Channel != longFastHash || p.HopLimit != 3 || farText(p) != "from the persona" {
		t.Fatalf("frame %v %q", p, farText(p))
	}
	// A PKI DM goes out as the ciphertext it came as.
	cipher := bytes.Repeat([]byte{0x42}, 20)
	r.node.Push(simTX(&pb.MeshPacket{From: personaNum, To: neighbourNum, Id: 1002, HopLimit: 3, HopStart: 3, WantAck: true, PkiEncrypted: true,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}, pb.PortNum_UNKNOWN_APP, cipher))
	p = r.farFrame(t, func(p *pb.MeshPacket) bool { return p.Id == 1002 })
	if p.To != neighbourNum || p.Channel != 0 || !p.WantAck || !bytes.Equal(p.GetEncrypted(), cipher) {
		t.Fatalf("PKI frame %v", p)
	}
	// The routing error the firmware addresses to node 0 never goes on air.
	r.node.Push(simTX(&pb.MeshPacket{From: personaNum, To: 0, Id: 1003, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}},
		pb.PortNum_ROUTING_APP, []byte{0x18, 0x08}))
	r.node.Push(simTX(&pb.MeshPacket{From: personaNum, To: wire.Broadcast, Id: 1004, HopLimit: 3, HopStart: 3,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}, pb.PortNum_TEXT_MESSAGE_APP, []byte("after")))
	p = r.farFrame(t, func(p *pb.MeshPacket) bool { return p.Id == 1003 || p.Id == 1004 })
	if p.Id != 1004 {
		t.Fatal("frame addressed to node 0 transmitted")
	}
	// Everything the host sends reaches the joined nodes at hop 0: they don't repeat it.
	pid, err := r.h.SendText(r.ops, wire.Broadcast, 0, "from ops", false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "persona hears ops", func() bool {
		return len(r.injected(func(p *pb.MeshPacket, c *pb.Compressed) bool {
			return p.Id == pid && p.HopLimit == 0 && p.HopStart == 0 && p.GetRxRssi() == loopRSSI && c.Portnum == pb.PortNum_UNKNOWN_APP
		})) == 1
	})
	// ...and the persona's own frames are not played back to it.
	if n := len(r.injected(func(p *pb.MeshPacket, _ *pb.Compressed) bool { return p.From == personaNum })); n != 0 {
		t.Fatalf("persona heard its own frames %d times", n)
	}
}

func TestAirHeardAndRelayed(t *testing.T) {
	r := newAirRig(t)
	orig := channelPacket(neighbourNum, 2001, 3, "from afar")
	frame, err := wire.EncodeFrame(orig)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.far.Send(r.ctx, frame); err != nil {
		t.Fatal(err)
	}
	var env *pb.MeshPacket
	eventually(t, "frame injected", func() bool {
		got := r.injected(func(p *pb.MeshPacket, c *pb.Compressed) bool {
			return p.Id == 2001 && c.Portnum == pb.PortNum_UNKNOWN_APP && bytes.Equal(c.Data, orig.GetEncrypted())
		})
		if len(got) > 0 {
			env = got[0]
		}
		return env != nil
	})
	if env.From != neighbourNum || env.Channel != longFastHash || env.HopLimit != 3 || env.HopStart != 3 || env.GetRxRssi() == 0 || env.RxTime == nil {
		t.Fatalf("envelope %v", env)
	}
	// The host's virtual identity got it too, but the host doesn't relay: the persona does.
	eventually(t, "ops message", func() bool {
		for _, m := range r.h.Messages.List(r.ops.NodeNum, r.ops.NodeID(), "", 0, 10) {
			if m.Text == "from afar" {
				return true
			}
		}
		return false
	})
	// The persona relays it (decoded in its envelope, relay byte set): the original bytes go out.
	// Like SimRadio, the relay's envelope keeps the RSSI and SNR the packet was heard with.
	r.node.Push(simTX(&pb.MeshPacket{From: neighbourNum, To: wire.Broadcast, Id: 2001, HopLimit: 2, HopStart: 3, RelayNode: 0x01,
		RxRssi: proto.Int32(-97), RxSnr: 6.25, Channel: 0, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}, pb.PortNum_TEXT_MESSAGE_APP, []byte("from afar")))
	p := r.farFrame(t, func(p *pb.MeshPacket) bool { return p.Id == 2001 })
	if p.HopLimit != 2 || p.RelayNode != 0x01 || !bytes.Equal(p.GetEncrypted(), orig.GetEncrypted()) {
		t.Fatalf("relayed frame %v", p)
	}
	for _, m := range r.h.Messages.List(r.persona.NodeNum, r.persona.NodeID(), "", 0, 10) {
		if m.Text == "from afar" {
			t.Fatal("the host delivered to the hosted persona itself")
		}
	}
}

func TestEnvelopeAndCache(t *testing.T) {
	if _, err := envelope(&pb.MeshPacket{PayloadVariant: &pb.MeshPacket_Decoded{}}, -50, 5, 3); err == nil {
		t.Fatal("decoded packet wrapped")
	}
	env, err := envelope(channelPacket(1, 2, 3, "x"), 0, 0, 3)
	if err != nil || env.GetRxRssi() != 0 || env.RxSnr == 0 {
		t.Fatalf("a zero RSSI/SNR frame must not look local to SimRadio: %v %v", env, err)
	}
	c := newCipherCache(2)
	c.put(channelPacket(1, 1, 3, "a"))
	c.put(channelPacket(1, 2, 3, "b"))
	c.put(channelPacket(1, 3, 3, "c"))
	if _, _, ok := c.get(1, 3); !ok {
		t.Fatal("newest entry evicted")
	}
	if len(c.items) > 2 {
		t.Fatalf("cache holds %d", len(c.items))
	}
	pki := channelPacket(1, 9, 3, "p")
	pki.PkiEncrypted = true
	c.put(pki)
	if _, _, ok := c.get(1, 9); ok {
		t.Fatal("PKI frame cached")
	}
}

func TestVersionsAndHWID(t *testing.T) {
	for v, ok := range map[string]bool{"2.8.0.47db0e3": true, "2.8.1": true, "2.10.0": true, "3.0": true, "2.7.26.54e0d8d": false, "": false} {
		if VersionAtLeast(v, MinFirmware) != ok {
			t.Errorf("%q: %v", v, !ok)
		}
	}
	a, b := HWIDFor("persona"), HWIDFor("persona-b")
	if len(a) != 12 || a[:2] != "02" || a == b || HWIDFor("persona") != a {
		t.Fatalf("hwids %s %s", a, b)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAirJoinLeave(t *testing.T) {
	r := newAirRig(t)
	pid, err := r.h.SendText(r.ops, wire.Broadcast, 0, "before leave", false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "heard while joined", func() bool {
		return len(r.injected(func(p *pb.MeshPacket, _ *pb.Compressed) bool { return p.Id == pid })) == 1
	})
	r.air.Leave(r.personaNode)
	eventually(t, "node off the air", func() bool { return len(r.air.snapshot()) == 0 })
	pid, err = r.h.SendText(r.ops, wire.Broadcast, 0, "after leave", false)
	if err != nil {
		t.Fatal(err)
	}
	r.farFrame(t, func(p *pb.MeshPacket) bool { return p.Id == pid }) // it went on air...
	if n := len(r.injected(func(p *pb.MeshPacket, _ *pb.Compressed) bool { return p.Id == pid })); n != 0 {
		t.Fatal("...but a node that left still heard it")
	}
	// Joining again puts it back.
	r.air.Join(r.ctx, r.personaNode)
	eventually(t, "node back on the air", func() bool { return len(r.air.snapshot()) == 1 })
}

// join adds another hosted node (a fake) to the rig's air as an identity.
func (r *airRig) join(t *testing.T, num uint32) *mtclienttest.Node {
	t.Helper()
	fake := mtclienttest.New(num)
	c := mtclient.New(mtclient.Options{Address: "fake2", Dial: fake.Dial, ReconnectInterval: 10 * time.Millisecond})
	if err := c.Start(r.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	wctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	if err := c.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	n := newNode("fake2", "", c, testLogf(t))
	id, err := mesh.NewRemoteIdentity(n, remoteState(c.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	n.Bind(r.h, id)
	before := len(r.air.snapshot())
	r.air.Join(r.ctx, n)
	eventually(t, "second node registered", func() bool { return len(r.air.snapshot()) == before+1 })
	return fake
}

func TestAirLocalDirectMessagesStayOffAir(t *testing.T) {
	r := newAirRig(t)
	const deskNum = 0x7e57de5c
	desk := r.join(t, deskNum)
	cipher := []byte("pki ciphertext for the desk, 32+")
	dm := func(id uint32) *pb.FromRadio {
		return simTX(&pb.MeshPacket{From: personaNum, To: deskNum, Id: id, HopLimit: 3, HopStart: 3, WantAck: true, PkiEncrypted: true},
			pb.PortNum_UNKNOWN_APP, cipher)
	}
	gotDM := func(id uint32) bool {
		for _, p := range desk.Packets() {
			var c pb.Compressed
			if p.Id == id && p.GetDecoded().GetPortnum() == pb.PortNum_SIMULATOR_APP && proto.Unmarshal(p.GetDecoded().GetPayload(), &c) == nil &&
				bytes.Equal(c.Data, cipher) && p.HopLimit == 3 {
				return true
			}
		}
		return false
	}
	r.node.Push(dm(3001))
	eventually(t, "the DM handed to the desk", func() bool { return gotDM(3001) })
	select {
	case f := <-r.far.Frames():
		if p := wire.DecodeFrame(f.Data, 0, 0); p != nil && p.Id == 3001 {
			t.Fatal("a local DM went on air")
		}
	case <-time.After(500 * time.Millisecond):
	}
	logged := false
	for _, rec := range r.h.Packets.List(100, 0, nil) {
		if rec.ID == 3001 && rec.Kind == "local" && rec.Transport == "internal" {
			logged = true
		}
	}
	if !logged {
		t.Fatal("local DM not in the packet log")
	}

	// With local DMs over RF, it goes on air like any other packet.
	cfg := r.h.Config()
	cfg.LocalDMOverRF = true
	if err := r.h.UpdateConfig(r.ctx, cfg); err != nil {
		t.Fatal(err)
	}
	r.node.Push(dm(3002))
	if p := r.farFrame(t, func(p *pb.MeshPacket) bool { return p.Id == 3002 }); !bytes.Equal(p.GetEncrypted(), cipher) {
		t.Fatalf("on air %v", p)
	}
}

func TestAirIntroducesLocalIdentities(t *testing.T) {
	r := newAirRig(t)
	const deskNum = 0x7e57de5d
	desk := r.join(t, deskNum)
	contacts := func(f *mtclienttest.Node) map[uint32]bool {
		out := map[uint32]bool{}
		for _, m := range f.Admins() {
			if c := m.GetAddContact(); c != nil && c.ManuallyVerified && len(c.GetUser().GetPublicKey()) == 32 {
				out[c.NodeNum] = true
			}
		}
		return out
	}
	eventually(t, "the desk knows the persona and the host's own identity", func() bool {
		c := contacts(desk)
		return c[personaNum] && c[r.ops.NodeNum]
	})
	eventually(t, "the persona knows the desk", func() bool { return contacts(r.node)[deskNum] })
}
