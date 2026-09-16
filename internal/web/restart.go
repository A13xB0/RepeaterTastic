package web

import (
	"reflect"

	"gopkg.in/yaml.v3"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

// cloneConfig deep-copies a configuration (YAML keeps secrets such as MQTT passwords).
func cloneConfig(c *config.Config) *config.Config {
	out := &config.Config{}
	if c == nil {
		return out
	}
	b, err := yaml.Marshal(c)
	if err == nil {
		_ = yaml.Unmarshal(b, out)
	}
	return out
}

// followUnopenedDevices points a radio whose modem hasn't opened yet at its saved device straight
// away, so choosing the port (in setup or Configuration) needs no restart until a modem has
// connected.
func (s *Server) followUnopenedDevices() {
	type retargeter interface {
		Retarget(driver, device string) bool
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.booted == nil {
		return
	}
	saved := map[string]config.RadioConfig{}
	for _, rc := range s.cfg.RadioConfigs() {
		saved[rc.ID] = rc
	}
	for _, rc := range s.radios {
		cur, ok := saved[rc.id]
		rt, canRetarget := rc.host.Radio().(retargeter)
		if !ok || !canRetarget {
			continue
		}
		was := s.bootedRadio(rc.id)
		if was == nil || !retargetable(*was, cur.Radio) {
			continue
		}
		if rt.Retarget(cur.Radio.Driver, cur.Radio.Device) {
			was.Driver, was.Device = cur.Radio.Driver, cur.Radio.Device
		}
	}
}

// bootedRadio is the modem settings a radio started with, or nil if it wasn't running. cfgMu must be held.
func (s *Server) bootedRadio(id string) *config.Radio {
	if id == config.MainRadioID {
		return &s.booted.Radio
	}
	var was *config.Radio
	for i := range s.booted.Radios {
		if s.booted.Radios[i].ID == id {
			was = &s.booted.Radios[i].Radio
		}
	}
	return was
}

// retargetable reports whether a modem's device or driver changed in a way the lazy modem radio
// can follow without a restart.
func retargetable(was, cur config.Radio) bool {
	if (was.Device == cur.Device && was.Driver == cur.Driver) || was.Baud != cur.Baud {
		return false
	}
	// A Meshtastic board is a different kind of radio: that takes a restart.
	return modemDriver(was.Driver) && modemDriver(cur.Driver)
}

// restartReasons lists saved changes that only take effect when the daemon restarts: what the
// running daemon started with against what is saved now.
func (s *Server) restartReasons() []string {
	s.followUnopenedDevices()
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.booted == nil {
		return nil
	}
	var out []string
	if s.restorePending.Load() {
		out = append(out, "restored backup")
	}
	out = append(out, daemonRestartReasons(s.booted, s.cfg, s.opt.Site == nil)...)
	return append(out, radioRestartReasons(s.booted, s.cfg)...)
}

// daemonRestartReasons lists changed settings that belong to the whole daemon. Without a site
// coordinator, turning the site airtime cap on or off needs one to start or stop.
func daemonRestartReasons(b, c *config.Config, noSite bool) []string {
	var out []string
	if b.Web.Bind != c.Web.Bind || b.Web.Port != c.Web.Port {
		out = append(out, "web address")
	}
	if b.MDNS.Enabled != c.MDNS.Enabled {
		out = append(out, "mDNS")
	}
	if (b.Site.DutyCyclePct > 0) != (c.Site.DutyCyclePct > 0) && noSite {
		out = append(out, "site airtime cap")
	}
	if !reflect.DeepEqual(b.Plugins, c.Plugins) {
		out = append(out, "plugins")
	}
	if b.Hosted != c.Hosted {
		out = append(out, "hosted meshtasticd")
	}
	return out
}

// radioRestartReasons lists radios added, removed or changed where the change needs a restart.
func radioRestartReasons(b, c *config.Config) []string {
	var out []string
	running := map[string]config.RadioConfig{}
	for _, rc := range b.RadioConfigs() {
		running[rc.ID] = rc
	}
	saved := map[string]bool{}
	for _, rc := range c.RadioConfigs() {
		saved[rc.ID] = true
		if was, ok := running[rc.ID]; ok {
			out = append(out, radioChanges(was, rc)...)
		} else {
			out = append(out, rc.Name+" added")
		}
	}
	for _, rc := range b.RadioConfigs() {
		if !saved[rc.ID] {
			out = append(out, rc.Name+" removed")
		}
	}
	return out
}

// radioChanges lists a running radio's saved changes that need a restart.
func radioChanges(was, rc config.RadioConfig) []string {
	var out []string
	if was.Radio != rc.Radio {
		out = append(out, rc.Name+" modem connection")
	}
	if (len(was.Links.MQTT) > 0 || len(rc.Links.MQTT) > 0) && !reflect.DeepEqual(was.Links.MQTT, rc.Links.MQTT) {
		out = append(out, rc.Name+" MQTT")
	}
	if was.Links.UDPMulticast != rc.Links.UDPMulticast {
		out = append(out, rc.Name+" UDP multicast")
	}
	return out
}

// modemDriver reports whether the lazy modem radio can switch to a driver without a restart.
func modemDriver(d string) bool { return d == "kiss" || d == "spi" }
