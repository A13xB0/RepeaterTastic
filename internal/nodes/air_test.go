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
	ctx     context.Context
	h       *mesh.Host
	node    *mtclienttest.Node
	persona *mesh.Identity
	ops     *mesh.Identity
	far     *sim.Radio
}

// newAirRig runs a host on a sim radio with a hosted relay persona (a fake node) and one virtual
// identity, plus a second radio ("far") that stands for the rest of the mesh.
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
	n := newNode("fake", "", c, t.Logf, false)
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
	air := NewAir(h, t.Logf)
	go air.Serve(ctx, c, persona, true)
	go func() { _ = h.Run(ctx) }()

	far := hub.Attach("far", 64)
	rp := h.RadioParams()
	if err := far.Configure(ctx, radio.Config{FrequencyHz: rp.FrequencyHz(), BandwidthHz: rp.BwHz(), SF: uint8(rp.SF), CR: uint8(rp.CR),
		SyncWord: rp.SyncWord, Preamble: uint16(rp.Preamble), TxPowerDBm: int8(rp.TxPowerDBm)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "air bridge registered", func() bool { return len(air.snapshot()) == 1 })
	return &airRig{ctx: ctx, h: h, node: node, persona: persona, ops: ops, far: far}
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
	// Our own virtual identity is heard by the persona, which must not repeat it.
	pid, err := r.h.SendText(r.ops, wire.Broadcast, 0, "from ops", false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "persona hears ops", func() bool {
		return len(r.injected(func(p *pb.MeshPacket, c *pb.Compressed) bool {
			return p.Id == pid && p.HopLimit == 0 && p.GetRxRssi() == loopRSSI && c.Portnum == pb.PortNum_UNKNOWN_APP
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
	r.node.Push(simTX(&pb.MeshPacket{From: neighbourNum, To: wire.Broadcast, Id: 2001, HopLimit: 2, HopStart: 3, RelayNode: 0x01,
		Channel: 0, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}, pb.PortNum_TEXT_MESSAGE_APP, []byte("from afar")))
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
