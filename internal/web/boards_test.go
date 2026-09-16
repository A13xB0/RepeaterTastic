package web

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/radio/spi"
)

// useBoardDirs points the board file folders at dirs for one test.
func useBoardDirs(t *testing.T, dirs ...string) {
	t.Helper()
	saved := meshtasticdDirs
	meshtasticdDirs = dirs
	t.Cleanup(func() { meshtasticdDirs = saved })
}

// writeBoardDirs makes config.d with a good board file and available.d with a broken one and a
// file that isn't a board, and returns the two folders.
func writeBoardDirs(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	configD, availableD := filepath.Join(root, "config.d"), filepath.Join(root, "available.d")
	rfm, err := os.ReadFile("../radio/spi/boards/lora-Adafruit-RFM9x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(configD, "lora-Adafruit-RFM9x.yaml"): rfm,
		filepath.Join(availableD, "lora-broken.yaml"):      []byte("Lora: [nope"),
		filepath.Join(availableD, "not-a-board.txt"):       []byte("x"),
	}
	for p, b := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return configD, availableD
}

func TestBoardsListInstalledThenBuiltIn(t *testing.T) {
	configD, availableD := writeBoardDirs(t)
	useBoardDirs(t, configD, availableD)
	env := newTestEnv(t, nil)
	code, _, list := call(t, env.srv, "GET", "/api/v1/boards", "", nil)
	if code != 200 || len(list) < 4 {
		t.Fatalf("boards: %d %v", code, list)
	}
	auto, installed, broken := list[0].(map[string]any), list[1].(map[string]any), list[2].(map[string]any)
	if auto["id"] != "auto" || auto["supported"] != true {
		t.Errorf("first = %v", auto)
	}
	if installed["source"] != "config.d" || installed["name"] != "Adafruit RFM9x" || installed["host"] != "Raspberry Pi" ||
		installed["id"] != filepath.Join(configD, "lora-Adafruit-RFM9x.yaml") || installed["supported"] != true {
		t.Errorf("installed = %v", installed)
	}
	if broken["source"] != "available.d" || broken["name"] != "broken" || broken["supported"] != false || broken["error"] == "" {
		t.Errorf("broken = %v", broken)
	}
	for _, x := range list[3:] {
		if m := x.(map[string]any); m["source"] != "built-in" {
			t.Fatalf("after the installed boards: %v", m)
		}
	}
}

func TestBoardEntryFor(t *testing.T) {
	usb := boardEntryFor("f", "lora-usb-thing.yaml", "built-in", spi.Board{USB: &spi.USBID{VID: 1}, Hosts: []string{"usb"}}, nil)
	if usb.Bus != "usb" || usb.Name != "usb-thing" || usb.Host != "USB" || !usb.Supported {
		t.Errorf("usb = %+v", usb)
	}
	hat := boardEntryFor("f", "x.yaml", "built-in", spi.Board{Name: "HAT", SPIDev: "/dev/spidev0.1"}, errors.New("unsupported chip"))
	if hat.Bus != "spidev0.1" || hat.Name != "HAT" || hat.Supported || hat.Error != "unsupported chip" {
		t.Errorf("hat = %+v", hat)
	}
}

func TestBoardRef(t *testing.T) {
	useBoardDirs(t, "/etc/meshtasticd/config.d", "/etc/meshtasticd/available.d")
	for dev, want := range map[string]bool{
		"auto": true, "lora-waveshare-sxxx": true,
		"/etc/meshtasticd/config.d/lora-x.yaml":    true,
		"/etc/meshtasticd/available.d/lora-y.yaml": true,
		"/etc/passwd":                            false,
		"/etc/meshtasticd/config.d/../../passwd": false,
		"/etc/meshtasticd/config.dx/lora.yaml":   false,
	} {
		if got := boardRef(dev); got != want {
			t.Errorf("boardRef(%q) = %v", dev, got)
		}
	}
}

func TestHostLabel(t *testing.T) {
	cases := map[string][]string{
		"":                         nil,
		"Raspberry Pi":             {"raspberry-pi"},
		"Luckfox Lyra Zero W, USB": {"luckfox-lyra-zero-w", "usb"},
		"Ecb41 PGE":                {"ecb41-pge"},
		"Double  Dash":             {"double--dash"},
	}
	for want, hosts := range cases {
		if got := hostLabel(hosts); got != want {
			t.Errorf("hostLabel(%v) = %q, want %q", hosts, got, want)
		}
	}
}
