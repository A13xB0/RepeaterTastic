package web

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func TestRegionsListAllowedPresets(t *testing.T) {
	env := newTestEnv(t, nil)
	// Before setup, the wizard reads them without a token.
	code, _, list := call(t, env.srv, "GET", "/api/v1/regions", "", nil)
	if code != 200 || len(list) != len(phy.Regions) {
		t.Fatalf("regions: %d, %d regions", code, len(list))
	}
	byName := map[string][]any{}
	for _, r := range list {
		m := r.(map[string]any)
		byName[m["name"].(string)], _ = m["presets"].([]any)
	}
	eu := jsonOf(byName["EU_868"])
	if !strings.Contains(eu, "LONG_FAST") || strings.Contains(eu, "TURBO") || strings.Contains(eu, "LITE_") {
		t.Fatalf("EU_868 presets = %s", eu)
	}
	if first := byName["EU_868"][0]; first != "LONG_FAST" {
		t.Fatalf("presets not in enum order: first is %v", first)
	}
	if env.signIn(t) == "" {
		t.Fatal("no token")
	}
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/regions", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("regions without a token after setup: %d", code)
	}
}

func TestPresetAllowed(t *testing.T) {
	preset := func(name string) phy.Preset {
		return phy.Preset(pb.Config_LoRaConfig_ModemPreset_value[name])
	}
	cases := []struct {
		region, preset string
		want           bool
	}{
		{"EU_866", "LONG_FAST", false},
		{"EU_N_868", "LONG_FAST", false},
		{"EU_868", "LONG_FAST", true},
		{"EU_868", "SHORT_TURBO", false},
		{"US", "SHORT_TURBO", true},
		{"US", "LONG_FAST", true},
	}
	for _, c := range cases {
		if got := presetAllowed(c.region, preset(c.preset)); got != c.want {
			t.Errorf("presetAllowed(%s, %s) = %v", c.region, c.preset, got)
		}
	}
	for name, v := range pb.Config_LoRaConfig_ModemPreset_value {
		if strings.HasPrefix(name, "LITE_") && presetAllowed("EU_866", phy.Preset(v)) != true {
			t.Errorf("EU_866 refuses %s", name)
		}
		if strings.HasPrefix(name, "NARROW_") && presetAllowed("EU_N_868", phy.Preset(v)) != true {
			t.Errorf("EU_N_868 refuses %s", name)
		}
	}
}

func TestPhyPreview(t *testing.T) {
	env := newTestEnv(t, nil)
	code, obj, _ := call(t, env.srv, "POST", "/api/v1/phy/preview", "", map[string]any{"region": "eu_868", "preset": "long_fast"})
	if code != 200 || obj["frequency_mhz"] != 869.525 || obj["primary_channel"] != "LongFast" {
		t.Fatalf("preview: %d %v", code, obj)
	}
	code, obj, _ = call(t, env.srv, "POST", "/api/v1/phy/preview", "", map[string]any{"region": "EU_868", "preset": "LONG_FAST", "primary_channel": "Ops"})
	if code != 200 || obj["primary_channel"] != "Ops" {
		t.Fatalf("named preview: %d %v", code, obj)
	}
	for _, body := range []any{map[string]any{"region": "EU_868", "preset": "WARP"}, map[string]any{"region": "MARS", "preset": "LONG_FAST"}, "x"} {
		if code, _, _ := call(t, env.srv, "POST", "/api/v1/phy/preview", "", body); code != 400 {
			t.Errorf("preview %v: %d", body, code)
		}
	}
}

func TestMapTileURLAndKeySource(t *testing.T) {
	for _, u := range append([]string{""}, config.LegacyMapTileURLs...) {
		if got := mapTileURL(u); got != config.DefaultMapTileURL {
			t.Errorf("mapTileURL(%q) = %q", u, got)
		}
	}
	if got := mapTileURL("https://x/{z}/{x}/{y}"); got != "https://x/{z}/{x}/{y}" {
		t.Errorf("custom URL = %q", got)
	}
	env := newTestEnv(t, func(o *Options) { o.MapKeySource, o.MapAPIKey = "environment", "k1" })
	tok := env.signIn(t)
	_, cfg, _ := call(t, env.srv, "GET", "/api/v1/config", tok, nil)
	if objectAt(cfg, "web")["map_key_source"] != "environment" {
		t.Fatalf("map key source = %v", cfg["web"])
	}
	_, st, _ := call(t, env.srv, "GET", "/api/v1/status", tok, nil)
	if u := objectAt(st, "map")["tile_url"].(string); !strings.Contains(u, "key=k1") {
		t.Fatalf("tile url = %s", u)
	}
	plain := newTestEnv(t, nil)
	if plain.s.mapKeySource() != "none" {
		t.Fatalf("default key source = %q", plain.s.mapKeySource())
	}
}

func TestDropMQTTDefaults(t *testing.T) {
	m := config.MQTT{Gateway: "relay", Mode: config.MQTTGateway, Format: "encrypted", BridgeAcknowledged: true,
		UplinkChannels: []string{}, DownlinkChannels: []string{}}
	dropMQTTDefaults(&m)
	if m.Gateway != "" || m.Mode != "" || m.Format != "" || m.BridgeAcknowledged || m.UplinkChannels != nil || m.DownlinkChannels != nil {
		t.Fatalf("gateway defaults kept: %+v", m)
	}
	m = config.MQTT{Mode: config.MQTTMonitor, Format: "json", UplinkChannels: []string{"a"}}
	dropMQTTDefaults(&m)
	if m.Mode != config.MQTTMonitor || m.Format != "" || len(m.UplinkChannels) != 1 {
		t.Fatalf("monitor defaults: %+v", m)
	}
	m = config.MQTT{Mode: config.MQTTBridge, Format: "json", BridgeAcknowledged: true}
	dropMQTTDefaults(&m)
	if m.Format != "json" || !m.BridgeAcknowledged {
		t.Fatalf("bridge settings dropped: %+v", m)
	}
}

func TestApplyRelayDTOFavorites(t *testing.T) {
	var d configDTO
	d.Relay.Rebroadcast = "ALL"
	d.Relay.Favorites = []string{"!0000ABCD", "not-a-node"}
	d.Relay.LocalDM = "also_rf"
	d.Relay.Role = "mute"
	next := config.Default()
	applyRelayDTO(next, d)
	if next.Relay.Rebroadcast != "" || next.Relay.Role != "client_mute" || !next.Links.LocalDMOverRF {
		t.Fatalf("relay = %+v", next.Relay)
	}
	if len(next.Relay.Favorites) != 2 || next.Relay.Favorites[0] != "!0000abcd" || next.Relay.Favorites[1] != "not-a-node" {
		t.Fatalf("favorites = %v", next.Relay.Favorites)
	}
}

func TestPutConfigRefusals(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"unknown section", map[string]any{"colour": map[string]any{}}},
		{"bad section", map[string]any{"radio": "fast"}},
		{"hop limit", map[string]any{"radio": map[string]any{"hop_limit": 0}}},
		{"nodeinfo", map[string]any{"airtime": map[string]any{"nodeinfo_interval": "1m"}}},
		{"position", map[string]any{"position": map[string]any{"interval": "5m"}}},
		{"mqtt name", map[string]any{"mqtt": []any{map[string]any{"name": " ", "map_report": map[string]any{"interval": "1h"}}}}},
		{"map report", map[string]any{"mqtt": map[string]any{"name": "one", "map_report": map[string]any{"interval": "1m"}}}},
		{"invalid", map[string]any{"radio": map[string]any{"hop_limit": 3, "region": "MARS", "preset": "LONG_FAST"}}},
	}
	for _, c := range cases {
		if code, obj, _ := call(t, env.srv, "PUT", "/api/v1/config", tok, c.body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", c.name, code, obj)
		}
	}
	if code, _ := env.do(t, "PUT", "/api/v1/config", tok, "application/json", "[]"); code != http.StatusBadRequest {
		t.Errorf("not an object: %d", code)
	}
}

func TestPutConfigSavesAndAppliesLive(t *testing.T) {
	level := new(slog.LevelVar)
	env := newTestEnv(t, func(o *Options) { o.LogLevel = level })
	tok := env.signIn(t)
	code, res, _ := call(t, env.srv, "PUT", "/api/v1/config", tok, map[string]any{
		"relay": map[string]any{"role": "client", "long_name": "Mast Top", "short_name": "TOP", "favorites": []string{"!0000ABCD"}},
		"web":   map[string]any{"bind": "0.0.0.0", "port": 8080, "session_ttl": "1h", "log_level": "warn"},
		"mqtt":  map[string]any{"name": "home", "address": "mqtt.example:1883", "password": "pw", "map_report": map[string]any{"interval": "1h"}},
	})
	if code != 200 {
		t.Fatalf("put: %d %v", code, res)
	}
	if level.Level() != slog.LevelWarn {
		t.Fatalf("log level = %v", level.Level())
	}
	if u := env.host.Relay().UserCopy(); u.LongName != "Mast Top" || u.ShortName != "TOP" {
		t.Fatalf("relay names = %v", u)
	}
	saved, err := os.ReadFile(env.cfg.Path())
	if err != nil || !strings.Contains(string(saved), "mqtt.example:1883") || !strings.Contains(string(saved), "'!0000abcd'") {
		t.Fatalf("saved config (%v):\n%s", err, saved)
	}
	mq := res["config"].(map[string]any)["mqtt"].([]any)[0].(map[string]any)
	if mq["password_set"] != true || mq["password"] != "" {
		t.Fatalf("mqtt = %v", mq)
	}
	// Back to info: the default, kept out of the file.
	call(t, env.srv, "PUT", "/api/v1/config", tok, map[string]any{"web": map[string]any{"bind": "0.0.0.0", "port": 8080, "session_ttl": "1h", "log_level": "info"}})
	if level.Level() != slog.LevelInfo {
		t.Fatalf("log level back = %v", level.Level())
	}
}

func TestConfigSaveFailureIsOnlyLogged(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	// A folder where the config file should be: it can't be replaced, but the change still applies.
	blockConfigFile(t, env.cfg.Path())
	code, _, _ := call(t, env.srv, "PUT", "/api/v1/config", tok, map[string]any{"relay": map[string]any{"role": "client", "long_name": "Relay", "short_name": "RLY"}})
	if code != 200 || env.cfg.Relay.Role != "client" {
		t.Fatalf("put with an unwritable config: %d %q", code, env.cfg.Relay.Role)
	}
	if err := env.s.saveConfigFile(); err == nil {
		t.Fatal("saving over a folder succeeded")
	}
}

// blockConfigFile puts a non-empty folder where the config file is, so saving it fails.
func blockConfigFile(t *testing.T, path string) {
	t.Helper()
	_ = os.Remove(path)
	if err := os.MkdirAll(path+"/keep", 0o700); err != nil {
		t.Fatal(err)
	}
}
