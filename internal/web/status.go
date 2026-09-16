// Status, relay mode, the event stream and logs.

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

func (s *Server) statusJSON(r *http.Request) map[string]any {
	rc := s.radioFor(r)
	h := rc.host
	now := time.Now()
	rp := h.RadioParams()
	hc := h.Config()
	info := h.Radio().Info()
	st := rc.stats(r.Context())
	txMs, rxMs := h.Air.HourTotals(now)
	duty := hc.DutyCyclePct
	if duty == 0 {
		duty = rp.Region.DutyCyclePct
	}
	if hc.OverrideDutyCycle {
		duty = 100
	}
	primary := hc.PrimaryChannel
	if primary == "" {
		primary = rp.PresetName()
	}
	relay := map[string]any{"role": hc.RelayRole}
	if rel := h.Relay(); rel != nil {
		u := rel.UserCopy()
		relay["node_id"], relay["node_num"], relay["long_name"], relay["short_name"] = rel.NodeID(), rel.NodeNum, u.LongName, u.ShortName
	}
	c := &h.Counters
	return map[string]any{
		"version": s.opt.Version, "uptime_s": int(now.Sub(h.Started()).Seconds()),
		"radio_id": rc.id, "radio_name": rc.name, "site": s.siteJSON(),
		"radio": map[string]any{"driver": info.Driver, "device": s.radioConfig(rc).Radio.Device, "firmware": info.Firmware, "name": info.Name,
			"connected": st.Connected, "configured": h.RadioConfigured(), "reconnects": st.Reconnects, "rx": st.RxPackets,
			"tx": st.TxPackets, "errors": st.Errors, "noise_floor_dbm": st.NoiseFloorDBm, "queue": h.QueueLen()},
		"phy":             phyJSON(rp, primary),
		"relay":           relay,
		"map":             map[string]any{"tile_url": withMapKey(mapTileURL(s.cfg.Web.MapTileURL), s.opt.MapAPIKey)},
		"restart_reasons": s.restartReasons(),
		"nodes":           s.nodesHealth(),
		"airtime": map[string]any{"window_s": 3600, "tx_ms": txMs, "rx_ms": rxMs, "duty_limit_pct": duty,
			"tx_pct": h.Air.TxPercent(now), "channel_util_pct": h.Air.ChannelUtilPercent(now)},
		"counters": map[string]uint64{"rx": c.Rx.Load(), "rx_dupe": c.RxDupe.Load(), "rx_undecryptable": c.RxUndecryptable.Load(),
			"rx_bad": c.RxBad.Load(), "tx": c.Tx.Load(), "tx_failed": c.TxFailed.Load(), "relayed": c.Relayed.Load(),
			"relay_cancelled": c.RelayCancelled.Load(), "ack_ok": c.AckOK.Load(), "ack_fail": c.AckFail.Load(),
			"dropped_duty": c.DroppedDuty.Load()},
	}
}

func phyJSON(rp phy.RadioParams, primary string) map[string]any {
	return map[string]any{"region": rp.Region.Name, "preset": rp.Preset.String(), "preset_name": rp.PresetName(),
		"frequency_mhz": rp.FrequencyMHz, "bw_khz": rp.BwKHz, "sf": rp.SF, "cr": rp.CR, "slot": rp.Slot,
		"num_slots": rp.NumSlots, "sync_word": rp.SyncWord, "preamble": rp.Preamble, "tx_power_dbm": rp.TxPowerDBm,
		"primary_channel": primary, "duty_cycle_pct": rp.Region.DutyCyclePct}
}

func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.statusJSON(r))
}

func (s *Server) putRelay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if rc := s.radioFor(r); rc != s.radios[0] {
		s.putExtraRelay(w, r, rc, req.Role)
		return
	}
	s.cfgMu.Lock()
	next := *s.cfg
	next.Relay.Role = mesh.NormalizeRelayRole(req.Role)
	s.cfgMu.Unlock()
	if err := s.applyConfig(r, &next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.statusJSON(r)["relay"])
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	writeJSON(w, http.StatusOK, s.opt.Logs.Recent(limit))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, unsub := s.hostFor(r).Bus.Subscribe(512)
	defer unsub()
	send := func(event string, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send("status", s.statusJSON(r)) {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if !send("status", s.statusJSON(r)) {
				return
			}
		case e, ok := <-ch:
			if !ok {
				return
			}
			payload := e.Data
			switch e.Type {
			case "identity":
				idStr, _ := e.Data.(string)
				num, _ := wire.ParseNodeID(idStr)
				if id := s.hostFor(r).Identity(num); id != nil {
					payload = s.identityJSON(id)
				} else {
					payload = map[string]any{"node_id": idStr, "deleted": true}
				}
			case "node":
				idStr, _ := e.Data.(string)
				num, _ := wire.ParseNodeID(idStr)
				en, ok := s.hostFor(r).DB.Get(num)
				if !ok {
					continue
				}
				payload = nodeJSON(en, s.localIDs(s.hostFor(r)))
			case "plugin":
				id, _ := e.Data.(string)
				if s.opt.Plugins == nil {
					continue
				}
				if in, err := s.opt.Plugins.Get(id); err == nil {
					payload = s.pluginJSON(in)
				} else {
					payload = map[string]any{"id": id, "deleted": true}
				}
			}
			if !send(e.Type, payload) {
				return
			}
		}
	}
}
