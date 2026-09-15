package sx126x

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Board is a LoRa radio wired to a Linux board, described the way meshtasticd describes it in
// /etc/meshtasticd/config.d/lora-*.yaml.
type Board struct {
	Name     string // Meta.name
	Module   string // sx1262, sx1268 or llcc68
	SPIDev   string // spidev0.0 (the /dev/ prefix is optional)
	SPISpeed uint32 // Hz, default 2 MHz
	CS       Pin    // not connected = the spidev's own chip select
	IRQ      Pin    // DIO1; not connected = poll the chip
	Busy     Pin    // required
	Reset    Pin    // not connected = no hardware reset
	TXen     Pin    // optional external PA/LNA switch
	RXen     Pin    // optional
	// High are held high while the radio is open: Enable_Pins (e.g. RAK6421 slot power) and
	// SX126X_ANT_SW.
	High     []Pin
	DIO2RF   bool    // DIO2_AS_RF_SWITCH
	TCXOVolt float64 // DIO3_TCXO_VOLTAGE (0 = crystal, no TCXO)
	MaxPower int     // SX126X_MAX_POWER dBm (0 = chip limit)
	// TxGain is TX_GAIN_LORA: an external PA's gain in dB, one value or one per chip dBm step from
	// 0. Requested power is antenna power, so the chip is set that much lower.
	TxGain []int
}

// Pin is a GPIO line on a gpiochip. meshtasticd writes a Broadcom number (line on the default
// chip) or {pin, gpiochip, line}.
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
	var doc struct {
		Meta struct {
			Name string `yaml:"name"`
		} `yaml:"Meta"`
		Lora map[string]yaml.Node `yaml:"Lora"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Board{}, err
	}
	if doc.Lora == nil {
		return Board{}, errors.New("no Lora section")
	}
	b := Board{Name: doc.Meta.Name, SPIDev: "spidev0.0", SPISpeed: 2_000_000}
	str := func(key string) string {
		n, ok := doc.Lora[key]
		if !ok {
			return ""
		}
		return strings.TrimSpace(n.Value)
	}
	b.Module = strings.ToLower(str("Module"))
	switch b.Module {
	case "sx1262", "sx1268", "llcc68":
	case "", "auto":
		return Board{}, errors.New("Lora.Module is auto or missing: name the chip (sx1262, sx1268 or llcc68)")
	default:
		return Board{}, fmt.Errorf("Lora.Module %s isn't supported yet (sx1262, sx1268, llcc68)", b.Module)
	}
	if v := str("spidev"); v != "" {
		if v == "ch341" {
			return Board{}, errors.New("CH341 USB-to-SPI (spidev: ch341) isn't supported yet")
		}
		b.SPIDev = v
	}
	if v := str("spiSpeed"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Board{}, fmt.Errorf("spiSpeed: %w", err)
		}
		b.SPISpeed = uint32(n)
	}
	defaultChip := 0
	if v := str("gpiochip"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Board{}, fmt.Errorf("gpiochip: %w", err)
		}
		defaultChip = n
	}
	var nodePin func(key string, n yaml.Node) (Pin, error)
	pin := func(key string) (Pin, error) {
		n, ok := doc.Lora[key]
		if !ok {
			return Pin{}, nil
		}
		return nodePin(key, n)
	}
	nodePin = func(key string, n yaml.Node) (Pin, error) {
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
	var err error
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
	if !b.Busy.Set {
		return Board{}, errors.New("Lora.Busy is required for SX126x chips")
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
	if v := str("SX126X_MAX_POWER"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Board{}, fmt.Errorf("SX126X_MAX_POWER: %w", err)
		}
		b.MaxPower = n
	}
	return b, nil
}

// ChipPower is the chip setting for power dBm at the antenna, following meshtasticd's
// RadioInterface::limitPower: subtract the PA gain, then clamp to the board and chip limits.
func (b Board) ChipPower(power int) int {
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
	if power > 22 {
		power = 22
	}
	if b.MaxPower > 0 && power > b.MaxPower {
		power = b.MaxPower
	}
	if power < -9 {
		power = -9
	}
	return power
}

// Summary is a one-line description for logs and tools.
func (b Board) Summary() string {
	name := b.Name
	if name == "" {
		name = b.Module
	}
	return fmt.Sprintf("%s (%s on %s, CS %s, IRQ %s, BUSY %s, RESET %s)", name, b.Module, b.SPIDev, b.CS, b.IRQ, b.Busy, b.Reset)
}
