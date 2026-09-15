package sx126x

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseMeshAdv(t *testing.T) {
	b, err := ParseBoard([]byte(`
Meta:
  name: MeshAdv Pi Hat
Lora:
  Module: sx1262
  CS: 21
  IRQ: 16
  Busy: 20
  Reset: 18
  TXen: 13
  RXen: 12
  DIO3_TCXO_VOLTAGE: true
  SX126X_MAX_POWER: 22
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Board{Name: "MeshAdv Pi Hat", Module: "sx1262", SPIDev: "spidev0.0", SPISpeed: 2_000_000,
		CS: Pin{Line: 21, Set: true}, IRQ: Pin{Line: 16, Set: true}, Busy: Pin{Line: 20, Set: true},
		Reset: Pin{Line: 18, Set: true}, TXen: Pin{Line: 13, Set: true}, RXen: Pin{Line: 12, Set: true},
		TCXOVolt: 1.8, MaxPower: 22}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("got  %+v\nwant %+v", b, want)
	}
}

func TestParseGPIOChipMapAndNC(t *testing.T) {
	b, err := ParseBoard([]byte(`
Lora:
  Module: sx1268
  gpiochip: 4
  spidev: spidev1.0
  spiSpeed: 4000000
  CS: RADIOLIB_NC
  IRQ:
    pin: 5
    gpiochip: 1
    line: 21
  Busy: 6
  SX126X_ANT_SW: 7
  Enable_Pins:
    - 12
    - pin: 3
      gpiochip: 2
      line: 9
  DIO2_AS_RF_SWITCH: true
  DIO3_TCXO_VOLTAGE: 3.3
`))
	if err != nil {
		t.Fatal(err)
	}
	if b.CS.Set || b.IRQ != (Pin{Chip: 1, Line: 21, Set: true}) || b.Busy != (Pin{Chip: 4, Line: 6, Set: true}) {
		t.Fatalf("pins %+v", b)
	}
	if !reflect.DeepEqual(b.High, []Pin{{Chip: 4, Line: 7, Set: true}, {Chip: 4, Line: 12, Set: true}, {Chip: 2, Line: 9, Set: true}}) {
		t.Fatalf("held-high pins %+v", b.High)
	}
	if b.SPIDev != "spidev1.0" || b.SPISpeed != 4_000_000 || !b.DIO2RF || b.TCXOVolt != 3.3 {
		t.Fatalf("board %+v", b)
	}
}

func TestParseRejects(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"ch341":   {"Lora:\n  Module: sx1262\n  spidev: ch341\n  Busy: 1\n", "CH341"},
		"auto":    {"Lora:\n  Module: auto\n  Busy: 1\n", "auto"},
		"lr1121":  {"Lora:\n  Module: lr1121\n  Busy: 1\n", "lr1121"},
		"no busy": {"Lora:\n  Module: sx1262\n", "Busy"},
		"no lora": {"Meta:\n  name: x\n", "Lora"},
	} {
		if _, err := ParseBoard([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error about %s", name, err, tc.want)
		}
	}
}

func TestChipPowerWithPAGain(t *testing.T) {
	b, err := ParseBoard([]byte("Lora:\n  Module: sx1262\n  Busy: 1\n  SX126X_MAX_POWER: 22\n" +
		"  TX_GAIN_LORA: [12, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 10, 10, 9, 8, 8, 7]\n"))
	if err != nil {
		t.Fatal(err)
	}
	// uMesh 30 dBm table, EU_868's 27 dBm: chip 19 dBm + 8 dB = 27; chip 20 + 8 would be 28.
	if got := b.ChipPower(27); got != 19 {
		t.Fatalf("27 dBm at the antenna: chip %d, want 19", got)
	}
	if got := (Board{TxGain: []int{8}}).ChipPower(30); got != 22 {
		t.Fatalf("single gain: chip %d, want 22", got)
	}
	if got := (Board{TxGain: []int{8}}).ChipPower(20); got != 12 {
		t.Fatalf("single gain: chip %d, want 12", got)
	}
	if got := (Board{}).ChipPower(27); got != 22 {
		t.Fatalf("no PA: chip %d, want 22", got)
	}
}
