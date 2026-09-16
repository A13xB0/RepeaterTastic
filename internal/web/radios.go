package web

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

// noRadio starts the error for a radio id that isn't there.
const noRadio = "no radio "

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
	pending, restart := s.pendingRadios()
	writeJSON(w, http.StatusOK, map[string]any{"radios": out, "site": s.siteJSON(), "pending": pending, "restart_required": restart})
}

// pendingRadios compares the radios running with the radios in the config: added ones start and
// removed ones stop at the next restart.
func (s *Server) pendingRadios() ([]map[string]any, bool) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	pending := []map[string]any{}
	inConfig := map[string]bool{config.MainRadioID: true}
	for _, ri := range s.cfg.Radios {
		inConfig[ri.ID] = true
		if s.radioByID(ri.ID) == nil {
			pending = append(pending, map[string]any{"id": ri.ID, "name": ri.Name, "device": ri.Radio.Device, "driver": ri.Radio.Driver,
				"region": ri.Mesh.Region, "preset": ri.Mesh.Preset, "tx_power_dbm": ri.Mesh.TxPowerDBm, "relay_role": ri.Relay.Role,
				"action": "start"})
		}
	}
	for _, rc := range s.radios {
		if !inConfig[rc.id] {
			pending = append(pending, map[string]any{"id": rc.id, "name": rc.name, "device": s.radioConfig(rc).Radio.Device,
				"action": "remove"})
		}
	}
	return pending, len(pending) > 0
}

// addRadio is POST /api/v1/radios: add a radio to the config. It starts at the next restart.
func (s *Server) addRadio(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Driver         string `json:"driver"`
		Device         string `json:"device"`
		Region         string `json:"region"`
		Preset         string `json:"preset"`
		PrimaryChannel string `json:"primary_channel"`
		TxPowerDBm     int    `json:"tx_power_dbm"`
		RelayRole      string `json:"relay_role"`
		CopyPosition   *bool  `json:"copy_position"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Driver == "" {
		req.Driver = "kiss"
	}
	s.cfgMu.Lock()
	whole := *s.cfg
	whole.Radios = append([]config.RadioInstance(nil), s.cfg.Radios...)
	s.cfgMu.Unlock()
	ri := config.RadioInstance{ID: req.ID, Name: strings.TrimSpace(req.Name),
		Radio: config.Radio{Driver: req.Driver, Device: strings.TrimSpace(req.Device)},
		Mesh: config.Mesh{Region: strings.ToUpper(req.Region), Preset: strings.ToUpper(req.Preset), PrimaryChannel: req.PrimaryChannel,
			TxPowerDBm: req.TxPowerDBm},
		Relay: config.Relay{Role: req.RelayRole}}
	if req.CopyPosition == nil || *req.CopyPosition { // same mast: same place
		ri.Position = whole.Position
	}
	if ri.Mesh.Preset == "" {
		ri.Mesh.Preset = "LONG_FAST"
	}
	s.cfgMu.Lock()
	whole = *s.cfg // check and add against the config as it is now
	whole.Radios = append(append([]config.RadioInstance(nil), s.cfg.Radios...), ri)
	whole.FillRadioDefaults()
	err := whole.Validate()
	if err == nil {
		s.cfg.Radios = whole.Radios
	}
	s.cfgMu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveIfPath(); err != nil {
		writeError(w, http.StatusInternalServerError, "radio added but the config file could not be saved: "+err.Error())
		return
	}
	s.log.Info("radio added from the web GUI", "radio", ri.ID, "device", ri.Radio.Device, "preset", ri.Mesh.Preset)
	writeJSON(w, http.StatusCreated, map[string]any{"id": ri.ID, "restart_required": true})
}

// patchRadio is PATCH /api/v1/radios/{id}: rename a radio.
func (s *Server) patchRadio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name *string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
		return
	}
	name := strings.TrimSpace(*req.Name)
	if len(name) > 40 {
		writeError(w, http.StatusBadRequest, "name must be at most 40 characters")
		return
	}
	s.cfgMu.Lock()
	name, found := s.renameRadio(id, name)
	s.cfgMu.Unlock()
	if !found {
		writeError(w, http.StatusNotFound, noRadio+id)
		return
	}
	if err := s.saveIfPath(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name})
}

// renameRadio names a radio in the config and on the running radio, returning the name it shows
// (a blank name shows as Main or the id). It reports false if there's no such radio. cfgMu must be held.
func (s *Server) renameRadio(id, name string) (string, bool) {
	found := id == config.MainRadioID
	if found {
		s.cfg.Site.MainRadioName = name
		if name == "" {
			name = "Main"
		}
	}
	for i := range s.cfg.Radios {
		if s.cfg.Radios[i].ID == id {
			s.cfg.Radios[i].Name, found = name, true
			if name == "" {
				name = id
			}
		}
	}
	if rc := s.radioByID(id); rc != nil && found {
		rc.name = name
	}
	return name, found
}

// pendingRadioEdit is the body of PUT /api/v1/radios/{id}.
type pendingRadioEdit struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Device     string `json:"device"`
	Region     string `json:"region"`
	Preset     string `json:"preset"`
	TxPowerDBm int    `json:"tx_power_dbm"`
	RelayRole  string `json:"relay_role"`
}

// apply copies the edit into a radio's entry; blank driver, region, preset and relay role are kept.
func (e pendingRadioEdit) apply(ri *config.RadioInstance) {
	ri.Name = strings.TrimSpace(e.Name)
	if e.Driver != "" {
		ri.Radio.Driver = e.Driver
	}
	ri.Radio.Device = strings.TrimSpace(e.Device)
	if e.Region != "" {
		ri.Mesh.Region = strings.ToUpper(e.Region)
	}
	if e.Preset != "" {
		ri.Mesh.Preset = strings.ToUpper(e.Preset)
	}
	ri.Mesh.TxPowerDBm = e.TxPowerDBm
	if e.RelayRole != "" {
		ri.Relay.Role = e.RelayRole
	}
}

// putRadio is PUT /api/v1/radios/{id}: change a radio that was added but hasn't started yet (its
// modem, preset, power and relay). A running radio is edited through /config?radio=<id>.
func (s *Server) putRadio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.radioByID(id) != nil {
		writeError(w, http.StatusConflict, "that radio is running; use Edit on it in Configuration → Radios")
		return
	}
	var req pendingRadioEdit
	if !readJSON(w, r, &req) {
		return
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	whole := *s.cfg
	whole.Radios = append([]config.RadioInstance(nil), s.cfg.Radios...)
	found := false
	for i := range whole.Radios {
		if whole.Radios[i].ID == id {
			found = true
			req.apply(&whole.Radios[i])
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, noRadio+id)
		return
	}
	whole.FillRadioDefaults()
	if err := whole.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfg.Radios = whole.Radios
	if s.cfg.Path() != "" {
		if err := s.cfg.Save(); err != nil { // cfgMu is held
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "restart_required": true})
}

// deleteRadio is DELETE /api/v1/radios/{id}: remove an extra radio from the config. It stops at
// the next restart; its state dir (identity keys, history) is kept, so adding the same id back
// brings its identities back.
func (s *Server) deleteRadio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == config.MainRadioID {
		writeError(w, http.StatusBadRequest, "the main radio can't be removed; change its device or preset instead")
		return
	}
	s.cfgMu.Lock()
	kept := make([]config.RadioInstance, 0, len(s.cfg.Radios))
	for _, ri := range s.cfg.Radios {
		if ri.ID != id {
			kept = append(kept, ri)
		}
	}
	found := len(kept) != len(s.cfg.Radios)
	if found {
		s.cfg.Radios = kept
	}
	s.cfgMu.Unlock()
	if !found {
		writeError(w, http.StatusNotFound, noRadio+id)
		return
	}
	if err := s.saveIfPath(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("radio removed from the web GUI", "radio", id)
	_, restart := s.pendingRadios()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "restart_required": restart})
}

// getSite / putSite are the settings shared by every radio on the mast.
func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.siteSettingsJSON())
}

func (s *Server) siteSettingsJSON() map[string]any {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	running := 0.0
	if s.opt.Site != nil {
		running = s.opt.Site.DutyCyclePct()
	}
	return map[string]any{"duty_cycle_percent": s.cfg.Site.DutyCyclePct, "main_radio_name": s.cfg.Site.MainRadioName,
		"running_duty_cycle_percent": running, "coordinator": s.opt.Site != nil}
}

func (s *Server) putSite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DutyCyclePct float64 `json:"duty_cycle_percent"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.DutyCyclePct < 0 || req.DutyCyclePct > 100 {
		writeError(w, http.StatusBadRequest, "duty_cycle_percent must be between 0 and 100")
		return
	}
	s.cfgMu.Lock()
	s.cfg.Site.DutyCyclePct = req.DutyCyclePct
	s.cfgMu.Unlock()
	restart := false
	if s.opt.Site != nil {
		s.opt.Site.SetDutyCyclePct(req.DutyCyclePct)
	} else {
		restart = req.DutyCyclePct > 0 // a single radio only gets a site coordinator at start
	}
	if err := s.saveIfPath(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := s.siteSettingsJSON()
	out["restart_required"] = restart
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveIfPath() error {
	if s.cfg.Path() == "" {
		return nil
	}
	return s.saveConfigFile()
}

// putExtraRelay changes an additional radio's relay role at runtime and in the config file.
func (s *Server) putExtraRelay(w http.ResponseWriter, r *http.Request, rc *radioCtx, role string) {
	if err := rc.host.SetRelayRole(role); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := rc.host.PushConfig(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "relay role changed here, but "+err.Error())
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
	s.cfgMu.Unlock()
	if err := s.saveIfPath(); err != nil {
		s.log.Warn("relay role changed but the config file could not be saved", "radio", rc.id, "err", err)
	}
	writeJSON(w, http.StatusOK, s.statusJSON(r)["relay"])
}

// applyRadioConfig validates and applies an extra radio's edited view, writing it back into
// that radio's radios: entry in the config file.
func (s *Server) applyRadioConfig(r *http.Request, rc *radioCtx, next *config.Config) error {
	s.cfgMu.Lock()
	whole := *s.cfg
	radios := append([]config.RadioInstance(nil), whole.Radios...)
	found := setRadioSections(radios, rc.id, next)
	whole.Radios = radios
	s.cfgMu.Unlock()
	if !found {
		return errors.New(noRadio + rc.id + " in the configuration")
	}
	if err := whole.Validate(); err != nil {
		return err
	}
	var view config.RadioConfig
	for _, v := range whole.RadioConfigs() {
		if v.ID == rc.id {
			view = v
		}
	}
	if err := rc.host.UpdateConfig(r.Context(), view.MeshConfig()); err != nil {
		return err
	}
	edited := lastRadioEntry(radios, rc.id)
	s.cfgMu.Lock()
	for i := range s.cfg.Radios { // only this radio's entry: others may have changed meanwhile
		if s.cfg.Radios[i].ID == rc.id {
			s.cfg.Radios[i] = edited
		}
	}
	*rc.cfg = *view.Config
	s.cfgMu.Unlock()
	if s.cfg.Path() != "" {
		if err := s.saveConfigFile(); err != nil {
			s.log.Warn("radio config applied but not saved", "radio", rc.id, "err", err)
		}
	}
	return nil
}

// setRadioSections copies the per-radio sections of next into the radio's entries, reporting
// whether there was one.
func setRadioSections(radios []config.RadioInstance, id string, next *config.Config) bool {
	found := false
	for i := range radios {
		if radios[i].ID == id {
			radios[i].Radio, radios[i].Mesh, radios[i].Relay = next.Radio, next.Mesh, next.Relay
			radios[i].Airtime, radios[i].Links, radios[i].Position = next.Airtime, next.Links, next.Position
			found = true
		}
	}
	return found
}

// lastRadioEntry is the last entry with that id.
func lastRadioEntry(radios []config.RadioInstance, id string) config.RadioInstance {
	var out config.RadioInstance
	for _, ri := range radios {
		if ri.ID == id {
			out = ri
		}
	}
	return out
}

// restartDaemon is POST /api/v1/restart: exit so systemd starts the daemon again with the
// saved config (the unit restarts on failure). Changes such as the MQTT connection are only
// read at start.
func (s *Server) restartDaemon(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
	s.log.Warn("restart requested from the web GUI")
	go func() {
		time.Sleep(500 * time.Millisecond) // let the response reach the browser
		if s.opt.Restart != nil {
			s.opt.Restart() // clean shutdown, then exit 75 for the supervisor
			return
		}
		os.Exit(75)
	}()
}

// radioIDOK reports whether a radio id is safe to use as a folder name.
func radioIDOK(id string) bool {
	if id == "" || len(id) > 24 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
