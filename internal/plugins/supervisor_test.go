package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/ScotMesh/RepeaterTastic/api/plugin/v1"
)

const scriptManifest = "id: script\napi: 1\nrun:\n  managed:\n    exec: run.sh\n"

// startedManager is a running Manager with a managed plugin whose run.sh is script, enabled.
func startedManager(t *testing.T, script string) *Manager {
	t.Helper()
	m := newTestManager(t, nil)
	installBundle(t, m, scriptManifest, script)
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); m.Wait() })
	return m
}

func pluginLog(m *Manager) string {
	lines, _ := m.Logs("script")
	return logText(lines)
}

func TestCrashingPluginGivesUp(t *testing.T) {
	m := startedManager(t, "echo out\necho err >&2\nexit 3")
	if err := m.Enable("script", nil); err != nil {
		t.Fatal(err)
	}
	crashed := func() bool { in, _ := m.Get("script"); return in.State == "crashed" }
	waitFor(t, 10*time.Second, crashed, func() string { return pluginLog(m) })
	in, _ := m.Get("script")
	log := pluginLog(m)
	if in.Restarts != maxQuickExit || !strings.Contains(in.Detail, "exit status 3") || in.StartedAt != 0 {
		t.Fatalf("after crashing: %+v", in)
	}
	for _, want := range []string{"stdout info: out", "stderr warn: err", "restarting in", "left off"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// Restart tries again (and it crashes again).
	if err := m.Restart("script"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { in, _ := m.Get("script"); return in.Restarts == 2*maxQuickExit && crashed() }, nil)
}

func TestPluginIgnoringTerminateIsKilled(t *testing.T) {
	m := startedManager(t, "trap '' TERM\necho ready\nwhile :; do sleep 0.05; done")
	if err := m.Enable("script", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return strings.Contains(pluginLog(m), "stdout info: ready") }, func() string { return pluginLog(m) })
	if in, _ := m.Get("script"); in.StartedAt == 0 || in.State != "starting" {
		t.Fatalf("running: %+v", in)
	}
	start := time.Now()
	if err := m.Disable("script"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 2*stopGrace-100*time.Millisecond {
		t.Fatalf("stopped after %s; it should have needed TERM and then KILL", took)
	}
	in, _ := m.Get("script")
	if in.State != "disabled" || !strings.Contains(pluginLog(m), "host info: stopped") {
		t.Fatalf("after disable: %+v\n%s", in, pluginLog(m))
	}
}

func TestPluginWithoutDataFolderCrashes(t *testing.T) {
	m := startedManager(t, "exit 0")
	writeFile(t, filepath.Join(m.dataRoot(), "script"), "a file where the data folder goes")
	if err := m.Enable("script", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { in, _ := m.Get("script"); return in.State == "crashed" }, nil)
	if in, _ := m.Get("script"); !strings.Contains(in.Detail, "not a directory") {
		t.Fatalf("detail %q", in.Detail)
	}
}

func TestPluginProgramMissingCrashes(t *testing.T) {
	m := startedManager(t, "exit 0")
	if err := os.Remove(filepath.Join(m.installedDir(), "script", "run.sh")); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable("script", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { in, _ := m.Get("script"); return in.State == "crashed" }, nil)
}

func TestStartPluginSkips(t *testing.T) {
	m := newTestManager(t, nil)
	for _, p := range []*plugin{
		{id: "a", rec: &record{}, run: &runner{}},
		{id: "b", rec: &record{}},
		{id: "c", rec: &record{Attached: true}, manifest: &Manifest{}},
	} {
		m.startPlugin(context.Background(), p)
		if p.state != "" {
			t.Errorf("%s started: %s", p.id, p.state)
		}
	}
}

func TestDropSessionKeepsNewerSession(t *testing.T) {
	m := &Manager{}
	sess := &session{token: "new", out: make(chan *pluginv1.HostMessage, 1), done: make(chan struct{})}
	p := &plugin{sess: sess}
	m.dropSession(p, "old")
	select {
	case <-sess.done:
		t.Fatal("closed another process's session")
	default:
	}
	m.dropSession(p, "new")
	<-sess.done
	m.dropSession(&plugin{}, "x")
}

func TestPluginEnv(t *testing.T) {
	t.Setenv("PATH", "/bin")
	t.Setenv("SECRET_THING", "x")
	env := strings.Join(pluginEnv(map[string]string{"RT_PLUGIN_ID": "p"}), "\n")
	if !strings.Contains(env, "PATH=/bin") || !strings.Contains(env, "RT_PLUGIN_ID=p") || strings.Contains(env, "SECRET_THING") {
		t.Fatalf("env %s", env)
	}
}
