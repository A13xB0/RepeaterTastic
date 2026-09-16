package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
)

// fakeMeshtasticd writes a program called meshtasticd that prints version and returns its path.
func fakeMeshtasticd(t *testing.T, version string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "meshtasticd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'meshtasticd "+version+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// tcpLauncher "runs" meshtasticd as a fake node listening on the instance's port.
type tcpLauncher struct {
	mu    sync.Mutex
	nodes map[int]*mtclienttest.Node
}

func (l *tcpLauncher) Run(ctx context.Context, in nodes.Instance, out io.Writer) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(in.Port))
	if err != nil {
		return err
	}
	l.mu.Lock()
	n := l.nodes[in.Port]
	if n == nil {
		n = mtclienttest.New(0x5e000000 + uint32(len(l.nodes)+1))
		l.nodes[in.Port] = n
	}
	l.mu.Unlock()
	_, _ = io.WriteString(out, "fake meshtasticd started\n")
	go n.Serve(ln)
	<-ctx.Done()
	ln.Close()
	n.Drop()
	return ctx.Err()
}

func (l *tcpLauncher) Version(context.Context) (string, error) { return "2.8.0.test", nil }
func (l *tcpLauncher) Describe() string                        { return "test launcher" }

// idleLauncher never runs anything.
type idleLauncher struct{}

func (idleLauncher) Run(ctx context.Context, _ nodes.Instance, _ io.Writer) error {
	<-ctx.Done()
	return ctx.Err()
}
func (idleLauncher) Version(context.Context) (string, error) { return "", errors.New("missing") }
func (idleLauncher) Describe() string                        { return "idle" }

func TestPutHosted(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	bin := fakeMeshtasticd(t, "2.8.1")
	code, obj, _ := call(t, env.srv, "PUT", "/api/v1/hosted", tok, map[string]any{"meshtasticd": " " + bin + " ", "port_base": 4500})
	if code != 200 || obj["meshtasticd"] != bin || obj["port_base"] != float64(4500) || obj["restart_required"] != true {
		t.Fatalf("put hosted: %d %v", code, obj)
	}
	if env.cfg.Hosted.PortBase != 0 {
		t.Fatalf("the default port base was written: %d", env.cfg.Hosted.PortBase)
	}
	if saved, _ := os.ReadFile(env.cfg.Path()); !strings.Contains(string(saved), bin) {
		t.Fatalf("not saved:\n%s", saved)
	}
	if code, obj, _ := call(t, env.srv, "PUT", "/api/v1/hosted", tok, map[string]any{"meshtasticd": fakeMeshtasticd(t, "2.7.0")}); code != 400 || !strings.Contains(obj["error"].(string), "too old") {
		t.Fatalf("old meshtasticd: %d %v", code, obj)
	}
	if code, _, _ := call(t, env.srv, "PUT", "/api/v1/hosted", tok, map[string]any{"meshtasticd": "/bin/sh"}); code != 400 {
		t.Fatalf("another program: %d", code)
	}
	if code, _ := env.do(t, "PUT", "/api/v1/hosted", tok, "application/json", "nope"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
	blockConfigFile(t, env.cfg.Path())
	if code, _, _ := call(t, env.srv, "PUT", "/api/v1/hosted", tok, map[string]any{"meshtasticd": bin}); code != http.StatusInternalServerError {
		t.Fatalf("unsaveable: %d", code)
	}
}

func TestHostedInstancesHealthAndLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var env *testEnv
	var hosting *nodes.Hosting
	env = newTestEnv(t, func(o *Options) {
		hosting = nodes.NewHosting(ctx, nodes.HostingOptions{Launcher: &tcpLauncher{nodes: map[int]*mtclienttest.Node{}},
			Air: nodes.NewLoRaAir(o.Host, nil), Radio: "main", Dir: t.TempDir(), PortBase: freePort(t)})
		o.Hosting = map[string]*nodes.Hosting{"main": hosting, "gone": nil}
	})
	t.Cleanup(cancel)
	tok := env.signIn(t)
	key, _ := decodeKey(keyWithLastByte(t, freeLastByte(env.host)))
	id, _ := mesh.NewIdentity(key, "Hosted Desk", "HD")
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	hid, err := hosting.HostIdentity(rctx, env.host, id.Record())
	if err != nil {
		t.Fatal(err)
	}
	_, h, _ := call(t, env.srv, "GET", "/api/v1/hosted", tok, nil)
	inst, _ := h["instances"].([]any)
	if len(inst) != 1 {
		t.Fatalf("instances = %v", h["instances"])
	}
	first := inst[0].(map[string]any)
	name := first["name"].(string)
	if first["radio"] != "main" || first["role"] != "identity" || name != "main-"+strings.TrimPrefix(hid.NodeID(), "!") {
		t.Fatalf("instance = %v", first)
	}
	_, st, _ := call(t, env.srv, "GET", "/api/v1/status", tok, nil)
	health := objectAt(st, "nodes")
	if health["nodes"] != float64(1) || health["launcher"] != "test launcher" {
		t.Fatalf("health = %v", health)
	}
	waitUntil(t, "the instance log", func() bool {
		code, _, lines := call(t, env.srv, "GET", "/api/v1/hosted/"+name+"/log", tok, nil)
		return code == 200 && strings.Contains(jsonOf(lines), "fake meshtasticd started")
	})
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/hosted/nope/log", tok, nil); code != http.StatusNotFound {
		t.Fatalf("log of a missing instance: %d", code)
	}
}

func TestNodesHealthAcrossRadios(t *testing.T) {
	s, _, hosts := newSiteServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	broken := nodes.NewHosting(ctx, nodes.HostingOptions{Launcher: idleLauncher{}, Air: nodes.NewLoRaAir(hosts[1], nil), Radio: "mf", Dir: t.TempDir()})
	broken.SetLauncherCheck("", errors.New("no such file"))
	fine := nodes.NewHosting(ctx, nodes.HostingOptions{Launcher: idleLauncher{}, Air: nodes.NewLoRaAir(hosts[0], nil), Radio: "main", Dir: t.TempDir()})
	fine.SetLauncherCheck("2.8.0", nil)
	s.opt.Hosting = map[string]*nodes.Hosting{"main": fine, "mf": broken}
	h := s.nodesHealth()
	problems, _ := h["problems"].([]string)
	if h["state"] != nodes.HealthError || len(problems) != 1 || !strings.HasPrefix(problems[0], "MediumFast: meshtasticd can't run") || h["version"] != "2.8.0" {
		t.Fatalf("health = %v", h)
	}
	if got := s.hostedInstances(); len(got) != 0 {
		t.Fatalf("instances = %v", got)
	}
}
