package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/radio/sim"
	"github.com/A13xB0/RepeaterTastic/pb"
	pluginv1 "github.com/A13xB0/RepeaterTastic/pluginapi/v1"
	"github.com/A13xB0/RepeaterTastic/pluginsdk"
)

// TestMain doubles as the plugin program: the bundle's run.sh starts this test binary with
// RT_TEST_PLUGIN set.
func TestMain(m *testing.M) {
	if os.Getenv("RT_TEST_PLUGIN") != "" || os.Getenv("RT_PLUGIN_ID") == "echo" {
		runEchoPlugin()
		return
	}
	os.Exit(m.Run())
}

// runEchoPlugin answers "ping" on a relay persona's channel with "pong <greeting>".
func runEchoPlugin() {
	c, err := pluginsdk.Connect(context.Background(), pluginsdk.Options{Version: "0.0.1"})
	if err != nil {
		os.Stderr.WriteString("connect: " + err.Error() + "\n")
		os.Exit(3)
	}
	var settings struct {
		Greeting string `json:"greeting"`
	}
	_ = c.Settings(&settings)
	_ = c.Status("connected", "ok", map[string]string{"radios": c.Welcome.Radios[0].Id})
	_ = c.Log("info", "echo plugin up")
	packets := 0
	for msg := range c.Events() {
		switch {
		case msg.GetPacket() != nil:
			packets++
		case msg.GetSettings() != nil:
			_ = c.Settings(&settings)
			_ = c.Status("greeting "+settings.Greeting, "ok", nil)
		case msg.GetText() != nil:
			t := msg.GetText()
			if t.Direction == "in" && t.Text == "ping" {
				_, err := c.Host.SendText(c.Context(), &pluginv1.SendTextRequest{RadioId: t.RadioId, Channel: t.Channel, Text: "pong " + settings.Greeting})
				if err != nil {
					_ = c.Log("error", "send: %v", err)
				}
				_ = c.Panel(map[string]int{"packets": packets})
			}
		case msg.GetStop() != nil:
			c.Close()
			os.Exit(0)
		}
	}
	os.Exit(0)
}

const echoManifest = `id: echo
name: Echo
version: 0.0.1
api: 1
permissions: [packets.read, messages.read, messages.send]
settings:
  - key: greeting
    label: Greeting
    type: string
    required: true
  - key: api_key
    type: secret
run:
  managed:
    exec: run.sh
`

func zipBundle(t *testing.T, files map[string]string, modes map[string]os.FileMode) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		mode := os.FileMode(0o644)
		if m, ok := modes[name]; ok {
			mode = m
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnpackRejectsUnsafeBundles(t *testing.T) {
	good := map[string]string{"plugin.yaml": echoManifest, "run.sh": "#!/bin/sh\n"}
	cases := map[string]struct {
		files map[string]string
		modes map[string]os.FileMode
		want  string
	}{
		"traversal": {map[string]string{"plugin.yaml": echoManifest, "run.sh": "", "../evil": "x"}, nil, "unsafe path"},
		"absolute":  {map[string]string{"plugin.yaml": echoManifest, "run.sh": "", "/etc/evil": "x"}, nil, "unsafe path"},
		"symlink":   {map[string]string{"plugin.yaml": echoManifest, "run.sh": "/etc/passwd"}, map[string]os.FileMode{"run.sh": os.ModeSymlink | 0o777}, "not a regular file"},
		"no exec":   {map[string]string{"plugin.yaml": echoManifest}, nil, "no program"},
		"no yaml":   {map[string]string{"run.sh": ""}, nil, "no plugin.yaml"},
		"bad id":    {map[string]string{"plugin.yaml": strings.Replace(echoManifest, "id: echo", "id: Echo!", 1), "run.sh": ""}, nil, "lowercase"},
		"bad perm":  {map[string]string{"plugin.yaml": strings.Replace(echoManifest, "packets.read", "radio.own", 1), "run.sh": ""}, nil, "unknown permission"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := zipBundle(t, c.files, c.modes)
			dir := t.TempDir()
			_, _, err := unpack(bytes.NewReader(b), int64(len(b)), dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("left files behind: %v", entries)
			}
		})
	}
	// One top-level folder is fine, and the program is made executable.
	nested := map[string]string{}
	for k, v := range good {
		nested["echo-0.0.1/"+k] = v
	}
	b := zipBundle(t, nested, nil)
	dir, m, err := unpack(bytes.NewReader(b), int64(len(b)), t.TempDir())
	if err != nil || m.ID != "echo" {
		t.Fatalf("nested bundle: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "run.sh")); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("run.sh isn't executable")
	}
}

func TestSettingsMaskAndMerge(t *testing.T) {
	m, err := ParseManifest([]byte(echoManifest))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := mergeSettings(m.Settings, nil, map[string]any{"greeting": "hi", "api_key": "s3cret"}, siteChoices{})
	if err != nil {
		t.Fatal(err)
	}
	masked, set := maskSettings(m.Settings, saved)
	if masked["api_key"] != SecretMask || len(set) != 1 || masked["greeting"] != "hi" {
		t.Fatalf("masked = %v %v", masked, set)
	}
	// Sending the mask back keeps the secret; an unknown key is refused.
	saved, err = mergeSettings(m.Settings, saved, map[string]any{"api_key": SecretMask, "greeting": "yo"}, siteChoices{})
	if err != nil || saved["api_key"] != "s3cret" || saved["greeting"] != "yo" {
		t.Fatalf("merge = %v %v", saved, err)
	}
	if _, err := mergeSettings(m.Settings, saved, map[string]any{"nope": 1}, siteChoices{}); err == nil {
		t.Fatal("unknown setting accepted")
	}
	// Lists: radios are checked against the site's radios; an empty list clears the setting.
	radios := Setting{Key: "radios", Label: "Radios", Type: "radios"}
	multi := Setting{Key: "ports", Label: "Ports", Type: "multiselect", Options: []string{"a", "b"}}
	got, err := mergeSettings([]Setting{radios, multi}, nil, map[string]any{"radios": []any{"main", "mf", "main"}, "ports": []any{"b"}}, siteChoices{radios: []string{"main", "mf"}})
	if err != nil || len(got["radios"].([]string)) != 2 || got["ports"].([]string)[0] != "b" {
		t.Fatalf("lists = %v %v", got, err)
	}
	if _, err := mergeSettings([]Setting{radios}, nil, map[string]any{"radios": []any{"nope"}}, siteChoices{radios: []string{"main"}}); err == nil {
		t.Fatal("unknown radio accepted")
	}
	if _, err := mergeSettings([]Setting{multi}, nil, map[string]any{"ports": []any{"c"}}, siteChoices{}); err == nil {
		t.Fatal("unknown option accepted")
	}
	if got, _ := mergeSettings([]Setting{radios}, got, map[string]any{"radios": []any{}}, siteChoices{}); got["radios"] != nil {
		t.Fatal("an empty list didn't clear the setting")
	}
	t.Setenv("RT_TEST_KEY", "from-env")
	if got := resolveSettings(m.Settings, map[string]any{"api_key": "${RT_TEST_KEY}"}, true)["api_key"]; got != "from-env" {
		t.Fatalf("env expansion = %v", got)
	}
}

func TestBudget(t *testing.T) {
	b := newBucket(12) // burst 2
	for i := 0; i < 2; i++ {
		if ok, _ := b.take(); !ok {
			t.Fatalf("send %d refused", i)
		}
	}
	if ok, wait := b.take(); ok || wait < 4*time.Minute {
		t.Fatalf("third send: ok=%v wait=%s", ok, wait)
	}
	if ok, _ := newBucket(0).take(); ok {
		t.Fatal("a zero budget allowed a send")
	}
}

// TestManagedPluginEndToEnd installs a bundle, enables it, and checks the plugin sees a channel
// message on the relay persona, answers it on air, reports status and stops when disabled.
func TestManagedPluginEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := sim.NewHub(0)
	hostA := newHost(t, ctx, hub, "A", log)
	hostB := newHost(t, ctx, hub, "B", log)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "plugins") // Unix socket paths must stay short
	if len(dir) > 80 {
		dir, _ = os.MkdirTemp("", "rtp")
		defer os.RemoveAll(dir)
	}
	cfg := config.Default().Plugins
	m, err := New(Options{Config: cfg, Dir: dir, Radios: []Radio{{ID: "main", Name: "Main", Host: hostA}}, Version: "test", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	b := zipBundle(t, map[string]string{"plugin.yaml": echoManifest, "run.sh": "#!/bin/sh\nexec " + self + " -test.run=^$\n"},
		map[string]os.FileMode{"run.sh": 0o755})
	if _, err := m.Install(bytes.NewReader(b), int64(len(b)), "test"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable("echo", []string{"packets.read", "messages.read", "messages.send"}); err == nil {
		t.Fatal("enabled without its required setting")
	}
	if err := m.SetSettings("echo", map[string]any{"greeting": "from echo"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable("echo", []string{"packets.read", "messages.read", "messages.send"}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = m.Run(ctx) }()
	waitFor(t, 15*time.Second, func() bool {
		in, _ := m.Get("echo")
		return in.Connected && in.Status != nil && in.Status.Summary == "connected"
	}, func() string { lines, _ := m.Logs("echo"); return logText(lines) })

	events, unsub := hostB.Bus.Subscribe(256)
	defer unsub()
	if _, err := hostB.SendText(hostB.Relay(), 0xffffffff, 0, "ping", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(15 * time.Second)
	for got := false; !got; {
		select {
		case e := <-events:
			if me, ok := e.Data.(mesh.MessageEvent); ok && me.Message.Direction == "in" && me.Message.Text == "pong from echo" {
				got = true
			}
		case <-deadline:
			lines, _ := m.Logs("echo")
			t.Fatalf("no pong on air; plugin log:\n%s", logText(lines))
		}
	}
	waitFor(t, 5*time.Second, func() bool { d, _ := m.PanelData("echo"); return strings.Contains(d, "packets") }, nil)

	// Settings reach the running plugin.
	if err := m.SetSettings("echo", map[string]any{"greeting": "hello again"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		in, _ := m.Get("echo")
		return in.Status != nil && in.Status.Summary == "greeting hello again"
	}, nil)

	if err := m.Disable("echo"); err != nil {
		t.Fatal(err)
	}
	in, _ := m.Get("echo")
	if in.Connected || in.State != "disabled" {
		t.Fatalf("after disable: connected=%v state=%s", in.Connected, in.State)
	}
	if err := m.Remove("echo", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "installed", "echo")); !os.IsNotExist(err) {
		t.Fatal("bundle folder left behind")
	}
}

func newHost(t *testing.T, ctx context.Context, hub *sim.Hub, name string, log *slog.Logger) *mesh.Host {
	t.Helper()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, NodeInfoInterval: time.Hour}, hub.Attach(name, 64), log)
	if err != nil {
		t.Fatal(err)
	}
	for {
		id, err := mesh.NewIdentity(nil, name+" relay", "")
		if err != nil {
			continue
		}
		id.IsRelay = true
		if h.AddIdentity(id) == nil {
			break
		}
	}
	go func() { _ = h.Run(ctx) }()
	return h
}

func waitFor(t *testing.T, d time.Duration, ok func() bool, why func() string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			msg := ""
			if why != nil {
				msg = why()
			}
			t.Fatalf("timed out\n%s", msg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func logText(lines []LogLine) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l.Source + " " + l.Level + ": " + l.Message + "\n")
	}
	return sb.String()
}
