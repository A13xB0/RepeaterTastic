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

func TestHostedConfig(t *testing.T) {
	c, err := load(t, "hosted: {persona: true, meshtasticd: /usr/local/bin/meshtasticd}\n")
	if err != nil || !c.Hosted.Persona || c.Hosted.HostedPortBase() != 4500 {
		t.Fatalf("hosted %+v %v", c.Hosted, err)
	}
	for yml, want := range map[string]string{
		"hosted: {meshtasticd: /bin/sh}\n":         "meshtasticd program",
		"hosted: {port_base: 80}\n":                "port_base",
		"hosted: {docker_image: '--privileged'}\n": "docker_image",
		"hosted: {docker_image: 'a b'}\n":          "docker_image",
	} {
		if _, err := load(t, yml); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", yml, err, want)
		}
	}
}
