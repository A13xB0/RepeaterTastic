package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSingleMQTTBlockLoadsAsList(t *testing.T) {
	c, err := load(t, "links:\n  mqtt:\n    enabled: true\n    address: mqtt.meshtastic.org:1883\n    root: msh/EU_868/Scotland\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Links.MQTT) != 1 || c.Links.MQTT[0].Name != "mqtt" || c.Links.MQTT[0].Root != "msh/EU_868/Scotland" ||
		c.Links.MQTT[0].ModeOrDefault() != MQTTGateway || c.Links.MQTT[0].SelectionOrDefault() != ChannelsIdentity {
		t.Fatalf("mqtt = %+v", c.Links.MQTT)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.Path())
	var back struct {
		Links struct {
			MQTT []map[string]any `yaml:"mqtt"`
		} `yaml:"links"`
	}
	if err := yaml.Unmarshal(b, &back); err != nil || len(back.Links.MQTT) != 1 {
		t.Fatalf("saved mqtt is not a list: %v\n%s", err, b)
	}
}

func TestMQTTListNamesAndFlags(t *testing.T) {
	c, err := load(t, `links:
  mqtt:
    - {enabled: true, address: a:1883, ok_to_mqtt: true}
    - {name: logger, enabled: true, address: b:1883, mode: monitor, uplink_channels: [LongFast]}
    - {enabled: false, address: c:1883, relay_mqtt: true}
`)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Links.MQTT
	if m[0].Name != "mqtt" || m[1].Name != "logger" || m[2].Name != "mqtt-2" {
		t.Fatalf("names = %s, %s, %s", m[0].Name, m[1].Name, m[2].Name)
	}
	if m[1].FormatOrDefault() != "json" || m[1].SelectionOrDefault() != ChannelsOverride {
		t.Fatalf("monitor format=%s selection=%s", m[1].FormatOrDefault(), m[1].SelectionOrDefault())
	}
	mc := c.MeshConfig()
	if !mc.OKToMQTT || !mc.IgnoreMQTT {
		t.Fatalf("ok_to_mqtt=%v ignore_mqtt=%v: a disabled connection must not enable relaying", mc.OKToMQTT, mc.IgnoreMQTT)
	}
}

func TestMQTTValidation(t *testing.T) {
	for want, yml := range map[string]string{
		"bridge_acknowledged":             "links: {mqtt: [{enabled: true, address: a:1, mode: bridge}]}",
		"named":                           "links: {mqtt: [{name: x, address: a:1}, {name: x, address: b:1}]}",
		"only allowed on a bridge; other": "links: {mqtt: [{enabled: true, address: a:1, relay_mqtt: true, relay_hops: 2}]}",
		"ignore_consent":                  "links: {mqtt: [{enabled: true, address: a:1, ignore_consent: true}]}",
		"mode must be":                    "links: {mqtt: [{mode: firehose}]}",
		"gateway must be":                 "links: {mqtt: [{gateway: bob}]}",
		"address is required":             "links: {mqtt: [{enabled: true}]}",
	} {
		if _, err := load(t, yml); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", yml, err)
		}
	}
	if _, err := load(t, "links: {mqtt: [{enabled: true, address: a:1, mode: bridge, bridge_acknowledged: true, relay_hops: 2, gateway: '!be77562b'}]}"); err != nil {
		t.Fatalf("acknowledged bridge rejected: %v", err)
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("REPEATERTASTIC_STATE_DIR", "/data")
	t.Setenv("REPEATERTASTIC_RADIO_DEVICE", "/dev/ttyACM0")
	t.Setenv("REPEATERTASTIC_WEB_PORT", "9090")
	c := Default()
	c.ApplyEnv()
	if c.StateDir != "/data" || c.Radio.Device != "/dev/ttyACM0" || c.Web.Port != 9090 {
		t.Fatalf("env not applied: %s %s %d", c.StateDir, c.Radio.Device, c.Web.Port)
	}
}
