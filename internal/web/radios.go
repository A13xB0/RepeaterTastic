package web

import (
	"net/http"
	"time"
)

// radioByID returns the radio with that ID, or nil.
func (s *Server) radioByID(id string) *radioCtx {
	for _, rc := range s.radios {
		if rc.id == id {
			return rc
		}
	}
	return nil
}

// siteJSON describes the site coordinator: nil when a single radio runs without a site budget.
func (s *Server) siteJSON() map[string]any {
	if s.opt.Site == nil {
		return nil
	}
	return map[string]any{"radios": len(s.radios), "duty_limit_pct": s.opt.Site.DutyCyclePct(), "tx_pct": s.opt.Site.TxPercent()}
}

// listRadios is GET /api/v1/radios: one summary per radio, main first.
func (s *Server) listRadios(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	out := make([]map[string]any, 0, len(s.radios))
	for i, rc := range s.radios {
		rp := rc.host.RadioParams()
		info := rc.host.Radio().Info()
		st := rc.stats(r.Context())
		idents := 0
		for _, id := range rc.host.Identities() {
			if !id.IsRelay {
				idents++
			}
		}
		var overlaps []string
		if s.opt.Site != nil {
			for _, o := range s.opt.Site.Overlaps(rc.host) {
				overlaps = append(overlaps, o.RadioID())
			}
		}
		relay := map[string]any{"role": rc.host.Config().RelayRole}
		if rel := rc.host.Relay(); rel != nil {
			u := rel.UserCopy()
			relay["node_id"], relay["long_name"] = rel.NodeID(), u.LongName
		}
		primary := rc.host.Config().PrimaryChannel
		if primary == "" {
			primary = rp.PresetName()
		}
		out = append(out, map[string]any{
			"id": rc.id, "name": rc.name, "main": i == 0,
			"device": s.radioConfig(rc).Radio.Device, "driver": info.Driver, "firmware": info.Firmware,
			"connected": st.Connected, "configured": rc.host.RadioConfigured(), "noise_floor_dbm": st.NoiseFloorDBm,
			"phy": phyJSON(rp, primary), "relay": relay, "identities": idents,
			"tx_pct": rc.host.Air.TxPercent(now), "channel_util_pct": rc.host.Air.ChannelUtilPercent(now),
			"overlaps": overlaps,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"radios": out, "site": s.siteJSON()})
}

// putExtraRelay changes an additional radio's relay role at runtime and in the config file.
func (s *Server) putExtraRelay(w http.ResponseWriter, r *http.Request, rc *radioCtx, role string) {
	if err := rc.host.SetRelayRole(role); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfgMu.Lock()
	for i := range s.cfg.Radios {
		if s.cfg.Radios[i].ID == rc.id {
			s.cfg.Radios[i].Relay.Role = role
		}
	}
	if rc.cfg != nil {
		rc.cfg.Relay.Role = role
	}
	err := s.cfg.Save()
	s.cfgMu.Unlock()
	if err != nil {
		s.log.Warn("relay role changed but the config file could not be saved", "radio", rc.id, "err", err)
	}
	writeJSON(w, http.StatusOK, s.statusJSON(r)["relay"])
}
