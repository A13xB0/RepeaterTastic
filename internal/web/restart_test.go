package web

import (
	"fmt"
	"sync"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
)

// lazyModem is a modem that hasn't opened yet and follows a new device when told to.
type lazyModem struct {
	*fakeModem
	mu      sync.Mutex
	targets []string
	refuse  bool
}

func (l *lazyModem) Retarget(driver, device string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.targets = append(l.targets, driver+" "+device)
	return !l.refuse
}

func (l *lazyModem) retargets() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.targets...)
}

func TestRetargetable(t *testing.T) {
	kiss := config.Radio{Driver: "kiss", Device: "/dev/ttyUSB0", Baud: 115200}
	cases := []struct {
		name string
		cur  config.Radio
		want bool
	}{
		{"unchanged", kiss, false},
		{"new port", config.Radio{Driver: "kiss", Device: "/dev/ttyUSB1", Baud: 115200}, true},
		{"new baud", config.Radio{Driver: "kiss", Device: "/dev/ttyUSB1", Baud: 9600}, false},
		{"to spi", config.Radio{Driver: "spi", Device: "lora-x", Baud: 115200}, true},
		{"to a board", config.Radio{Driver: "meshtastic", Device: "/dev/ttyACM0", Baud: 115200}, false},
	}
	for _, c := range cases {
		if got := retargetable(kiss, c.cur); got != c.want {
			t.Errorf("%s: retargetable = %v", c.name, got)
		}
	}
	if modemDriver("none") || !modemDriver("spi") {
		t.Error("modemDriver")
	}
}

func TestFollowUnopenedDevices(t *testing.T) {
	modem := &lazyModem{fakeModem: &fakeModem{Radio: null.New(), info: radio.Info{Driver: "kiss"}}}
	env := newRadioEnv(t, modem, false, func(o *Options) {
		o.Config.Radio.Driver, o.Config.Radio.Device = "kiss", "/dev/ttyUSB0"
	})
	s := env.s
	s.cfgMu.Lock()
	s.cfg.Radio.Device = "/dev/ttyUSB1"
	s.cfgMu.Unlock()
	if reasons := s.restartReasons(); len(reasons) != 0 {
		t.Fatalf("a followed port still needs a restart: %v", reasons)
	}
	if got := modem.retargets(); fmt.Sprint(got) != "[kiss /dev/ttyUSB1]" {
		t.Fatalf("retargets = %v", got)
	}
	if s.bootedRadio(config.MainRadioID).Device != "/dev/ttyUSB1" {
		t.Fatalf("booted device = %q", s.bootedRadio(config.MainRadioID).Device)
	}
	// A modem that has opened refuses, so the change waits for a restart.
	modem.mu.Lock()
	modem.refuse = true
	modem.mu.Unlock()
	s.cfgMu.Lock()
	s.cfg.Radio.Device = "/dev/ttyUSB2"
	s.cfgMu.Unlock()
	if reasons := s.restartReasons(); fmt.Sprint(reasons) != "[Main modem connection]" {
		t.Fatalf("reasons = %v", reasons)
	}
}

func TestBootedRadio(t *testing.T) {
	s := &Server{booted: &config.Config{Radios: []config.RadioInstance{{ID: "mf", Radio: config.Radio{Device: "/dev/x"}}}}}
	if r := s.bootedRadio("mf"); r == nil || r.Device != "/dev/x" {
		t.Fatalf("mf = %v", r)
	}
	if s.bootedRadio("new") != nil {
		t.Fatal("a radio that didn't start has booted settings")
	}
	// Without a booted config nothing needs a restart.
	s = &Server{}
	if s.restartReasons() != nil {
		t.Fatal("restart reasons without a booted config")
	}
	if c := cloneConfig(nil); c == nil {
		t.Fatal("cloneConfig(nil) = nil")
	}
}

func TestDaemonRestartReasons(t *testing.T) {
	b := config.Default()
	c := cloneConfig(b)
	c.Web.Port++
	c.MDNS.Enabled = !b.MDNS.Enabled
	c.Site.DutyCyclePct = 5
	c.Plugins.Enabled = !b.Plugins.Enabled
	c.Hosted.DockerImage = "meshtastic/meshtasticd:2.8"
	got := fmt.Sprint(daemonRestartReasons(b, c, true))
	if got != "[web address mDNS site airtime cap plugins hosted meshtasticd]" {
		t.Fatalf("reasons = %s", got)
	}
	// With a site coordinator the cap applies live.
	if got := fmt.Sprint(daemonRestartReasons(b, c, false)); got != "[web address mDNS plugins hosted meshtasticd]" {
		t.Fatalf("reasons with a site = %s", got)
	}
}
