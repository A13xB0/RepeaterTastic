package spi

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Chip families, by meshtasticd's Lora.Module names.
const (
	ModuleSX1262 = "sx1262"
	ModuleSX1268 = "sx1268"
	ModuleLLCC68 = "llcc68"
	ModuleRF95   = "rf95" // SX1276/SX1278 (RFM95/96/98)
	ModuleSX1280 = "sx1280"
	ModuleLR1110 = "lr1110"
	ModuleLR1120 = "lr1120"
	ModuleLR1121 = "lr1121"
)

// errAutoModule is a config with Lora.Module: auto, which needs board detection.
var errAutoModule = errors.New("Lora.Module is auto")

// Board is a LoRa radio wired to a Linux board, described the way meshtasticd describes it in
// /etc/meshtasticd/config.d/lora-*.yaml.
type Board struct {
	Name string // Meta.name
	// Hosts is Meta.compatible: the computers this file is for (raspberry-pi, luckfox-lyra-zero-w,
	// usb…). The same HAT has one file per host, so this is what tells them apart.
	Hosts    []string
	Module   string  // one of the Module* constants
	SPIDev   string  // spidev0.0 (the /dev/ prefix is optional); unused for USB
	SPISpeed uint32  // Hz, default 2 MHz
	USB      *USBID  // spidev: ch341, a CH341 USB-to-SPI adapter
	CS       Pin     // not connected = the spidev's own chip select
	IRQ      Pin     // DIO1 (DIO0 on RF95, DIO9 on LR11x0); not connected = poll the chip
	Busy     Pin     // not connected = fixed delays (RF95 has no BUSY line)
	Reset    Pin     // not connected = no hardware reset
	TXen     Pin     // optional external PA/LNA switch
	RXen     Pin     // optional
	DIO2RF   bool    // DIO2_AS_RF_SWITCH (SX126x)
	TCXOVolt float64 // DIO3_TCXO_VOLTAGE (0 = crystal, no TCXO)
	// MaxPower is the chip's power limit in dBm from SX126X_MAX_POWER, RF95_MAX_POWER,
	// SX128X_MAX_POWER or LR1110_MAX_POWER, with meshtasticd's defaults. MaxPowerHF is
	// LR1120_MAX_POWER, the LR112x limit at 2.4 GHz.
	MaxPower   int
	MaxPowerHF int
	// TxGain is TX_GAIN_LORA: an external PA's gain in dB, one value or one per chip dBm step from
	// 0. Requested power is antenna power, so the chip is set that much lower.
	TxGain []int
	// High are held high while the radio is open: Enable_Pins (e.g. RAK6421 slot power) and
	// SX126X_ANT_SW.
	High []Pin
	// RFSwitch is an LR11x0 rfswitch_table: the chip drives its own DIO5–DIO10 as RF switches.
	RFSwitch *RFSwitchTable
}

// USBID picks a CH341 adapter.
type USBID struct {
	VID, PID uint16
	Serial   string // USB_Serialnum; empty = the first adapter found
}

// RFSwitchTable is meshtasticd's rfswitch_table: which DIOs switch the RF path, and their level in
// each chip mode.
type RFSwitchTable struct {
	Pins  []string          // DIO5, DIO6, DIO7, DIO8, DIO10
	Modes map[string][]bool // MODE_STBY, MODE_RX, MODE_TX, MODE_TX_HP, MODE_TX_HF, MODE_GNSS, MODE_WIFI
}

// Pin is a GPIO line on a gpiochip, or a CH341 pin (D0–D7) on USB boards. meshtasticd writes a
// number (a line on the default chip) or {pin, gpiochip, line}.
type Pin struct {
	Chip int
	Line int
	Set  bool
}

func (p Pin) String() string {
	if !p.Set {
		return "not connected"
	}
	return fmt.Sprintf("gpiochip%d line %d", p.Chip, p.Line)
}

// LoadBoard reads a meshtasticd board file (or a whole config.yaml with a Lora section).
func LoadBoard(path string) (Board, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Board{}, err
	}
	board, err := ParseBoard(b)
	if err != nil {
		return Board{}, fmt.Errorf("%s: %w", path, err)
	}
	return board, nil
}

// ParseBoard parses meshtasticd YAML.
func ParseBoard(data []byte) (Board, error) {
	// Walk the node tree rather than decoding into maps: like meshtasticd's yaml-cpp, the first of
	// a repeated key wins (lora-lyra-picocalc-wio-sx1262.yaml repeats gpiochip).
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Board{}, err
	}
	var doc struct {
		Meta map[string]yaml.Node
		Lora map[string]yaml.Node
	}
	section := func(top *yaml.Node, name string) map[string]yaml.Node {
		for i := 0; i+1 < len(top.Content); i += 2 {
			if top.Content[i].Value != name || top.Content[i+1].Kind != yaml.MappingNode {
				continue
			}
			m := map[string]yaml.Node{}
			body := top.Content[i+1].Content
			for j := 0; j+1 < len(body); j += 2 {
				if _, seen := m[body[j].Value]; !seen {
					m[body[j].Value] = *body[j+1]
				}
			}
			return m
		}
		return nil
	}
	if len(root.Content) == 1 && root.Content[0].Kind == yaml.MappingNode {
		doc.Meta = section(root.Content[0], "Meta")
		doc.Lora = section(root.Content[0], "Lora")
	}
	if doc.Lora == nil {
		return Board{}, errors.New("no Lora section")
	}
	b := Board{SPIDev: "spidev0.0", SPISpeed: 2_000_000}
	if n, ok := doc.Meta["name"]; ok {
		b.Name = n.Value
	}
	if n, ok := doc.Meta["compatible"]; ok && n.Kind == yaml.SequenceNode {
		for _, item := range n.Content {
			if v := strings.TrimSpace(item.Value); v != "" {
				b.Hosts = append(b.Hosts, v)
			}
		}
	}
	str := func(key string) string {
		n, ok := doc.Lora[key]
		if !ok {
			return ""
		}
		return strings.TrimSpace(n.Value)
	}
	integer := func(key string, def int) (int, error) {
		v := str(key)
		if v == "" {
			return def, nil
		}
		n, err := strconv.ParseInt(v, 0, 32) // USB_PID: 0x5512
		if err != nil {
			return 0, fmt.Errorf("%s: %q isn't a number", key, v)
		}
		return int(n), nil
	}

	b.Module = strings.ToLower(str("Module"))
	switch b.Module {
	case ModuleSX1262, ModuleSX1268, ModuleLLCC68, ModuleRF95, ModuleSX1280, ModuleLR1110, ModuleLR1120, ModuleLR1121:
	case "", "auto":
		return Board{}, errAutoModule
	case "sim":
		return Board{}, errors.New("Lora.Module sim is meshtasticd's simulated radio, not hardware")
	default:
		return Board{}, fmt.Errorf("Lora.Module %s isn't supported (sx1262, sx1268, llcc68, RF95, sx1280, lr1110, lr1120, lr1121)", b.Module)
	}

	if v := str("spidev"); v == "ch341" {
		vid, err := integer("USB_VID", 0x1A86)
		if err != nil {
			return Board{}, err
		}
		pid, err := integer("USB_PID", 0x5512)
		if err != nil {
			return Board{}, err
		}
		b.USB = &USBID{VID: uint16(vid), PID: uint16(pid), Serial: str("USB_Serialnum")}
		b.SPIDev = ""
	} else if v != "" {
		b.SPIDev = v
	}
	speed, err := integer("spiSpeed", int(b.SPISpeed))
	if err != nil {
		return Board{}, err
	}
	b.SPISpeed = uint32(speed)
	defaultChip, err := integer("gpiochip", 0)
	if err != nil {
		return Board{}, err
	}

	nodePin := func(key string, n yaml.Node) (Pin, error) {
		switch n.Kind {
		case yaml.ScalarNode:
			if n.Value == "" || strings.EqualFold(n.Value, "RADIOLIB_NC") {
				return Pin{}, nil
			}
			line, err := strconv.Atoi(n.Value)
			if err != nil {
				return Pin{}, fmt.Errorf("%s: %q isn't a pin number", key, n.Value)
			}
			if line < 0 {
				return Pin{}, nil
			}
			return Pin{Chip: defaultChip, Line: line, Set: true}, nil
		case yaml.MappingNode:
			var m struct {
				Pin      *int `yaml:"pin"`
				GPIOChip *int `yaml:"gpiochip"`
				Line     *int `yaml:"line"`
			}
			if err := n.Decode(&m); err != nil {
				return Pin{}, fmt.Errorf("%s: %w", key, err)
			}
			p := Pin{Chip: defaultChip, Set: true}
			switch {
			case m.Line != nil:
				p.Line = *m.Line
			case m.Pin != nil:
				p.Line = *m.Pin
			default:
				return Pin{}, fmt.Errorf("%s: needs line or pin", key)
			}
			if m.GPIOChip != nil {
				p.Chip = *m.GPIOChip
			}
			return p, nil
		}
		return Pin{}, fmt.Errorf("%s: unexpected value", key)
	}
	pin := func(key string) (Pin, error) {
		n, ok := doc.Lora[key]
		if !ok {
			return Pin{}, nil
		}
		return nodePin(key, n)
	}
	for _, f := range []struct {
		key string
		dst *Pin
	}{{"CS", &b.CS}, {"IRQ", &b.IRQ}, {"Busy", &b.Busy}, {"Reset", &b.Reset}, {"TXen", &b.TXen}, {"RXen", &b.RXen}} {
		if *f.dst, err = pin(f.key); err != nil {
			return Board{}, err
		}
	}
	if p, err := pin("SX126X_ANT_SW"); err != nil {
		return Board{}, err
	} else if p.Set {
		b.High = append(b.High, p)
	}
	if n, ok := doc.Lora["Enable_Pins"]; ok {
		if n.Kind != yaml.SequenceNode {
			return Board{}, errors.New("Enable_Pins: want a list of pins")
		}
		for _, item := range n.Content {
			p, err := nodePin("Enable_Pins", *item)
			if err != nil {
				return Board{}, err
			}
			if p.Set {
				b.High = append(b.High, p)
			}
		}
	}
	if b.USB != nil && !b.CS.Set {
		b.CS = Pin{Line: 0, Set: true} // CH341 D0, as meshtasticd's Ch341Hal
	}

	if v := str("DIO2_AS_RF_SWITCH"); v != "" {
		b.DIO2RF = v == "true"
	}
	switch v := str("DIO3_TCXO_VOLTAGE"); v {
	case "", "false":
	case "true":
		b.TCXOVolt = 1.8 // meshtasticd's default for "true"
	default:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return Board{}, fmt.Errorf("DIO3_TCXO_VOLTAGE: %q", v)
		}
		b.TCXOVolt = f
	}

	// Power limits, with meshtasticd's defaults (PortduinoGlue.h).
	powerKey, powerDef := "SX126X_MAX_POWER", 22
	switch b.Module {
	case ModuleRF95:
		powerKey, powerDef = "RF95_MAX_POWER", 20
	case ModuleSX1280:
		powerKey, powerDef = "SX128X_MAX_POWER", 13
	case ModuleLR1110, ModuleLR1120, ModuleLR1121:
		powerKey, powerDef = "LR1110_MAX_POWER", 22
	}
	if b.MaxPower, err = integer(powerKey, powerDef); err != nil {
		return Board{}, err
	}
	if b.MaxPowerHF, err = integer("LR1120_MAX_POWER", 13); err != nil {
		return Board{}, err
	}
	if n, ok := doc.Lora["TX_GAIN_LORA"]; ok {
		switch n.Kind {
		case yaml.SequenceNode:
			if err := n.Decode(&b.TxGain); err != nil {
				return Board{}, fmt.Errorf("TX_GAIN_LORA: %w", err)
			}
		case yaml.ScalarNode:
			g, err := strconv.Atoi(n.Value)
			if err != nil {
				return Board{}, fmt.Errorf("TX_GAIN_LORA: %q", n.Value)
			}
			b.TxGain = []int{g}
		}
	}

	if n, ok := doc.Lora["rfswitch_table"]; ok {
		var raw map[string][]string
		if err := n.Decode(&raw); err != nil {
			return Board{}, fmt.Errorf("rfswitch_table: %w", err)
		}
		t := &RFSwitchTable{Pins: raw["pins"], Modes: map[string][]bool{}}
		if len(t.Pins) > 5 {
			return Board{}, errors.New("rfswitch_table: at most 5 pins")
		}
		for _, p := range t.Pins {
			if lrDIOIndex(p) < 0 {
				return Board{}, fmt.Errorf("rfswitch_table: pin %q (want DIO5, DIO6, DIO7, DIO8 or DIO10)", p)
			}
		}
		for mode, levels := range raw {
			if mode == "pins" {
				continue
			}
			for _, l := range levels {
				t.Modes[mode] = append(t.Modes[mode], strings.EqualFold(l, "HIGH"))
			}
		}
		b.RFSwitch = t
	}
	return b, nil
}

// ChipPower is the chip setting for power dBm at the antenna, following meshtasticd's
// RadioInterface::limitPower: subtract the PA gain, then clamp to the board limit and the chip's
// range [min, max].
func (b Board) ChipPower(power, min, max int) int {
	switch len(b.TxGain) {
	case 0:
	case 1:
		if b.TxGain[0] > 0 {
			power -= b.TxGain[0]
		}
	default:
		for dbm, gain := range b.TxGain {
			if dbm+gain > power || dbm == len(b.TxGain)-1 {
				power -= gain
				break
			}
		}
	}
	if power > max {
		power = max
	}
	if power < min {
		power = min
	}
	return power
}

// Summary is a one-line description for logs and tools.
func (b Board) Summary() string {
	name := b.Name
	if name == "" {
		name = b.Module
	}
	bus := b.SPIDev
	if b.USB != nil {
		bus = fmt.Sprintf("CH341 USB %04x:%04x", b.USB.VID, b.USB.PID)
		if b.USB.Serial != "" {
			bus += " serial " + b.USB.Serial
		}
		return fmt.Sprintf("%s (%s on %s, CS D%d, IRQ %s, BUSY %s, RESET %s)", name, b.Module, bus, b.CS.Line,
			usbPin(b.IRQ), usbPin(b.Busy), usbPin(b.Reset))
	}
	return fmt.Sprintf("%s (%s on %s, CS %s, IRQ %s, BUSY %s, RESET %s)", name, b.Module, bus, b.CS, b.IRQ, b.Busy, b.Reset)
}

func usbPin(p Pin) string {
	if !p.Set {
		return "not connected"
	}
	return fmt.Sprintf("D%d", p.Line)
}
