// Package mtclient is a client for the Meshtastic client API (the "phone API"): the stream protocol
// a board speaks over USB serial and meshtasticd speaks on TCP port 4403. A Client connects, runs
// the want_config handshake, mirrors the node's configuration, channels and node database, and
// reconnects by itself when the link drops or the node reboots.
package mtclient

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ScotMesh/RepeaterTastic/pb"
)

// DefaultPort is meshtasticd's API port.
const DefaultPort = 4403

// ErrNotConnected is returned by sends while the node is not connected and configured.
var ErrNotConnected = errors.New("mtclient: node not connected")

// Options configures a Client. Zero values pick the defaults noted on each field.
type Options struct {
	// Address is host:port (or just host, port 4403) for TCP, or a serial device such as
	// /dev/ttyACM0 or /dev/serial/by-id/….
	Address string
	Baud    int // serial only, default 115200

	// Dial opens the transport; the default dials Address. Tests substitute a fake node.
	Dial func(ctx context.Context) (io.ReadWriteCloser, error)

	ConfigTimeout     time.Duration // handshake limit, default 60s (a board with a full node DB is slow)
	Heartbeat         time.Duration // default 60s; the firmware drops idle API clients
	ReconnectInterval time.Duration // first retry, doubling to 30s; default 1s
	RequestTimeout    time.Duration // admin replies and acks, default 15s

	Logf func(format string, args ...any)
}

// IsSerial reports whether addr names a serial device rather than a TCP address.
func IsSerial(addr string) bool {
	return strings.HasPrefix(addr, "/dev/") || strings.HasPrefix(addr, "COM")
}

// TCPAddress normalises host or host:port to host:port, adding the default port.
func TCPAddress(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("mtclient: empty address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		bare := addr
		if strings.Count(addr, ":") > 1 && !strings.HasPrefix(addr, "[") { // bare IPv6
			bare = "[" + addr + "]"
		}
		host, port, err = net.SplitHostPort(fmt.Sprintf("%s:%d", bare, DefaultPort))
	}
	if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 ||
		(strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return "", fmt.Errorf("mtclient: bad address %q", addr)
	}
	return net.JoinHostPort(host, port), nil
}

// EventKind says what an Event carries.
type EventKind int

const (
	// Configured: the handshake finished (first connect or a reconnect); Snapshot is fresh.
	Configured EventKind = iota
	// Disconnected: the link dropped; Err says why.
	Disconnected
	// Received: a FromRadio after the handshake (packets, queue status, log records, …).
	Received
)

// Event is one thing that happened on the link.
type Event struct {
	Kind      EventKind
	FromRadio *pb.FromRadio // Received
	Err       error         // Disconnected
}

// Snapshot is the node as the client last saw it. Its messages are copies.
type Snapshot struct {
	Connected    bool
	ConfiguredAt time.Time
	Reconnects   int
	MyInfo       *pb.MyNodeInfo
	Metadata     *pb.DeviceMetadata
	Config       *pb.LocalConfig
	ModuleConfig *pb.LocalModuleConfig
	Channels     []*pb.Channel // by index; nil entries for indexes the node didn't send
	Nodes        map[uint32]*pb.NodeInfo
}

// NodeNum is the node's own number, or 0 before the first handshake.
func (s Snapshot) NodeNum() uint32 { return s.MyInfo.GetMyNodeNum() }

// Self is the node's own NodeInfo entry, if it sent one.
func (s Snapshot) Self() *pb.NodeInfo { return s.Nodes[s.NodeNum()] }

// Client is one connection to one node.
type Client struct {
	opts Options

	mu        sync.Mutex
	conn      io.ReadWriteCloser
	ready     bool // handshake done on conn
	readyCh   chan struct{}
	snap      Snapshot
	subs      map[chan Event]struct{}
	waiters   map[uint32]*waiter // request id → pending Request
	writeMu   sync.Mutex
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
	firstErr  error
	firstOnce sync.Once
	firstDone chan struct{}
}

// New returns a client; Start connects it.
func New(o Options) *Client {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.ConfigTimeout, 60*time.Second)
	def(&o.Heartbeat, 60*time.Second)
	def(&o.ReconnectInterval, time.Second)
	def(&o.RequestTimeout, 15*time.Second)
	if o.Baud == 0 {
		o.Baud = 115200
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {
			// No logger given: drop the messages.
		}
	}
	return &Client{opts: o, readyCh: make(chan struct{}), subs: map[chan Event]struct{}{},
		waiters: map[uint32]*waiter{}, done: make(chan struct{}), firstDone: make(chan struct{})}
}

// Start connects in the background and keeps reconnecting until Close or ctx ends.
func (c *Client) Start(ctx context.Context) error {
	if c.opts.Dial == nil {
		if IsSerial(c.opts.Address) {
			dev, baud := c.opts.Address, c.opts.Baud
			c.opts.Dial = func(context.Context) (io.ReadWriteCloser, error) {
				return serial.Open(dev, &serial.Mode{BaudRate: baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
			}
		} else {
			addr, err := TCPAddress(c.opts.Address)
			if err != nil {
				return err
			}
			c.opts.Dial = func(ctx context.Context) (io.ReadWriteCloser, error) {
				d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
				return d.DialContext(ctx, "tcp", addr)
			}
		}
	}
	ctx, c.cancel = context.WithCancel(ctx)
	go c.run(ctx)
	return nil
}

// Wait blocks until the first handshake finishes, the first attempt fails, or ctx ends. The
// client keeps retrying after a failed first attempt.
func (c *Client) Wait(ctx context.Context) error {
	select {
	case <-c.firstDone:
		return c.firstErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitReady blocks until the node is connected and configured.
func (c *Client) WaitReady(ctx context.Context) error {
	for {
		c.mu.Lock()
		ready, ch := c.ready, c.readyCh
		c.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return ErrNotConnected
		}
	}
}

// Close disconnects and stops reconnecting.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if c.cancel != nil {
		<-c.done
	}
	return nil
}

// Reconnect drops the link so the client dials again and re-reads the whole configuration. Use it
// after changes the node applies without rebooting.
func (c *Client) Reconnect() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// Snapshot returns a copy of the mirrored node state.
func (c *Client) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snap
	s.MyInfo = clone(s.MyInfo)
	s.Metadata = clone(s.Metadata)
	s.Config = clone(s.Config)
	s.ModuleConfig = clone(s.ModuleConfig)
	s.Channels = make([]*pb.Channel, len(c.snap.Channels))
	for i, ch := range c.snap.Channels {
		s.Channels[i] = clone(ch)
	}
	s.Nodes = make(map[uint32]*pb.NodeInfo, len(c.snap.Nodes))
	for k, v := range c.snap.Nodes {
		s.Nodes[k] = clone(v)
	}
	return s
}

func clone[T proto.Message](m T) T {
	var zero T
	if any(m) == any(zero) {
		return m
	}
	return proto.Clone(m).(T)
}

// Subscribe delivers events until the returned cancel is called. A slow subscriber misses
// events rather than stalling the link.
func (c *Client) Subscribe(buf int) (<-chan Event, func()) {
	ch := make(chan Event, buf)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.mu.Unlock()
	}
}

func (c *Client) publishLocked(e Event) {
	for ch := range c.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Send writes one ToRadio. It fails unless the node is connected and configured.
func (c *Client) Send(tr *pb.ToRadio) error {
	c.mu.Lock()
	conn, ready := c.conn, c.ready
	c.mu.Unlock()
	if !ready {
		return ErrNotConnected
	}
	return c.write(conn, tr)
}

func (c *Client) write(conn io.Writer, tr *pb.ToRadio) error {
	b, err := proto.Marshal(tr)
	if err != nil {
		return err
	}
	frame, err := AppendFrame(nil, b)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if d, ok := conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = d.SetWriteDeadline(time.Now().Add(10 * time.Second))
	}
	_, err = conn.Write(frame)
	return err
}

// NewPacketID returns a random non-zero packet id.
func NewPacketID() uint32 {
	var b [4]byte
	for {
		_, _ = rand.Read(b[:])
		if id := binary.LittleEndian.Uint32(b[:]); id != 0 {
			return id
		}
	}
}

// SendPacket sends a MeshPacket from the node and returns its id. A packet without an id gets one,
// and one for another node without a hop limit gets the node's configured limit: the firmware
// transmits a client's hop_limit 0 as it is.
func (c *Client) SendPacket(p *pb.MeshPacket) (uint32, error) {
	if p.Id == 0 {
		p.Id = NewPacketID()
	}
	if p.HopLimit == 0 {
		c.mu.Lock()
		me, hop := c.snap.NodeNum(), c.snap.Config.GetLora().GetHopLimit()
		c.mu.Unlock()
		if p.To != me {
			if hop == 0 {
				hop = 3
			}
			p.HopLimit = hop
		}
	}
	return p.Id, c.Send(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: p}})
}

type waiter struct {
	ch           chan *pb.MeshPacket
	wantResponse bool // a routing ack isn't the answer; wait for the response (or a routing error)
}

// Request sends p (a decoded packet) and waits for the packet answering it: a decoded reply whose
// request_id is p's id. With want_response set that is the response itself (or a routing error);
// otherwise it is the routing ack or nak.
func (c *Client) Request(ctx context.Context, p *pb.MeshPacket) (*pb.MeshPacket, error) {
	if p.Id == 0 {
		p.Id = NewPacketID()
	}
	w := &waiter{ch: make(chan *pb.MeshPacket, 1), wantResponse: p.GetDecoded().GetWantResponse()}
	c.mu.Lock()
	c.waiters[p.Id] = w
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiters, p.Id)
		c.mu.Unlock()
	}()
	if _, err := c.SendPacket(p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	select {
	case r := <-w.ch:
		return r, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("mtclient: no answer to packet %08x: %w", p.Id, ctx.Err())
	case <-c.done:
		return nil, ErrNotConnected
	}
}

// Admin sends an admin message to the node itself. A get_* request returns the node's admin
// response; anything else returns once the node acknowledges it (nil response).
func (c *Client) Admin(ctx context.Context, m *pb.AdminMessage) (*pb.AdminMessage, error) {
	c.mu.Lock()
	me := c.snap.NodeNum()
	c.mu.Unlock()
	payload, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	wantResponse := isAdminGet(m)
	p := &pb.MeshPacket{To: me, WantAck: !wantResponse, Channel: 0, HopLimit: 0,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ADMIN_APP, Payload: payload, WantResponse: wantResponse}}}
	r, err := c.Request(ctx, p)
	if err != nil {
		return nil, err
	}
	d := r.GetDecoded()
	switch d.GetPortnum() {
	case pb.PortNum_ROUTING_APP:
		var rt pb.Routing
		if err := proto.Unmarshal(d.GetPayload(), &rt); err != nil {
			return nil, err
		}
		if e := rt.GetErrorReason(); e != pb.Routing_NONE {
			return nil, fmt.Errorf("mtclient: node refused the admin message: %s", e)
		}
		return nil, nil
	case pb.PortNum_ADMIN_APP:
		var am pb.AdminMessage
		if err := proto.Unmarshal(d.GetPayload(), &am); err != nil {
			return nil, err
		}
		return &am, nil
	}
	return nil, fmt.Errorf("mtclient: unexpected answer on port %s", d.GetPortnum())
}

// isAdminGet reports whether m asks the node for something (field names get_*_request).
func isAdminGet(m *pb.AdminMessage) bool {
	f := m.ProtoReflect().WhichOneof(m.ProtoReflect().Descriptor().Oneofs().ByName("payload_variant"))
	return f != nil && strings.HasPrefix(string(f.Name()), "get_") && strings.HasSuffix(string(f.Name()), "_request")
}

// ------------------------------------------------------------------------------------ link loop

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	backoff := c.opts.ReconnectInterval
	for ctx.Err() == nil {
		configured, err := c.session(ctx)
		c.first(err)
		if ctx.Err() != nil {
			break
		}
		c.opts.Logf("mtclient: %s: %v", c.opts.Address, err)
		if configured {
			backoff = c.opts.ReconnectInterval
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	c.first(ctx.Err())
}

// first settles Wait: the first handshake or the first failure, whichever comes first.
func (c *Client) first(err error) {
	c.firstOnce.Do(func() {
		c.firstErr = err
		close(c.firstDone)
	})
}

// session runs one connection: dial, handshake, then read until the link fails.
func (c *Client) session(ctx context.Context) (configured bool, err error) {
	conn, err := c.opts.Dial(ctx)
	if err != nil {
		return false, err
	}
	if err := c.attach(conn); err != nil {
		return false, err
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-sctx.Done()
		_ = conn.Close()
	}()

	nonce := NewPacketID()
	hs := newHandshake(nonce)
	configTimer := time.AfterFunc(c.opts.ConfigTimeout, func() {
		c.opts.Logf("mtclient: %s: no config after %v", c.opts.Address, c.opts.ConfigTimeout)
		cancel()
	})
	defer configTimer.Stop()

	if err := c.requestConfig(conn, nonce); err != nil {
		return false, err
	}

	heartbeat := time.NewTicker(c.opts.Heartbeat)
	defer heartbeat.Stop()
	go c.sendHeartbeats(sctx, conn, heartbeat.C)

	rerr := ReadFrames(conn, func(b []byte) bool {
		return c.handleFrame(b, hs, func() {
			configTimer.Stop()
			configured = true
		})
	})
	c.detach(rerr)
	if rerr == nil {
		rerr = errors.New("node rebooted")
	}
	if !configured && sctx.Err() != nil && ctx.Err() == nil {
		rerr = fmt.Errorf("no config within %v", c.opts.ConfigTimeout)
	}
	return configured, rerr
}

// attach makes conn the client's link, or closes it if the client has been closed meanwhile.
func (c *Client) attach(conn io.ReadWriteCloser) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		conn.Close()
		return context.Canceled
	}
	c.conn = conn
	c.mu.Unlock()
	return nil
}

// requestConfig wakes the node and asks for its whole configuration, tagged with nonce.
func (c *Client) requestConfig(conn io.Writer, nonce uint32) error {
	c.writeMu.Lock()
	_, err := conn.Write(wakeup)
	c.writeMu.Unlock()
	if err != nil {
		return err
	}
	return c.write(conn, &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: nonce}})
}

// sendHeartbeats sends a heartbeat on every tick until ctx ends.
func (c *Client) sendHeartbeats(ctx context.Context, conn io.Writer, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			_ = c.write(conn, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{Nonce: NewPacketID()}}})
		}
	}
}

// handleFrame takes one FromRadio: into the handshake until it completes (calling configured,
// then going ready), and into the mirror after. It returns false when the node has rebooted.
func (c *Client) handleFrame(b []byte, hs *handshake, configured func()) bool {
	fr := &pb.FromRadio{}
	if proto.Unmarshal(b, fr) != nil {
		return true
	}
	if !hs.done {
		if hs.take(fr) {
			configured()
			c.becomeReady(hs)
		}
		return true
	}
	if _, ok := fr.PayloadVariant.(*pb.FromRadio_Rebooted); ok {
		return false // the node restarts its API session; reconnect for a fresh handshake
	}
	c.received(fr)
	return true
}

// detach clears the link after it ends, telling subscribers if it had been ready.
func (c *Client) detach(rerr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	wasReady := c.ready
	c.ready = false
	c.readyCh = make(chan struct{})
	c.conn = nil
	c.snap.Connected = false
	if wasReady {
		c.publishLocked(Event{Kind: Disconnected, Err: rerr})
	}
}

func (c *Client) becomeReady(hs *handshake) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snap.MyInfo != nil {
		c.snap.Reconnects++
	}
	c.snap.Connected = true
	c.snap.ConfiguredAt = time.Now()
	c.snap.MyInfo = hs.myInfo
	c.snap.Metadata = hs.metadata
	c.snap.Config = hs.config
	c.snap.ModuleConfig = hs.moduleConfig
	c.snap.Channels = hs.channels
	c.snap.Nodes = hs.nodes
	c.ready = true
	close(c.readyCh)
	c.publishLocked(Event{Kind: Configured})
	c.first(nil)
}

// received updates the mirror from one post-handshake FromRadio and fans it out.
func (c *Client) received(fr *pb.FromRadio) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch v := fr.PayloadVariant.(type) {
	case *pb.FromRadio_Packet:
		c.notePacketLocked(v.Packet)
	case *pb.FromRadio_NodeInfo:
		c.snap.Nodes[v.NodeInfo.GetNum()] = v.NodeInfo
	case *pb.FromRadio_Channel:
		setChannel(&c.snap.Channels, v.Channel)
	case *pb.FromRadio_Config:
		mergeOneof(c.snap.Config, v.Config)
	case *pb.FromRadio_ModuleConfig:
		mergeOneof(c.snap.ModuleConfig, v.ModuleConfig)
	case *pb.FromRadio_Metadata:
		c.snap.Metadata = v.Metadata
	case *pb.FromRadio_MyInfo:
		c.snap.MyInfo = v.MyInfo
	}
	c.publishLocked(Event{Kind: Received, FromRadio: fr})
}

// notePacketLocked answers a pending Request and keeps the node DB's last-heard details current.
func (c *Client) notePacketLocked(p *pb.MeshPacket) {
	d := p.GetDecoded()
	c.answerWaiterLocked(p, d)
	if p.GetFrom() == 0 || d == nil {
		return
	}
	n := c.snap.Nodes[p.GetFrom()]
	if n == nil {
		n = &pb.NodeInfo{Num: p.GetFrom()}
		c.snap.Nodes[p.GetFrom()] = n
	}
	if p.GetRxTime() != 0 {
		n.LastHeard = p.GetRxTime()
	}
	if p.GetRxSnr() != 0 {
		n.Snr = p.GetRxSnr()
	}
	if p.HopStart != 0 {
		h := p.GetHopStart() - p.GetHopLimit()
		n.HopsAway = &h
	}
	notePayload(n, d)
}

// answerWaiterLocked hands p to the Request waiting for it, if any. A routing ack doesn't answer a
// request that wants a response; a routing error does.
func (c *Client) answerWaiterLocked(p *pb.MeshPacket, d *pb.Data) {
	id := d.GetRequestId()
	if id == 0 {
		return
	}
	w, ok := c.waiters[id]
	if !ok || (d.GetPortnum() == pb.PortNum_ROUTING_APP && w.wantResponse && !routingError(d)) {
		return
	}
	select {
	case w.ch <- p:
	default:
	}
}

// notePayload copies a heard user, position or device metrics into the node's entry.
func notePayload(n *pb.NodeInfo, d *pb.Data) {
	switch d.GetPortnum() {
	case pb.PortNum_NODEINFO_APP:
		var u pb.User
		if proto.Unmarshal(d.GetPayload(), &u) == nil {
			n.User = &u
		}
	case pb.PortNum_POSITION_APP:
		var pos pb.Position
		if proto.Unmarshal(d.GetPayload(), &pos) == nil && (pos.LatitudeI != nil || pos.LongitudeI != nil) {
			n.Position = &pos
		}
	case pb.PortNum_TELEMETRY_APP:
		var t pb.Telemetry
		if proto.Unmarshal(d.GetPayload(), &t) == nil && t.GetDeviceMetrics() != nil {
			n.DeviceMetrics = t.GetDeviceMetrics()
		}
	}
}

func routingError(d *pb.Data) bool {
	if d.GetPortnum() != pb.PortNum_ROUTING_APP {
		return false
	}
	var rt pb.Routing
	return proto.Unmarshal(d.GetPayload(), &rt) == nil && rt.GetErrorReason() != pb.Routing_NONE
}

// ------------------------------------------------------------------------------------ handshake

type handshake struct {
	nonce        uint32
	done         bool
	myInfo       *pb.MyNodeInfo
	metadata     *pb.DeviceMetadata
	config       *pb.LocalConfig
	moduleConfig *pb.LocalModuleConfig
	channels     []*pb.Channel
	nodes        map[uint32]*pb.NodeInfo
}

func newHandshake(nonce uint32) *handshake {
	return &handshake{nonce: nonce, config: &pb.LocalConfig{}, moduleConfig: &pb.LocalModuleConfig{},
		nodes: map[uint32]*pb.NodeInfo{}}
}

// take records one handshake frame and reports whether the handshake is complete.
func (h *handshake) take(fr *pb.FromRadio) bool {
	switch v := fr.PayloadVariant.(type) {
	case *pb.FromRadio_MyInfo:
		h.myInfo = v.MyInfo
	case *pb.FromRadio_Metadata:
		h.metadata = v.Metadata
	case *pb.FromRadio_NodeInfo:
		h.nodes[v.NodeInfo.GetNum()] = v.NodeInfo
	case *pb.FromRadio_Channel:
		setChannel(&h.channels, v.Channel)
	case *pb.FromRadio_Config:
		mergeOneof(h.config, v.Config)
	case *pb.FromRadio_ModuleConfig:
		mergeOneof(h.moduleConfig, v.ModuleConfig)
	case *pb.FromRadio_ConfigCompleteId:
		// A stale completion from an earlier session's request is ignored.
		h.done = v.ConfigCompleteId == h.nonce && h.myInfo != nil
	}
	return h.done
}

func setChannel(chs *[]*pb.Channel, ch *pb.Channel) {
	i := int(ch.GetIndex())
	if i < 0 || i >= 64 {
		return
	}
	for len(*chs) <= i {
		*chs = append(*chs, nil)
	}
	(*chs)[i] = ch
}

// mergeOneof copies the set member of src's payload_variant oneof into the dst field of the same
// name: Config.lora → LocalConfig.lora, ModuleConfig.mqtt → LocalModuleConfig.mqtt.
func mergeOneof(dst, src proto.Message) {
	sr := src.ProtoReflect()
	oo := sr.Descriptor().Oneofs().ByName("payload_variant")
	if oo == nil {
		return
	}
	f := sr.WhichOneof(oo)
	if f == nil {
		return
	}
	dr := dst.ProtoReflect()
	df := dr.Descriptor().Fields().ByName(f.Name())
	if df == nil || df.Message() == nil || df.Message().FullName() != f.Message().FullName() {
		return
	}
	dr.Set(df, protoreflect.ValueOfMessage(proto.Clone(sr.Get(f).Message().Interface()).ProtoReflect()))
}
