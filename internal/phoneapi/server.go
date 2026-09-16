package phoneapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const (
	start1       = 0x94
	start2       = 0xC3
	maxFrame     = 512
	idleTimeout  = 15 * time.Minute
	sendQueueLen = 512

	headerContentType    = "Content-Type"
	headerProtobufSchema = "X-Protobuf-Schema"
	contentTypeProtobuf  = "application/x-protobuf"
	meshProtoSchemaURL   = "https://raw.githubusercontent.com/meshtastic/protobufs/master/meshtastic/mesh.proto"
)

// Server listens on one identity's port. A TCP client speaking the stream protocol (0x94 0xC3 framing,
// as the apps and Python CLI do) and an HTTP client (the Meshtastic web client's /api/v1/toradio and
// /api/v1/fromradio) are told apart by the first byte.
type Server struct {
	host *mesh.Host
	id   *mesh.Identity
	log  *slog.Logger
	ln   net.Listener

	httpOnce    sync.Once
	httpConns   chan net.Conn
	httpSess    *Session
	httpQueue   chan *pb.FromRadio
	httpSessMu  sync.Mutex
	httpTouched time.Time

	wg sync.WaitGroup
}

// Listen starts serving identity id on addr (e.g. "0.0.0.0:4403").
func Listen(ctx context.Context, h *mesh.Host, id *mesh.Identity, addr string, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{host: h, id: id, log: log.With("identity", id.NodeID(), "addr", addr), ln: ln,
		httpConns: make(chan net.Conn, 16)}
	s.wg.Add(1)
	go s.acceptLoop(ctx)
	return s, nil
}

func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close stops the listener and waits for connections to finish.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

func (s *Server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()
	go func() {
		<-ctx.Done()
		_ = s.ln.Close()
	}()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			s.log.Warn("accept", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.route(ctx, c)
		}()
	}
}

type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func (s *Server) route(ctx context.Context, c net.Conn) {
	br := bufio.NewReaderSize(c, 4096)
	_ = c.SetReadDeadline(time.Now().Add(idleTimeout))
	first, err := br.Peek(1)
	if err != nil {
		_ = c.Close()
		return
	}
	pc := &peekConn{Conn: c, r: br}
	// HTTP requests start with an upper-case method name. Anything else is the stream protocol,
	// including the 0xC3 wake-up bytes some clients send before the first frame.
	if first[0] < 'A' || first[0] > 'Z' {
		s.serveStream(ctx, pc)
		return
	}
	s.httpOnce.Do(func() { go s.serveHTTP(ctx) })
	select {
	case s.httpConns <- pc:
	case <-ctx.Done():
		_ = c.Close()
	}
}

// ------------------------------------------------------------------------------------ stream

func (s *Server) serveStream(ctx context.Context, c net.Conn) {
	s.log.Info("client connected", "remote", c.RemoteAddr())
	out := make(chan *pb.FromRadio, sendQueueLen)
	sess := NewSession(s.host, s.id, s.log, queueSender(out))
	done := make(chan struct{})
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() {
		defer writerDone.Done()
		writeFrames(c, out, done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-done:
		}
	}()

	err := readFrames(c, func(payload []byte) bool {
		_ = c.SetReadDeadline(time.Now().Add(idleTimeout))
		tr := &pb.ToRadio{}
		if proto.Unmarshal(payload, tr) != nil {
			return true
		}
		return !sess.HandleToRadio(tr)
	})
	sess.Close()
	close(done)
	writerDone.Wait()
	_ = c.Close()
	s.log.Info("client disconnected", "remote", c.RemoteAddr(), "err", err)
}

// queueSender is a session's send function onto q, dropping frames rather than blocking when full.
func queueSender(q chan<- *pb.FromRadio) func(*pb.FromRadio) bool {
	return func(fr *pb.FromRadio) bool {
		select {
		case q <- fr:
			return true
		default:
			return false
		}
	}
}

// writeFrames writes out's frames to c until done, flushing once the queue is drained. A write
// error closes c, which ends the reader too.
func writeFrames(c net.Conn, out <-chan *pb.FromRadio, done <-chan struct{}) {
	bw := bufio.NewWriter(c)
	for {
		select {
		case <-done:
			return
		case fr := <-out:
			written, ok := writeFrame(c, bw, fr)
			if !ok {
				_ = c.Close()
				return
			}
			if written && len(out) == 0 && bw.Flush() != nil {
				_ = c.Close()
				return
			}
		}
	}
}

// writeFrame buffers one framed FromRadio, skipping (written false) any that can't be framed;
// ok false means the connection has failed.
func writeFrame(c net.Conn, bw *bufio.Writer, fr *pb.FromRadio) (written, ok bool) {
	b, err := proto.Marshal(fr)
	if err != nil || len(b) > maxFrame {
		return false, true
	}
	hdr := []byte{start1, start2, byte(len(b) >> 8), byte(len(b))}
	_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := bw.Write(hdr); err != nil {
		return false, false
	}
	_, _ = bw.Write(b)
	return true, true
}

// readFrames parses 0x94 0xC3 len16 frames, skipping any text (a client's debug console noise).
func readFrames(r io.Reader, fn func([]byte) bool) error {
	br := bufio.NewReader(r)
	for {
		if err := skipToFrameStart(br); err != nil {
			return err
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(lenBuf[:]))
		if n > maxFrame {
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}
		if !fn(payload) {
			return nil
		}
	}
}

// skipToFrameStart reads up to and including the next 0x94 0xC3 start marker.
func skipToFrameStart(br *bufio.Reader) error {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b != start1 {
			continue
		}
		b, err = br.ReadByte()
		if err != nil {
			return err
		}
		if b == start2 {
			return nil
		}
		if b == start1 {
			_ = br.UnreadByte()
		}
	}
}

// -------------------------------------------------------------------------------------- HTTP

type chanListener struct {
	ch   chan net.Conn
	done <-chan struct{}
	addr net.Addr
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

func (s *Server) serveHTTP(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/toradio", s.handleToRadio)
	mux.HandleFunc("/api/v1/fromradio", s.handleFromRadio)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentType, "text/plain")
		fmt.Fprintf(w, "RepeaterTastic virtual node %s (%s)\nMeshtastic HTTP API: /api/v1/toradio, /api/v1/fromradio\n",
			s.id.NodeID(), s.id.UserCopy().GetLongName())
	})
	srv := &http.Server{Handler: cors(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	_ = srv.Serve(&chanListener{ch: s.httpConns, done: ctx.Done(), addr: s.ln.Addr()})
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", headerContentType)
		w.Header().Set("Access-Control-Expose-Headers", headerProtobufSchema)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// httpSession is the single shared web-client session, like the firmware's webAPI.
func (s *Server) httpSession() *Session {
	s.httpSessMu.Lock()
	defer s.httpSessMu.Unlock()
	s.httpTouched = time.Now()
	if s.httpSess == nil {
		s.httpQueue = make(chan *pb.FromRadio, sendQueueLen)
		s.httpSess = NewSession(s.host, s.id, s.log, queueSender(s.httpQueue))
	}
	return s.httpSess
}

// setProtobufHeaders marks a response as a Meshtastic protobuf, as the firmware's web API does.
func setProtobufHeaders(w http.ResponseWriter) {
	w.Header().Set(headerContentType, contentTypeProtobuf)
	w.Header().Set(headerProtobufSchema, meshProtoSchemaURL)
}

func (s *Server) handleToRadio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "use PUT", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFrame+1))
	if err != nil || len(body) > maxFrame {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	tr := &pb.ToRadio{}
	if err := proto.Unmarshal(body, tr); err != nil {
		http.Error(w, "bad protobuf", http.StatusBadRequest)
		return
	}
	sess := s.httpSession()
	if sess.HandleToRadio(tr) {
		s.httpSessMu.Lock()
		sess.Close()
		s.httpSess = nil
		s.httpSessMu.Unlock()
	}
	setProtobufHeaders(w)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleFromRadio(w http.ResponseWriter, r *http.Request) {
	s.httpSession()
	all, _ := strconv.ParseBool(r.URL.Query().Get("all"))
	setProtobufHeaders(w)
	s.httpSessMu.Lock()
	q := s.httpQueue
	s.httpSessMu.Unlock()
	for {
		select {
		case fr := <-q:
			b, _ := proto.Marshal(fr)
			_, _ = w.Write(b)
			if !all {
				return
			}
		default:
			return
		}
	}
}

// ----------------------------------------------------------------------------------- manager

// Manager keeps one Server per enabled identity with an API port, following host changes.
type Manager struct {
	host *mesh.Host
	log  *slog.Logger

	mu      sync.Mutex
	servers map[uint32]*managed
	failed  map[uint32]string // the last error starting each identity's server, logged once
	runCtx  context.Context   // Run's context: servers live as long as the manager, never a request
}

type managed struct {
	srv    *Server
	addr   string
	cancel context.CancelFunc
}

func NewManager(h *mesh.Host, log *slog.Logger) *Manager {
	return &Manager{host: h, log: log, servers: map[uint32]*managed{}, failed: map[uint32]string{}}
}

// Run syncs servers now and whenever an identity changes.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()
	events, unsub := m.host.Bus.Subscribe(64)
	defer unsub()
	m.Sync(ctx)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for num := range m.servers {
				m.stopLocked(num)
			}
			m.mu.Unlock()
			return
		case e := <-events:
			if e.Type == "identity" {
				m.Sync(ctx)
			}
		case <-tick.C:
			m.Sync(ctx)
		}
	}
}

// Sync starts, restarts or stops servers to match the host's identities.
func (m *Manager) Sync(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want, byNum := m.wantedServers()
	for num, s := range m.servers {
		if addr, ok := want[num]; !ok || addr != s.addr || (s.srv == nil) {
			m.stopLocked(num)
		}
	}
	for num, addr := range want {
		if _, running := m.servers[num]; !running {
			m.startLocked(ctx, byNum[num], addr)
		}
	}
}

// wantedServers lists the listen address for every enabled, non-relay identity with an API port.
func (m *Manager) wantedServers() (want map[uint32]string, byNum map[uint32]*mesh.Identity) {
	want = map[uint32]string{}
	byNum = map[uint32]*mesh.Identity{}
	for _, id := range m.host.Identities() {
		if id.IsRelay || !id.Enabled || id.APIPort <= 0 {
			continue
		}
		bind := id.APIBind
		if bind == "" {
			bind = "0.0.0.0"
		}
		want[id.NodeNum] = net.JoinHostPort(bind, strconv.Itoa(id.APIPort))
		byNum[id.NodeNum] = id
	}
	return want, byNum
}

// startLocked starts id's server on addr, logging a failure only when it changes; a failed
// server is retried on the next sync. The caller holds m.mu.
func (m *Manager) startLocked(ctx context.Context, id *mesh.Identity, addr string) {
	num := id.NodeNum
	sctx, cancel := context.WithCancel(ctx)
	srv, err := Listen(sctx, m.host, id, addr, m.log)
	if err != nil {
		cancel()
		if m.failed[num] != err.Error() {
			m.failed[num] = err.Error()
			m.log.Error("client API not started; retrying quietly", "identity", id.NodeID(), "addr", addr, "err", err)
		}
		return
	}
	delete(m.failed, num)
	m.log.Info("client API listening", "identity", id.NodeID(), "addr", addr)
	m.servers[num] = &managed{srv: srv, addr: addr, cancel: cancel}
}

// stopLocked closes an identity's server, if it has one. The caller holds m.mu.
func (m *Manager) stopLocked(num uint32) {
	s, ok := m.servers[num]
	if !ok {
		return
	}
	s.cancel()
	if s.srv != nil {
		_ = s.srv.Close()
	}
	delete(m.servers, num)
}

// Status reports the listening address or error for an identity.
func (m *Manager) Status(num uint32) (addr string, running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[num]
	if !ok || s.srv == nil {
		return "", false
	}
	return s.srv.Addr().String(), true
}

// Restart stops an identity's server and starts it again straight away (dropping connected apps).
func (m *Manager) Restart(ctx context.Context, num uint32) {
	m.Stop(num)
	m.mu.Lock()
	if m.runCtx != nil {
		ctx = m.runCtx // a request's context would stop the server as soon as the request ends
	}
	m.mu.Unlock()
	m.Sync(ctx)
}

// Stop closes an identity's server now (dropping connected apps), e.g. before it moves to
// another radio's manager and that manager binds the same port.
func (m *Manager) Stop(num uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked(num)
}

// SyncNow is Sync with the manager's own context, for callers outside Run (after a move).
func (m *Manager) SyncNow() {
	m.mu.Lock()
	ctx := m.runCtx
	m.mu.Unlock()
	if ctx != nil {
		m.Sync(ctx)
	}
}
