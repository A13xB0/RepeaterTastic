package pluginsdk

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	pluginv1 "github.com/ScotMesh/RepeaterTastic/pluginapi/v1"
)

func TestMain(m *testing.M) {
	heartbeatInterval = 10 * time.Millisecond
	os.Exit(m.Run())
}

type sessionFunc func(pluginv1.PluginHost_SessionServer) error

// fakeHost is a PluginHost whose Session behaviour each test supplies.
type fakeHost struct {
	pluginv1.UnimplementedPluginHostServer
	session sessionFunc
}

func (f *fakeHost) Session(s pluginv1.PluginHost_SessionServer) error { return f.session(s) }

func serve(t *testing.T, lis net.Listener, fn sessionFunc) {
	t.Helper()
	srv := grpc.NewServer()
	pluginv1.RegisterPluginHostServer(srv, &fakeHost{session: fn})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
}

// tcpHost starts a fake host on 127.0.0.1 and returns its address.
func tcpHost(t *testing.T, fn sessionFunc) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serve(t, lis, fn)
	return lis.Addr().String()
}

func clearEnv(t *testing.T) {
	for _, k := range []string{"RT_PLUGIN_ID", "RT_PLUGIN_SOCKET", "RT_PLUGIN_ADDR", "RT_PLUGIN_TOKEN"} {
		t.Setenv(k, "")
	}
}

func welcome(settings string) *pluginv1.HostMessage {
	return &pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Welcome{Welcome: &pluginv1.Welcome{SettingsJson: settings, HostVersion: "test"}}}
}

// handshake checks the token and Hello, then sends Welcome.
func handshake(s pluginv1.PluginHost_SessionServer, settings string) error {
	md, _ := metadata.FromIncomingContext(s.Context())
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer tok" {
		return errors.New("bad token")
	}
	m, err := s.Recv()
	if err != nil {
		return err
	}
	if h := m.GetHello(); h == nil || h.PluginId != "hello" || h.ApiVersion != 1 {
		return errors.New("bad hello")
	}
	return s.Send(welcome(settings))
}

func TestConnectMissingOptions(t *testing.T) {
	clearEnv(t)
	cases := []struct {
		o    Options
		want string
	}{
		{Options{Addr: "x:1", Token: "t"}, "no plugin id"},
		{Options{ID: "a", Token: "t"}, "no RepeaterTastic"},
		{Options{ID: "a", Addr: "x:1"}, "no token"},
	}
	for _, c := range cases {
		if _, err := Connect(context.Background(), c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err %v, want %q", c.o, err, c.want)
		}
	}
}

func TestConnectBadTarget(t *testing.T) {
	clearEnv(t)
	if _, err := Connect(context.Background(), Options{ID: "a", Addr: "dns://%zz/x", Token: "t"}); err == nil {
		t.Fatal("bad target accepted")
	}
}

func TestConnectCancelledContext(t *testing.T) {
	clearEnv(t)
	addr := tcpHost(t, func(s pluginv1.PluginHost_SessionServer) error { return handshake(s, "{}") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Connect(ctx, Options{ID: "hello", Addr: addr, Token: "tok"}); err == nil {
		t.Fatal("connected with a cancelled context")
	}
}

func TestConnectRejected(t *testing.T) {
	clearEnv(t)
	cases := map[string]sessionFunc{
		"closed": func(pluginv1.PluginHost_SessionServer) error { return errors.New("go away") },
		"no welcome": func(s pluginv1.PluginHost_SessionServer) error {
			if _, err := s.Recv(); err != nil {
				return err
			}
			return s.Send(&pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Stop{Stop: &pluginv1.Stop{}}})
		},
	}
	for name, fn := range cases {
		addr := tcpHost(t, fn)
		if c, err := Connect(context.Background(), Options{ID: "hello", Addr: addr, Token: "tok"}); err == nil {
			c.Close()
			t.Errorf("%s: connected", name)
		}
	}
}

// recorder collects what the plugin sends until the stream ends.
func recorder(got chan<- *pluginv1.PluginMessage, script ...*pluginv1.HostMessage) sessionFunc {
	return func(s pluginv1.PluginHost_SessionServer) error {
		if err := handshake(s, `{"n":1}`); err != nil {
			return err
		}
		go func() {
			for {
				m, err := s.Recv()
				if err != nil {
					return
				}
				got <- m
			}
		}()
		for _, m := range script {
			if err := s.Send(m); err != nil {
				return err
			}
		}
		<-s.Context().Done()
		return nil
	}
}

type settings struct {
	N int `json:"n"`
}

func waitFor(t *testing.T, got <-chan *pluginv1.PluginMessage, match func(*pluginv1.PluginMessage) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m := <-got:
			if match(m) {
				return
			}
		case <-deadline:
			t.Fatal("expected message not received")
		}
	}
}

func TestSessionFromEnvOverUnixSocket(t *testing.T) {
	clearEnv(t)
	sock := filepath.Join(t.TempDir(), "rt.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *pluginv1.PluginMessage, 64)
	stop := &pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Stop{Stop: &pluginv1.Stop{Reason: "bye"}}}
	serve(t, lis, recorder(got,
		&pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Settings{Settings: &pluginv1.SettingsChanged{SettingsJson: `{"n":2}`}}},
		stop))
	t.Setenv("RT_PLUGIN_ID", "hello")
	t.Setenv("RT_PLUGIN_SOCKET", sock)
	t.Setenv("RT_PLUGIN_TOKEN", "tok")
	c, err := Connect(context.Background(), Options{Version: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Welcome.HostVersion != "test" {
		t.Fatalf("welcome %v", c.Welcome)
	}
	var kinds []string
	for m := range c.Events() {
		kinds = append(kinds, kindOf(m))
	}
	if strings.Join(kinds, ",") != "settings,stop" {
		t.Fatalf("events %v", kinds)
	}
	var s settings
	if err := c.Settings(&s); err != nil || s.N != 2 {
		t.Fatalf("settings %+v %v", s, err)
	}
	checkEnded(t, c, nil)
}

func kindOf(m *pluginv1.HostMessage) string {
	switch {
	case m.GetSettings() != nil:
		return "settings"
	case m.GetStop() != nil:
		return "stop"
	}
	return "other"
}

func checkEnded(t *testing.T, c *Client, wantErr error) {
	t.Helper()
	select {
	case <-c.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context not done")
	}
	if (c.Err() == nil) != (wantErr == nil) {
		t.Fatalf("Err %v, want %v", c.Err(), wantErr)
	}
}

func TestSessionSendsToHost(t *testing.T) {
	clearEnv(t)
	got := make(chan *pluginv1.PluginMessage, 64)
	addr := tcpHost(t, recorder(got))
	c, err := Connect(context.Background(), Options{ID: "hello", Addr: addr, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var s settings
	if err := c.Settings(&s); err != nil || s.N != 1 {
		t.Fatalf("settings %+v %v", s, err)
	}
	if err := c.Status("fine", "ok", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, got, func(m *pluginv1.PluginMessage) bool {
		st := m.GetStatus()
		return st != nil && st.Summary == "fine" && st.State == "ok" && st.Fields["k"] == "v"
	})
	if err := c.Log("warn", "x=%d", 3); err != nil {
		t.Fatal(err)
	}
	waitFor(t, got, func(m *pluginv1.PluginMessage) bool {
		l := m.GetLog()
		return l != nil && l.Level == "warn" && l.Message == "x=3"
	})
	if err := c.Panel(map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, got, func(m *pluginv1.PluginMessage) bool { return m.GetPanel().GetJson() == `{"a":1}` })
	waitFor(t, got, func(m *pluginv1.PluginMessage) bool { return m.GetHeartbeat() != nil })
}

func TestPanelEncodeError(t *testing.T) {
	c := &Client{}
	if err := c.Panel(func() {}); err == nil {
		t.Fatal("unencodable panel accepted")
	}
}

func TestSessionEndsWithHostError(t *testing.T) {
	clearEnv(t)
	addr := tcpHost(t, func(s pluginv1.PluginHost_SessionServer) error {
		if err := handshake(s, "{}"); err != nil {
			return err
		}
		return errors.New("host shutting down")
	})
	c, err := Connect(context.Background(), Options{ID: "hello", Addr: addr, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range c.Events() {
		t.Fatal("unexpected event")
	}
	checkEnded(t, c, errors.New("any"))
	if !strings.Contains(c.Err().Error(), "host shutting down") {
		t.Fatalf("Err %v", c.Err())
	}
}

func TestCloseEndsSession(t *testing.T) {
	clearEnv(t)
	got := make(chan *pluginv1.PluginMessage, 64)
	addr := tcpHost(t, recorder(got))
	c, err := Connect(context.Background(), Options{ID: "hello", Addr: addr, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	for range c.Events() {
		t.Fatal("unexpected event")
	}
	checkEnded(t, c, errors.New("cancelled"))
	if err := c.Status("late", "ok", nil); err == nil {
		t.Fatal("send after Close succeeded")
	}
}
