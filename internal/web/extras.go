package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/radio/kiss"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
)

// ------------------------------------------------------------------------------ config (GUI shape)

// mqttDTO is one broker connection as the GUI edits it.
type mqttDTO struct {
	Key                string   `json:"key"` // read-only: the saved name, to match edits (and the kept password) to it
	Name               string   `json:"name"`
	Enabled            bool     `json:"enabled"`
	Address            string   `json:"address"`
	Username           string   `json:"username"`
	Password           string   `json:"password"`     // write-only: empty keeps the saved one
	PasswordSet        bool     `json:"password_set"` // read-only
	ClearPassword      bool     `json:"clear_password"`
	TLS                bool     `json:"tls"`
	Root               string   `json:"root"`
	Mode               string   `json:"mode"`
	Gateway            string   `json:"gateway"`
	Format             string   `json:"format"`
	UplinkChannels     []string `json:"uplink_channels"`
	DownlinkChannels   []string `json:"downlink_channels"`
	ChannelSelection   string   `json:"channel_selection"`
	IgnoreConsent      bool     `json:"ignore_consent"`
	OKToMQTT           bool     `json:"ok_to_mqtt"`
	RelayMQTT          bool     `json:"relay_mqtt"`
	RelayHops          int      `json:"relay_hops"`
	CrossLink          bool     `json:"cross_link"`
	BridgeAcknowledged bool     `json:"bridge_acknowledged"`
	DownlinkPerMinute  int      `json:"downlink_per_minute"`
	UplinkPerMinute    int      `json:"uplink_per_minute"`
	MapReport          struct {
		Enabled           bool    `json:"enabled"`
		Interval          string  `json:"interval"`
		PositionPrecision int     `json:"position_precision"`
		Latitude          float64 `json:"latitude"`
		Longitude         float64 `json:"longitude"`
	} `json:"map_report"`
}

func toMQTTDTO(m config.MQTT) mqttDTO {
	d := mqttDTO{Key: m.Name, Name: m.Name, Enabled: m.Enabled, Address: m.Address, Username: m.Username,
		PasswordSet: m.Password != "", TLS: m.TLS, Root: m.Root, Mode: m.ModeOrDefault(), Gateway: m.Gateway,
		Format: m.FormatOrDefault(), UplinkChannels: m.UplinkChannels, DownlinkChannels: m.DownlinkChannels,
		ChannelSelection: m.SelectionOrDefault(), IgnoreConsent: m.IgnoreConsent, OKToMQTT: m.OKToMQTT,
		RelayMQTT: m.RelayMQTT, RelayHops: m.RelayHops, CrossLink: m.CrossLink, BridgeAcknowledged: m.BridgeAcknowledged,
		DownlinkPerMinute: m.DownlinkPerMinute, UplinkPerMinute: m.UplinkPerMinute}
	if d.Gateway == "" {
		d.Gateway = "relay"
	}
	if d.UplinkChannels == nil {
		d.UplinkChannels = []string{}
	}
	if d.DownlinkChannels == nil {
		d.DownlinkChannels = []string{}
	}
	if d.DownlinkPerMinute == 0 {
		d.DownlinkPerMinute = 30
	}
	if d.UplinkPerMinute == 0 {
		d.UplinkPerMinute = 120
	}
	mr := m.MapReport
	d.MapReport.Enabled, d.MapReport.PositionPrecision = mr.Enabled, mr.PositionPrecision
	d.MapReport.Latitude, d.MapReport.Longitude = mr.Latitude, mr.Longitude
	if d.MapReport.PositionPrecision == 0 {
		d.MapReport.PositionPrecision = 14
	}
	d.MapReport.Interval = "1h"
	if mr.Interval > 0 {
		d.MapReport.Interval = shortDuration(mr.Interval)
	}
	return d
}

// fromMQTTDTOs turns the GUI's connection list back into config, keeping saved passwords
// for connections whose password field was left empty.
func fromMQTTDTOs(ds []mqttDTO, saved config.MQTTLinks) (config.MQTTLinks, error) {
	out := config.MQTTLinks{}
	for i, d := range ds {
		m := config.MQTT{Name: strings.TrimSpace(d.Name), Enabled: d.Enabled, Address: strings.TrimSpace(d.Address),
			Username: d.Username, TLS: d.TLS, Root: strings.Trim(strings.TrimSpace(d.Root), "/"), Mode: d.Mode,
			Gateway: strings.TrimSpace(d.Gateway), Format: d.Format, UplinkChannels: d.UplinkChannels,
			DownlinkChannels: d.DownlinkChannels, ChannelSelection: d.ChannelSelection, IgnoreConsent: d.IgnoreConsent,
			OKToMQTT: d.OKToMQTT, RelayMQTT: d.RelayMQTT, RelayHops: d.RelayHops, CrossLink: d.CrossLink,
			BridgeAcknowledged: d.BridgeAcknowledged, DownlinkPerMinute: d.DownlinkPerMinute, UplinkPerMinute: d.UplinkPerMinute}
		if m.Name == "" {
			return nil, fmt.Errorf("mqtt connection %d needs a name", i+1)
		}
		if m.Gateway == "relay" {
			m.Gateway = ""
		}
		if m.Mode == config.MQTTGateway {
			m.Mode = ""
		}
		if m.Format == "encrypted" || (m.Format == "json" && m.Mode == config.MQTTMonitor) {
			m.Format = ""
		}
		if m.Mode != config.MQTTBridge {
			m.BridgeAcknowledged = false
		}
		if len(m.UplinkChannels) == 0 {
			m.UplinkChannels = nil
		}
		if len(m.DownlinkChannels) == 0 {
			m.DownlinkChannels = nil
		}
		key := d.Key
		if key == "" {
			key = m.Name
		}
		switch {
		case d.Password != "":
			m.Password = d.Password
		case !d.ClearPassword:
			for _, old := range saved {
				if old.Name == key {
					m.Password = old.Password
				}
			}
		}
		m.MapReport.Enabled, m.MapReport.PositionPrecision = d.MapReport.Enabled, d.MapReport.PositionPrecision
		m.MapReport.Latitude, m.MapReport.Longitude = d.MapReport.Latitude, d.MapReport.Longitude
		iv, err := time.ParseDuration(d.MapReport.Interval)
		if err != nil || iv < 15*time.Minute {
			return nil, fmt.Errorf("mqtt %s: map_report.interval must be a duration of at least 15m, e.g. 1h", m.Name)
		}
		m.MapReport.Interval = iv
		out = append(out, m)
	}
	return out, nil
}

type configDTO struct {
	Radio struct {
		Type               string  `json:"type"`
		Port               string  `json:"port"`
		Region             string  `json:"region"`
		Preset             string  `json:"preset"`
		PrimaryChannel     string  `json:"primary_channel"`
		TxPowerDBm         int     `json:"tx_power_dbm"`
		FrequencyOffsetMHz float64 `json:"frequency_offset_mhz"`
	} `json:"radio"`
	Relay struct {
		Role      string `json:"role"`
		LongName  string `json:"long_name"`
		ShortName string `json:"short_name"`
		LocalDM   string `json:"local_dm"`
	} `json:"relay"`
	Airtime struct {
		DutyCyclePercent     float64 `json:"duty_cycle_percent"`
		IdentitySharePercent float64 `json:"identity_share_percent"`
		NodeInfoInterval     string  `json:"nodeinfo_interval"`
		Position             string  `json:"position"`
		Telemetry            string  `json:"telemetry"`
		CWMin                int     `json:"cw_min"`
		CWMax                int     `json:"cw_max"`
	} `json:"airtime"`
	Web struct {
		Bind       string `json:"bind"`
		Port       int    `json:"port"`
		SessionTTL string `json:"session_ttl"`
	} `json:"web"`
	Position struct {
		Latitude      float64 `json:"latitude"`
		Longitude     float64 `json:"longitude"`
		Altitude      int     `json:"altitude"`
		PrecisionBits int     `json:"precision_bits"`
		Interval      string  `json:"interval"`
		Identities    string  `json:"identities"`
	} `json:"position"`
	Hardware struct {
		HwModel   string `json:"hw_model"`  // "auto" or a Meshtastic HardwareModel name
		Effective string `json:"effective"` // what identities advertise right now (read-only)
		Modem     string `json:"modem"`     // the board name the modem reports (read-only)
	} `json:"hardware"`
	MQTT    []mqttDTO `json:"mqtt"`
	RadioID string    `json:"radio_id"` // read-only: which radio this is
	Main    bool      `json:"main"`     // read-only: web settings only exist on the main radio
}

func toDTO(c *config.Config, h *mesh.Host) configDTO {
	var d configDTO
	d.Radio.Type, d.Radio.Port = c.Radio.Driver, c.Radio.Device
	d.Radio.Region, d.Radio.Preset, d.Radio.PrimaryChannel = c.Mesh.Region, c.Mesh.Preset, c.Mesh.PrimaryChannel
	d.Radio.TxPowerDBm, d.Radio.FrequencyOffsetMHz = c.Mesh.TxPowerDBm, c.Mesh.FreqOffsetMHz
	d.Relay.Role, d.Relay.LongName, d.Relay.ShortName = c.Relay.Role, c.Relay.LongName, c.Relay.ShortName
	if r := h.Relay(); r != nil {
		u := r.UserCopy()
		d.Relay.LongName, d.Relay.ShortName = u.LongName, u.ShortName
	}
	d.Relay.LocalDM = "software"
	if c.Links.LocalDMOverRF {
		d.Relay.LocalDM = "also_rf"
	}
	d.Airtime.DutyCyclePercent = c.Airtime.DutyCyclePct
	if d.Airtime.DutyCyclePercent == 0 {
		d.Airtime.DutyCyclePercent = h.RadioParams().Region.DutyCyclePct
	}
	d.Airtime.IdentitySharePercent = c.Airtime.IdentitySharePct
	d.Airtime.NodeInfoInterval = shortDuration(c.Airtime.NodeInfoInterval)
	d.Airtime.Position, d.Airtime.Telemetry, d.Airtime.CWMin, d.Airtime.CWMax = "off", "off", 3, 8
	d.Web.Bind, d.Web.Port, d.Web.SessionTTL = c.Web.Bind, c.Web.Port, shortDuration(c.Web.SessionTTL)
	p := c.Position
	d.Position.Latitude, d.Position.Longitude, d.Position.Altitude = p.Latitude, p.Longitude, p.Altitude
	d.Position.PrecisionBits, d.Position.Identities = p.PrecisionBits, p.Identities
	if d.Position.PrecisionBits == 0 {
		d.Position.PrecisionBits = 32
	}
	if d.Position.Identities == "" {
		d.Position.Identities = "relay"
	}
	d.Position.Interval = "3h"
	if p.Interval > 0 {
		d.Position.Interval = shortDuration(p.Interval)
	}
	d.Hardware.HwModel = strings.ToUpper(strings.TrimSpace(c.Mesh.HwModel))
	if d.Hardware.HwModel == "" {
		d.Hardware.HwModel = "AUTO"
	}
	d.Hardware.Effective, d.Hardware.Modem = h.Hardware().String(), h.Radio().Info().Name
	d.MQTT = []mqttDTO{}
	for _, m := range c.Links.MQTT {
		d.MQTT = append(d.MQTT, toMQTTDTO(m))
	}
	return d
}

func shortDuration(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	s = strings.TrimSuffix(s, "0m")
	if s == "" {
		return "0s"
	}
	return s
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	rc := s.radioFor(r)
	s.cfgMu.Lock()
	d := toDTO(s.radioConfig(rc), rc.host)
	s.cfgMu.Unlock()
	d.RadioID, d.Main = rc.id, rc == s.radios[0]
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !readJSON(w, r, &raw) {
		return
	}
	rc := s.radioFor(r)
	main := rc == s.radios[0]
	s.cfgMu.Lock()
	base := s.radioConfig(rc)
	cur := toDTO(base, rc.host)
	next := *base
	old := *base
	s.cfgMu.Unlock()

	d := cur
	for section, body := range raw {
		var target any
		switch section {
		case "radio":
			target = &d.Radio
		case "relay":
			target = &d.Relay
		case "airtime":
			target = &d.Airtime
		case "web":
			if !main {
				writeError(w, http.StatusBadRequest, "web settings belong to the main radio")
				return
			}
			target = &d.Web
		case "position":
			target = &d.Position
		case "hardware":
			target = &d.Hardware
		case "mqtt":
			if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '{' { // one connection, as before multi-MQTT
				body = append(append([]byte{'['}, t...), ']')
			}
			d.MQTT = nil
			target = &d.MQTT
		default:
			writeError(w, http.StatusBadRequest, "unknown config section "+section)
			return
		}
		if err := json.Unmarshal(body, target); err != nil {
			writeError(w, http.StatusBadRequest, section+": "+err.Error())
			return
		}
	}
	next.Radio.Driver, next.Radio.Device = d.Radio.Type, d.Radio.Port
	next.Mesh.Region, next.Mesh.Preset, next.Mesh.PrimaryChannel = strings.ToUpper(d.Radio.Region), strings.ToUpper(d.Radio.Preset), d.Radio.PrimaryChannel
	next.Mesh.TxPowerDBm, next.Mesh.FreqOffsetMHz = d.Radio.TxPowerDBm, d.Radio.FrequencyOffsetMHz
	next.Relay.Role, next.Relay.LongName, next.Relay.ShortName = d.Relay.Role, d.Relay.LongName, d.Relay.ShortName
	next.Links.LocalDMOverRF = d.Relay.LocalDM == "also_rf"
	if d.Airtime.DutyCyclePercent != rc.host.RadioParams().Region.DutyCyclePct {
		next.Airtime.DutyCyclePct = d.Airtime.DutyCyclePercent
	}
	next.Airtime.IdentitySharePct = d.Airtime.IdentitySharePercent
	if iv, err := time.ParseDuration(d.Airtime.NodeInfoInterval); err == nil && iv >= 10*time.Minute {
		next.Airtime.NodeInfoInterval = iv
	} else if d.Airtime.NodeInfoInterval != cur.Airtime.NodeInfoInterval {
		writeError(w, http.StatusBadRequest, "nodeinfo_interval must be a duration of at least 10m, e.g. 3h")
		return
	}
	next.Web.Bind, next.Web.Port = d.Web.Bind, d.Web.Port
	if ttl, err := time.ParseDuration(d.Web.SessionTTL); err == nil && ttl >= time.Minute {
		next.Web.SessionTTL = ttl
	}

	next.Position = config.Position{Latitude: d.Position.Latitude, Longitude: d.Position.Longitude, Altitude: d.Position.Altitude,
		PrecisionBits: d.Position.PrecisionBits, Identities: strings.ToLower(d.Position.Identities)}
	if iv, err := time.ParseDuration(d.Position.Interval); err == nil && iv >= 30*time.Minute {
		next.Position.Interval = iv
	} else {
		writeError(w, http.StatusBadRequest, "position.interval must be a duration of at least 30m, e.g. 3h")
		return
	}
	next.Mesh.HwModel = strings.ToUpper(strings.TrimSpace(d.Hardware.HwModel))
	if next.Mesh.HwModel == "AUTO" {
		next.Mesh.HwModel = "auto"
	}

	mq, err := fromMQTTDTOs(d.MQTT, old.Links.MQTT)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	next.Links.MQTT = mq

	if main {
		err = s.applyConfig(r, &next)
	} else {
		err = s.applyRadioConfig(r, rc, &next)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if rel := rc.host.Relay(); rel != nil && (next.Relay.LongName != old.Relay.LongName || next.Relay.ShortName != old.Relay.ShortName) {
		rel.SetOwner(next.Relay.LongName, next.Relay.ShortName)
		s.saveIdentities()
	}
	restart := old.Radio != next.Radio || old.Web.Bind != next.Web.Bind || old.Web.Port != next.Web.Port ||
		!reflect.DeepEqual(old.Links.MQTT, next.Links.MQTT)
	s.cfgMu.Lock()
	out := toDTO(s.radioConfig(rc), rc.host)
	s.cfgMu.Unlock()
	out.RadioID, out.Main = rc.id, main
	writeJSON(w, http.StatusOK, map[string]any{"config": out, "restart_required": restart})
}

// ---------------------------------------------------------------------------------- setup probe

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	res := map[string]any{"ok": false, "driver": "kiss", "firmware": "", "name": "", "sync_word_ok": false, "error": ""}
	// The running modem already owns this port: report it rather than opening it twice.
	if req.Device == "" || req.Device == s.cfg.Radio.Device {
		info := s.hostFor(r).Radio().Info()
		st := s.radioStats(r.Context())
		res["driver"], res["firmware"], res["name"] = info.Driver, info.Firmware, info.Name
		res["ok"] = st.Connected
		res["sync_word_ok"] = s.hostFor(r).RadioConfigured()
		if !st.Connected {
			res["error"] = "the modem isn't answering on " + s.cfg.Radio.Device
		} else if !s.hostFor(r).RadioConfigured() {
			res["error"] = "the modem answers but rejected Meshtastic's sync word: flash the RepeaterTastic KISS firmware"
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	m, err := kiss.Open(ctx, kiss.Options{Device: req.Device, HandshakeTimeout: 5 * time.Second, Logf: func(string, ...any) {}})
	if err != nil {
		res["error"] = "no modem answered on " + req.Device + ": " + err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	defer m.Close()
	info := m.Info()
	res["ok"], res["firmware"], res["name"] = true, info.Firmware, info.Name
	res["sync_word_ok"] = m.Version() >= kiss.PatchedVersion
	if m.Version() < kiss.PatchedVersion {
		res["error"] = "stock MeshCore KISS firmware can't use Meshtastic's sync word: flash the RepeaterTastic build"
	}
	writeJSON(w, http.StatusOK, res)
}

// setupOrAuth lets the setup wizard use a handler before a password exists.
func (s *Server) setupOrAuth(h http.HandlerFunc) http.HandlerFunc {
	authed := s.requireAuth(h)
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth.SetupNeeded() {
			h(w, r)
			return
		}
		authed(w, r)
	}
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !s.auth.CheckPassword(req.Current) {
		writeError(w, http.StatusBadRequest, "the current password is wrong")
		return
	}
	if err := s.auth.SetPassword(req.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A new password signs out every session; this browser gets a fresh one to stay signed in.
	tok, exp := s.auth.IssueJWT()
	s.log.Info("admin password changed; other sessions signed out")
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires": exp.UnixMilli()})
}

// logoutAll is POST /api/v1/auth/logout-all: sign out every browser session, this one included.
func (s *Server) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.RevokeSessions(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("all web sessions signed out")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------------- identities

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

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	key, err := url.PathUnescape(r.PathValue("key"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad conversation key")
		return
	}
	s.hostFor(r).Messages.MarkRead(id.NodeNum, key)
	s.hostFor(r).Bus.Publish(mesh.Event{Type: "identity", Data: id.NodeID()})
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------------------------ statistics

type rfPoint struct {
	Time          int64   `json:"time"`
	NoiseFloorDBm float64 `json:"noise_floor_dbm"`
	ChannelUtil   float64 `json:"channel_util_pct"`
	Rx            uint64  `json:"rx"`
	Tx            uint64  `json:"tx"`
}

// rfHistory samples noise floor and channel utilisation every minute for a week.
type rfHistory struct {
	mu     sync.Mutex
	points []rfPoint
}

const rfKeep = 7 * 24 * 60

func (s *Server) sampleRF(ctx context.Context, rc *radioCtx) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	var lastRx, lastTx uint64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			st := rc.stats(ctx)
			rx, tx := rc.host.Counters.Rx.Load(), rc.host.Counters.Tx.Load()
			p := rfPoint{Time: now.Truncate(time.Minute).UnixMilli(), NoiseFloorDBm: float64(st.NoiseFloorDBm),
				ChannelUtil: rc.host.Air.ChannelUtilPercent(now), Rx: rx - lastRx, Tx: tx - lastTx}
			lastRx, lastTx = rx, tx
			rc.rf.mu.Lock()
			rc.rf.points = append(rc.rf.points, p)
			if len(rc.rf.points) > rfKeep {
				rc.rf.points = rc.rf.points[len(rc.rf.points)-rfKeep:]
			}
			rc.rf.mu.Unlock()
		}
	}
}

func bucketFor(window time.Duration) time.Duration {
	switch {
	case window <= time.Hour:
		return time.Minute
	case window <= 24*time.Hour:
		return 10 * time.Minute
	default:
		return time.Hour
	}
}

func (s *Server) statsRF(w http.ResponseWriter, r *http.Request) {
	window := windowParam(r)
	bucket := bucketFor(window)
	cut := time.Now().Add(-window).UnixMilli()
	rc := s.radioFor(r)
	rc.rf.mu.Lock()
	src := append([]rfPoint(nil), rc.rf.points...)
	rc.rf.mu.Unlock()
	type acc struct {
		p         rfPoint
		n, nNoise int
	}
	var order []int64
	agg := map[int64]*acc{}
	for _, p := range src {
		if p.Time < cut {
			continue
		}
		k := time.UnixMilli(p.Time).Truncate(bucket).UnixMilli()
		a, ok := agg[k]
		if !ok {
			a = &acc{p: rfPoint{Time: k}}
			agg[k] = a
			order = append(order, k)
		}
		a.n++
		a.p.ChannelUtil += p.ChannelUtil
		a.p.Rx += p.Rx
		a.p.Tx += p.Tx
		if p.NoiseFloorDBm != 0 {
			a.p.NoiseFloorDBm += p.NoiseFloorDBm
			a.nNoise++
		}
	}
	points := []rfPoint{}
	for _, k := range order {
		a := agg[k]
		a.p.ChannelUtil /= float64(a.n)
		if a.nNoise > 0 {
			a.p.NoiseFloorDBm /= float64(a.nNoise)
		}
		points = append(points, a.p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket_s": int(bucket.Seconds()), "points": points})
}

func (s *Server) statsIdentities(w http.ResponseWriter, r *http.Request) {
	window := windowParam(r)
	now := time.Now()
	cut := now.Add(-window).UnixMilli()
	airtime := map[string]float64{}
	var relayMs float64
	for _, b := range s.hostFor(r).Air.Buckets(now, window) {
		for k, v := range b.ByIdentity {
			airtime[wire.NodeID(k)] += v
		}
		relayMs += b.RelayMs
	}
	type stat struct {
		NodeID    string  `json:"node_id"`
		Tx        int     `json:"tx"`
		Rx        int     `json:"rx"`
		AckOK     int     `json:"ack_ok"`
		AckFail   int     `json:"ack_fail"`
		AirtimeMs float64 `json:"airtime_ms"`
	}
	stats := map[string]*stat{}
	var order []string
	for _, id := range s.hostFor(r).Identities() {
		st := &stat{NodeID: id.NodeID(), AirtimeMs: airtime[id.NodeID()]}
		if id.IsRelay {
			st.AirtimeMs += relayMs
		}
		stats[id.NodeID()] = st
		order = append(order, id.NodeID())
		for _, m := range s.hostFor(r).Messages.Window(id.NodeNum, cut) {
			switch m.Status {
			case "acked":
				st.AckOK++
			case "failed":
				st.AckFail++
			}
		}
	}
	relayID := ""
	if rel := s.hostFor(r).Relay(); rel != nil {
		relayID = rel.NodeID()
	}
	for _, p := range s.hostFor(r).Packets.List(5000, 0, func(p *mesh.PacketRecord) bool { return p.Time >= cut }) {
		switch {
		case p.Direction == "tx" && p.Kind == "ours":
			if st := stats[p.From]; st != nil {
				st.Tx++
			}
		case p.Direction == "tx" && p.Kind == "relayed":
			if st := stats[relayID]; st != nil {
				st.Tx++
			}
		case p.Direction == "rx" && p.Kind == "delivered":
			if st := stats[p.To]; st != nil {
				st.Rx++
			} else if p.To == "!ffffffff" {
				for _, st := range stats {
					st.Rx++
				}
			}
		}
	}
	out := make([]*stat, 0, len(order))
	for _, k := range order {
		out = append(out, stats[k])
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------- traceroute timeouts

type traceWait struct {
	mu      sync.Mutex
	pending map[string]time.Time // identity|target → deadline
}

func (s *Server) watchTraceroutes(ctx context.Context, rc *radioCtx) {
	events, unsub := rc.host.Bus.Subscribe(64)
	defer unsub()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			if tr, ok := e.Data.(mesh.TracerouteResult); ok && e.Type == "traceroute" {
				s.traces.mu.Lock()
				delete(s.traces.pending, tr.Identity+"|"+tr.Target)
				s.traces.mu.Unlock()
			}
		case now := <-t.C:
			s.traces.mu.Lock()
			for k, deadline := range s.traces.pending {
				if now.After(deadline) {
					parts := strings.SplitN(k, "|", 2)
					if num, err := wire.ParseNodeID(parts[0]); err != nil || rc.host.Identity(num) == nil {
						continue // another radio's traceroute
					}
					delete(s.traces.pending, k)
					rc.host.Bus.Publish(mesh.Event{Type: "traceroute", Data: map[string]any{
						"identity": parts[0], "target": parts[1], "route": []string{}, "snr_towards": []float64{},
						"route_back": []string{}, "snr_back": []float64{}, "error": "no response within 60 s"}})
				}
			}
			s.traces.mu.Unlock()
		}
	}
}

func (s *Server) expectTraceroute(from, target string) {
	s.traces.mu.Lock()
	s.traces.pending[from+"|"+target] = time.Now().Add(60 * time.Second)
	s.traces.mu.Unlock()
}

// -------------------------------------------------------------------------------------- links

func (s *Server) linkJSON() map[string]any { return s.udpLinkJSON(s.radios[0]) }

func (s *Server) udpLinkJSON(rc *radioCtx) map[string]any {
	u := map[string]any{"name": "udp", "type": "udp_multicast", "enabled": s.radioConfig(rc).Links.UDPMulticast.Enabled,
		"connected": false, "rx": 0, "tx": 0, "detail": "239.0.0.69:4403 + 224.0.0.69:4403"}
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
	rc := s.radioFor(r)
	writeJSON(w, http.StatusOK, append([]any{s.udpLinkJSON(rc)}, s.mqttLinksJSON(rc)...))
}

func (s *Server) patchLink(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("name") != "udp" {
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Enabled != nil {
		s.cfgMu.Lock()
		s.cfg.Links.UDPMulticast.Enabled = *req.Enabled
		s.cfgMu.Unlock()
		if err := s.saveConfigFile(); err != nil {
			s.log.Warn("saving config", "err", err)
		}
	}
	out := s.linkJSON()
	running := s.radioFor(r).udp != nil
	out["restart_required"] = running != s.cfg.Links.UDPMulticast.Enabled
	writeJSON(w, http.StatusOK, out)
}
