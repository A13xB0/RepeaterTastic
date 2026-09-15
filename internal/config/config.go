// Package config loads repeatertastic.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
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
	StateDir   string     `yaml:"state_dir" json:"state_dir"`
	LogLevel   string     `yaml:"log_level" json:"log_level"`
	Identities []Identity `yaml:"identities" json:"identities"`

	path string
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
}

type Links struct {
	LocalDMOverRF bool         `yaml:"local_dm_over_rf" json:"local_dm_over_rf"`
	UDPMulticast  UDPMulticast `yaml:"udp_multicast" json:"udp_multicast"`
}

type UDPMulticast struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Group   string `yaml:"group" json:"group"`
}

type Web struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Bind    string `yaml:"bind" json:"bind"`
	Port    int    `yaml:"port" json:"port"`
}

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
		Mesh:     Mesh{Region: "EU_868", Preset: "LONG_FAST", HopLimit: 3},
		Relay:    Relay{Role: mesh.RoleClient, LongName: "RepeaterTastic Relay", ShortName: "RPTR"},
		Airtime:  Airtime{NodeInfoInterval: 3 * time.Hour},
		Links:    Links{UDPMulticast: UDPMulticast{Group: "239.0.0.69:4403"}},
		Web:      Web{Enabled: true, Bind: "0.0.0.0", Port: 8080},
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
	case "kiss", "sim":
	default:
		return fmt.Errorf("radio.driver must be kiss or sim, not %q", c.Radio.Driver)
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
	}
}
