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

// restartReasons lists saved changes that only take effect when the daemon restarts: what the
// running daemon started with against what is saved now.
func (s *Server) restartReasons() []string {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.booted == nil {
		return nil
	}
	var out []string
	if s.restorePending.Load() {
		out = append(out, "restored backup")
	}
	b, c := s.booted, s.cfg
	if b.Web.Bind != c.Web.Bind || b.Web.Port != c.Web.Port {
		out = append(out, "web address")
	}
	if b.MDNS.Enabled != c.MDNS.Enabled {
		out = append(out, "mDNS")
	}
	if (b.Site.DutyCyclePct > 0) != (c.Site.DutyCyclePct > 0) && s.opt.Site == nil {
		out = append(out, "site airtime cap")
	}
	if !reflect.DeepEqual(b.Plugins, c.Plugins) {
		out = append(out, "plugins")
	}
	running := map[string]config.RadioConfig{}
	for _, rc := range b.RadioConfigs() {
		running[rc.ID] = rc
	}
	saved := map[string]bool{}
	for _, rc := range c.RadioConfigs() {
		saved[rc.ID] = true
		was, ok := running[rc.ID]
		switch {
		case !ok:
			out = append(out, rc.Name+" added")
			continue
		case was.Radio != rc.Radio:
			out = append(out, rc.Name+" modem connection")
		}
		if (len(was.Links.MQTT) > 0 || len(rc.Links.MQTT) > 0) && !reflect.DeepEqual(was.Links.MQTT, rc.Links.MQTT) {
			out = append(out, rc.Name+" MQTT")
		}
		if was.Links.UDPMulticast != rc.Links.UDPMulticast {
			out = append(out, rc.Name+" UDP multicast")
		}
	}
	for _, rc := range b.RadioConfigs() {
		if !saved[rc.ID] {
			out = append(out, rc.Name+" removed")
		}
	}
	return out
}
