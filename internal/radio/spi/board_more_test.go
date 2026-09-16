package spi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinString(t *testing.T) {
	if got := (Pin{}).String(); got != "not connected" {
		t.Errorf("unset pin = %q", got)
	}
	if got := (Pin{Chip: 2, Line: 17, Set: true}).String(); got != "gpiochip2 line 17" {
		t.Errorf("pin = %q", got)
	}
}

func TestLoadBoard(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte("Meta:\n  name: Bench\nLora:\n  Module: RF95\n  IRQ: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBoard(good)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Bench" || b.Module != ModuleRF95 || b.IRQ != (Pin{Line: 5, Set: true}) || b.MaxPower != 20 {
		t.Fatalf("board %+v", b)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("Lora:\n  Module: lr2021\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBoard(bad); err == nil || !strings.HasPrefix(err.Error(), bad+": ") {
		t.Fatalf("bad board error = %v", err)
	}
	if _, err := LoadBoard(filepath.Join(dir, "missing.yaml")); !os.IsNotExist(err) {
		t.Fatalf("missing board error = %v", err)
	}
}

func TestParseBoardErrors(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"not yaml", "Lora: [unclosed", "yaml"},
		{"lora not a mapping", "Lora: 5\n", "no Lora section"},
		{"spi speed", "Lora:\n  Module: sx1262\n  spiSpeed: fast\n", `spiSpeed: "fast" isn't a number`},
		{"gpiochip", "Lora:\n  Module: sx1262\n  gpiochip: x\n", "gpiochip"},
		{"usb vid", "Lora:\n  Module: sx1262\n  spidev: ch341\n  USB_VID: nope\n", "USB_VID"},
		{"usb pid", "Lora:\n  Module: sx1262\n  spidev: ch341\n  USB_PID: nope\n", "USB_PID"},
		{"pin word", "Lora:\n  Module: sx1262\n  CS: twelve\n", `CS: "twelve" isn't a pin number`},
		{"pin list", "Lora:\n  Module: sx1262\n  IRQ: [1, 2]\n", "IRQ: unexpected value"},
		{"pin mapping type", "Lora:\n  Module: sx1262\n  Busy:\n    line: abc\n", "Busy: "},
		{"pin mapping empty", "Lora:\n  Module: sx1262\n  Reset:\n    gpiochip: 1\n", "Reset: needs line or pin"},
		{"ant sw", "Lora:\n  Module: sx1262\n  SX126X_ANT_SW: x\n", "SX126X_ANT_SW"},
		{"enable not list", "Lora:\n  Module: sx1262\n  Enable_Pins: 5\n", "Enable_Pins: want a list"},
		{"enable item", "Lora:\n  Module: sx1262\n  Enable_Pins: [x]\n", "Enable_Pins"},
		{"tcxo", "Lora:\n  Module: sx1262\n  DIO3_TCXO_VOLTAGE: high\n", "DIO3_TCXO_VOLTAGE"},
		{"max power", "Lora:\n  Module: sx1262\n  SX126X_MAX_POWER: lots\n", "SX126X_MAX_POWER"},
		{"hf power", "Lora:\n  Module: lr1121\n  LR1120_MAX_POWER: lots\n", "LR1120_MAX_POWER"},
		{"gain scalar", "Lora:\n  Module: sx1262\n  TX_GAIN_LORA: big\n", "TX_GAIN_LORA"},
		{"gain list", "Lora:\n  Module: sx1262\n  TX_GAIN_LORA: [1, x]\n", "TX_GAIN_LORA"},
		{"rfswitch shape", "Lora:\n  Module: lr1121\n  rfswitch_table: 5\n", "rfswitch_table"},
		{"rfswitch pins", "Lora:\n  Module: lr1121\n  rfswitch_table:\n    pins: [DIO5, DIO6, DIO7, DIO8, DIO10, DIO5]\n", "at most 5 pins"},
		{"empty module", "Lora:\n  CS: 1\n", "auto"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseBoard([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ParseBoard = %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestParseBoardDetails(t *testing.T) {
	b, err := ParseBoard([]byte(`
Meta:
  name: Details
  compatible: [raspberry-pi, "  ", usb]
Lora:
  Module: SX1262
  spidev: /dev/spidev2.1
  CS: -1
  IRQ:
    pin: 22
  Enable_Pins: [-1, RADIOLIB_NC, 4]
  DIO2_AS_RF_SWITCH: false
  DIO3_TCXO_VOLTAGE: false
  TX_GAIN_LORA: 6
`))
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Details" || len(b.Hosts) != 2 || b.Hosts[0] != "raspberry-pi" || b.Hosts[1] != "usb" {
		t.Errorf("meta %q %q", b.Name, b.Hosts)
	}
	if b.Module != ModuleSX1262 || b.SPIDev != "/dev/spidev2.1" || b.CS.Set || b.IRQ != (Pin{Line: 22, Set: true}) {
		t.Errorf("bus/pins %+v", b)
	}
	if len(b.High) != 1 || b.High[0] != (Pin{Line: 4, Set: true}) {
		t.Errorf("held high %+v", b.High)
	}
	if b.DIO2RF || b.TCXOVolt != 0 || len(b.TxGain) != 1 || b.TxGain[0] != 6 {
		t.Errorf("rf %+v", b)
	}
}

// A CH341 board with its own CS keeps it, and the stock IDs are the default.
func TestParseCH341Defaults(t *testing.T) {
	b, err := ParseBoard([]byte("Lora:\n  Module: sx1262\n  spidev: ch341\n  CS: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b.USB == nil || *b.USB != (USBID{VID: 0x1A86, PID: 0x5512}) || b.SPIDev != "" || b.CS != (Pin{Line: 3, Set: true}) {
		t.Fatalf("board %+v usb %+v", b, b.USB)
	}
}

func TestChipPowerClamps(t *testing.T) {
	cases := []struct {
		name  string
		gain  []int
		power int
		want  int
	}{
		{"below chip minimum", nil, -20, -9},
		{"negative single gain ignored", []int{-3}, 10, 10},
		{"table past its end uses the last gain", []int{1, 1, 1}, 30, 22},
		{"table low power", []int{5, 5, 5}, 4, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Board{TxGain: c.gain}).ChipPower(c.power, -9, 22); got != c.want {
				t.Fatalf("ChipPower(%d) = %d, want %d", c.power, got, c.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	cases := []struct {
		name  string
		board Board
		want  string
	}{
		{"spidev", Board{Name: "Hat", Module: ModuleSX1262, SPIDev: "spidev0.0", IRQ: Pin{Line: 16, Set: true}},
			"Hat (sx1262 on spidev0.0, CS not connected, IRQ gpiochip0 line 16, BUSY not connected, RESET not connected)"},
		{"unnamed usb", Board{Module: ModuleSX1262, USB: &USBID{VID: 0x1A86, PID: 0x5512}, Busy: Pin{Line: 4, Set: true}},
			"sx1262 (sx1262 on CH341 USB 1a86:5512, CS D0, IRQ not connected, BUSY D4, RESET not connected)"},
		{"usb serial", Board{Name: "Stick", Module: ModuleLR1121, USB: &USBID{VID: 1, PID: 2, Serial: "42"}, CS: Pin{Line: 1, Set: true}},
			"Stick (lr1121 on CH341 USB 0001:0002 serial 42, CS D1, IRQ not connected, BUSY not connected, RESET not connected)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.board.Summary(); got != c.want {
				t.Fatalf("Summary =\n  %s\nwant\n  %s", got, c.want)
			}
		})
	}
}
