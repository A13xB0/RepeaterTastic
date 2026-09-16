package web

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// fakeModem is a radio that reports a driver, a device and whether it answers.
type fakeModem struct {
	*null.Radio
	info      radio.Info
	connected bool
}

func (f *fakeModem) Info() radio.Info                  { return f.info }
func (f *fakeModem) Stats(context.Context) radio.Stats { return radio.Stats{Connected: f.connected} }

// diagModem is a fakeModem that reports chip diagnostics, as an SPI radio does.
type diagModem struct{ *fakeModem }

func (diagModem) Diagnostics() []string { return []string{"chip SX1262", "busy ok"} }

func probe(t *testing.T, env *testEnv, body map[string]any) (int, map[string]any) {
	t.Helper()
	code, obj, _ := call(t, env.srv, "POST", "/api/v1/setup/probe", "", body)
	return code, obj
}

func TestProbeRunningKISSModem(t *testing.T) {
	modem := &fakeModem{Radio: null.New(), info: radio.Info{Driver: "kiss", Device: "/dev/ttyUSB7", Firmware: "1.2", Name: "RAK"}, connected: true}
	env := newRadioEnv(t, modem, false, nil)
	code, res := probe(t, env, map[string]any{"device": ""})
	if code != 200 || res["ok"] != true || res["firmware"] != "1.2" || !strings.Contains(res["error"].(string), "sync word") {
		t.Fatalf("probe the running modem: %d %v", code, res)
	}
	// Detection doesn't open a port the running modem holds.
	code, res = probe(t, env, map[string]any{"driver": "auto", "device": "/dev/ttyUSB7"})
	if code != 200 || res["ok"] != true || res["driver"] != "kiss" || jsonOf(res["details"]) != `["this radio already uses the modem"]` {
		t.Fatalf("detect the running modem: %d %v", code, res)
	}
	modem.connected = false
	env.s.radios[0].statsAt = env.s.radios[0].statsAt.AddDate(-1, 0, 0) // forget the cached stats
	code, res = probe(t, env, map[string]any{"driver": "kiss", "device": ""})
	if code != 200 || res["ok"] != false || !strings.Contains(res["error"].(string), "isn't answering") {
		t.Fatalf("probe a silent modem: %d %v", code, res)
	}
}

func TestProbeKISSRefusalsAndMissingPort(t *testing.T) {
	env := newTestEnv(t, nil)
	for _, body := range []map[string]any{{"driver": "lora"}, {"driver": "kiss", "device": "/etc/passwd"}} {
		if code, res := probe(t, env, body); code != http.StatusBadRequest {
			t.Errorf("probe %v: %d %v", body, code, res)
		}
	}
	if code, _ := env.do(t, "POST", "/api/v1/setup/probe", "", "application/json", "nope"); code != http.StatusBadRequest {
		t.Errorf("bad body: %d", code)
	}
	code, res := probe(t, env, map[string]any{"device": "/dev/ttyUSB-none"})
	if code != 200 || res["ok"] != false || !strings.Contains(res["error"].(string), "no modem answered") {
		t.Fatalf("probe a missing port: %d %v", code, res)
	}
}

func TestProbeSPI(t *testing.T) {
	env := newTestEnv(t, nil)
	for _, dev := range []string{"", "/etc/passwd"} {
		if code, res := probe(t, env, map[string]any{"driver": "spi", "device": dev}); code != http.StatusBadRequest {
			t.Errorf("spi %q: %d %v", dev, code, res)
		}
	}
	for _, dev := range []string{"no-such-board", "/etc/meshtasticd/config.d/lora-no-such-board.yaml"} {
		code, res := probe(t, env, map[string]any{"driver": "spi", "device": dev})
		if code != 200 || res["ok"] != false || res["error"] == "" {
			t.Errorf("spi %q: %d %v", dev, code, res)
		}
	}
}

func TestProbeRunningSPIRadio(t *testing.T) {
	modem := &fakeModem{Radio: null.New(), info: radio.Info{Driver: "spi", Firmware: "SX1262", Name: "Waveshare"}, connected: true}
	spiCfg := func(o *Options) { o.Config.Radio.Driver, o.Config.Radio.Device = "spi", "lora-waveshare" }
	env := newRadioEnv(t, diagModem{modem}, false, spiCfg)
	code, res := probe(t, env, map[string]any{"driver": "spi", "device": ""})
	if code != 200 || res["ok"] != true || res["name"] != "Waveshare" || jsonOf(res["details"]) != `["chip SX1262","busy ok"]` {
		t.Fatalf("probe the running board: %d %v", code, res)
	}
	silent := &fakeModem{Radio: null.New(), info: radio.Info{Driver: "spi"}}
	env = newRadioEnv(t, silent, false, spiCfg)
	code, res = probe(t, env, map[string]any{"driver": "spi", "device": "lora-waveshare"})
	if code != 200 || res["ok"] != false || !strings.Contains(res["error"].(string), "lora-waveshare") {
		t.Fatalf("probe a silent board: %d %v", code, res)
	}
}

// serveFakeBoard serves a fake Meshtastic node on 127.0.0.1 and returns its address.
func serveFakeBoard(t *testing.T, node *mtclienttest.Node) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go node.Serve(ln)
	t.Cleanup(func() { ln.Close(); node.Drop() })
	return ln.Addr().String()
}

func TestProbeBoardOverTCP(t *testing.T) {
	env := newTestEnv(t, nil)
	addr := serveFakeBoard(t, mtclienttest.New(0x0b0a4d01))
	code, res := probe(t, env, map[string]any{"driver": nodes.BoardDriver, "device": addr})
	if code != 200 || res["ok"] != true || res["node_id"] != "!0b0a4d01" || res["region"] != "EU_868" || res["firmware"] != "Meshtastic 2.8.0.fake" {
		t.Fatalf("probe a board: %d %v", code, res)
	}
	// A serial board that isn't there is reported.
	code, res = probe(t, env, map[string]any{"driver": nodes.BoardDriver, "device": "/dev/ttyACM-none"})
	if code != 200 || res["ok"] != false || res["error"] == "" {
		t.Fatalf("probe a missing serial board: %d %v", code, res)
	}
	if code, _ := probe(t, env, map[string]any{"driver": nodes.BoardDriver, "device": ""}); code != http.StatusBadRequest {
		t.Fatalf("probe a board without a device: %d", code)
	}
	if code, _ := probe(t, env, map[string]any{"driver": nodes.BoardDriver, "device": "[::1"}); code != http.StatusBadRequest {
		t.Fatalf("probe a bad address: %d", code)
	}
}

func TestDetectFindsABoard(t *testing.T) {
	env := newTestEnv(t, nil)
	// Nothing answers on a missing port, so detection reports both attempts.
	code, res := probe(t, env, map[string]any{"driver": "auto", "device": "/dev/ttyACM-none"})
	if code != 200 || res["ok"] != false || !strings.Contains(jsonOf(res["details"]), "Meshtastic: ") {
		t.Fatalf("detect: %d %v", code, res)
	}
}

func TestProbeRunningBoard(t *testing.T) {
	fake := mtclienttest.New(0x0b0a4d02)
	addr := serveFakeBoard(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	board, err := nodes.OpenBoard(ctx, addr, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { board.Close() })
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := board.Node().Client().WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	env := newRadioEnv(t, board, false, nil)
	code, res := probe(t, env, map[string]any{"driver": nodes.BoardDriver, "device": addr})
	if code != 200 || res["ok"] != true || res["node_id"] != "!0b0a4d02" {
		t.Fatalf("probe the running board: %d %v", code, res)
	}
	fake.Drop()
	cancel()
}

func TestFillBoardProbe(t *testing.T) {
	res := map[string]any{"ok": false}
	fillBoardProbe(res, mtclient.Snapshot{})
	if len(res) != 1 {
		t.Fatalf("an unknown board filled %v", res)
	}
	snap := mtclient.Snapshot{MyInfo: &pb.MyNodeInfo{MyNodeNum: 0x1234},
		Config: &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{}},
		Nodes:  map[uint32]*pb.NodeInfo{0x1234: {User: &pb.User{LongName: "Board", ShortName: "BRD"}}}}
	fillBoardProbe(res, snap)
	if res["ok"] != false || res["name"] != "Board" || res["region"] != "UNSET" || !strings.Contains(jsonOf(res["details"]), "region isn't set") {
		t.Fatalf("offline board = %v", res)
	}
}

func TestLANAddr(t *testing.T) {
	ctx := context.Background()
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "10.1.2.3:4403": true, "[fe80::1]:1": true, "8.8.8.8:4403": false,
		"no-port": false, "localhost:4403": true} {
		if got := lanAddr(ctx, addr); got != want {
			t.Errorf("lanAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestCheckBoardDeviceAfterSetup(t *testing.T) {
	env := newTestEnv(t, nil)
	if err := env.s.checkBoardDevice(context.Background(), "8.8.8.8"); err == nil {
		t.Fatal("a public address was allowed before setup")
	}
	env.signIn(t)
	if err := env.s.checkBoardDevice(context.Background(), "8.8.8.8"); err != nil {
		t.Fatalf("a public address after setup: %v", err)
	}
	if err := env.s.checkBoardDevice(context.Background(), "/dev/ttyACM0"); err != nil {
		t.Fatalf("a serial board: %v", err)
	}
	if err := env.s.checkBoardDevice(context.Background(), "/dev/sda"); err == nil {
		t.Fatal("a disk was allowed as a board")
	}
}

func TestSerialPortsAndDescriptions(t *testing.T) {
	env := newTestEnv(t, nil)
	code, _, list := call(t, env.srv, "GET", "/api/v1/serial-ports", "", nil)
	if code != 200 || list == nil {
		t.Fatalf("serial ports: %d %v", code, list)
	}
	for _, p := range list {
		m := p.(map[string]any)
		if m["path"] == "" || m["device"] == "" {
			t.Errorf("port = %v", m)
		}
	}
	for in, want := range map[string]string{
		"usb-Silicon_Labs_CP2102_USB_to_UART_0001-if00-port0": "Silicon Labs CP2102 USB to UART 0001",
		"ttyUSB0":          "ttyUSB0",
		"usb-RAK_Wireless": "RAK Wireless",
		"-if00":            "-if00",
	} {
		if got := describePort(in); got != want {
			t.Errorf("describePort(%q) = %q, want %q", in, got, want)
		}
	}
	discardLogf("ignored %d", 1)
}

func TestSetupChoices(t *testing.T) {
	primary := " Ops "
	next := config.Default()
	req := &setupRequest{Region: "us", Preset: "medium_fast", RelayRole: "mute", PrimaryChannel: &primary,
		Hosted: &config.Hosted{Meshtasticd: " /usr/bin/meshtasticd ", DockerImage: " meshtastic/meshtasticd:2.8 "}}
	if err := applySetupChoices(next, req); err != nil {
		t.Fatal(err)
	}
	if next.Mesh.Region != "US" || next.Mesh.Preset != "MEDIUM_FAST" || next.Relay.Role != "client_mute" || next.Mesh.PrimaryChannel != "Ops" ||
		next.Hosted.Meshtasticd != "/usr/bin/meshtasticd" || next.Hosted.DockerImage != "meshtastic/meshtasticd:2.8" {
		t.Fatalf("choices = %+v %+v %+v", next.Mesh, next.Relay, next.Hosted)
	}
	cases := []struct {
		driver, device string
		wantErr        bool
		wantDriver     string
		wantDevice     string
	}{
		{"", "/dev/ttyUSB3", false, "kiss", "/dev/ttyUSB3"},
		{"bogus", "", true, "", ""},
		{"spi", "", true, "", ""},
		{nodes.BoardDriver, "", true, "", ""},
		{"spi", "lora-x", false, "spi", "lora-x"},
		{"", "/dev/ttyUSB4", false, "spi", "lora-x"}, // an older wizard doesn't overwrite the board
	}
	c := config.Default()
	c.Radio.Driver = "kiss"
	for _, tc := range cases {
		err := applySetupRadio(c, tc.driver, tc.device)
		if (err != nil) != tc.wantErr {
			t.Errorf("applySetupRadio(%q, %q) = %v", tc.driver, tc.device, err)
			continue
		}
		if err == nil && (c.Radio.Driver != tc.wantDriver || c.Radio.Device != tc.wantDevice) {
			t.Errorf("applySetupRadio(%q, %q) left %s %s", tc.driver, tc.device, c.Radio.Driver, c.Radio.Device)
		}
	}
}

func TestPostSetupRefusals(t *testing.T) {
	env := newTestEnv(t, nil)
	for _, body := range []map[string]any{
		{"password": "short"},
		{"password": "correct horse", "region": "MARS"},
		{"password": "correct horse", "driver": "spi"},
	} {
		if code, obj, _ := call(t, env.srv, "POST", "/api/v1/setup", "", body); code != http.StatusBadRequest {
			t.Errorf("setup %v: %d %v", body, code, obj)
		}
	}
	if code, _ := env.do(t, "POST", "/api/v1/setup", "", "application/json", "nope"); code != http.StatusBadRequest {
		t.Errorf("bad body: %d", code)
	}
	if !env.s.auth.SetupNeeded() {
		t.Fatal("a refused setup set a password")
	}
	env.signIn(t)
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/setup", "", map[string]any{"password": "another password"}); code != http.StatusConflict {
		t.Fatalf("setup again: %d", code)
	}
}

func TestCheckMeshtasticdWithAProgram(t *testing.T) {
	env := newTestEnv(t, nil)
	bin := fakeMeshtasticd(t, "2.8.1")
	code, obj, _ := call(t, env.srv, "POST", "/api/v1/setup/meshtasticd", "", map[string]any{"meshtasticd": bin})
	if code != 200 || obj["ok"] != true || obj["version"] != "2.8.1" || obj["error"] != "" {
		t.Fatalf("check: %d %v", code, obj)
	}
	if code, _ := env.do(t, "POST", "/api/v1/setup/meshtasticd", "", "application/json", "nope"); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
}
