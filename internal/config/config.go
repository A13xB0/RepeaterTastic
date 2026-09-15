// Package config loads repeatertastic.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/pb"
	"github.com/A13xB0/RepeaterTastic/internal/phy"
)

type Config struct {
	Radio      Radio      `yaml:"radio" json:"radio"`
	Mesh       Mesh       `yaml:"mesh" json:"mesh"`
	Relay      Relay      `yaml:"relay" json:"relay"`
	Airtime    Airtime    `yaml:"airtime" json:"airtime"`
	Links      Links      `yaml:"links" json:"links"`
	Web        Web        `yaml:"web" json:"web"`
	MDNS       MDNS       `yaml:"mdns" json:"mdns"`
	StateDir   string     `yaml:"state_dir" json:"state_dir"`
	LogLevel   string     `yaml:"log_level" json:"log_level"`
	Identities []Identity `yaml:"identities" json:"identities"`
	Position   Position   `yaml:"position" json:"position"`

	// Radios are additional radios on the same site, each on its own preset. The
	// top-level radio/mesh/relay/airtime/links/identities above are the "main" radio.
	Radios []RadioInstance `yaml:"radios,omitempty" json:"radios,omitempty"`
	Site   Site            `yaml:"site,omitempty" json:"site,omitempty"`

	path string
}

// RadioInstance is one extra radio: its own modem, preset, relay persona and identities.
type RadioInstance struct {
	ID         string     `yaml:"id" json:"id"`     // short and stable: names the state dir and ?radio= in the API
	Name       string     `yaml:"name" json:"name"` // shown in the GUI
	Radio      Radio      `yaml:"radio" json:"radio"`
	Mesh       Mesh       `yaml:"mesh" json:"mesh"`
	Relay      Relay      `yaml:"relay" json:"relay"`
	Airtime    Airtime    `yaml:"airtime" json:"airtime"`
	Links      Links      `yaml:"links" json:"links"`
	Identities []Identity `yaml:"identities" json:"identities"`
	Position   Position   `yaml:"position" json:"position"`
}

// Position is a fixed site location broadcast by the relay persona (or every identity).
type Position struct {
	Latitude  float64 `yaml:"latitude" json:"latitude"`
	Longitude float64 `yaml:"longitude" json:"longitude"`
	Altitude  int     `yaml:"altitude" json:"altitude"` // metres above sea level
	// PrecisionBits keeps that many bits of latitude/longitude: 32 = exact, 16 ≈ 360 m, 13 ≈ 3 km. 0 = 32.
	PrecisionBits int           `yaml:"precision_bits" json:"precision_bits"`
	Interval      time.Duration `yaml:"interval" json:"interval"` // 0 = 3h, minimum 30m
	// Identities is "relay" (default) or "all".
	Identities string `yaml:"identities" json:"identities"`
}

// Site holds settings shared by every radio on the mast.
type Site struct {
	// DutyCyclePct caps the summed transmit airtime of all radios over the last hour,
	// on top of each radio's own limit. 0 = no site-wide cap.
	DutyCyclePct float64 `yaml:"duty_cycle_percent" json:"duty_cycle_percent"`
}

// MainRadioID is the ID of the radio described by the top-level config.
const MainRadioID = "main"

// RadioConfig is one radio's view of the configuration: shared settings from the
// file, per-radio sections from the radio's own block, and its own state dir.
type RadioConfig struct {
	ID   string
	Name string
	*Config
}

// RadioConfigs lists every radio, main first. Extra radios get their own state dir
// (state_dir/radios/<id>) so identities and history never mix.
func (c *Config) RadioConfigs() []RadioConfig {
	out := []RadioConfig{{ID: MainRadioID, Name: "Main", Config: c}}
	for _, ri := range c.Radios {
		v := *c
		v.Radio, v.Mesh, v.Relay, v.Airtime, v.Links, v.Identities = ri.Radio, ri.Mesh, ri.Relay, ri.Airtime, ri.Links, ri.Identities
		v.Position = ri.Position
		v.StateDir = filepath.Join(c.StateDir, "radios", ri.ID)
		v.Radios = nil
		name := ri.Name
		if name == "" {
			name = ri.ID
		}
		out = append(out, RadioConfig{ID: ri.ID, Name: name, Config: &v})
	}
	return out
}

// fillRadioDefaults gives extra radios the defaults a top-level radio would get, inheriting
// the region and NodeInfo interval from the main radio. Extra relays default to mute:
// a new radio on a mast shouldn't start repeating until someone decides it should.
func (c *Config) fillRadioDefaults() {
	d := Default()
	for i := range c.Radios {
		r := &c.Radios[i]
		if r.Radio.Driver == "" {
			r.Radio.Driver = d.Radio.Driver
		}
		if r.Radio.Baud == 0 {
			r.Radio.Baud = d.Radio.Baud
		}
		if r.Mesh.Region == "" {
			r.Mesh.Region = c.Mesh.Region
		}
		if r.Mesh.HopLimit == 0 {
			r.Mesh.HopLimit = d.Mesh.HopLimit
		}
		if r.Relay.Role == "" {
			r.Relay.Role = mesh.RoleMute
		}
		if r.Relay.LongName == "" {
			r.Relay.LongName = "RepeaterTastic " + r.ID + " Relay"
		}
		if r.Relay.ShortName == "" {
			r.Relay.ShortName = strings.ToUpper(r.ID)
			if len(r.Relay.ShortName) > 4 {
				r.Relay.ShortName = r.Relay.ShortName[:4]
			}
		}
		if r.Airtime.NodeInfoInterval == 0 {
			r.Airtime.NodeInfoInterval = c.Airtime.NodeInfoInterval
		}
	}
}

var radioIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

func (c *Config) validateRadios() error {
	seenID := map[string]bool{MainRadioID: true}
	seenDev := map[string]string{}
	seenPort := map[int]string{}
	for i, rc := range c.RadioConfigs() {
		if i > 0 {
			if !radioIDPattern.MatchString(rc.ID) {
				return fmt.Errorf("radios: id %q must be 1-24 lowercase letters, digits or dashes", rc.ID)
			}
			if seenID[rc.ID] {
				return fmt.Errorf("radios: id %q is used twice (%q is the top-level radio)", rc.ID, MainRadioID)
			}
			seenID[rc.ID] = true
			if err := rc.Config.validateOne(); err != nil {
				return fmt.Errorf("radios[%s]: %w", rc.ID, err)
			}
		}
		if rc.Radio.Driver == "kiss" && rc.Radio.Device != "" {
			if other, ok := seenDev[rc.Radio.Device]; ok {
				return fmt.Errorf("radios %s and %s both use %s", other, rc.ID, rc.Radio.Device)
			}
			seenDev[rc.Radio.Device] = rc.ID
		}
		for _, id := range rc.Identities {
			if id.APIPort <= 0 {
				continue
			}
			if other, ok := seenPort[id.APIPort]; ok {
				return fmt.Errorf("api_port %d is used by radios %s and %s", id.APIPort, other, rc.ID)
			}
			seenPort[id.APIPort] = rc.ID
		}
	}
	return nil
}

type Radio struct {
	Driver string `yaml:"driver" json:"driver"` // kiss | sim
	Device string `yaml:"device" json:"device"`
	Baud   int    `yaml:"baud" json:"baud"`
}

type Mesh struct {
	Region          string  `yaml:"region" json:"region"`
	Preset          string  `yaml:"preset" json:"preset"`
	PrimaryChannel  string  `yaml:"primary_channel" json:"primary_channel"`
	ChannelNum      int     `yaml:"channel_num" json:"channel_num"`
	OverrideFreqMHz float64 `yaml:"override_frequency_mhz" json:"override_frequency_mhz"`
	FreqOffsetMHz   float64 `yaml:"frequency_offset_mhz" json:"frequency_offset_mhz"`
	TxPowerDBm      int     `yaml:"tx_power_dbm" json:"tx_power_dbm"`
	HopLimit        uint32  `yaml:"hop_limit" json:"hop_limit"`
	// HwModel is the hardware identities advertise: "auto" or "" = the modem's board (Heltec V3 →
	// HELTEC_V3), or a Meshtastic HardwareModel name such as PORTDUINO or RAK4631.
	HwModel string `yaml:"hw_model" json:"hw_model"`
}

type Relay struct {
	Role      string `yaml:"role" json:"role"`
	LongName  string `yaml:"long_name" json:"long_name"`
	ShortName string `yaml:"short_name" json:"short_name"`
}

type Airtime struct {
	DutyCyclePct      float64       `yaml:"duty_cycle_percent" json:"duty_cycle_percent"`
	OverrideDutyCycle bool          `yaml:"override_duty_cycle" json:"override_duty_cycle"`
	NodeInfoInterval  time.Duration `yaml:"nodeinfo_interval" json:"nodeinfo_interval"`
	IdentitySharePct  float64       `yaml:"identity_share_percent" json:"identity_share_percent"`
}

type Links struct {
	LocalDMOverRF bool         `yaml:"local_dm_over_rf" json:"local_dm_over_rf"`
	UDPMulticast  UDPMulticast `yaml:"udp_multicast" json:"udp_multicast"`
	MQTT          MQTT         `yaml:"mqtt" json:"mqtt"`
}

// MQTT is the gateway link to a Meshtastic MQTT broker. Which channels go up and
// down is set per identity channel (uplink/downlink), as in the firmware.
type MQTT struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Address  string `yaml:"address" json:"address"` // host:port
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"-"`
	TLS      bool   `yaml:"tls" json:"tls"`
	// Root is the topic prefix; "" = msh/<region>, as the apps default it.
	Root string `yaml:"root" json:"root"`
	// OKToMQTT sets OK_TO_MQTT on our identities' packets: consent for other gateways to
	// uplink them (the firmware's lora.config_ok_to_mqtt). Applies with the link off too.
	OKToMQTT bool `yaml:"ok_to_mqtt" json:"ok_to_mqtt"`
	// RelayMQTT lets the relay persona rebroadcast packets that came from MQTT. Off by
	// default (the firmware's lora.ignore_mqtt): broker traffic never costs airtime.
	RelayMQTT bool `yaml:"relay_mqtt" json:"relay_mqtt"`
	// DownlinkPerMinute caps packets accepted from the broker (0 = 30).
	DownlinkPerMinute int       `yaml:"downlink_per_minute" json:"downlink_per_minute"`
	MapReport         MapReport `yaml:"map_report" json:"map_report"`
}

// MapReport periodically publishes the relay persona to the broker's map topic.
type MapReport struct {
	Enabled  bool          `yaml:"enabled" json:"enabled"`
	Interval time.Duration `yaml:"interval" json:"interval"` // 0 = 1h, minimum 15m
	// PositionPrecision is how many bits of latitude/longitude are kept (the firmware's
	// map_report_settings.position_precision): 14 ≈ 1.5 km, 16 ≈ 360 m. 0 = 14.
	PositionPrecision int     `yaml:"position_precision" json:"position_precision"`
	Latitude          float64 `yaml:"latitude" json:"latitude"`
	Longitude         float64 `yaml:"longitude" json:"longitude"`
	Altitude          int     `yaml:"altitude" json:"altitude"`
}

type UDPMulticast struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Group   string `yaml:"group" json:"group"`
}

// MDNS advertises each identity as _meshtastic._tcp so apps can discover it.
type MDNS struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

type Web struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Bind    string `yaml:"bind" json:"bind"`
	Port    int    `yaml:"port" json:"port"`
	// SessionTTL is how long a web login lasts.
	SessionTTL time.Duration `yaml:"session_ttl" json:"session_ttl"`
	// MapTileURL is the Leaflet tile template for the nodes map. The public OSM
	// servers' usage policy discourages heavy app use, so busy sites should
	// point this at their own or a commercial tile server.
	MapTileURL string `yaml:"map_tile_url" json:"map_tile_url"`
}

// DefaultMapTileURL is CARTO's Positron basemap (OpenStreetMap data). The OpenStreetMap tile
// servers refuse browsers on a LAN address: no Referer gets 403 and a private-IP Referer 400.
const DefaultMapTileURL = "https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png"

// LegacyMapTileURL was the default before; configs saved with it get the new default.
const LegacyMapTileURL = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"

// Identity seeds a virtual node on first start; afterwards identities live in the state dir.
type Identity struct {
	LongName  string `yaml:"long_name" json:"long_name"`
	ShortName string `yaml:"short_name" json:"short_name"`
	APIPort   int    `yaml:"api_port" json:"api_port"`
	APIBind   string `yaml:"api_bind" json:"api_bind"`
}

// Default returns a working EU_868 LongFast configuration.
func Default() *Config {
	return &Config{
		Radio:    Radio{Driver: "kiss", Device: "/dev/ttyUSB0", Baud: 115200},
		Mesh:     Mesh{Region: "EU_868", Preset: "LONG_FAST", HopLimit: 3, TxPowerDBm: 20},
		Relay:    Relay{Role: mesh.RoleClient, LongName: "RepeaterTastic Relay", ShortName: "RPTR"},
		Airtime:  Airtime{NodeInfoInterval: 3 * time.Hour, IdentitySharePct: 25},
		Links:    Links{UDPMulticast: UDPMulticast{Group: "239.0.0.69:4403"}},
		Web:      Web{Enabled: true, Bind: "0.0.0.0", Port: 8080, SessionTTL: 7 * 24 * time.Hour, MapTileURL: DefaultMapTileURL},
		MDNS:     MDNS{Enabled: true},
		StateDir: "/var/lib/repeatertastic",
		LogLevel: "info",
	}
}

// Load reads path over the defaults. A missing file yields defaults (and Path set, for saving).
func Load(path string) (*Config, error) {
	c := Default()
	c.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.fillRadioDefaults()
	c.path = path
	return c, c.Validate()
}

func (c *Config) Path() string { return c.path }

// Save writes the config back to its file.
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("config has no file path")
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) PresetValue() (phy.Preset, error) {
	v, ok := pb.Config_LoRaConfig_ModemPreset_value[strings.ToUpper(c.Mesh.Preset)]
	if !ok {
		return 0, fmt.Errorf("unknown preset %q", c.Mesh.Preset)
	}
	return phy.Preset(v), nil
}

func (c *Config) Validate() error {
	if err := c.validateOne(); err != nil {
		return err
	}
	if c.Site.DutyCyclePct < 0 || c.Site.DutyCyclePct > 100 {
		return fmt.Errorf("site.duty_cycle_percent must be between 0 and 100")
	}
	return c.validateRadios()
}

// validateOne checks one radio's sections.
func (c *Config) validateOne() error {
	if _, err := c.PresetValue(); err != nil {
		return err
	}
	if _, ok := phy.Regions[strings.ToUpper(c.Mesh.Region)]; !ok {
		return fmt.Errorf("unknown region %q", c.Mesh.Region)
	}
	switch c.Relay.Role {
	case mesh.RoleClient, mesh.RoleRouter, mesh.RoleMute:
	default:
		return fmt.Errorf("relay.role must be client, router or mute, not %q", c.Relay.Role)
	}
	switch c.Radio.Driver {
	case "kiss", "sim", "none":
	default:
		return fmt.Errorf("radio.driver must be kiss or none, not %q", c.Radio.Driver)
	}
	if name := strings.ToUpper(strings.TrimSpace(c.Mesh.HwModel)); name != "" && name != "AUTO" {
		if _, ok := pb.HardwareModel_value[name]; !ok {
			return fmt.Errorf("mesh.hw_model %q is not a Meshtastic hardware model (or auto)", c.Mesh.HwModel)
		}
	}
	if p := c.Position; p.Latitude < -90 || p.Latitude > 90 || p.Longitude < -180 || p.Longitude > 180 {
		return errors.New("position.latitude/longitude out of range")
	}
	if p := c.Position.PrecisionBits; p < 0 || p > 32 {
		return errors.New("position.precision_bits must be 0-32")
	}
	switch strings.ToLower(c.Position.Identities) {
	case "", "relay", "all":
	default:
		return errors.New(`position.identities must be "relay" or "all"`)
	}
	if m := c.Links.MQTT; m.Enabled {
		if m.Address == "" {
			return errors.New("links.mqtt.address is required when the MQTT link is enabled")
		}
		if m.MapReport.Enabled && m.MapReport.Latitude == 0 && m.MapReport.Longitude == 0 &&
			c.Position.Latitude == 0 && c.Position.Longitude == 0 {
			return errors.New("links.mqtt.map_report needs a position (its own latitude/longitude or the radio's position:)")
		}
		if p := m.MapReport.PositionPrecision; p < 0 || p > 32 {
			return errors.New("links.mqtt.map_report.position_precision must be 0-32")
		}
	}
	return nil
}

// MeshConfig converts to the host configuration.
func (c *Config) MeshConfig() mesh.Config {
	preset, _ := c.PresetValue()
	return mesh.Config{
		Region: strings.ToUpper(c.Mesh.Region), Preset: preset, PrimaryChannel: c.Mesh.PrimaryChannel,
		ChannelNum: c.Mesh.ChannelNum, OverrideFreqMHz: c.Mesh.OverrideFreqMHz, FreqOffsetMHz: c.Mesh.FreqOffsetMHz,
		TxPowerDBm: c.Mesh.TxPowerDBm, HopLimit: c.Mesh.HopLimit, RelayRole: c.Relay.Role,
		DutyCyclePct: c.Airtime.DutyCyclePct, OverrideDutyCycle: c.Airtime.OverrideDutyCycle,
		NodeInfoInterval: c.Airtime.NodeInfoInterval, LocalDMOverRF: c.Links.LocalDMOverRF, StateDir: c.StateDir,
		OKToMQTT: c.Links.MQTT.OKToMQTT, IgnoreMQTT: !c.Links.MQTT.RelayMQTT,
		HwModel: c.hwModel(),
		Position: mesh.FixedPosition{Latitude: c.Position.Latitude, Longitude: c.Position.Longitude,
			Altitude: int32(c.Position.Altitude), PrecisionBits: uint32(c.Position.PrecisionBits),
			Interval: c.Position.Interval, AllIdentities: strings.EqualFold(c.Position.Identities, "all")},
	}
}

// hwModel resolves mesh.hw_model; UNSET means "use the modem's board".
func (c *Config) hwModel() pb.HardwareModel {
	name := strings.ToUpper(strings.TrimSpace(c.Mesh.HwModel))
	if name == "" || name == "AUTO" {
		return pb.HardwareModel_UNSET
	}
	return pb.HardwareModel(pb.HardwareModel_value[name])
}

// MeshConfig is MeshConfig with this radio's ID set.
func (rc RadioConfig) MeshConfig() mesh.Config {
	m := rc.Config.MeshConfig()
	m.RadioID = rc.ID
	return m
}
