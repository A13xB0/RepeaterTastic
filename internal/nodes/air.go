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

// Air is a mesh interface for hosted nodes: what a meshtasticd on a sim radio transmits and hears.
// Every air follows one rule: nodes on the same air hear each other at hop limit 0, so none of
// them repeats a frame that went out from the same place.
type Air interface {
	// Join puts a node on the air until Leave or ctx ends.
	Join(ctx context.Context, n *Node)
	// Leave takes a node off the air.
	Leave(n *Node)
	// Relay is the node that repeats on this air when the air brings one, else nil: the host then
	// joins a hosted node with a router role.
	Relay() *Node
}

// LoRaAir is the air of a radio RepeaterTastic drives (a KISS modem or the spi driver), the way
// Meshtasticator connects simulated nodes:
//
//   - Every frame the radio hears is injected into every joined node, with its RSSI and SNR.
//   - A joined node's transmission becomes a frame on the host's transmit queue: PKI ciphertext as
//     it came, channel payloads re-encrypted with the node's channel key (or, for a relay, the
//     ciphertext as first heard).
//   - What the host transmits is injected into the other joined nodes at hop limit 0.
//
// It brings no relay, and joined nodes are on air at zero hops.
type LoRaAir struct {
	h    *mesh.Host
	logf func(string, ...any)

	relay *Node // the node that repeats on this air, when the radio brings one (a board)

	mu     sync.RWMutex
	nodes  map[uint32]*airNode
	joined map[*Node]context.CancelFunc
	recent *cipherCache
}

var _ Air = (*LoRaAir)(nil)

type airNode struct {
	num  uint32
	node *Node
}

const (
	loopRSSI = -30  // what a co-located node hears
	loopSNR  = 12.0 // dB
	cacheTTL = 10 * time.Minute
)

// NewLoRaAir puts an air on h's radio.
func NewLoRaAir(h *mesh.Host, logf func(string, ...any)) *LoRaAir {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &LoRaAir{h: h, logf: logf, nodes: map[uint32]*airNode{}, joined: map[*Node]context.CancelFunc{},
		recent: newCipherCache(1024)}
	h.AddAirTap(a)
	return a
}

// Relay is the radio's own relay (a board), or nil for a radio we drive.
func (a *LoRaAir) Relay() *Node { return a.relay }

// WithRelay makes n (the board a BoardRadio is) the air's relay. Joined nodes are then a hop behind it.
func (a *LoRaAir) WithRelay(n *Node) *LoRaAir {
	a.relay = n
	return a
}

// Join puts n on the air.
func (a *LoRaAir) Join(ctx context.Context, n *Node) {
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if old, ok := a.joined[n]; ok {
		old()
	}
	a.joined[n] = cancel
	a.mu.Unlock()
	go a.serve(ctx, n)
}

// Leave takes n off the air.
func (a *LoRaAir) Leave(n *Node) {
	a.mu.Lock()
	cancel := a.joined[n]
	delete(a.joined, n)
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// serve carries one node's transmissions until ctx ends.
func (a *LoRaAir) serve(ctx context.Context, node *Node) {
	c := node.client
	events, stop := c.Subscribe(1024)
	defer stop()
	n := &airNode{node: node}
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
		go a.introduce(ctx, n)
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
				go a.introduce(ctx, n)
			case mtclient.Received:
				p := e.FromRadio.GetPacket()
				// Every SIMULATOR_APP packet a node hands its client is a transmission (SimRadio's
				// startSend). A relay keeps the RSSI and SNR it was heard with, so those say nothing.
				if p.GetDecoded().GetPortnum() == pb.PortNum_SIMULATOR_APP {
					if !node.OnAir() {
						continue // not yet the node it stands for
					}
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
func (a *LoRaAir) transmit(n *airNode, p *pb.MeshPacket) error {
	pkt, plain, err := a.frameFor(n, p)
	if errors.Is(err, errNotOurs) {
		return nil
	}
	if err != nil {
		return err
	}
	if pkt.From == n.num && pkt.To != wire.Broadcast && !a.h.Config().LocalDMOverRF {
		if target := a.node(pkt.To); target != nil && target != n {
			// A direct message to a node on this same air: hand it over without going on air, as
			// a direct neighbour would hear it (the answer comes back the same way).
			a.inject(target, pkt, loopRSSI, loopSNR, pkt.HopLimit, pkt.HopStart)
			a.h.LogInternal(pkt, plain)
			return nil
		}
	}
	a.recent.put(pkt)
	return a.h.QueueHosted(pkt, plain, n.num)
}

// introduce tells a node that has just connected about every other identity on the host, and them
// about it, with their public keys (add_contact, marked verified). Over the air nodes learn keys from
// NodeInfo; nodes that share a host may never have heard each other's, and without the sender's key
// a direct message between them can't be read.
func (a *LoRaAir) introduce(ctx context.Context, n *airNode) {
	if !n.node.OnAir() {
		return // it takes its own key first; the next connection introduces it
	}
	num := n.node.client.Snapshot().NodeNum()
	contact := func(id *mesh.Identity) *pb.AdminMessage {
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_AddContact{AddContact: &pb.SharedContact{
			NodeNum: id.NodeNum, User: id.UserCopy(), ManuallyVerified: true}}}
	}
	send := func(to *Node, m *pb.AdminMessage) {
		actx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := to.Admin(actx, m); err != nil && ctx.Err() == nil {
			a.logf("air: %s: contact not added: %v", wire.NodeID(to.client.Snapshot().NodeNum()), err)
		}
	}
	self := a.h.Identity(num)
	for _, id := range a.h.Identities() {
		if id.NodeNum == num || len(id.UserCopy().GetPublicKey()) != 32 {
			continue
		}
		send(n.node, contact(id))
		if other := a.node(id.NodeNum); other != nil && self != nil && len(self.UserCopy().GetPublicKey()) == 32 {
			send(other.node, contact(self))
		}
	}
	// A board relay isn't joined, but hears the node through its proxy: tell it too.
	if a.relay != nil && a.relay != n.node && self != nil && len(self.UserCopy().GetPublicKey()) == 32 {
		send(a.relay, contact(self))
	}
}

// node is the joined node with that number, or nil.
func (a *LoRaAir) node(num uint32) *airNode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.nodes[num]
}

// frameFor builds the encrypted packet a hosted node's envelope stands for.
func (a *LoRaAir) frameFor(n *airNode, p *pb.MeshPacket) (*pb.MeshPacket, *pb.Data, error) {
	if p.GetTo() == 0 || p.GetFrom() == 0 {
		return nil, nil, errNotOurs // the firmware addresses a failed client DM's error to node 0
	}
	var c pb.Compressed
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), &c); err != nil {
		return nil, nil, err
	}
	out := &pb.MeshPacket{From: p.From, To: p.To, Id: p.Id, HopLimit: p.HopLimit, HopStart: p.HopStart,
		WantAck: p.WantAck, ViaMqtt: p.ViaMqtt, NextHop: p.NextHop, RelayNode: p.RelayNode, Priority: p.Priority,
		PkiEncrypted: p.PkiEncrypted, TransportMechanism: pb.MeshPacket_TRANSPORT_LORA}
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
	id := n.node.Current()
	if id == nil {
		return nil, nil, errors.New("the node has no identity on the host yet")
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
func (a *LoRaAir) Heard(f radio.Frame) {
	p := wire.DecodeFrame(f.Data, int32(f.RSSI), f.SNR)
	if p == nil {
		return
	}
	a.recent.put(p)
	for _, n := range a.snapshot() {
		a.inject(n, p, p.GetRxRssi(), p.RxSnr, p.HopLimit, p.HopStart)
	}
}

// Transmitted injects what the host sent into the joined nodes that didn't send it, at hop limit
// 0: they stand where the sender does, so they know the packet and don't repeat it.
func (a *LoRaAir) Transmitted(frame []byte, pkt *pb.MeshPacket, origin uint32) {
	p := wire.DecodeFrame(frame, loopRSSI, loopSNR)
	if p == nil {
		return
	}
	for _, n := range a.snapshot() {
		if n.num != origin {
			a.inject(n, p, loopRSSI, loopSNR, 0, 0) // hop start 0 too: zero hops away, not hop-start many
		}
	}
}

func (a *LoRaAir) snapshot() []*airNode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*airNode, 0, len(a.nodes))
	for _, n := range a.nodes {
		out = append(out, n)
	}
	return out
}

// inject hands a frame to a hosted node as if its sim radio had received it.
func (a *LoRaAir) inject(n *airNode, p *pb.MeshPacket, rssi int32, snr float32, hopLimit, hopStart uint32) {
	env, err := envelope(p, rssi, snr, hopLimit)
	if err == nil {
		env.HopStart = hopStart
	}
	if err != nil {
		return
	}
	if err := n.node.client.Send(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: env}}); err != nil && !errors.Is(err, mtclient.ErrNotConnected) {
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
