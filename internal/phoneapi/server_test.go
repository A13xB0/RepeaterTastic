package phoneapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func frame(payload []byte) []byte {
	return append([]byte{start1, start2, byte(len(payload) >> 8), byte(len(payload))}, payload...)
}

func collectFrames(t *testing.T, in []byte, stopAfter int) ([][]byte, error) {
	t.Helper()
	var got [][]byte
	err := readFrames(bytes.NewReader(in), func(p []byte) bool {
		got = append(got, p)
		return len(got) < stopAfter
	})
	return got, err
}

func TestReadFramesSkipsNoiseAndOversize(t *testing.T) {
	var in []byte
	in = append(in, "boot text"...)
	in = append(in, start1, 'x')                       // a lone start byte
	in = append(in, start1, start1, start2, 0, 1, 'a') // a doubled start byte still frames
	in = append(in, start1, start2, 0xFF, 0xFF)        // too long: skipped, its body read as noise
	in = append(in, frame([]byte("bc"))...)
	got, err := collectFrames(t, in, 10)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	if len(got) != 2 || string(got[0]) != "a" || string(got[1]) != "bc" {
		t.Fatalf("frames %q", got)
	}
}

func TestReadFramesStopsAndTruncation(t *testing.T) {
	in := append(frame([]byte("one")), frame([]byte("two"))...)
	got, err := collectFrames(t, in, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("stop: %q %v", got, err)
	}
	tests := map[string][]byte{
		"after start byte": {start1},
		"in length":        {start1, start2, 0},
		"in body":          {start1, start2, 0, 5, 'a'},
	}
	for name, in := range tests {
		if _, err := collectFrames(t, in, 10); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// failConn fails every write.
type failConn struct{ net.Conn }

func (failConn) Write([]byte) (int, error)        { return 0, errors.New("broken") }
func (failConn) SetWriteDeadline(time.Time) error { return nil }

func TestWriteFrameSkipsOversizeAndReportsFailure(t *testing.T) {
	big := &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: make([]byte, maxFrame+1)}}}}
	c := failConn{}
	if written, ok := writeFrame(c, bufio.NewWriterSize(c, 16), big); written || !ok {
		t.Fatalf("oversize frame: written=%v ok=%v", written, ok)
	}
	small := &pb.FromRadio{Id: 1}
	// A tiny buffer forces the header write through to the failing connection.
	if written, ok := writeFrame(c, bufio.NewWriterSize(c, 1), small); written || ok {
		t.Fatalf("failed write: written=%v ok=%v", written, ok)
	}
}

func TestWriteFramesClosesOnFlushError(t *testing.T) {
	a, b := net.Pipe()
	_ = b.Close()
	out := make(chan *pb.FromRadio, 1)
	out <- &pb.FromRadio{Id: 1}
	finished := make(chan struct{})
	go func() {
		writeFrames(a, out, make(chan struct{}))
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("writer kept going after a failed flush")
	}
}

func TestQueueSenderDropsWhenFull(t *testing.T) {
	q := make(chan *pb.FromRadio, 1)
	send := queueSender(q)
	if !send(&pb.FromRadio{}) || send(&pb.FromRadio{}) {
		t.Fatal("queue sender should take one frame then drop")
	}
}

func dialServer(t *testing.T, srv *Server) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("connection not closed by the server: %v", err)
	}
}

func TestStreamDisconnectClosesConnection(t *testing.T) {
	_, id, srv := testServer(t)
	c := dialServer(t, srv)
	// Garbage between frames is ignored.
	_, _ = c.Write(frame([]byte{0xFF, 0xFF}))
	writeToRadio(t, c, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Disconnect{Disconnect: true}})
	expectClosed(t, c)
	if id.ClientCount() != 0 {
		t.Fatal("session still attached")
	}
}

func TestEmptyConnectionIgnored(t *testing.T) {
	_, _, srv := testServer(t)
	c := dialServer(t, srv)
	_ = c.(*net.TCPConn).CloseWrite()
	expectClosed(t, c)
}

func TestContextEndClosesClients(t *testing.T) {
	h, id, _ := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := Listen(ctx, h, id, "127.0.0.1:0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := dialServer(t, srv)
	writeToRadio(t, c, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
	if readFromRadio(t, c).GetQueueStatus() == nil {
		t.Fatal("no heartbeat reply")
	}
	// An HTTP connection waiting to be served is dropped too.
	hc := dialServer(t, srv)
	_, _ = hc.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	cancel()
	expectClosed(t, c)
	if err := srv.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}

func TestListenFailsOnBusyPort(t *testing.T) {
	h, id, srv := testServer(t)
	if _, err := Listen(context.Background(), h, id, srv.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("second listener on the same port")
	}
}

// httpResult is what a test needs from a response, read and closed.
type httpResult struct {
	status int
	header http.Header
	body   []byte
}

func httpDo(t *testing.T, method, url string, body []byte) httpResult {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{status: resp.StatusCode, header: resp.Header, body: b}
}

func TestHTTPRequestHandling(t *testing.T) {
	_, id, srv := testServer(t)
	base := "http://" + srv.Addr().String()
	bigBody := make([]byte, maxFrame+1)
	tests := []struct {
		name, method, path string
		body               []byte
		status             int
	}{
		{"preflight", http.MethodOptions, "/api/v1/toradio", nil, http.StatusNoContent},
		{"get toradio", http.MethodGet, "/api/v1/toradio", nil, http.StatusMethodNotAllowed},
		{"oversize body", http.MethodPut, "/api/v1/toradio", bigBody, http.StatusBadRequest},
		{"bad protobuf", http.MethodPut, "/api/v1/toradio", []byte{0xFF}, http.StatusBadRequest},
		{"index", http.MethodGet, "/", nil, http.StatusOK},
	}
	for _, tc := range tests {
		res := httpDo(t, tc.method, base+tc.path, tc.body)
		if res.status != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, res.status, tc.status)
		}
		if res.header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: no CORS header", tc.name)
		}
		if tc.name == "index" && !strings.Contains(string(res.body), id.NodeID()) {
			t.Errorf("index page %q", res.body)
		}
	}
}

func putToRadio(t *testing.T, base string, tr *pb.ToRadio) {
	t.Helper()
	b, _ := proto.Marshal(tr)
	res := httpDo(t, http.MethodPost, base+"/api/v1/toradio", b)
	if res.status != http.StatusOK || res.header.Get(headerContentType) != contentTypeProtobuf {
		t.Fatalf("toradio status %d, type %q", res.status, res.header.Get(headerContentType))
	}
}

// fromRadioAll reads everything queued for the web client in one ?all=true request.
func fromRadioAll(t *testing.T, base string) []byte {
	t.Helper()
	return httpDo(t, http.MethodGet, base+"/api/v1/fromradio?all=true", nil).body
}

func TestHTTPDisconnectResetsSession(t *testing.T) {
	_, id, srv := testServer(t)
	base := "http://" + srv.Addr().String()
	putToRadio(t, base, &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 5}})
	if id.ClientCount() != 1 {
		t.Fatal("web session not attached")
	}
	putToRadio(t, base, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
	putToRadio(t, base, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
	// ?all=true drains several frames (concatenated) at once.
	if body := fromRadioAll(t, base); len(body) == 0 {
		t.Fatal("nothing queued")
	}
	if body := fromRadioAll(t, base); len(body) != 0 {
		t.Fatalf("queue not drained: %d bytes left", len(body))
	}

	putToRadio(t, base, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Disconnect{Disconnect: true}})
	if id.ClientCount() != 0 {
		t.Fatal("web session still attached after disconnect")
	}
}

func TestChanListener(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	done := make(chan struct{})
	l := &chanListener{ch: make(chan net.Conn), done: done, addr: addr}
	if l.Addr() != addr || l.Close() != nil {
		t.Fatal("listener addr/close")
	}
	close(done)
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept after done: %v", err)
	}
}

// ----------------------------------------------------------------------------------- manager

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func setAPI(id *mesh.Identity, enabled bool, port int) {
	id.SetSettings(func(s *mesh.IdentitySettings) {
		s.Enabled, s.APIPort, s.APIBind = enabled, port, "127.0.0.1"
	})
}

func waitStatus(t *testing.T, m *Manager, num uint32, running bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		addr, ok := m.Status(num)
		if ok == running {
			return addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("server running=%v, want %v", ok, running)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startManager(t *testing.T, h *mesh.Host) (*Manager, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	m := NewManager(h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return m, cancel, done
}

func TestManagerFollowsIdentities(t *testing.T) {
	h, id, _ := testServer(t)
	port := freePort(t)
	setAPI(id, true, port)
	m, cancel, done := startManager(t, h)
	addr := waitStatus(t, m, id.NodeNum, true)
	if !strings.HasSuffix(addr, ":"+strconv.Itoa(port)) {
		t.Fatalf("listening on %s, want port %d", addr, port)
	}
	if _, ok := m.Status(h.Relay().NodeNum); ok {
		t.Fatal("relay persona got a client API")
	}

	// Disabling the identity and announcing it stops the server.
	setAPI(id, false, port)
	h.Bus.Publish(mesh.Event{Type: "identity"})
	waitStatus(t, m, id.NodeNum, false)

	setAPI(id, true, port)
	m.SyncNow()
	waitStatus(t, m, id.NodeNum, true)

	m.Restart(context.Background(), id.NodeNum)
	waitStatus(t, m, id.NodeNum, true)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("restarted server not reachable: %v", err)
	}
	_ = c.Close()

	m.Stop(id.NodeNum)
	waitStatus(t, m, id.NodeNum, false)

	m.SyncNow()
	waitStatus(t, m, id.NodeNum, true)
	cancel()
	<-done
	if _, err := net.Dial("tcp", addr); err == nil {
		t.Fatal("server still listening after the manager stopped")
	}
}

func TestManagerRetriesBusyPort(t *testing.T) {
	h, id, _ := testServer(t)
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := blocker.Addr().(*net.TCPAddr).Port
	setAPI(id, true, port)
	m := NewManager(h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.SyncNow() // no Run context yet: nothing happens
	ctx := context.Background()
	m.Sync(ctx)
	m.Sync(ctx)
	if _, ok := m.Status(id.NodeNum); ok {
		t.Fatal("server running on a busy port")
	}
	if m.failed[id.NodeNum] == "" {
		t.Fatal("failure not remembered")
	}
	_ = blocker.Close()
	m.Sync(ctx)
	if _, ok := m.Status(id.NodeNum); !ok {
		t.Fatal("server not started once the port was free")
	}
	if _, stillFailed := m.failed[id.NodeNum]; stillFailed {
		t.Fatal("failure kept after a successful start")
	}
	m.Stop(id.NodeNum)
	m.Stop(id.NodeNum) // stopping twice is harmless
}
