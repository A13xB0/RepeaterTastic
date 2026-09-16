// Identity endpoints: list, create, edit, move, delete, keys.

package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func (s *Server) restartAPI(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	if id.IsRelay {
		writeError(w, http.StatusConflict, "the relay persona has no client API")
		return
	}
	s.radioFor(r).api.Restart(r.Context(), id.NodeNum)
	w.WriteHeader(http.StatusNoContent)
}

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
		c := map[string]any{"index": i, "role": ch.Role.String(), "name": name, "display_name": dn,
			"psk": base64.StdEncoding.EncodeToString(st.GetPsk()), "hash": wire.ChannelHash(dn, key, st.GetUseAead()),
			"uplink": st.GetUplinkEnabled(), "downlink": st.GetDownlinkEnabled(), "locked": i == 0}
		chans = append(chans, c)
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
		"role": u.Role.String(), "hw_model": rc.host.Hardware().String(), "public_key": base64.StdEncoding.EncodeToString(id.PublicKey),
		"is_relay": id.IsRelay, "real_node": id.Remote() != nil, "hosted": id.Hosted(), "enabled": id.Enabled, "api": api, "outbox": id.BacklogLen(),
		"airtime_ms_1h": mine, "share_pct": share, "created_at": id.CreatedAt.UnixMilli(), "channels": chans,
		"last_byte": wire.LastByte(id.NodeNum), "share_limit_pct": s.shareLimit(id), "hop_limit": id.MaxHops(),
		"position": identityPositionJSON(id), "position_secs": id.PositionInterval(),
		"unread":   rc.host.Messages.UnreadTotal(id.NodeNum, id.NodeID()),
		"radio_id": rc.id, "radio_name": rc.name,
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
	for _, rc := range s.radiosFor(r) {
		for _, id := range rc.host.Identities() {
			out = append(out, s.identityJSON(id))
		}
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
		HopLimit   uint32  `json:"hop_limit"`
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
	// One key, one radio: the same node on two radios would answer twice and split its chats.
	if other := s.radioHolding(id.NodeNum); other != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("%s already exists on radio %s; move it there instead of importing it again", id.NodeID(), other.name))
		return
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
	if err := id.SetMaxHops(req.HopLimit); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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
	rec := id.Record()
	if !nodes.IdentityRoleAllowed(id.UserCopy().GetRole()) {
		writeError(w, http.StatusBadRequest, hostedRoleError)
		return
	}
	id, err = host.AddRecord(r.Context(), rec)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.saveIdentities()
	writeJSON(w, http.StatusCreated, s.identityJSON(id))
}

// hostedRoleError explains the roles an identity on meshtasticd can have.
const hostedRoleError = "an identity on meshtasticd never repeats: its role must be CLIENT_MUTE, TRACKER, SENSOR or TAK_TRACKER"

// radioHolding is the radio an identity with that node number is on, or nil.
func (s *Server) radioHolding(num uint32) *radioCtx {
	for _, rc := range s.radios {
		if rc.host.Identity(num) != nil {
			return rc
		}
	}
	return nil
}

// moveIdentity is POST /identities/{id}/move {"radio_id"}: take the identity off air on its radio
// and put it on another with the same key, node ID, channels, settings, app port and chats.
// The primary channel follows the new radio's preset (LongFast becomes MediumFast).
func (s *Server) moveIdentity(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	var req struct {
		RadioID string `json:"radio_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	from, to := s.radioFor(r), s.radioByID(req.RadioID)
	switch {
	case to == nil:
		writeError(w, http.StatusBadRequest, "no radio "+req.RadioID)
		return
	case to == from:
		writeJSON(w, http.StatusOK, s.identityJSON(id))
		return
	case id.IsRelay:
		writeError(w, http.StatusConflict, "a relay persona belongs to its radio and can't be moved")
		return
	}
	for _, other := range to.host.Identities() {
		if wire.LastByte(other.NodeNum) == wire.LastByte(id.NodeNum) {
			writeError(w, http.StatusConflict, fmt.Sprintf("%s shares its last byte with %s on %s, so they can't share a radio", id.NodeID(), other.NodeID(), to.name))
			return
		}
	}

	rec := id.Record()
	if !nodes.IdentityRoleAllowed(id.UserCopy().GetRole()) {
		writeError(w, http.StatusConflict, hostedRoleError+"; change its role before moving it to "+to.name)
		return
	}
	if err := from.host.DropIdentity(id.NodeNum); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if from.api != nil {
		from.api.Stop(id.NodeNum) // free the port before the other radio's manager binds it
	}
	dropped := from.host.DropOutgoing(id.NodeNum, "moved to "+to.name+" before it was sent")
	msgs, read := from.host.Messages.Take(id.NodeNum)
	moved, err := to.host.AddRecord(r.Context(), rec)
	if err != nil {
		_, _ = from.host.AddRecord(r.Context(), rec) // put it back as it was
		from.host.Messages.Put(id.NodeNum, msgs, read)
		if from.api != nil {
			from.api.SyncNow()
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	id = moved
	to.host.Messages.Put(id.NodeNum, msgs, read)
	if dropped > 0 {
		s.log.Info("unsent messages marked failed by the move", "identity", id.NodeID(), "count", dropped)
	}
	if to.api != nil {
		to.api.SyncNow()
	}
	s.saveIdentities()
	s.log.Info("identity moved", "identity", id.NodeID(), "from", from.id, "to", to.id)
	writeJSON(w, http.StatusOK, s.identityJSON(id))
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
		LongName  *string         `json:"long_name"`
		ShortName *string         `json:"short_name"`
		Enabled   *bool           `json:"enabled"`
		APIPort   *int            `json:"api_port"`
		APIBind   *string         `json:"api_bind"`
		Role      *string         `json:"role"`
		Share     *float64        `json:"share_limit_pct"`
		HopLimit  *uint32         `json:"hop_limit"`
		Position  json.RawMessage `json:"position"` // {"latitude","longitude","altitude"} or null to remove
		PosSecs   *uint32         `json:"position_secs"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// Check everything first, so a request is applied completely or not at all.
	bad := func(code int, msg string) { writeError(w, code, msg) }
	if req.Role != nil && !id.IsRelay {
		v, ok := pb.Config_DeviceConfig_Role_value[strings.ToUpper(*req.Role)]
		if !ok {
			bad(http.StatusBadRequest, fmt.Sprintf("unknown role %q", *req.Role))
			return
		}
		if id.Hosted() && !nodes.IdentityRoleAllowed(pb.Config_DeviceConfig_Role(v)) {
			bad(http.StatusBadRequest, hostedRoleError)
			return
		}
	}
	var pos *mesh.IdentityPosition
	if len(req.Position) > 0 && string(req.Position) != "null" {
		pos = &mesh.IdentityPosition{}
		if err := json.Unmarshal(req.Position, pos); err != nil {
			bad(http.StatusBadRequest, "position: "+err.Error())
			return
		}
		if pos.Latitude < -90 || pos.Latitude > 90 || pos.Longitude < -180 || pos.Longitude > 180 || (pos.Latitude == 0 && pos.Longitude == 0) {
			bad(http.StatusBadRequest, "position out of range")
			return
		}
	}
	if req.PosSecs != nil && *req.PosSecs != 0 && *req.PosSecs < 1800 {
		bad(http.StatusBadRequest, "position_secs must be 0 (the radio's) or at least 1800")
		return
	}
	if req.HopLimit != nil && *req.HopLimit > wire.HopMax {
		bad(http.StatusBadRequest, fmt.Sprintf("hop_limit must be 0-%d", wire.HopMax))
		return
	}
	if req.Share != nil && (*req.Share < 0 || *req.Share > 100) {
		bad(http.StatusBadRequest, "share_limit_pct must be between 0 and 100")
		return
	}
	var bind *string
	if req.APIBind != nil {
		b := strings.TrimSpace(*req.APIBind)
		if b != "" && net.ParseIP(b) == nil {
			bad(http.StatusBadRequest, "api_bind must be an IP address such as 127.0.0.1, or empty for every interface")
			return
		}
		bind = &b
	}
	if req.APIPort != nil && !id.IsRelay {
		if *req.APIPort < 1 || *req.APIPort > 65535 {
			bad(http.StatusBadRequest, "api_port must be 1-65535")
			return
		}
		want := id.APIBind
		if bind != nil {
			want = *bind
		}
		for _, orc := range s.radios { // one host, one port space, whatever the radio
			for _, other := range orc.host.Identities() {
				if other != id && other.APIPort == *req.APIPort && (other.APIBind == want || other.APIBind == "" || want == "") {
					bad(http.StatusConflict, fmt.Sprintf("port %d is already used by %s on %s", *req.APIPort, other.NodeID(), orc.name))
					return
				}
			}
		}
	}

	// Apply.
	if req.Role != nil && !id.IsRelay {
		_ = id.SetRole(*req.Role)
	}
	if len(req.Position) > 0 {
		_ = id.SetFixedPosition(pos)
		s.radioOf(id).host.RecordOwnPositions()
	}
	if req.PosSecs != nil {
		id.SetPositionInterval(*req.PosSecs)
	}
	if req.HopLimit != nil {
		_ = id.SetMaxHops(*req.HopLimit)
	}
	long, short := "", ""
	if req.LongName != nil {
		long = *req.LongName
	}
	if req.ShortName != nil {
		short = *req.ShortName
	}
	id.SetOwner(long, short)
	if (long != "" || short != "") && id.Remote() != nil {
		if err := pushOwner(r.Context(), id); err != nil {
			bad(http.StatusBadGateway, err.Error())
			return
		}
	}
	nodeSettings := req.Role != nil || req.Enabled != nil || req.HopLimit != nil || len(req.Position) > 0 || req.PosSecs != nil
	id.SetSettings(func(x *mesh.IdentitySettings) {
		if req.Enabled != nil && !id.IsRelay {
			x.Enabled = *req.Enabled
		}
		if req.APIPort != nil && !id.IsRelay {
			x.APIPort = *req.APIPort
		}
		if bind != nil {
			x.APIBind = *bind
		}
		if req.Share != nil {
			x.ShareLimitPct = *req.Share
		}
	})
	s.hostFor(r).DB.Update(id.NodeNum, func(e *mesh.NodeEntry) { e.User = id.UserCopy() })
	s.hostFor(r).ChannelsChanged()
	s.hostFor(r).Bus.Publish(mesh.Event{Type: "identity", Data: id.NodeID()})
	s.saveIdentities()
	if ca, ok := id.Remote().(mesh.ConfigApplier); ok && nodeSettings && id.Hosted() {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		err := ca.ApplyConfig(ctx, s.hostFor(r).Config())
		cancel()
		if err != nil {
			bad(http.StatusBadGateway, "saved here, but meshtasticd didn't take the settings: "+err.Error())
			return
		}
	}
	if (long != "" || short != "") && id.Remote() == nil { // a node announces its new name itself
		s.hostFor(r).RequestNodeInfo(id, wire.Broadcast)
	}
	writeJSON(w, http.StatusOK, s.identityJSON(id))
}

func (s *Server) deleteIdentity(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	if err := s.hostFor(r).DropIdentity(id.NodeNum); err != nil {
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
	if len(id.PrivateKey) != 32 {
		writeError(w, http.StatusNotFound, "this node keeps its own private key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"private_key": base64.StdEncoding.EncodeToString(id.PrivateKey),
		"public_key": base64.StdEncoding.EncodeToString(id.PublicKey)})
}

func identityPositionJSON(id *mesh.Identity) any {
	if p, ok := id.FixedPosition(); ok {
		return p
	}
	return nil
}
