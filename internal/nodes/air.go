package nodes

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Air is the air bridge: meshtasticd instances on sim radios (hosted nodes) sharing a host's
// real radio, the way Meshtasticator connects simulated nodes.
//
//   - Every frame the radio hears is injected into every hosted node, with its RSSI and SNR.
//   - A hosted node's transmission becomes a frame on the host's transmit queue: PKI ciphertext as
//     it came, channel payloads re-encrypted with the node's channel key (or, for a relay, the
//     ciphertext as first heard).
//   - What the host transmits is injected into the other hosted nodes as a strong local frame, so
//     co-located nodes hear each other. The relay persona hears the host's own identities (and
//     other hosted identities) with hop limit 0: it knows the packet, and doesn't repeat it from
//     the same mast.
type Air struct {
	h    *mesh.Host
	logf func(string, ...any)

	mu     sync.RWMutex
	nodes  map[uint32]*airNode
	recent *cipherCache
}

type airNode struct {
	num     uint32
	client  *mtclient.Client
	id      *mesh.Identity
	persona bool
}

const (
	loopRSSI = -30  // what a co-located node hears
	loopSNR  = 12.0 // dB
	cacheTTL = 10 * time.Minute
)

// NewAir attaches an air bridge to h.
func NewAir(h *mesh.Host, logf func(string, ...any)) *Air {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &Air{h: h, logf: logf, nodes: map[uint32]*airNode{}, recent: newCipherCache(1024)}
	h.AddAirTap(a)
	return a
}

// Serve bridges one hosted node until ctx ends. id is its identity on the host; persona marks the
// relay persona.
func (a *Air) Serve(ctx context.Context, c *mtclient.Client, id *mesh.Identity, persona bool) {
	events, stop := c.Subscribe(1024)
	defer stop()
	n := &airNode{client: c, id: id, persona: persona}
	register := func() {
		num := c.Snapshot().NodeNum()
		a.mu.Lock()
		if n.num != 0 && n.num != num {
			delete(a.nodes, n.num)
		}
		n.num = num
		a.nodes[num] = n
		a.mu.Unlock()
	}
	if c.Snapshot().Connected {
		register()
	}
	defer func() {
		a.mu.Lock()
		if a.nodes[n.num] == n {
			delete(a.nodes, n.num)
		}
		a.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			switch e.Kind {
			case mtclient.Configured:
				register()
			case mtclient.Received:
				p := e.FromRadio.GetPacket()
				if p.GetDecoded().GetPortnum() == pb.PortNum_SIMULATOR_APP && p.GetRxRssi() == 0 && p.GetRxSnr() == 0 {
					if err := a.transmit(n, p); err != nil {
						a.logf("air: %s: frame not sent: %v", wire.NodeID(n.num), err)
					}
				}
			}
		}
	}
}

// ------------------------------------------------------------------------------------ out

// errNotOurs is returned for envelopes that aren't a transmission (a packet looped back to its
// sender, a routing error the firmware addresses to node 0).
var errNotOurs = errors.New("not a transmission")

// transmit turns a hosted node's SIMULATOR_APP envelope into a frame on the host's queue.
func (a *Air) transmit(n *airNode, p *pb.MeshPacket) error {
	pkt, plain, err := a.frameFor(n, p)
	if errors.Is(err, errNotOurs) {
		return nil
	}
	if err != nil {
		return err
	}
	a.recent.put(pkt)
	return a.h.QueueHosted(pkt, plain, n.num)
}

// frameFor builds the encrypted packet a hosted node's envelope stands for.
func (a *Air) frameFor(n *airNode, p *pb.MeshPacket) (*pb.MeshPacket, *pb.Data, error) {
	if p.GetTo() == 0 || p.GetFrom() == 0 {
		return nil, nil, errNotOurs // the firmware addresses a failed client DM's error to node 0
	}
	var c pb.Compressed
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), &c); err != nil {
		return nil, nil, err
	}
	out := &pb.MeshPacket{From: p.From, To: p.To, Id: p.Id, HopLimit: p.HopLimit, HopStart: p.HopStart,
		WantAck: p.WantAck, ViaMqtt: p.ViaMqtt, NextHop: p.NextHop, RelayNode: p.RelayNode, Priority: p.Priority,
		PkiEncrypted: p.PkiEncrypted}
	if c.Portnum == pb.PortNum_UNKNOWN_APP {
		// Ciphertext: a PKI DM (channel 0 on air) or a channel packet the node couldn't read.
		out.Channel = p.Channel
		out.PayloadVariant = &pb.MeshPacket_Encrypted{Encrypted: c.Data}
		return out, nil, nil
	}
	data := proto.Clone(p.GetDecoded()).(*pb.Data)
	data.Portnum, data.Payload = c.Portnum, c.Data
	if p.From != n.num {
		// A relay of a channel packet: send the bytes as heard, not a re-encoding.
		if hash, enc, ok := a.recent.get(p.From, p.Id); ok {
			out.Channel = uint32(hash)
			out.PayloadVariant = &pb.MeshPacket_Encrypted{Encrypted: enc}
			return out, data, nil
		}
	}
	id := a.h.Identity(n.num) // the identity may have been swapped since Serve started
	if id == nil {
		id = n.id
	}
	hash, key, aead, ok := a.h.ChannelKey(id, int(p.Channel))
	if !ok {
		return nil, nil, fmt.Errorf("the node has no channel %d", p.Channel)
	}
	plain, err := proto.Marshal(data)
	if err != nil {
		return nil, nil, err
	}
	var enc []byte
	if aead {
		if enc, err = wire.AEADEncrypt(key, p.From, p.To, p.Id, plain); err != nil {
			return nil, nil, err
		}
	} else {
		enc = wire.AESCTR(key, p.From, p.Id, plain)
	}
	if len(enc) > wire.MaxPayload {
		return nil, nil, errors.New("payload too large for a LoRa frame")
	}
	out.Channel = uint32(hash)
	out.PayloadVariant = &pb.MeshPacket_Encrypted{Encrypted: enc}
	return out, data, nil
}

// ------------------------------------------------------------------------------------ in

// Heard injects a frame off the air into every hosted node.
func (a *Air) Heard(f radio.Frame) {
	p := wire.DecodeFrame(f.Data, int32(f.RSSI), f.SNR)
	if p == nil {
		return
	}
	a.recent.put(p)
	for _, n := range a.snapshot() {
		a.inject(n, p, p.GetRxRssi(), p.RxSnr, p.HopLimit)
	}
}

// Transmitted injects what the host sent into the hosted nodes that didn't send it.
func (a *Air) Transmitted(frame []byte, pkt *pb.MeshPacket, origin uint32) {
	p := wire.DecodeFrame(frame, loopRSSI, loopSNR)
	if p == nil {
		return
	}
	a.mu.RLock()
	from := a.nodes[origin]
	a.mu.RUnlock()
	identity := from == nil || !from.persona // one of our identities, hosted or not
	for _, n := range a.snapshot() {
		if n == from {
			continue
		}
		hop := p.HopLimit
		if n.persona && identity {
			hop = 0 // the persona stands where the identity does: known, not repeated
		}
		a.inject(n, p, loopRSSI, loopSNR, hop)
	}
}

func (a *Air) snapshot() []*airNode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*airNode, 0, len(a.nodes))
	for _, n := range a.nodes {
		out = append(out, n)
	}
	return out
}

// inject hands a frame to a hosted node as if its sim radio had received it.
func (a *Air) inject(n *airNode, p *pb.MeshPacket, rssi int32, snr float32, hopLimit uint32) {
	env, err := envelope(p, rssi, snr, hopLimit)
	if err != nil {
		return
	}
	if err := n.client.Send(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: env}}); err != nil && !errors.Is(err, mtclient.ErrNotConnected) {
		a.logf("air: %s: frame not injected: %v", wire.NodeID(n.num), err)
	}
}

// envelope wraps an encrypted packet the way SimRadio expects a received frame.
func envelope(p *pb.MeshPacket, rssi int32, snr float32, hopLimit uint32) (*pb.MeshPacket, error) {
	enc := p.GetEncrypted()
	if enc == nil {
		return nil, wire.ErrNotEncrypted
	}
	cb, err := proto.Marshal(&pb.Compressed{Portnum: pb.PortNum_UNKNOWN_APP, Data: enc})
	if err != nil {
		return nil, err
	}
	now := uint32(time.Now().Unix())
	if rssi == 0 && snr == 0 {
		snr = 0.25 // SimRadio takes rx_rssi 0 and rx_snr 0 for a local packet
	}
	return &pb.MeshPacket{From: p.From, To: p.To, Id: p.Id, Channel: p.Channel, HopLimit: hopLimit, HopStart: p.HopStart,
		WantAck: p.WantAck, ViaMqtt: p.ViaMqtt, NextHop: p.NextHop, RelayNode: p.RelayNode,
		RxRssi: proto.Int32(rssi), RxSnr: snr, RxTime: proto.Uint32(now),
		TransportMechanism: pb.MeshPacket_TRANSPORT_LORA,
		PayloadVariant:     &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_SIMULATOR_APP, Payload: cb}}}, nil
}

// ------------------------------------------------------------------------------------ cache

// cipherCache remembers recent frames' ciphertext by (from, id), so a hosted relay sends the
// bytes it heard.
type cipherCache struct {
	mu    sync.Mutex
	max   int
	items map[[2]uint32]cipherEntry
}

type cipherEntry struct {
	hash uint8
	enc  []byte
	at   time.Time
}

func newCipherCache(max int) *cipherCache {
	return &cipherCache{max: max, items: map[[2]uint32]cipherEntry{}}
}

func (c *cipherCache) put(p *pb.MeshPacket) {
	enc := p.GetEncrypted()
	if enc == nil || p.PkiEncrypted {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	k := [2]uint32{p.From, p.Id}
	if _, ok := c.items[k]; ok {
		return
	}
	if len(c.items) >= c.max {
		for key, e := range c.items {
			if now.Sub(e.at) > cacheTTL || len(c.items) >= c.max {
				delete(c.items, key)
			}
		}
	}
	c.items[k] = cipherEntry{hash: uint8(p.Channel), enc: append([]byte(nil), enc...), at: now}
}

func (c *cipherCache) get(from, id uint32) (uint8, []byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[[2]uint32{from, id}]
	if !ok || time.Since(e.at) > cacheTTL {
		return 0, nil, false
	}
	return e.hash, e.enc, true
}
