package web

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/logbuf"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

func TestPutRelayOnMainRadio(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	code, obj, _ := call(t, env.srv, "PUT", "/api/v1/relay", tok, map[string]any{"role": "monitor"})
	if code != 200 || obj["role"] != "monitor" || obj["node_id"] != env.host.Relay().NodeID() {
		t.Fatalf("relay monitor: %d %v", code, obj)
	}
	if env.cfg.Relay.Role != "monitor" || env.host.Config().RelayRole != "monitor" {
		t.Fatalf("role not applied: config %q host %q", env.cfg.Relay.Role, env.host.Config().RelayRole)
	}
	if code, _, _ := call(t, env.srv, "PUT", "/api/v1/relay", tok, map[string]any{"role": "bogus"}); code != http.StatusBadRequest {
		t.Fatalf("bogus role: %d", code)
	}
	if code, _, _ := call(t, env.srv, "PUT", "/api/v1/relay", tok, "x"); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
}

func TestPutRelayOnExtraRadio(t *testing.T) {
	srv, hosts := testWebTwoRadiosHosts(t)
	tok := setupAndSignIn(t, srv)
	code, obj, _ := call(t, srv, "PUT", "/api/v1/relay?radio=mf", tok, map[string]any{"role": "off"})
	if code != 200 || obj["role"] != "off" || hosts[1].Config().RelayRole != "off" {
		t.Fatalf("mf relay off: %d %v", code, obj)
	}
	if hosts[0].Config().RelayRole == "off" {
		t.Fatal("the main radio's relay changed too")
	}
	if _, cfg, _ := call(t, srv, "GET", "/api/v1/config?radio=mf", tok, nil); objectAt(cfg, "relay")["role"] != "off" {
		t.Fatalf("mf config relay = %v", cfg["relay"])
	}
	if code, _, _ := call(t, srv, "PUT", "/api/v1/relay?radio=mf", tok, map[string]any{"role": "bogus"}); code != http.StatusBadRequest {
		t.Fatalf("bogus role on mf: %d", code)
	}
}

func TestLogsEndpoint(t *testing.T) {
	env := newTestEnv(t, func(o *Options) {
		o.Log = slog.New(logbuf.NewHandler(slog.NewTextHandler(io.Discard, nil), o.Logs))
	})
	tok := env.signIn(t)
	for i := 0; i < 3; i++ {
		call(t, env.srv, "POST", "/api/v1/auth/logout-all", tok, nil)
		tok = env.signIn(t)
	}
	_, _, all := call(t, env.srv, "GET", "/api/v1/logs?limit=0", tok, nil)
	_, _, one := call(t, env.srv, "GET", "/api/v1/logs?limit=1", tok, nil)
	if len(all) < 3 || len(one) != 1 {
		t.Fatalf("logs: %d entries, limit=1 gave %d", len(all), len(one))
	}
	if !strings.Contains(jsonOf(all), "all web sessions signed out") {
		t.Fatalf("logs = %v", all)
	}
}

// noFlush is a ResponseWriter that can't stream.
type noFlush struct{ rec *httptest.ResponseRecorder }

func (n noFlush) Header() http.Header         { return n.rec.Header() }
func (n noFlush) Write(b []byte) (int, error) { return n.rec.Write(b) }
func (n noFlush) WriteHeader(code int)        { n.rec.WriteHeader(code) }

func TestEventsNeedsStreaming(t *testing.T) {
	env := newTestEnv(t, nil)
	rec := httptest.NewRecorder()
	env.s.events(noFlush{rec}, httptest.NewRequest("GET", "/api/v1/events", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("events without a flusher: %d", rec.Code)
	}
}

// failWriter is a streaming ResponseWriter whose writes fail.
type failWriter struct{ h http.Header }

func (f failWriter) Header() http.Header     { return f.h }
func (failWriter) Write([]byte) (int, error) { return 0, errors.New("gone") }
func (failWriter) WriteHeader(int)           {}
func (failWriter) Flush()                    {}

func TestEventsStopsWhenTheClientHasGone(t *testing.T) {
	env := newTestEnv(t, nil)
	done := make(chan struct{})
	go func() {
		env.s.events(failWriter{h: http.Header{}}, httptest.NewRequest("GET", "/api/v1/events", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("events kept writing to a closed client")
	}
}

func TestSSESend(t *testing.T) {
	rec := httptest.NewRecorder()
	out := sseStream{w: rec, fl: rec}
	if !out.send("x", make(chan int)) {
		t.Fatal("an unencodable value should be skipped, not end the stream")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("unencodable value written: %q", rec.Body.String())
	}
	if !out.send("hello", map[string]int{"a": 1}) || rec.Body.String() != "event: hello\ndata: {\"a\":1}\n\n" {
		t.Fatalf("event = %q", rec.Body.String())
	}
	if (sseStream{w: failWriter{h: http.Header{}}, fl: failWriter{}}).send("x", 1) {
		t.Fatal("a failed write should end the stream")
	}
}

func TestForwardEventsStopsWithTheRequest(t *testing.T) {
	sub := make(chan mesh.Event, 1)
	sub <- mesh.Event{Type: "x"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { forwardEvents(ctx, nil, sub, make(chan radioEvent)); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forwardEvents blocked after the request ended")
	}
	// A closed subscription ends it too.
	closed := make(chan mesh.Event)
	close(closed)
	forwardEvents(context.Background(), nil, closed, make(chan radioEvent))
}

func TestEventStreamCarriesBusEvents(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	req, _ := http.NewRequest("GET", env.srv.URL+"/api/v1/events?token="+tok, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %v", resp.StatusCode, resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	waitForLine(t, sc, "event: status")
	env.host.Bus.Publish(mesh.Event{Type: "custom", Data: map[string]string{"k": "v"}})
	waitForLine(t, sc, "event: custom")
	if !sc.Scan() || sc.Text() != `data: {"k":"v"}` {
		t.Fatalf("custom data = %q", sc.Text())
	}
}

// waitForLine reads the stream until a line equals want.
func waitForLine(t *testing.T, sc *bufio.Scanner, want string) {
	t.Helper()
	for sc.Scan() {
		if sc.Text() == want {
			return
		}
	}
	t.Fatalf("stream ended before %q: %v", want, sc.Err())
}

func TestEventPayloads(t *testing.T) {
	env := newTestEnv(t, nil)
	s, rc := env.s, env.s.radios[0]
	one := []*radioCtx{rc}
	relay := env.host.Relay()

	p, ok := s.eventPayload(radioEvent{rc, mesh.Event{Type: "identity", Data: relay.NodeID()}}, one)
	if !ok || p.(map[string]any)["node_id"] != relay.NodeID() || p.(map[string]any)["long_name"] != "Relay" {
		t.Fatalf("identity payload = %v %v", p, ok)
	}
	p, ok = s.eventPayload(radioEvent{rc, mesh.Event{Type: "identity", Data: "!0000abcd"}}, one)
	if !ok || p.(map[string]any)["deleted"] != true {
		t.Fatalf("gone identity payload = %v %v", p, ok)
	}

	if _, ok := s.eventPayload(radioEvent{rc, mesh.Event{Type: "node", Data: "!0000abcd"}}, one); ok {
		t.Fatal("an unknown node was sent")
	}
	env.host.DB.Update(0xabcd, func(e *mesh.NodeEntry) { e.LastHeard = time.Now() })
	p, ok = s.eventPayload(radioEvent{rc, mesh.Event{Type: "node", Data: wire.NodeID(0xabcd)}}, one)
	if !ok || jsonOf(p.(map[string]any)["heard_by"]) != `["main"]` {
		t.Fatalf("node payload = %v %v", p, ok)
	}

	p, ok = s.eventPayload(radioEvent{rc, mesh.Event{Type: "log", Data: "line"}}, one)
	if !ok || p != "line" {
		t.Fatalf("log payload = %v %v", p, ok)
	}
	if _, ok := s.eventPayload(radioEvent{rc, mesh.Event{Type: "plugin", Data: "widget"}}, one); ok {
		t.Fatal("a plugin event was sent with plugins off")
	}
	p, ok = s.eventPayload(radioEvent{rc, mesh.Event{Type: "packet", Data: 7}}, one)
	if !ok || p != 7 {
		t.Fatalf("other payload = %v %v", p, ok)
	}
}

func TestSiteEventPayloads(t *testing.T) {
	s, _, hosts := newSiteServer(t)
	all := s.radios
	main, mf := all[0], all[1]

	// Log events come from every radio's bus: only the main radio's are sent.
	if _, ok := s.eventPayload(radioEvent{mf, mesh.Event{Type: "log", Data: "x"}}, all); ok {
		t.Fatal("a log event was sent twice")
	}
	if p, ok := s.eventPayload(radioEvent{main, mesh.Event{Type: "log", Data: "x"}}, all); !ok || p != "x" {
		t.Fatalf("main log event = %v %v", p, ok)
	}
	// An identity that moved to mf is announced by mf, not as deleted on main.
	mfRelay := hosts[1].Relay().NodeID()
	if _, ok := s.eventPayload(radioEvent{main, mesh.Event{Type: "identity", Data: mfRelay}}, all); ok {
		t.Fatal("a moved identity was reported deleted")
	}
	// Nodes are merged across the site.
	hosts[1].DB.Update(0x1234, func(e *mesh.NodeEntry) { e.LastHeard = time.Now() })
	p, ok := s.eventPayload(radioEvent{mf, mesh.Event{Type: "node", Data: wire.NodeID(0x1234)}}, all)
	if !ok || jsonOf(p.(map[string]any)["heard_by"]) != `["mf"]` {
		t.Fatalf("site node payload = %v %v", p, ok)
	}
	if _, ok := s.eventPayload(radioEvent{mf, mesh.Event{Type: "node", Data: "!00009999"}}, all); ok {
		t.Fatal("an unknown node was sent to the site stream")
	}
}

func TestPutRelayOnExtraRadioTheNodeRefuses(t *testing.T) {
	srv, hosts := testWebTwoRadiosHosts(t)
	tok := setupAndSignIn(t, srv)
	hosts[1].AddConfigApplier(&fakeRemote{fail: errors.New("node offline")})
	code, obj, _ := call(t, srv, "PUT", "/api/v1/relay?radio=mf", tok, map[string]any{"role": "client"})
	if code != http.StatusBadGateway || !strings.Contains(obj["error"].(string), "node offline") {
		t.Fatalf("relay on an offline node: %d %v", code, obj)
	}
}
