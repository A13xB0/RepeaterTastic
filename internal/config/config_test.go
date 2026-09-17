package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

// expectLoadErrors loads each YAML document and checks the error mentions the wanted text.
func expectLoadErrors(t *testing.T, cases map[string]string) {
	t.Helper()
	for yml, want := range cases {
		if _, err := load(t, yml); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to mention %q", yml, err, want)
		}
	}
}

func expectLoadOK(t *testing.T, docs ...string) {
	t.Helper()
	for _, yml := range docs {
		if _, err := load(t, yml); err != nil {
			t.Errorf("%q: %v", yml, err)
		}
	}
}

func TestPluginDir(t *testing.T) {
	c := Default()
	c.StateDir = "/srv/rt"
	if got := c.PluginDir(); got != filepath.Join("/srv/rt", "plugins") {
		t.Fatalf("default plugin dir %q", got)
	}
	c.Plugins.Dir = "/opt/plugins"
	if got := c.PluginDir(); got != "/opt/plugins" {
		t.Fatalf("explicit plugin dir %q", got)
	}
}

func TestHostedPortBase(t *testing.T) {
	h := Hosted{PortBase: 5000}
	if h.HostedPortBase() != 5000 || h.RadioPortBase(2) != 5200 {
		t.Fatalf("port base %d radio 2 %d", h.HostedPortBase(), h.RadioPortBase(2))
	}
	if (Hosted{}).RadioPortBase(1) != 4600 {
		t.Fatal("default radio port base")
	}
	// Each radio takes a block of 100 ports; 15 extra radios from 64000 run past 65535.
	c := Default()
	c.Hosted.PortBase = 64000
	c.Radios = make([]RadioInstance, 15)
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "too high") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadFileErrors(t *testing.T) {
	if _, err := load(t, "radio: [unclosed"); err == nil || !strings.Contains(err.Error(), "c.yaml") {
		t.Fatalf("bad yaml: %v", err)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("loading a directory succeeded")
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "absent.yaml")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path() != p || c.Radio.Driver != "kiss" || c.Mesh.Region != "EU_868" {
		t.Fatalf("defaults %+v", c)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Load(p)
	if err != nil || back.Mesh.Preset != "LONG_FAST" || back.Web.Port != 8080 {
		t.Fatalf("reloaded %+v, %v", back, err)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestSaveErrors(t *testing.T) {
	if err := Default().Save(); err == nil || !strings.Contains(err.Error(), "no file path") {
		t.Fatalf("pathless save: %v", err)
	}
	c, err := Load(filepath.Join(t.TempDir(), "missing-dir", "c.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err == nil {
		t.Fatal("save into a missing directory succeeded")
	}
}

func TestMQTTLinksYAMLShapes(t *testing.T) {
	expectLoadErrors(t, map[string]string{
		"links: {mqtt: 5}\n":                          "links.mqtt must be",
		"links: {mqtt: {enabled: [1]}}\n":             "cannot unmarshal",
		"links: {mqtt: [{enabled: [1]}]}\n":           "cannot unmarshal",
		"links: {mqtt: [{format: xml}]}\n":            "format must be",
		"links: {mqtt: [{channel_selection: all}]}\n": "channel_selection must be",
	})
}

func TestMQTTValidationRanges(t *testing.T) {
	expectLoadErrors(t, map[string]string{
		"links: {mqtt: [{enabled: true, address: a:1, map_report: {position_precision: 40}}]}\n": "position_precision",
		"links: {mqtt: [{mode: bridge, bridge_acknowledged: true, relay_hops: 9}]}\n":            "relay_hops must be 0-7",
		"links: {mqtt: [{name: m, enabled: true, address: a:1, map_report: {enabled: true}}]}\n": "links.mqtt m: map_report needs a position",
	})
	expectLoadOK(t,
		"position: {latitude: 57.1, longitude: -2.1}\nlinks: {mqtt: [{enabled: true, address: a:1, map_report: {enabled: true}}]}\n",
		"links: {mqtt: [{enabled: true, address: a:1, map_report: {enabled: true, latitude: 1}}]}\n",
		"links: {mqtt: [{format: both, channel_selection: combine, gateway: relay}]}\n",
	)
}

func TestUnnamedMQTTLinkLabel(t *testing.T) {
	c := Default()
	c.Links.MQTT = MQTTLinks{{Enabled: true}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "links.mqtt #1: address is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestMQTTLinkFlags(t *testing.T) {
	cases := []struct {
		links        MQTTLinks
		ok, relayMQT bool
	}{
		{nil, false, false},
		{MQTTLinks{{OKToMQTT: true}}, true, false},
		{MQTTLinks{{RelayMQTT: true}}, false, false},
		{MQTTLinks{{Enabled: true}, {Enabled: true, RelayMQTT: true}}, false, true},
	}
	for i, tc := range cases {
		if tc.links.OKToMQTT() != tc.ok || tc.links.RelayMQTT() != tc.relayMQT {
			t.Errorf("case %d: ok=%v relay=%v", i, tc.links.OKToMQTT(), tc.links.RelayMQTT())
		}
	}
	if (MQTT{Format: "both", Mode: MQTTMonitor}).FormatOrDefault() != "both" {
		t.Error("explicit format ignored")
	}
	if (MQTT{ChannelSelection: ChannelsCombine, UplinkChannels: []string{"x"}}).SelectionOrDefault() != ChannelsCombine {
		t.Error("explicit selection ignored")
	}
}

func TestTopLevelValidation(t *testing.T) {
	expectLoadErrors(t, map[string]string{
		"log_level: loud\n":                                  "log_level",
		"web: {map_tile_url: 'ftp://x/{z}/{x}/{y}'}\n":       "map_tile_url",
		"web: {map_tile_url: 'https://tiles/{z}/{x}.png'}\n": "map_tile_url",
		"site: {duty_cycle_percent: 101}\n":                  "site.duty_cycle_percent",
		"site: {duty_cycle_percent: -1}\n":                   "site.duty_cycle_percent",
		"plugins: {messages_per_hour: 601}\n":                "plugins.messages_per_hour",
		"plugins: {traceroutes_per_hour: -1}\n":              "plugins.messages_per_hour",
		"plugins: {entries: [{id: a}, {id: a}]}\n":           "unique id",
		"plugins: {entries: [{enabled: true}]}\n":            "unique id",
		"mesh: {region: MARS}\n":                             "unknown region",
		"airtime: {telemetry_interval: 5m}\n":                "telemetry_interval",
	})
	expectLoadOK(t,
		"log_level: DEBUG\nweb: {map_tile_url: ''}\nplugins: {entries: [{id: a}, {id: b}]}\n",
		"web: {map_tile_url: 'http://tiles.lan/{z}/{x}/{y}.png'}\nairtime: {telemetry_interval: 1h}\n",
	)
}

func TestRadioDriverValidation(t *testing.T) {
	expectLoadErrors(t, map[string]string{
		"radio: {driver: usb}\n":                           "radio.driver must be",
		"radio: {driver: spi, device: '  '}\n":             "driver spi needs radio.device",
		"radio: {driver: meshtastic, device: ''}\n":        "driver meshtastic needs radio.device",
		"radio: {driver: meshtastic, device: 'host:0'}\n":  "radio.device:",
		"radio: {driver: meshtastic, device: 'a:b:c:z'}\n": "radio.device:",
	})
	expectLoadOK(t,
		"radio: {driver: sim}\n",
		"radio: {driver: none}\n",
		"radio: {driver: spi, device: auto}\n",
		"radio: {driver: meshtastic, device: /dev/ttyACM0}\n",
		"radio: {driver: meshtastic, device: COM3}\n",
		"radio: {driver: meshtastic, device: board.lan}\n",
	)
}

func TestHwModel(t *testing.T) {
	cases := map[string]pb.HardwareModel{
		"":            pb.HardwareModel_UNSET,
		" auto ":      pb.HardwareModel_UNSET,
		"rak4631":     pb.HardwareModel_RAK4631,
		" PORTDUINO ": pb.HardwareModel_PORTDUINO,
	}
	for name, want := range cases {
		c := Default()
		c.Mesh.HwModel = name
		if err := c.Validate(); err != nil {
			t.Errorf("%q: %v", name, err)
		}
		if got := c.MeshConfig().HwModel; got != want {
			t.Errorf("%q: hw model %v, want %v", name, got, want)
		}
	}
	expectLoadErrors(t, map[string]string{"mesh: {hw_model: TOASTER}\n": "mesh.hw_model"})
}

func TestPositionValidation(t *testing.T) {
	expectLoadErrors(t, map[string]string{
		"position: {latitude: 91}\n":       "out of range",
		"position: {longitude: -181}\n":    "out of range",
		"position: {precision_bits: 33}\n": "precision_bits",
		"position: {identities: some}\n":   "position.identities",
	})
	c, err := load(t, "position: {latitude: 57.5, longitude: -4.2, altitude: 120, precision_bits: 16, interval: 1h, identities: ALL}\n")
	if err != nil {
		t.Fatal(err)
	}
	p := c.MeshConfig().Position
	if p.Latitude != 57.5 || p.Longitude != -4.2 || p.Altitude != 120 || p.PrecisionBits != 16 ||
		p.Interval != time.Hour || !p.AllIdentities {
		t.Fatalf("position %+v", p)
	}
}

func TestMeshtasticBoardsShareAddress(t *testing.T) {
	expectLoadErrors(t, map[string]string{
		"radio: {driver: meshtastic, device: Board.lan}\nradios: [{id: b, radio: {driver: meshtastic, device: 'board.LAN:4403'}, mesh: {preset: MEDIUM_FAST}}]\n": "both use",
	})
	expectLoadOK(t,
		"radio: {driver: meshtastic, device: board-a.lan}\nradios: [{id: b, radio: {driver: meshtastic, device: board-b.lan}, mesh: {preset: MEDIUM_FAST}}]\n",
		// sim and none radios have no device to claim.
		"radio: {driver: sim, device: x}\nradios: [{id: b, radio: {driver: sim, device: x}, mesh: {preset: MEDIUM_FAST}}]\n",
	)
}

func TestSymlinkedSerialDevicesClash(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "ttyUSB0")
	link := filepath.Join(dir, "by-id-modem")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	yml := "radio: {device: " + real + "}\nradios: [{id: b, radio: {device: " + link + "}, mesh: {preset: MEDIUM_FAST}}]\n"
	expectLoadErrors(t, map[string]string{yml: "both use"})
}

func TestExtraRadioErrors(t *testing.T) {
	expectLoadErrors(t, map[string]string{
		"radios: [{id: b, radio: {device: /dev/b}, mesh: {preset: MEDIUM_FAST}}, {id: b, radio: {device: /dev/c}, mesh: {preset: MEDIUM_FAST}}]\n": "used twice",
		"radios: [{id: Main Radio}]\n":                                       "lowercase",
		"radios: [{id: b, radio: {device: /dev/b}, mesh: {preset: NOPE}}]\n": "radios[b]: unknown preset",
		"identities: [{long_name: A, api_port: 4404}]\nradios: [{id: b, radio: {device: /dev/b}, mesh: {preset: MEDIUM_FAST}, identities: [{long_name: B, api_port: 4404}]}]\n": "api_port 4404",
	})
}

func TestFillRadioDefaults(t *testing.T) {
	c := Default()
	c.Mesh.Region = "US"
	c.Radios = []RadioInstance{
		{ID: "longname", Links: Links{MQTT: MQTTLinks{{}, {Name: "mqtt"}}}},
		{ID: "x", Relay: Relay{Role: "router", LongName: "Keep", ShortName: "K"}, Mesh: Mesh{Region: "EU_433", HopLimit: 5}},
	}
	c.FillRadioDefaults()
	a, b := c.Radios[0], c.Radios[1]
	if a.Radio.Driver != "kiss" || a.Radio.Baud != 115200 || a.Mesh.Region != "US" || a.Mesh.HopLimit != 3 ||
		a.Relay.Role != "client_mute" || a.Relay.LongName != "RepeaterTastic longname Relay" || a.Relay.ShortName != "LONG" ||
		a.Airtime.NodeInfoInterval != 3*time.Hour {
		t.Fatalf("filled radio %+v", a)
	}
	if a.Links.MQTT[0].Name != "mqtt-2" || a.Links.MQTT[1].Name != "mqtt" {
		t.Fatalf("mqtt names %q %q", a.Links.MQTT[0].Name, a.Links.MQTT[1].Name)
	}
	if b.Relay.Role != "router" || b.Relay.LongName != "Keep" || b.Relay.ShortName != "K" || b.Mesh.Region != "EU_433" || b.Mesh.HopLimit != 5 {
		t.Fatalf("set fields overwritten: %+v", b)
	}
}

func TestRadioConfigNames(t *testing.T) {
	c := Default()
	c.Site.MainRadioName = "Longfast"
	c.Radios = []RadioInstance{{ID: "mf"}, {ID: "sf", Name: "Short"}}
	rcs := c.RadioConfigs()
	if rcs[0].Name != "Longfast" || rcs[1].Name != "mf" || rcs[2].Name != "Short" || rcs[1].Radios != nil {
		t.Fatalf("names %q %q %q", rcs[0].Name, rcs[1].Name, rcs[2].Name)
	}
}

func TestApplyEnvIgnoresBadPort(t *testing.T) {
	for _, v := range []string{"http", "0", "70000"} {
		t.Setenv("REPEATERTASTIC_WEB_PORT", v)
		t.Setenv("REPEATERTASTIC_STATE_DIR", "  ")
		c := Default()
		c.ApplyEnv()
		if c.Web.Port != 8080 || c.StateDir != "/var/lib/repeatertastic" {
			t.Errorf("port %q: web port %d state %q", v, c.Web.Port, c.StateDir)
		}
	}
}

func TestFavoriteNumsSkipsBadIDs(t *testing.T) {
	got := favoriteNums([]string{"!00000001", "bob", "0x10"})
	if len(got) != 2 || got[0] != 1 || got[1] != 16 {
		t.Fatalf("favorites %v", got)
	}
}
