package web

import (
	"net/http"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/links/mqtt"
	"github.com/ScotMesh/RepeaterTastic/internal/links/udp"
)

func TestLinksShowRunningConnections(t *testing.T) {
	env := newTestEnv(t, func(o *Options) {
		o.Config.Links.MQTT = config.MQTTLinks{
			{Name: "home", Enabled: true, Address: "broker.example:1883", MapReport: config.MapReport{Interval: time.Hour}},
			{Name: "spare", Address: "spare.example:1883", Gateway: "!0000abcd", MapReport: config.MapReport{Interval: time.Hour}},
		}
		o.MQTT = []*mqtt.Link{mqtt.New(o.Host, mqtt.Options{Name: "home", Address: "broker.example:1883", Root: "msh/test/"}, o.Log)}
		l, err := udp.New(o.Host, nil, 0, "", o.Log)
		if err != nil {
			t.Fatal(err)
		}
		o.UDP = l
	})
	tok := env.signIn(t)
	code, _, list := call(t, env.srv, "GET", "/api/v1/links", tok, nil)
	if code != 200 || len(list) != 3 {
		t.Fatalf("links: %d %v", code, list)
	}
	udpLink, home, spare := list[0].(map[string]any), list[1].(map[string]any), list[2].(map[string]any)
	if udpLink["name"] != "udp" || udpLink["connected"] != false || udpLink["detail"] != "239.0.0.69:4403 + 224.0.0.69:4403" {
		t.Errorf("udp = %v", udpLink)
	}
	if home["detail"] != "broker.example:1883 · msh/test" || home["root"] != "msh/test" || home["gateway_id"] != env.host.Relay().NodeID() ||
		home["gateway"] != "relay" || home["radio_id"] != "main" {
		t.Errorf("home = %v", home)
	}
	// A connection that isn't running shows its settings only.
	if spare["detail"] != "spare.example:1883" || spare["gateway"] != "!0000abcd" || spare["connected"] != false || spare["gateway_id"] != nil {
		t.Errorf("spare = %v", spare)
	}
}

func TestPatchLinkChecks(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	if code, _, _ := call(t, env.srv, "PATCH", "/api/v1/links/mqtt:home", tok, map[string]any{"enabled": true}); code != http.StatusNotFound {
		t.Fatalf("patch mqtt: %d", code)
	}
	for _, g := range []string{"10.0.0.1:4403", "239.0.0.1", "nonsense"} {
		if code, _, _ := call(t, env.srv, "PATCH", "/api/v1/links/udp", tok, map[string]any{"group": g}); code != http.StatusBadRequest {
			t.Errorf("group %q: %d", g, code)
		}
	}
	if code, _ := env.do(t, "PATCH", "/api/v1/links/udp", tok, "application/json", "nope"); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
	code, obj, _ := call(t, env.srv, "PATCH", "/api/v1/links/udp", tok, map[string]any{"enabled": true, "group": " 239.1.2.3:5000 "})
	if code != 200 || obj["group"] != "239.1.2.3:5000" || obj["enabled"] != true || obj["restart_required"] != true {
		t.Fatalf("patch udp: %d %v", code, obj)
	}
	// Saving can fail; the change still applies.
	blockConfigFile(t, env.cfg.Path())
	if code, obj, _ := call(t, env.srv, "PATCH", "/api/v1/links/udp", tok, map[string]any{"group": ""}); code != 200 || obj["group"] != "" {
		t.Fatalf("patch without saving: %d %v", code, obj)
	}
}
