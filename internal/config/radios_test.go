package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, yml string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestSingleRadioConfigIsMain(t *testing.T) {
	c, err := load(t, "radio:\n  device: /dev/ttyUSB0\nstate_dir: /var/lib/rt\n")
	if err != nil {
		t.Fatal(err)
	}
	rcs := c.RadioConfigs()
	if len(rcs) != 1 || rcs[0].ID != MainRadioID || rcs[0].StateDir != "/var/lib/rt" {
		t.Fatalf("radio configs = %+v", rcs)
	}
}

func TestExtraRadioInheritsAndIsolates(t *testing.T) {
	c, err := load(t, `radio: {device: /dev/rt-lf}
state_dir: /var/lib/rt
radios:
  - id: mf
    name: MediumFast
    radio: {device: /dev/rt-mf}
    mesh: {preset: MEDIUM_FAST}
    identities: [{long_name: MF Desk, short_name: MFD, api_port: 4410}]
`)
	if err != nil {
		t.Fatal(err)
	}
	rcs := c.RadioConfigs()
	if len(rcs) != 2 {
		t.Fatalf("want 2 radios, got %d", len(rcs))
	}
	mf := rcs[1]
	if mf.StateDir != filepath.Join("/var/lib/rt", "radios", "mf") || mf.Mesh.Region != "EU_868" ||
		mf.Relay.Role != "mute" || mf.Radio.Driver != "kiss" || mf.Radio.Baud != 115200 || mf.MeshConfig().RadioID != "mf" {
		t.Fatalf("mf radio = %+v", mf)
	}
	if rcs[0].Mesh.Preset != "LONG_FAST" || len(rcs[0].Identities) != 0 {
		t.Fatalf("main radio changed: %+v", rcs[0].Mesh)
	}
}

func TestRadioValidation(t *testing.T) {
	for name, yml := range map[string]string{
		"duplicate device": "radio: {device: /dev/a}\nradios: [{id: b, radio: {device: /dev/a}}]\n",
		"bad id":           "radios: [{id: Main Radio}]\n",
		"reserved id":      "radios: [{id: main}]\n",
		"port clash":       "identities: [{long_name: A, api_port: 4404}]\nradios: [{id: b, radio: {device: /dev/b}, identities: [{long_name: B, api_port: 4404}]}]\n",
		"bad preset":       "radios: [{id: b, radio: {device: /dev/b}, mesh: {preset: NOPE}}]\n",
	} {
		if _, err := load(t, yml); err == nil || !strings.Contains(err.Error(), "") {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMeshtasticRadio(t *testing.T) {
	c, err := load(t, "radio: {driver: meshtastic, device: /dev/ttyACM0}\n"+
		"radios: [{id: b, radio: {driver: meshtastic, device: pi.local}, mesh: {preset: MEDIUM_FAST}}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if rcs := c.RadioConfigs(); rcs[1].Radio.Driver != "meshtastic" || rcs[1].Radio.Device != "pi.local" {
		t.Fatalf("radio configs = %+v", rcs)
	}
	for yml, want := range map[string]string{
		"radio: {driver: meshtastic, device: ''}\n":        "needs radio.device",
		"radio: {driver: meshtastic, device: 'a:b:c:d'}\n": "bad address",
		"radio: {driver: meshtastic, device: Pi.local}\n" +
			"radios: [{id: b, radio: {driver: meshtastic, device: 'pi.local:4403'}, mesh: {preset: MEDIUM_FAST}}]\n": "both use",
		"radio: {driver: lora}\n": "kiss, spi, meshtastic or none",
	} {
		if _, err := load(t, yml); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", yml, err, want)
		}
	}
}
