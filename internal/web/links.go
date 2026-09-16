// UDP and MQTT links.

package web

import (
	"net"
	"net/http"
	"strings"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

func (s *Server) udpLinkJSON(rc *radioCtx) map[string]any {
	uc := s.radioConfig(rc).Links.UDPMulticast
	group := uc.Group
	if group == "" {
		group = "239.0.0.69:4403"
	}
	u := map[string]any{"name": "udp", "type": "udp_multicast", "enabled": uc.Enabled, "group": uc.Group,
		"connected": false, "rx": 0, "tx": 0, "detail": group + " + 224.0.0.69:4403"}
	if l := rc.udp; l != nil {
		u["connected"], u["rx"], u["tx"] = l.Connected(), l.Rx.Load(), l.Tx.Load()
	}
	return u
}

func (s *Server) mqttLinksJSON(rc *radioCtx) []any {
	out := []any{}
	for _, mc := range s.radioConfig(rc).Links.MQTT {
		gw := mc.Gateway
		if gw == "" {
			gw = "relay"
		}
		m := map[string]any{"name": "mqtt:" + mc.Name, "connection": mc.Name, "type": "mqtt", "enabled": mc.Enabled,
			"connected": false, "rx": 0, "tx": 0, "dropped": 0, "detail": mc.Address, "broker": mc.Address, "tls": mc.TLS,
			"mode": mc.ModeOrDefault(), "format": mc.FormatOrDefault(), "gateway": gw, "ok_to_mqtt": mc.OKToMQTT,
			"relay_mqtt": mc.RelayMQTT, "cross_link": mc.CrossLink, "map_report": mc.MapReport.Enabled,
			"root": mc.Root, "downlink": []string{}, "uplink": []string{}}
		for _, l := range rc.mqtt {
			if l.Connection() != mc.Name {
				continue
			}
			m["connected"], m["rx"], m["tx"], m["dropped"] = l.Connected(), l.Rx.Load(), l.Tx.Load(), l.Dropped.Load()
			m["root"], m["downlink"], m["uplink"] = l.Root(), l.Subscriptions(), l.UplinkChannels()
			m["detail"] = l.Broker() + " · " + l.Root()
			if id := l.GatewayIdentity(); id != nil {
				m["gateway_id"] = id.NodeID()
			}
		}
		out = append(out, m)
	}
	return out
}

func (s *Server) links(w http.ResponseWriter, r *http.Request) {
	out := []any{}
	for _, rc := range s.radiosFor(r) {
		for _, l := range append([]any{s.udpLinkJSON(rc)}, s.mqttLinksJSON(rc)...) {
			m := l.(map[string]any)
			m["radio_id"], m["radio_name"] = rc.id, rc.name
			out = append(out, m)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) patchLink(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("name") != "udp" {
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	var req struct {
		Enabled *bool   `json:"enabled"`
		Group   *string `json:"group"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	rc := s.radioFor(r)
	if req.Group != nil {
		g := strings.TrimSpace(*req.Group)
		if g != "" {
			host, port, err := net.SplitHostPort(g)
			ip := net.ParseIP(host)
			if err != nil || ip == nil || !ip.IsMulticast() || port == "" {
				writeError(w, http.StatusBadRequest, "group must be a multicast address and port, e.g. 239.0.0.69:4403")
				return
			}
		}
		req.Group = &g
	}
	s.cfgMu.Lock()
	set := func(u *config.UDPMulticast) {
		if req.Enabled != nil {
			u.Enabled = *req.Enabled
		}
		if req.Group != nil {
			u.Group = *req.Group
		}
	}
	if rc == s.radios[0] {
		set(&s.cfg.Links.UDPMulticast)
	} else {
		for i := range s.cfg.Radios { // the radio's entry in the file, and its live view
			if s.cfg.Radios[i].ID == rc.id {
				set(&s.cfg.Radios[i].Links.UDPMulticast)
			}
		}
		if rc.cfg != nil {
			set(&rc.cfg.Links.UDPMulticast)
		}
	}
	s.cfgMu.Unlock()
	if err := s.saveIfPath(); err != nil {
		s.log.Warn("saving config", "err", err)
	}
	out := s.udpLinkJSON(rc)
	out["restart_required"] = len(s.restartReasons()) > 0
	writeJSON(w, http.StatusOK, out)
}
