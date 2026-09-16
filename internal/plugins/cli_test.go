package plugins

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

func cliConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Plugins.Dir = shortPluginDir(t)
	return cfg
}

// runCLI runs the CLI and returns its output.
func runCLI(t *testing.T, cfg *config.Config, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := CLI(cfg, args, &out)
	return out.String(), err
}

func mustCLI(t *testing.T, cfg *config.Config, args ...string) string {
	t.Helper()
	out, err := runCLI(t, cfg, args...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out
}

func bundleFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeFile(t, path, string(zipBundle(t, goodBundle, nil)))
	return path
}

func TestCLIUsageAndErrors(t *testing.T) {
	cfg := cliConfig(t)
	for _, args := range [][]string{nil, {"help"}, {"-h"}} {
		if out := mustCLI(t, cfg, args...); !strings.HasPrefix(out, "usage:") {
			t.Errorf("%v: %q", args, out)
		}
	}
	if out := mustCLI(t, cfg, "permissions"); !strings.Contains(out, "traceroute.send") || strings.Count(out, "\n") != len(Permissions) {
		t.Errorf("permissions: %q", out)
	}
	errs := map[string][]string{
		"unknown command": {"frobnicate"},
		"needs":           {"enable"},
		"no such plugin":  {"disable", "nope"},
		"flag provided":   {"remove", "x", "-bogus"},
		"no such file":    {"install", "/does/not/exist.zip"},
		"not a zip":       {"install", "/dev/null"},
	}
	for want, args := range errs {
		if _, err := runCLI(t, cfg, args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v, want %q", args, err, want)
		}
	}
	file := filepath.Join(t.TempDir(), "f")
	writeFile(t, file, "")
	bad := config.Default()
	bad.Plugins.Dir = filepath.Join(file, "plugins")
	if _, err := runCLI(t, bad, "list"); err == nil {
		t.Error("CLI ran without a plugins folder")
	}
}

func TestCLILifecycle(t *testing.T) {
	cfg := cliConfig(t)
	out := mustCLI(t, cfg, "install", bundleFile(t, "echo.zip"))
	if !strings.Contains(out, "Installed Echo 0.0.1 (echo)") {
		t.Fatalf("install: %q", out)
	}
	if _, err := runCLI(t, cfg, "enable", "echo", "all"); err == nil || !strings.Contains(err.Error(), "fill in Greeting") {
		t.Fatalf("enable without settings: %v", err)
	}
	m, err := New(Options{Config: cfg.Plugins, Dir: cfg.PluginDir(), Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetSettings("echo", map[string]any{"greeting": "hi"}); err != nil {
		t.Fatal(err)
	}
	out = mustCLI(t, cfg, "enable", "echo", "all")
	if out != "Echo enabled with messages.read, messages.send, packets.read\n" {
		t.Fatalf("enable: %q", out)
	}
	out = mustCLI(t, cfg, "ls")
	if !strings.Contains(out, "echo  Echo  0.0.1    managed  on      packets.read,messages.read,messages.send") {
		t.Fatalf("list:\n%s", out)
	}
	if out := mustCLI(t, cfg, "disable", "echo"); out != "echo disabled\n" {
		t.Fatalf("disable: %q", out)
	}
	writeFile(t, filepath.Join(cfg.PluginDir(), "data", "echo", "db"), "x")
	if out := mustCLI(t, cfg, "rm", "echo", "-keep-data"); out != "echo removed\n" {
		t.Fatalf("remove: %q", out)
	}
	if _, err := os.Stat(filepath.Join(cfg.PluginDir(), "data", "echo", "db")); err != nil {
		t.Fatal("-keep-data lost the data")
	}
	if out := mustCLI(t, cfg, "list"); strings.Count(out, "\n") != 1 {
		t.Fatalf("list after remove:\n%s", out)
	}
}

func TestCLIInstallFromURL(t *testing.T) {
	bundle := zipBundle(t, goodBundle, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/echo.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(bundle)
	}))
	defer srv.Close()
	cfg := cliConfig(t)
	if out := mustCLI(t, cfg, "install", srv.URL+"/echo.zip?x=1"); !strings.Contains(out, "Installed Echo") {
		t.Fatalf("install: %q", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.PluginDir(), "inbox", ".tmp")); len(entries) != 0 {
		t.Fatalf("download left behind: %v", entries)
	}
	if _, err := runCLI(t, cfg, "install", srv.URL+"/missing.zip"); err == nil {
		t.Fatal("installed a 404")
	}
}

func TestCLIHandsBundleToDaemon(t *testing.T) {
	cfg := cliConfig(t)
	m, err := New(Options{Config: cfg.Plugins, Dir: cfg.PluginDir(), Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, m.SocketPath())
	for src, name := range map[string]string{bundleFile(t, "echo.zip"): "echo.zip", bundleFile(t, "echo-bundle"): "echo-bundle.zip"} {
		out := mustCLI(t, cfg, "install", src)
		if !strings.Contains(out, "Handed "+filepath.Base(src)) || !strings.Contains(out, ".rejected") {
			t.Fatalf("output %q", out)
		}
		b, err := os.ReadFile(filepath.Join(m.InboxDir(), name))
		if err != nil || len(b) == 0 {
			t.Fatalf("%s not in the inbox: %v", name, err)
		}
	}
	if _, err := m.Get("echo"); err == nil {
		t.Fatal("the CLI installed it itself")
	}
	if err := copyToFile(filepath.Join(m.InboxDir(), "no", "such", "dir"), strings.NewReader("")); err == nil {
		t.Fatal("copyToFile into a missing folder")
	}
}

// fakeDaemon accepts connections on sock, as a running RepeaterTastic would.
func fakeDaemon(t *testing.T, sock string) {
	t.Helper()
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
}

func TestCLISwitch(t *testing.T) {
	cases := map[string]Info{
		"off":                            {},
		"on":                             {Enabled: true, State: "running"},
		"on, needs review (config file)": {Enabled: true, State: "needs_review", Pinned: true},
		"off, waiting":                   {State: "waiting"},
	}
	for want, in := range cases {
		if got := cliSwitch(in); got != want {
			t.Errorf("cliSwitch(%+v) = %q, want %q", in, got, want)
		}
	}
	chownLike(filepath.Join(t.TempDir(), "missing")) // not root, or no folder: nothing to do
}
