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

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/pb"
)

const (
	start1       = 0x94
	start2       = 0xC3
	maxFrame     = 512
	idleTimeout  = 15 * time.Minute
	sendQueueLen = 512
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
	if first[0] == start1 {
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
	sess := NewSession(s.host, s.id, s.log, func(fr *pb.FromRadio) bool {
		select {
		case out <- fr:
			return true
		default:
			return false
		}
	})
	done := make(chan struct{})
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() {
		defer writerDone.Done()
		bw := bufio.NewWriter(c)
		for {
			select {
			case <-done:
				return
			case fr := <-out:
				b, err := proto.Marshal(fr)
				if err != nil || len(b) > maxFrame {
					continue
				}
				hdr := []byte{start1, start2, byte(len(b) >> 8), byte(len(b))}
				_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if _, err := bw.Write(hdr); err != nil {
					_ = c.Close()
					return
				}
				_, _ = bw.Write(b)
				if len(out) == 0 {
					if err := bw.Flush(); err != nil {
						_ = c.Close()
						return
					}
				}
			}
		}
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

// readFrames parses 0x94 0xC3 len16 frames, skipping any text (a client's debug console noise).
func readFrames(r io.Reader, fn func([]byte) bool) error {
	br := bufio.NewReader(r)
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
		if b != start2 {
			if b == start1 {
				_ = br.UnreadByte()
			}
			continue
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
		w.Header().Set("Content-Type", "text/plain")
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Expose-Headers", "X-Protobuf-Schema")
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
		q := s.httpQueue
		s.httpSess = NewSession(s.host, s.id, s.log, func(fr *pb.FromRadio) bool {
			select {
			case q <- fr:
				return true
			default:
				return false
			}
		})
	}
	return s.httpSess
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
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("X-Protobuf-Schema", "https://raw.githubusercontent.com/meshtastic/protobufs/master/meshtastic/mesh.proto")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleFromRadio(w http.ResponseWriter, r *http.Request) {
	s.httpSession()
	all, _ := strconv.ParseBool(r.URL.Query().Get("all"))
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("X-Protobuf-Schema", "https://raw.githubusercontent.com/meshtastic/protobufs/master/meshtastic/mesh.proto")
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
}

type managed struct {
	srv    *Server
	addr   string
	cancel context.CancelFunc
	err    string
}

func NewManager(h *mesh.Host, log *slog.Logger) *Manager {
	return &Manager{host: h, log: log, servers: map[uint32]*managed{}}
}

// Run syncs servers now and whenever an identity changes.
func (m *Manager) Run(ctx context.Context) {
	events, unsub := m.host.Bus.Subscribe(64)
	defer unsub()
	m.Sync(ctx)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for num, s := range m.servers {
				s.cancel()
				if s.srv != nil {
					_ = s.srv.Close()
				}
				delete(m.servers, num)
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
	want := map[uint32]string{}
	byNum := map[uint32]*mesh.Identity{}
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
	for num, s := range m.servers {
		if addr, ok := want[num]; !ok || addr != s.addr || (s.srv == nil) {
			s.cancel()
			if s.srv != nil {
				_ = s.srv.Close()
			}
			delete(m.servers, num)
		}
	}
	for num, addr := range want {
		if _, running := m.servers[num]; running {
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		srv, err := Listen(sctx, m.host, byNum[num], addr, m.log)
		ms := &managed{srv: srv, addr: addr, cancel: cancel}
		if err != nil {
			cancel()
			ms.srv, ms.err = nil, err.Error()
			m.log.Error("client API not started", "identity", byNum[num].NodeID(), "addr", addr, "err", err)
			continue // retried on the next sync
		}
		m.log.Info("client API listening", "identity", byNum[num].NodeID(), "addr", addr)
		m.servers[num] = ms
	}
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
