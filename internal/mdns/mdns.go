// Package mdns advertises each virtual node as a _meshtastic._tcp service, the way WiFi firmware
// does (WiFiAPClient.cpp: TXT shortname, id, pio_env), so apps that browse the LAN find them.
// It is a minimal responder: PTR/SRV/TXT/A answers for our own records only.
package mdns

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	typeA   = 1
	typePTR = 12
	typeTXT = 16
	typeSRV = 33
	typeANY = 255
	classIN = 1
	flush   = 0x8000
	ttl     = 120

	serviceType = "_meshtastic._tcp.local."
	metaQuery   = "_services._dns-sd._udp.local."
)

var group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Seams for tests: how the socket is opened and how often announcements go out.
var (
	listenMulticast  = func() (*net.UDPConn, error) { return net.ListenMulticastUDP("udp4", nil, group) }
	announceDelay    = 500 * time.Millisecond
	announceRepeat   = time.Second
	announceInterval = time.Minute
)

// Service is one advertised node.
type Service struct {
	Instance string // e.g. "Base Camp (!274d0520)"
	Port     int
	TXT      map[string]string
}

// Responder answers queries for the current set of services.
type Responder struct {
	log      *slog.Logger
	hostname string

	mu       sync.Mutex
	services []Service
	changed  chan struct{}
}

func New(log *slog.Logger) *Responder {
	host, _ := osHostname()
	host = strings.TrimSuffix(strings.ReplaceAll(host, ".", "-"), "-local")
	if host == "" {
		host = "repeatertastic"
	}
	return &Responder{log: log.With("component", "mdns"), hostname: host + ".local.", changed: make(chan struct{}, 1)}
}

// SetServices replaces the advertised services and re-announces.
func (r *Responder) SetServices(s []Service) {
	r.mu.Lock()
	r.services = append([]Service(nil), s...)
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *Responder) Run(ctx context.Context) error {
	conn, err := listenMulticast()
	if err != nil {
		return err
	}
	// RFC 6762 §11: responders ignore answers that don't come from port 5353, so reply from the
	// listening socket.
	out := conn
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go r.announcer(ctx, out)
	buf := make([]byte, 9000)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			continue
		}
		questions, isQuery := parseQuestions(buf[:n])
		if !isQuery {
			continue
		}
		if resp := r.answer(questions, localIPv4For(src.IP)); resp != nil {
			dst := group
			if src.Port != 5353 { // legacy unicast query: reply directly
				dst = src
			}
			_, _ = out.WriteToUDP(resp, dst)
		}
	}
}

func (r *Responder) announcer(ctx context.Context, out *net.UDPConn) {
	announce := func() {
		ip := localIPv4For(nil)
		if resp := r.answer([]question{{name: serviceType, qtype: typePTR}}, ip); resp != nil {
			_, _ = out.WriteToUDP(resp, group)
		}
	}
	t := time.NewTicker(announceInterval)
	defer t.Stop()
	time.Sleep(announceDelay)
	announce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.changed:
			announce()
			time.Sleep(announceRepeat)
			announce()
		case <-t.C:
			announce()
		}
	}
}

type question struct {
	name  string
	qtype uint16
}

type record struct {
	name  string
	rtype uint16
	class uint16
	data  []byte
}

func (r *Responder) answer(qs []question, ip net.IP) []byte {
	r.mu.Lock()
	svcs := append([]Service(nil), r.services...)
	r.mu.Unlock()
	if len(svcs) == 0 {
		return nil
	}
	set := answerSet{hostname: r.hostname}
	for _, q := range qs {
		set.addQuestion(q, svcs)
	}
	if len(set.answers) == 0 && len(set.extra) == 0 {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		set.fillAddress(ip4)
	}
	return encodeMessage(set.answers, set.extra)
}

// answerSet collects the answer and additional records for one reply.
type answerSet struct {
	hostname       string
	answers, extra []record
}

// wants reports whether q asks for records of type t.
func wants(q question, t uint16) bool { return q.qtype == t || q.qtype == typeANY }

// instanceName is the full service instance name for s.
func instanceName(s Service) string { return escape(s.Instance) + "." + serviceType }

// addQuestion adds the records that answer q.
func (a *answerSet) addQuestion(q question, svcs []Service) {
	name := strings.ToLower(q.name)
	switch {
	case name == metaQuery && wants(q, typePTR):
		a.answers = append(a.answers, record{metaQuery, typePTR, classIN, encodeName(serviceType)})
	case name == serviceType && wants(q, typePTR):
		for _, s := range svcs {
			a.addService(s, true)
		}
	case name == strings.ToLower(a.hostname) && wants(q, typeA):
		// A placeholder: fillAddress supplies the address, and records without data are dropped.
		a.answers = append(a.answers, record{a.hostname, typeA, classIN | flush, nil})
	default:
		for _, s := range svcs {
			if name == strings.ToLower(instanceName(s)) {
				a.addService(s, false)
			}
		}
	}
}

// addService adds s's SRV and TXT records, and its PTR record when withPTR is set.
func (a *answerSet) addService(s Service, withPTR bool) {
	inst := instanceName(s)
	if withPTR {
		a.answers = append(a.answers, record{serviceType, typePTR, classIN, encodeName(inst)})
	}
	srv := make([]byte, 6)
	binary.BigEndian.PutUint16(srv[4:], uint16(s.Port))
	a.extra = append(a.extra, record{inst, typeSRV, classIN | flush, append(srv, encodeName(a.hostname)...)},
		record{inst, typeTXT, classIN | flush, encodeTXT(s.TXT)})
}

// fillAddress fills in the A placeholders and adds the address to the additional records.
func (a *answerSet) fillAddress(ip4 net.IP) {
	rec := record{a.hostname, typeA, classIN | flush, []byte(ip4)}
	for i := range a.answers {
		if a.answers[i].rtype == typeA {
			a.answers[i] = rec
		}
	}
	if len(a.extra) > 0 {
		a.extra = append(a.extra, rec)
	}
}

// ------------------------------------------------------------------------------ DNS encoding

func escape(s string) string { return strings.ReplaceAll(s, ".", "\\.") }

func encodeName(name string) []byte {
	var b []byte
	var label []byte
	flushLabel := func() {
		if len(label) > 63 {
			label = label[:63]
		}
		if len(label) > 0 {
			b = append(b, byte(len(label)))
			b = append(b, label...)
		}
		label = label[:0]
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '\\' && i+1 < len(name):
			i++
			label = append(label, name[i])
		case c == '.':
			flushLabel()
		default:
			label = append(label, c)
		}
	}
	flushLabel()
	return append(b, 0)
}

func encodeTXT(kv map[string]string) []byte {
	var b []byte
	for _, k := range []string{"id", "shortname", "pio_env", "fw"} {
		v, ok := kv[k]
		if !ok {
			continue
		}
		s := k + "=" + v
		if len(s) > 255 {
			s = s[:255]
		}
		b = append(b, byte(len(s)))
		b = append(b, s...)
	}
	if len(b) == 0 {
		b = []byte{0}
	}
	return b
}

func encodeMessage(answers, extra []record) []byte {
	keep := func(rs []record) []record {
		out := rs[:0:0]
		for _, r := range rs {
			if r.data != nil {
				out = append(out, r)
			}
		}
		return out
	}
	answers, extra = keep(answers), keep(extra)
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[2:], 0x8400) // response, authoritative
	binary.BigEndian.PutUint16(msg[6:], uint16(len(answers)))
	binary.BigEndian.PutUint16(msg[10:], uint16(len(extra)))
	for _, rr := range append(answers, extra...) {
		msg = append(msg, encodeName(rr.name)...)
		var h [10]byte
		binary.BigEndian.PutUint16(h[0:], rr.rtype)
		binary.BigEndian.PutUint16(h[2:], rr.class)
		binary.BigEndian.PutUint32(h[4:], ttl)
		binary.BigEndian.PutUint16(h[8:], uint16(len(rr.data)))
		msg = append(msg, h[:]...)
		msg = append(msg, rr.data...)
	}
	return msg
}

// parseQuestions decodes the question section of a query (with name compression).
func parseQuestions(b []byte) ([]question, bool) {
	if len(b) < 12 {
		return nil, false
	}
	if binary.BigEndian.Uint16(b[2:])&0x8000 != 0 {
		return nil, false // a response
	}
	qd := int(binary.BigEndian.Uint16(b[4:]))
	off := 12
	var qs []question
	for i := 0; i < qd; i++ {
		name, n, ok := readName(b, off)
		if !ok || n+4 > len(b) {
			return qs, len(qs) > 0
		}
		qs = append(qs, question{name: name, qtype: binary.BigEndian.Uint16(b[n:])})
		off = n + 4
	}
	return qs, true
}

func readName(b []byte, off int) (string, int, bool) {
	var parts []string
	end := -1
	for jumps := 0; jumps < 16; {
		if off >= len(b) {
			return "", 0, false
		}
		l := int(b[off])
		switch {
		case l == 0:
			return strings.Join(parts, ".") + ".", endOr(end, off+1), true
		case l&0xC0 == 0xC0:
			if off+1 >= len(b) {
				return "", 0, false
			}
			end = endOr(end, off+2)
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3FFF)
			jumps++
		default:
			if off+1+l > len(b) {
				return "", 0, false
			}
			parts = append(parts, strings.ReplaceAll(string(b[off+1:off+1+l]), ".", "\\."))
			off += 1 + l
		}
	}
	return "", 0, false
}

// endOr returns end, or v if end hasn't been set yet (the name's end is where the first pointer is).
func endOr(end, v int) int {
	if end < 0 {
		return v
	}
	return end
}

// localIPv4For picks the interface address on the same subnet as peer (or the first usable one).
func localIPv4For(peer net.IP) net.IP {
	addrs, _ := net.InterfaceAddrs()
	var first net.IP
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		if first == nil {
			first = ipn.IP.To4()
		}
		if peer != nil && ipn.Contains(peer) {
			return ipn.IP.To4()
		}
	}
	return first
}
