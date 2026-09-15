// Package udp is Meshtastic's LAN mesh: encrypted MeshPacket protobufs over UDP multicast, as native
// meshtasticd nodes use (network.enabled_protocols UDP_BROADCAST). Firmware 2.7 uses 224.0.0.69 and
// 2.8 uses 239.0.0.69, so by default we join and send on both.
package udp

import (
	"context"
	"hash/fnv"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const DefaultPort = 4403

var DefaultGroups = []string{"239.0.0.69", "224.0.0.69"}

type Link struct {
	host   *mesh.Host
	log    *slog.Logger
	groups []*net.UDPAddr
	iface  *net.Interface
	conns  []*net.UDPConn
	out    *net.UDPConn

	Rx, Tx, Dropped atomic.Uint64
	connected       atomic.Bool

	// Both group sockets receive both groups on Linux, and our own sends loop back: remember
	// recent datagrams so each is handled once.
	seenMu sync.Mutex
	seen   map[uint64]time.Time
}

func (l *Link) firstSighting(b []byte) bool {
	h := fnv.New64a()
	h.Write(b)
	k := h.Sum64()
	now := time.Now()
	l.seenMu.Lock()
	defer l.seenMu.Unlock()
	if t, ok := l.seen[k]; ok && now.Sub(t) < 10*time.Second {
		return false
	}
	if len(l.seen) > 1024 {
		for kk, t := range l.seen {
			if now.Sub(t) > 10*time.Second {
				delete(l.seen, kk)
			}
		}
	}
	l.seen[k] = now
	return true
}

// New prepares a link; iface "" lets the OS pick the interface.
func New(h *mesh.Host, groups []string, port int, iface string, log *slog.Logger) (*Link, error) {
	if len(groups) == 0 {
		groups = DefaultGroups
	}
	if port == 0 {
		port = DefaultPort
	}
	l := &Link{host: h, log: log.With("link", "udp"), seen: map[uint64]time.Time{}}
	for _, g := range groups {
		a, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(g, strconv.Itoa(port)))
		if err != nil {
			return nil, err
		}
		l.groups = append(l.groups, a)
	}
	if iface != "" {
		ifi, err := net.InterfaceByName(iface)
		if err != nil {
			return nil, err
		}
		l.iface = ifi
	}
	return l, nil
}

func (l *Link) Name() string    { return "udp" }
func (l *Link) Connected() bool { return l.connected.Load() }

// Run joins the groups and feeds received packets to the host until ctx ends.
func (l *Link) Run(ctx context.Context) error {
	out, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return err
	}
	l.out = out
	for _, g := range l.groups {
		c, err := net.ListenMulticastUDP("udp4", l.iface, g)
		if err != nil {
			l.log.Error("joining multicast group", "group", g, "err", err)
			continue
		}
		_ = c.SetReadBuffer(1 << 20)
		l.conns = append(l.conns, c)
	}
	if len(l.conns) == 0 {
		return err
	}
	l.connected.Store(true)
	l.host.AddLink(l)
	l.log.Info("UDP multicast link up", "groups", l.groups)
	go func() {
		<-ctx.Done()
		for _, c := range l.conns {
			_ = c.Close()
		}
		_ = out.Close()
	}()
	done := make(chan struct{}, len(l.conns))
	for _, c := range l.conns {
		go func(c *net.UDPConn) {
			defer func() { done <- struct{}{} }()
			l.readLoop(c)
		}(c)
	}
	for range l.conns {
		<-done
	}
	l.connected.Store(false)
	return ctx.Err()
}

func (l *Link) readLoop(c *net.UDPConn) {
	buf := make([]byte, 1024)
	for {
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !l.firstSighting(buf[:n]) {
			continue
		}
		p := &pb.MeshPacket{}
		if proto.Unmarshal(buf[:n], p) != nil {
			l.Dropped.Add(1)
			continue
		}
		// Firmware accepts only the encrypted variant from UDP and ignores packets from node 0.
		if p.GetEncrypted() == nil || p.From == 0 {
			l.Dropped.Add(1)
			continue
		}
		p.RxSnr, p.RxRssi, p.RxTime = 0, nil, nil
		p.TransportMechanism = pb.MeshPacket_TRANSPORT_MULTICAST_UDP
		l.Rx.Add(1)
		l.host.HandleReceived(p, nil)
	}
}

// SendPacket publishes an encrypted packet to every group (called by the host after each LoRa TX).
func (l *Link) SendPacket(p *pb.MeshPacket) {
	if l.out == nil || p.GetEncrypted() == nil {
		return
	}
	cp := proto.Clone(p).(*pb.MeshPacket)
	cp.RxSnr, cp.RxRssi, cp.RxTime = 0, nil, nil
	cp.TransportMechanism = pb.MeshPacket_TRANSPORT_LORA
	b, err := proto.Marshal(cp)
	if err != nil {
		return
	}
	l.firstSighting(b)
	for _, g := range l.groups {
		if _, err := l.out.WriteToUDP(b, g); err != nil {
			l.log.Debug("udp send", "group", g, "err", err)
			continue
		}
	}
	l.Tx.Add(1)
}
