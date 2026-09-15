package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/phy"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

// ---------------------------------------------------------------------------------- setup/auth

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"needed": s.auth.SetupNeeded()})
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.SetupNeeded() {
		writeError(w, http.StatusConflict, "setup has already been completed; sign in instead")
		return
	}
	var req struct {
		Password  string `json:"password"`
		Region    string `json:"region"`
		Preset    string `json:"preset"`
		Device    string `json:"device"`
		RelayRole string `json:"relay_role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.cfgMu.Lock()
	next := *s.cfg
	if req.Region != "" {
		next.Mesh.Region = strings.ToUpper(req.Region)
	}
	if req.Preset != "" {
		next.Mesh.Preset = strings.ToUpper(req.Preset)
	}
	if req.RelayRole != "" {
		next.Relay.Role = req.RelayRole
	}
	restart := false
	if req.Device != "" && req.Device != next.Radio.Device {
		next.Radio.Device = req.Device
		restart = true
	}
	if err := next.Validate(); err != nil {
		s.cfgMu.Unlock()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfgMu.Unlock()
	if err := s.auth.SetPassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.applyConfig(r, &next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok, exp := s.auth.IssueJWT()
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires": exp.UnixMilli(), "restart_required": restart})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v, _ := s.loginFails.LoadOrStore(clientIP(r), &loginState{})
	ls := v.(*loginState)
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if time.Now().Before(ls.until) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again in a minute")
		return
	}
	if !s.auth.CheckPassword(req.Password) {
		ls.fails++
		if ls.fails >= 5 {
			ls.until, ls.fails = time.Now().Add(time.Minute), 0
		}
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	ls.fails = 0
	tok, exp := s.auth.IssueJWT()
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires": exp.UnixMilli()})
}

// ---------------------------------------------------------------------------------------- status

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
		"phy":   phyJSON(rp, primary),
		"relay": relay,
		"map":   map[string]any{"tile_url": mapTileURL(s.cfg.Web.MapTileURL)},
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
	next.Relay.Role = req.Role
	s.cfgMu.Unlock()
	if err := s.applyConfig(r, &next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.statusJSON(r)["relay"])
}

// ------------------------------------------------------------------------------------ identities

func (s *Server) identityJSON(id *mesh.Identity) map[string]any {
	rc := s.radioOf(id)
	now := time.Now()
	u := id.UserCopy()
	rp := rc.host.RadioParams()
	display := rp.PresetName()
	var chans []map[string]any
	for i := 0; i < mesh.MaxChannels; i++ {
		ch := id.ChannelCopy(i)
		if ch == nil || ch.Role == pb.Channel_DISABLED {
			continue
		}
		st := ch.GetSettings()
		name := st.GetName()
		dn := name
		if dn == "" {
			dn = display
		}
		key := wire.ExpandPSK(st.GetPsk())
		chans = append(chans, map[string]any{"index": i, "role": ch.Role.String(), "name": name, "display_name": dn,
			"psk": base64.StdEncoding.EncodeToString(st.GetPsk()), "hash": wire.ChannelHash(dn, key, st.GetUseAead()),
			"uplink": st.GetUplinkEnabled(), "downlink": st.GetDownlinkEnabled(), "locked": i == 0})
	}
	txTotal, _ := rc.host.Air.HourTotals(now)
	mine := rc.host.Air.IdentityHourMs(now, id.NodeNum)
	share := 0.0
	if txTotal > 0 {
		share = mine / txTotal * 100
	}
	var api any
	if !id.IsRelay {
		bind := id.APIBind
		if bind == "" {
			bind = "0.0.0.0"
		}
		_, running := rc.api.Status(id.NodeNum)
		api = map[string]any{"bind": bind, "port": id.APIPort, "clients": id.ClientCount(), "listening": running}
	}
	return map[string]any{
		"node_id": id.NodeID(), "node_num": id.NodeNum, "long_name": u.LongName, "short_name": u.ShortName,
		"role": u.Role.String(), "hw_model": u.HwModel.String(), "public_key": base64.StdEncoding.EncodeToString(id.PublicKey),
		"is_relay": id.IsRelay, "enabled": id.Enabled, "api": api, "outbox": id.BacklogLen(),
		"airtime_ms_1h": mine, "share_pct": share, "created_at": id.CreatedAt.UnixMilli(), "channels": chans,
		"last_byte": wire.LastByte(id.NodeNum), "share_limit_pct": s.shareLimit(id),
		"unread": rc.host.Messages.UnreadTotal(id.NodeNum, id.NodeID()),
	}
}

func (s *Server) shareLimit(id *mesh.Identity) float64 {
	if id.ShareLimitPct > 0 {
		return id.ShareLimitPct
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.Airtime.IdentitySharePct
}

func (s *Server) identityParam(w http.ResponseWriter, r *http.Request) *mesh.Identity {
	num, err := wire.ParseNodeID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "node id must look like !a1c40e07")
		return nil
	}
	id := s.hostFor(r).Identity(num)
	if id == nil {
		writeError(w, http.StatusNotFound, "no identity "+wire.NodeID(num)+" on this host")
	}
	return id
}

func (s *Server) listIdentities(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, id := range s.hostFor(r).Identities() {
		out = append(out, s.identityJSON(id))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) nextFreePort() int {
	used := map[int]bool{}
	for _, rc := range s.radios { // API ports are per host, not per radio
		for _, id := range rc.host.Identities() {
			used[id.APIPort] = true
		}
	}
	for p := 4403; p < 4503; p++ {
		if !used[p] {
			return p
		}
	}
	return 0
}

func decodeKey(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		b, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	}
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, base64 encoded")
	}
	return b, nil
}

func (s *Server) previewKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PrivateKey string `json:"private_key"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	priv, err := decodeKey(req.PrivateKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := mesh.NewIdentity(priv, "", "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var collision any
	if c := s.hostFor(r).DB.LastByteCollision(id.NodeNum); c != 0 {
		collision = wire.NodeID(c)
	}
	if s.hostFor(r).Identity(id.NodeNum) != nil {
		collision = id.NodeID()
	}
	writeJSON(w, http.StatusOK, map[string]any{"private_key": base64.StdEncoding.EncodeToString(id.PrivateKey),
		"public_key": base64.StdEncoding.EncodeToString(id.PublicKey), "node_id": id.NodeID(), "node_num": id.NodeNum,
		"last_byte": wire.LastByte(id.NodeNum), "collision": collision})
}

func (s *Server) createIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LongName   string  `json:"long_name"`
		ShortName  string  `json:"short_name"`
		PrivateKey string  `json:"private_key"`
		APIPort    int     `json:"api_port"`
		APIBind    string  `json:"api_bind"`
		Role       string  `json:"role"`
		ShareLimit float64 `json:"share_limit_pct"`
		RadioID    string  `json:"radio_id"` // "" = ?radio= or the main radio
	}
	if !readJSON(w, r, &req) {
		return
	}
	rc := s.radioFor(r)
	if req.RadioID != "" {
		if rc = s.radioByID(req.RadioID); rc == nil {
			writeError(w, http.StatusBadRequest, "no radio "+req.RadioID)
			return
		}
	}
	host := rc.host
	if strings.TrimSpace(req.LongName) == "" {
		writeError(w, http.StatusBadRequest, "long_name is required")
		return
	}
	priv, err := decodeKey(req.PrivateKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var id *mesh.Identity
	for attempt := 0; attempt < 500; attempt++ {
		id, err = mesh.NewIdentity(priv, req.LongName, req.ShortName)
		if err != nil {
			if priv != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			continue
		}
		if priv != nil || host.DB.LastByteCollision(id.NodeNum) == 0 {
			break
		}
	}
	if req.APIPort == 0 {
		req.APIPort = s.nextFreePort()
	}
	for _, orc := range s.radios { // one host, one port space, whatever the radio
		for _, other := range orc.host.Identities() {
			if other.APIPort == req.APIPort && (other.APIBind == req.APIBind || other.APIBind == "" || req.APIBind == "") {
				writeError(w, http.StatusConflict, fmt.Sprintf("port %d is already used by %s", req.APIPort, other.NodeID()))
				return
			}
		}
	}
	id.APIPort, id.APIBind, id.ShareLimitPct = req.APIPort, req.APIBind, req.ShareLimit
	// Only the relay persona repeats, so a new identity says so unless asked otherwise.
	if req.Role == "" {
		req.Role = pb.Config_DeviceConfig_CLIENT_MUTE.String()
	}
	if req.Role != "" {
		if err := id.SetRole(req.Role); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := host.AddIdentity(id); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.saveIdentities()
	writeJSON(w, http.StatusCreated, s.identityJSON(id))
}

func (s *Server) saveIdentities() {
	for _, rc := range s.radios {
		if err := rc.host.SaveIdentities(); err != nil {
			s.log.Error("saving identities", "radio", rc.id, "err", err)
		}
	}
}

func (s *Server) patchIdentity(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	var req struct {
		LongName  *string  `json:"long_name"`
		ShortName *string  `json:"short_name"`
		Enabled   *bool    `json:"enabled"`
		APIPort   *int     `json:"api_port"`
		APIBind   *string  `json:"api_bind"`
		Role      *string  `json:"role"`
		Share     *float64 `json:"share_limit_pct"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Role != nil && !id.IsRelay {
		if err := id.SetRole(*req.Role); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Share != nil {
		if *req.Share < 0 || *req.Share > 100 {
			writeError(w, http.StatusBadRequest, "share_limit_pct must be between 0 and 100")
			return
		}
		id.ShareLimitPct = *req.Share
	}
	long, short := "", ""
	if req.LongName != nil {
		long = *req.LongName
	}
	if req.ShortName != nil {
		short = *req.ShortName
	}
	id.SetOwner(long, short)
	if req.Enabled != nil && !id.IsRelay {
		id.Enabled = *req.Enabled
	}
	if req.APIPort != nil && !id.IsRelay {
		for _, other := range s.hostFor(r).Identities() {
			if other != id && other.APIPort == *req.APIPort {
				writeError(w, http.StatusConflict, fmt.Sprintf("port %d is already used by %s", *req.APIPort, other.NodeID()))
				return
			}
		}
		id.APIPort = *req.APIPort
	}
	if req.APIBind != nil {
		id.APIBind = *req.APIBind
	}
	s.hostFor(r).DB.Update(id.NodeNum, func(e *mesh.NodeEntry) { e.User = id.UserCopy() })
	s.hostFor(r).ChannelsChanged()
	s.hostFor(r).Bus.Publish(mesh.Event{Type: "identity", Data: id.NodeID()})
	s.saveIdentities()
	if long != "" || short != "" {
		s.hostFor(r).RequestNodeInfo(id, wire.Broadcast)
	}
	writeJSON(w, http.StatusOK, s.identityJSON(id))
}

func (s *Server) deleteIdentity(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	if err := s.hostFor(r).RemoveIdentity(id.NodeNum); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.saveIdentities()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getKey(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"private_key": base64.StdEncoding.EncodeToString(id.PrivateKey),
		"public_key": base64.StdEncoding.EncodeToString(id.PublicKey)})
}

func (s *Server) putChannel(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 || idx >= mesh.MaxChannels {
		writeError(w, http.StatusBadRequest, "channel index must be 0-7")
		return
	}
	var req struct {
		Name     string `json:"name"`
		PSK      string `json:"psk"`
		Role     string `json:"role"`
		Uplink   bool   `json:"uplink"`
		Downlink bool   `json:"downlink"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	psk, err := base64.StdEncoding.DecodeString(req.PSK)
	if err != nil {
		writeError(w, http.StatusBadRequest, "psk must be base64 (AQ== is the default key)")
		return
	}
	role := pb.Channel_SECONDARY
	switch strings.ToUpper(req.Role) {
	case "PRIMARY":
		role = pb.Channel_PRIMARY
	case "DISABLED":
		role = pb.Channel_DISABLED
	}
	if idx == 0 {
		if role != pb.Channel_PRIMARY {
			writeError(w, http.StatusConflict, "channel 0 is the shared primary channel; its role can't change")
			return
		}
		if req.Name != s.hostFor(r).Config().PrimaryChannel {
			writeError(w, http.StatusConflict,
				"the primary channel name is shared by every identity because it picks the frequency; change it under Configuration → Radio")
			return
		}
	}
	if len(req.Name) > 11 {
		writeError(w, http.StatusBadRequest, "channel names are at most 11 characters")
		return
	}
	old := id.ChannelCopy(idx)
	ch := &pb.Channel{Index: int32(idx), Role: role, Settings: &pb.ChannelSettings{Name: req.Name, Psk: psk,
		UplinkEnabled: req.Uplink, DownlinkEnabled: req.Downlink, ModuleSettings: old.GetSettings().GetModuleSettings()}}
	if err := s.hostFor(r).SetChannel(id, ch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.saveIdentities()
	writeJSON(w, http.StatusOK, s.identityJSON(id))
}

func (s *Server) getChannelURL(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	rp := s.hostFor(r).RadioParams()
	set := &pb.ChannelSet{LoraConfig: &pb.Config_LoRaConfig{UsePreset: true, ModemPreset: rp.Preset, Region: rp.Region.Code,
		HopLimit: s.hostFor(r).Config().HopLimit, TxEnabled: true}}
	for i := 0; i < mesh.MaxChannels; i++ {
		if ch := id.ChannelCopy(i); ch != nil && ch.Role != pb.Channel_DISABLED {
			set.Settings = append(set.Settings, ch.Settings)
		}
	}
	b, _ := proto.Marshal(set)
	writeJSON(w, http.StatusOK, map[string]string{"url": "https://meshtastic.org/e/#" + base64.RawURLEncoding.EncodeToString(b)})
}

func (s *Server) postChannelURL(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	frag := req.URL
	if i := strings.Index(frag, "#"); i >= 0 {
		frag = frag[i+1:]
	}
	frag = strings.TrimRight(strings.TrimSpace(frag), "=")
	frag = strings.NewReplacer("+", "-", "/", "_").Replace(frag)
	b, err := base64.RawURLEncoding.DecodeString(frag)
	set := &pb.ChannelSet{}
	if err != nil || proto.Unmarshal(b, set) != nil || len(set.Settings) == 0 {
		writeError(w, http.StatusBadRequest, "that doesn't look like a Meshtastic channel URL (https://meshtastic.org/e/#…)")
		return
	}
	primaryName := s.hostFor(r).Config().PrimaryChannel
	next := 1
	for i, st := range set.Settings {
		if i == 0 && (st.GetName() == primaryName || st.GetName() == "") {
			ch := id.ChannelCopy(0)
			ch.Settings.Psk = st.Psk
			_ = s.hostFor(r).SetChannel(id, ch)
			continue
		}
		if next >= mesh.MaxChannels {
			break
		}
		_ = s.hostFor(r).SetChannel(id, &pb.Channel{Index: int32(next), Role: pb.Channel_SECONDARY, Settings: st})
		next++
	}
	s.saveIdentities()
	writeJSON(w, http.StatusOK, s.identityJSON(id))
}

// -------------------------------------------------------------------------------------- messages

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	convs := s.hostFor(r).Messages.Conversations(id.NodeNum, id.NodeID())
	// Every enabled channel is a conversation even before anything is said on it:
	// the chat page only opens listed conversations, so a new identity could
	// otherwise send DMs but never post in a channel.
	listed := map[string]bool{}
	for _, c := range convs {
		listed[c.Key] = true
	}
	for i := 0; i < mesh.MaxChannels; i++ {
		ch := id.ChannelCopy(i)
		key := "ch:" + strconv.Itoa(i)
		if ch == nil || ch.Role == pb.Channel_DISABLED || listed[key] {
			continue
		}
		convs = append(convs, mesh.ConversationSummary{Key: key})
	}
	for i := range convs {
		convs[i].Title = s.conversationTitle(id, convs[i].Key)
	}
	// Channels without history sort after every conversation that has some, in slot order.
	sort.SliceStable(convs, func(i, j int) bool { return convs[i].LastTime > convs[j].LastTime })
	writeJSON(w, http.StatusOK, convs)
}

func (s *Server) conversationTitle(id *mesh.Identity, key string) string {
	if strings.HasPrefix(key, "ch:") {
		idx, _ := strconv.Atoi(key[3:])
		if ch := id.ChannelCopy(idx); ch != nil {
			if n := ch.GetSettings().GetName(); n != "" {
				return n
			}
		}
		return s.radioOf(id).host.RadioParams().PresetName()
	}
	num, err := wire.ParseNodeID(strings.TrimPrefix(key, "dm:"))
	if err == nil {
		if e, ok := s.radioOf(id).host.DB.Get(num); ok && e.User != nil {
			return e.User.LongName
		}
	}
	return strings.TrimPrefix(key, "dm:")
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	writeJSON(w, http.StatusOK, s.hostFor(r).Messages.List(id.NodeNum, id.NodeID(), q.Get("conversation"), before, limit))
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	var req struct {
		To      string `json:"to"`
		Channel int    `json:"channel"`
		Text    string `json:"text"`
		WantAck *bool  `json:"want_ack"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	to := wire.Broadcast
	if req.To != "" {
		n, err := wire.ParseNodeID(req.To)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be a node id like !a1c40e07")
			return
		}
		to = n
	}
	wantAck := true
	if req.WantAck != nil {
		wantAck = *req.WantAck
	}
	pid, err := s.hostFor(r).SendText(id, to, req.Channel, req.Text, wantAck)
	if err != nil && pid == 0 {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, m := range s.hostFor(r).Messages.List(id.NodeNum, id.NodeID(), "", 0, 20) {
		if m.ID == pid && m.Direction == "out" {
			writeJSON(w, http.StatusAccepted, m)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": pid})
}

// ----------------------------------------------------------------------------------------- nodes

func nodeJSON(e mesh.NodeEntry, knownBy []string) map[string]any {
	n := map[string]any{"node_id": wire.NodeID(e.Num), "node_num": e.Num, "has_public_key": e.PublicKey() != nil,
		"snr": e.SNR, "rssi": e.RSSI, "via_mqtt": e.ViaMQTT, "local": e.Local, "favorite": e.Favorite, "ignored": e.Ignored}
	if e.User != nil {
		n["long_name"], n["short_name"], n["hw_model"], n["role"] = e.User.LongName, e.User.ShortName, e.User.HwModel.String(), e.User.Role.String()
		n["has_user"] = true
	} else {
		// Heard but no NodeInfo yet: the firmware's own placeholder names, so every
		// node always has the fields the GUI sorts and filters on.
		id := wire.NodeID(e.Num)
		short := id[len(id)-4:]
		n["long_name"], n["short_name"], n["hw_model"], n["role"] = "Meshtastic "+short, short,
			pb.HardwareModel_UNSET.String(), pb.Config_DeviceConfig_CLIENT.String()
		n["has_user"] = false
	}
	if !e.LastHeard.IsZero() {
		n["last_heard"] = e.LastHeard.UnixMilli()
	} else {
		n["last_heard"] = nil
	}
	if e.HopsAway >= 0 {
		n["hops_away"] = e.HopsAway
	} else {
		n["hops_away"] = nil
	}
	if e.NextHop != 0 {
		n["next_hop"] = e.NextHop
	} else {
		n["next_hop"] = nil
	}
	if p := e.Position; p != nil && (p.GetLatitudeI() != 0 || p.GetLongitudeI() != 0) {
		n["position"] = map[string]any{"lat": float64(p.GetLatitudeI()) / 1e7, "lon": float64(p.GetLongitudeI()) / 1e7,
			"alt": p.GetAltitude(), "time": int64(p.Time) * 1000}
	} else {
		n["position"] = nil
	}
	if m := e.Metrics; m != nil {
		n["telemetry"] = map[string]any{"battery": m.GetBatteryLevel(), "voltage": m.GetVoltage(),
			"channel_util": m.GetChannelUtilization(), "air_util_tx": m.GetAirUtilTx()}
	} else {
		n["telemetry"] = nil
	}
	if e.Local {
		n["known_by"] = []string{}
	} else {
		n["known_by"] = knownBy
	}
	return n
}

func (s *Server) localIDs(h *mesh.Host) []string {
	var ids []string
	for _, id := range h.Identities() {
		if id.Enabled {
			ids = append(ids, id.NodeID())
		}
	}
	return ids
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	known := s.localIDs(s.hostFor(r))
	for _, e := range s.hostFor(r).DB.Snapshot() {
		out = append(out, nodeJSON(e, known))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) fromIdentity(w http.ResponseWriter, r *http.Request) (*mesh.Identity, uint32, bool) {
	target, err := wire.ParseNodeID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad node id")
		return nil, 0, false
	}
	var req struct {
		From string `json:"from"`
	}
	if !readJSON(w, r, &req) {
		return nil, 0, false
	}
	var from *mesh.Identity
	if req.From == "" {
		from = s.hostFor(r).Relay()
	} else if n, err := wire.ParseNodeID(req.From); err == nil {
		from = s.hostFor(r).Identity(n)
	}
	if from == nil {
		writeError(w, http.StatusBadRequest, "from must be one of this host's identities")
		return nil, 0, false
	}
	return from, target, true
}

func (s *Server) traceroute(w http.ResponseWriter, r *http.Request) {
	from, target, ok := s.fromIdentity(w, r)
	if !ok {
		return
	}
	if err := s.hostFor(r).Traceroute(from, target); err != nil {
		writeError(w, http.StatusTooManyRequests, "traceroute not sent: "+err.Error())
		return
	}
	s.expectTraceroute(from.NodeID(), wire.NodeID(target))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

func (s *Server) requestNodeInfo(w http.ResponseWriter, r *http.Request) {
	from, target, ok := s.fromIdentity(w, r)
	if !ok {
		return
	}
	s.hostFor(r).RequestNodeInfo(from, target)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	num, err := wire.ParseNodeID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad node id")
		return
	}
	if s.hostFor(r).Identity(num) != nil {
		writeError(w, http.StatusConflict, "that node is one of this host's identities; delete it under Identities")
		return
	}
	s.hostFor(r).DB.Delete(num)
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------------------------------- packets

func (s *Server) listPackets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	node, port, kind := q.Get("node"), q.Get("port"), q.Get("kind")
	dir, channel, text := q.Get("direction"), q.Get("channel"), strings.ToLower(q.Get("q"))
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	writeJSON(w, http.StatusOK, s.hostFor(r).Packets.List(limit, before, func(p *mesh.PacketRecord) bool {
		switch {
		case node != "" && p.From != node && p.To != node:
			return false
		case port != "" && p.Port != port:
			return false
		case kind != "" && p.Kind != kind:
			return false
		case dir != "" && p.Direction != dir:
			return false
		case channel != "" && p.Channel != channel:
			return false
		case since > 0 && p.Time < since:
			return false
		case text != "" && !strings.Contains(strings.ToLower(p.Summary), text):
			return false
		}
		return true
	}))
}

func windowParam(r *http.Request) time.Duration {
	switch r.URL.Query().Get("window") {
	case "1h":
		return time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func (s *Server) statsAirtime(w http.ResponseWriter, r *http.Request) {
	buckets := []map[string]any{}
	for _, b := range s.hostFor(r).Air.Buckets(time.Now(), windowParam(r)) {
		by := map[string]float64{}
		for k, v := range b.ByIdentity {
			by[wire.NodeID(k)] = v
		}
		buckets = append(buckets, map[string]any{"time": b.Start.UnixMilli(), "tx_ms": b.TxMs, "rx_ms": b.RxMs,
			"relay_ms": b.RelayMs, "by_identity": by})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket_s": 600, "buckets": buckets})
}

func (s *Server) statsPorts(w http.ResponseWriter, r *http.Request) {
	cut := time.Now().Add(-windowParam(r)).UnixMilli()
	counts := map[string][2]int{}
	for _, p := range s.hostFor(r).Packets.List(5000, 0, func(p *mesh.PacketRecord) bool { return p.Time >= cut && p.Port != "" }) {
		c := counts[p.Port]
		if p.Direction == "tx" {
			c[1]++
		} else {
			c[0]++
		}
		counts[p.Port] = c
	}
	out := []map[string]any{}
	for port, c := range counts {
		out = append(out, map[string]any{"port": port, "rx": c[0], "tx": c[1]})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["rx"].(int)+out[i]["tx"].(int) > out[j]["rx"].(int)+out[j]["tx"].(int)
	})
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------------------- config

// applyConfig validates, applies live and saves a new config.
func (s *Server) applyConfig(r *http.Request, next *config.Config) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if err := s.hostFor(r).UpdateConfig(r.Context(), next.MeshConfig()); err != nil {
		return err
	}
	s.cfgMu.Lock()
	path := s.cfg.Path()
	identities := s.cfg.Identities
	*s.cfg = *next
	s.cfg.Identities = identities
	s.cfgMu.Unlock()
	if path != "" {
		if err := s.saveConfigFile(); err != nil {
			s.log.Warn("config applied but not saved", "path", path, "err", err)
		}
	}
	return nil
}

func (s *Server) saveConfigFile() error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.Save()
}

func (s *Server) serialPorts(w http.ResponseWriter, r *http.Request) {
	out := []map[string]string{}
	seen := map[string]bool{}
	byID, _ := filepath.Glob("/dev/serial/by-id/*")
	for _, p := range byID {
		target, _ := filepath.EvalSymlinks(p)
		seen[target] = true
		out = append(out, map[string]string{"path": p, "description": describePort(filepath.Base(p)), "device": target})
	}
	for _, pat := range []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/ttyAMA*", "/dev/cu.usb*"} {
		ms, _ := filepath.Glob(pat)
		for _, p := range ms {
			if !seen[p] {
				out = append(out, map[string]string{"path": p, "description": filepath.Base(p), "device": p})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func describePort(name string) string {
	name = strings.TrimPrefix(name, "usb-")
	if i := strings.LastIndex(name, "-if"); i > 0 {
		name = name[:i]
	}
	return strings.ReplaceAll(name, "_", " ")
}

func (s *Server) regions(w http.ResponseWriter, r *http.Request) {
	names := make([]string, 0, len(phy.Regions))
	for n := range phy.Regions {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []map[string]any{}
	for _, n := range names {
		reg := phy.Regions[n]
		var presets []string
		for p := range phy.Presets {
			if _, err := phy.Resolve(phy.Options{Region: n, Preset: p}); err == nil && presetAllowed(n, p) {
				presets = append(presets, p.String())
			}
		}
		sort.Slice(presets, func(i, j int) bool {
			return pb.Config_LoRaConfig_ModemPreset_value[presets[i]] < pb.Config_LoRaConfig_ModemPreset_value[presets[j]]
		})
		out = append(out, map[string]any{"name": n, "presets": presets, "duty_cycle_pct": reg.DutyCyclePct,
			"power_limit_dbm": reg.PowerLimitDBm, "start_mhz": reg.StartMHz, "end_mhz": reg.EndMHz})
	}
	writeJSON(w, http.StatusOK, out)
}

func presetAllowed(region string, p phy.Preset) bool {
	name := p.String()
	switch region {
	case "EU_866":
		return strings.HasPrefix(name, "LITE_")
	case "EU_N_868":
		return strings.HasPrefix(name, "NARROW_")
	case "EU_868":
		return !strings.HasSuffix(name, "_TURBO") && !strings.HasPrefix(name, "LITE_") && !strings.HasPrefix(name, "NARROW_") && !strings.HasPrefix(name, "TINY_")
	}
	return !strings.HasPrefix(name, "LITE_") && !strings.HasPrefix(name, "NARROW_") && !strings.HasPrefix(name, "TINY_")
}

func (s *Server) phyPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Region         string `json:"region"`
		Preset         string `json:"preset"`
		PrimaryChannel string `json:"primary_channel"`
		TxPowerDBm     int    `json:"tx_power_dbm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pv, ok := pb.Config_LoRaConfig_ModemPreset_value[strings.ToUpper(req.Preset)]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown preset")
		return
	}
	rp, err := phy.Resolve(phy.Options{Region: strings.ToUpper(req.Region), Preset: phy.Preset(pv), PrimaryChannelName: req.PrimaryChannel,
		TxPowerDBm: req.TxPowerDBm})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	primary := req.PrimaryChannel
	if primary == "" {
		primary = rp.PresetName()
	}
	writeJSON(w, http.StatusOK, phyJSON(rp, primary))
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, t := range s.auth.Tokens() {
		var last any
		if t.LastUsed > 0 {
			last = t.LastUsed
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "created_at": t.Created, "last_used": last})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "give the token a name, e.g. \"Home Assistant\"")
		return
	}
	t, secret, err := s.auth.CreateToken(req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": t.ID, "name": t.Name, "token": secret, "created_at": t.Created, "last_used": nil})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if !s.auth.DeleteToken(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "no such token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type backupFile struct {
	Format     string                `json:"format"`
	Created    int64                 `json:"created"`
	Version    string                `json:"version"`
	Config     string                `json:"config_yaml"`
	Identities []mesh.IdentityRecord `json:"identities"`
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.Lock()
	y, _ := yaml.Marshal(s.cfg)
	s.cfgMu.Unlock()
	b := backupFile{Format: "repeatertastic-backup-1", Created: time.Now().UnixMilli(), Version: s.opt.Version, Config: string(y)}
	for _, id := range s.hostFor(r).Identities() {
		b.Identities = append(b.Identities, id.Record())
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="repeatertastic-backup-%s.json"`, time.Now().Format("2006-01-02")))
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var b backupFile
	if !readJSON(w, r, &b) {
		return
	}
	if b.Format != "repeatertastic-backup-1" || len(b.Identities) == 0 {
		writeError(w, http.StatusBadRequest, "not a RepeaterTastic backup file")
		return
	}
	for _, rec := range b.Identities {
		if _, err := mesh.IdentityFromRecord(rec); err != nil {
			writeError(w, http.StatusBadRequest, "backup contains an invalid identity: "+err.Error())
			return
		}
	}
	data, _ := json.MarshalIndent(b.Identities, "", "  ")
	if err := os.WriteFile(filepath.Join(s.cfg.StateDir, "identities.json"), data, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b.Config != "" && s.cfg.Path() != "" {
		c := config.Default()
		if err := yaml.Unmarshal([]byte(b.Config), c); err == nil && c.Validate() == nil {
			_ = os.WriteFile(s.cfg.Path(), []byte(b.Config), 0o600)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"restart_required": true})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	writeJSON(w, http.StatusOK, s.opt.Logs.Recent(limit))
}

// ------------------------------------------------------------------------------------------ SSE

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
			var payload any = e.Data
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
			}
			if !send(e.Type, payload) {
				return
			}
		}
	}
}

// mapTileURL falls back to the public OSM tiles for configs written before
// web.map_tile_url existed.
func mapTileURL(u string) string {
	if u == "" {
		return config.DefaultMapTileURL
	}
	return u
}
