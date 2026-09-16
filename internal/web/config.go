// Configuration: the GUI's config view, PHY preview and map tiles.

package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

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
		Baud               int     `json:"baud"`
		HopLimit           uint32  `json:"hop_limit"`
		ChannelNum         int     `json:"channel_num"`            // 0 = from the primary channel name
		OverrideFreqMHz    float64 `json:"override_frequency_mhz"` // 0 = from the region and preset
	} `json:"radio"`
	Relay struct {
		Role        string   `json:"role"`
		Rebroadcast string   `json:"rebroadcast"`
		Favorites   []string `json:"favorites"`
		LongName    string   `json:"long_name"`
		ShortName   string   `json:"short_name"`
		LocalDM     string   `json:"local_dm"`
	} `json:"relay"`
	Airtime struct {
		DutyCyclePercent     float64 `json:"duty_cycle_percent"`
		IdentitySharePercent float64 `json:"identity_share_percent"`
		NodeInfoInterval     string  `json:"nodeinfo_interval"`
		TelemetryInterval    string  `json:"telemetry_interval"` // "off" or a duration of at least 30m
		OverrideDutyCycle    bool    `json:"override_duty_cycle"`
		CWMin                int     `json:"cw_min"` // read-only: the firmware's contention window
		CWMax                int     `json:"cw_max"`
	} `json:"airtime"`
	Web struct {
		Bind         string `json:"bind"`
		Port         int    `json:"port"`
		SessionTTL   string `json:"session_ttl"`
		MapTileURL   string `json:"map_tile_url"`   // "" = the built-in default
		MapKeySource string `json:"map_key_source"` // read-only: built in, environment or none
		MDNS         bool   `json:"mdns"`
		LogLevel     string `json:"log_level"`
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
	d.Radio.Baud, d.Radio.HopLimit, d.Radio.ChannelNum, d.Radio.OverrideFreqMHz = c.Radio.Baud, c.Mesh.HopLimit, c.Mesh.ChannelNum, c.Mesh.OverrideFreqMHz
	if d.Radio.HopLimit == 0 {
		d.Radio.HopLimit = 3
	}
	d.Relay.Rebroadcast = c.Relay.Rebroadcast
	d.Relay.Favorites = append([]string{}, c.Relay.Favorites...)
	if d.Relay.Rebroadcast == "" {
		d.Relay.Rebroadcast = "all"
	}
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
	d.Airtime.TelemetryInterval, d.Airtime.OverrideDutyCycle = "off", c.Airtime.OverrideDutyCycle
	if c.Airtime.TelemetryInterval > 0 {
		d.Airtime.TelemetryInterval = shortDuration(c.Airtime.TelemetryInterval)
	}
	d.Airtime.CWMin, d.Airtime.CWMax = phy.CWMin, phy.CWMax
	d.Web.Bind, d.Web.Port, d.Web.SessionTTL = c.Web.Bind, c.Web.Port, shortDuration(c.Web.SessionTTL)
	d.Web.MapTileURL, d.Web.MDNS, d.Web.LogLevel = c.Web.MapTileURL, c.MDNS.Enabled, strings.ToLower(c.LogLevel)
	if d.Web.LogLevel == "" {
		d.Web.LogLevel = "info"
	}
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

// shortDuration writes a duration the way people type it: 30m, 1h30m, 3h, 90s.
func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	sec := int((d % time.Minute) / time.Second)
	out := ""
	if h > 0 {
		out += strconv.Itoa(h) + "h"
	}
	if m > 0 {
		out += strconv.Itoa(m) + "m"
	}
	if sec > 0 || out == "" {
		out += strconv.Itoa(sec) + "s"
	}
	return out
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	rc := s.radioFor(r)
	s.cfgMu.Lock()
	d := toDTO(s.radioConfig(rc), rc.host)
	s.cfgMu.Unlock()
	d.RadioID, d.Main = rc.id, rc == s.radios[0]
	d.Web.MapKeySource = s.mapKeySource()
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
	if d.Radio.Baud > 0 {
		next.Radio.Baud = d.Radio.Baud
	}
	if d.Radio.HopLimit < 1 || d.Radio.HopLimit > 7 {
		writeError(w, http.StatusBadRequest, "radio.hop_limit must be 1-7")
		return
	}
	next.Mesh.HopLimit, next.Mesh.ChannelNum, next.Mesh.OverrideFreqMHz = d.Radio.HopLimit, d.Radio.ChannelNum, d.Radio.OverrideFreqMHz
	next.Airtime.OverrideDutyCycle = d.Airtime.OverrideDutyCycle
	switch d.Airtime.TelemetryInterval {
	case "", "off", "0", "0s":
		next.Airtime.TelemetryInterval = 0
	default:
		iv, err := time.ParseDuration(d.Airtime.TelemetryInterval)
		if err != nil || iv < 30*time.Minute {
			writeError(w, http.StatusBadRequest, "airtime.telemetry_interval must be off or a duration of at least 30m, e.g. 3h")
			return
		}
		next.Airtime.TelemetryInterval = iv
	}
	next.Relay.Rebroadcast = strings.ToLower(d.Relay.Rebroadcast)
	next.Relay.Favorites = nil
	for _, f := range d.Relay.Favorites {
		if n, err := wire.ParseNodeID(f); err == nil {
			next.Relay.Favorites = append(next.Relay.Favorites, wire.NodeID(n))
		} else {
			next.Relay.Favorites = append(next.Relay.Favorites, f) // Validate says what's wrong
		}
	}
	if next.Relay.Rebroadcast == "all" {
		next.Relay.Rebroadcast = "" // the default: keep it out of the file
	}
	next.Relay.Role, next.Relay.LongName, next.Relay.ShortName = mesh.NormalizeRelayRole(d.Relay.Role), d.Relay.LongName, d.Relay.ShortName
	next.Links.LocalDMOverRF = d.Relay.LocalDM == "also_rf"
	next.Airtime.DutyCyclePct = d.Airtime.DutyCyclePercent
	if d.Airtime.DutyCyclePercent == rc.host.RadioParams().Region.DutyCyclePct {
		next.Airtime.DutyCyclePct = 0 // the region's default, so it follows the region
	}
	next.Airtime.IdentitySharePct = d.Airtime.IdentitySharePercent
	if iv, err := time.ParseDuration(d.Airtime.NodeInfoInterval); err == nil && iv >= 10*time.Minute {
		next.Airtime.NodeInfoInterval = iv
	} else if d.Airtime.NodeInfoInterval != cur.Airtime.NodeInfoInterval {
		writeError(w, http.StatusBadRequest, "nodeinfo_interval must be a duration of at least 10m, e.g. 3h")
		return
	}
	next.Web.Bind, next.Web.Port = d.Web.Bind, d.Web.Port
	if main {
		next.Web.MapTileURL, next.MDNS.Enabled, next.LogLevel = strings.TrimSpace(d.Web.MapTileURL), d.Web.MDNS, strings.ToLower(d.Web.LogLevel)
		if next.LogLevel == "info" && old.LogLevel == "" {
			next.LogLevel = ""
		}
	}
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
	if main && s.opt.LogLevel != nil && next.LogLevel != old.LogLevel {
		var l slog.Level
		if l.UnmarshalText([]byte(strings.ToUpper(next.LogLevel))) == nil || next.LogLevel == "" {
			s.opt.LogLevel.Set(l) // "" leaves l at info
		}
	}
	restart := len(s.restartReasons()) > 0
	s.cfgMu.Lock()
	out := toDTO(s.radioConfig(rc), rc.host)
	s.cfgMu.Unlock()
	out.RadioID, out.Main = rc.id, main
	out.Web.MapKeySource = s.mapKeySource()
	writeJSON(w, http.StatusOK, map[string]any{"config": out, "restart_required": restart})
}

func (s *Server) mapKeySource() string {
	if s.opt.MapKeySource == "" {
		return "none"
	}
	return s.opt.MapKeySource
}

// applyConfig validates, applies live and saves a new config.
func (s *Server) applyConfig(r *http.Request, next *config.Config) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if err := s.radios[0].host.UpdateConfig(r.Context(), next.MeshConfig()); err != nil {
		return err
	}
	s.cfgMu.Lock()
	path := s.cfg.Path()
	// Sections with their own endpoints may have changed since next was copied: keep the current ones.
	next.Identities, next.Radios, next.Site = s.cfg.Identities, s.cfg.Radios, s.cfg.Site
	*s.cfg = *next
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

// mapTileURL falls back to the public OSM tiles for configs written before
// web.map_tile_url existed.
func mapTileURL(u string) string {
	if u == "" || slices.Contains(config.LegacyMapTileURLs, u) {
		return config.DefaultMapTileURL
	}
	return u
}

// withMapKey fills {api_key} in a tile URL. Without a key the query parameter holding it
// (?key={api_key}, &api_key={api_key}, ...) is dropped, so a keyless provider URL still works.
func withMapKey(u, key string) string {
	if !strings.Contains(u, "{api_key}") {
		return u
	}
	if key != "" {
		return strings.ReplaceAll(u, "{api_key}", url.QueryEscape(key))
	}
	u = mapKeyParam.ReplaceAllStringFunc(u, func(m string) string {
		if m[0] == '?' && strings.HasSuffix(m, "&") {
			return "?"
		}
		return ""
	})
	return strings.ReplaceAll(u, "{api_key}", "")
}

var mapKeyParam = regexp.MustCompile(`\?[A-Za-z0-9_]+=\{api_key\}&|[?&][A-Za-z0-9_]+=\{api_key\}`)
