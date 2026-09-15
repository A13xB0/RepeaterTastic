package spi

import (
	"errors"
	"fmt"
	"hash/crc32"
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
  SX126X_MAX_POWER: 20
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Board{Name: "MeshAdv Pi Hat", Module: "sx1262", SPIDev: "spidev0.0", SPISpeed: 2_000_000,
		CS: Pin{Line: 21, Set: true}, IRQ: Pin{Line: 16, Set: true}, Busy: Pin{Line: 20, Set: true},
		Reset: Pin{Line: 18, Set: true}, TXen: Pin{Line: 13, Set: true}, RXen: Pin{Line: 12, Set: true},
		TCXOVolt: 1.8, MaxPower: 20, MaxPowerHF: 13}
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
	if b.SPIDev != "spidev1.0" || b.SPISpeed != 4_000_000 || !b.DIO2RF || b.TCXOVolt != 3.3 || b.MaxPower != 22 {
		t.Fatalf("board %+v", b)
	}
}

func TestParseCH341AndModules(t *testing.T) {
	b, err := ParseBoard([]byte("Lora:\n  Module: lr1121\n  IRQ: 6\n  Reset: 2\n  Busy: 4\n  spidev: ch341\n" +
		"  USB_PID: 0x5512\n  USB_VID: 0x1A86\n  USB_Serialnum: 13374201\n  DIO3_TCXO_VOLTAGE: 1.8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b.USB == nil || *b.USB != (USBID{VID: 0x1A86, PID: 0x5512, Serial: "13374201"}) || b.CS != (Pin{Line: 0, Set: true}) {
		t.Fatalf("USB board %+v %+v", b, b.USB)
	}
	if b.MaxPower != 22 || b.MaxPowerHF != 13 {
		t.Fatalf("LR11x0 limits %d/%d", b.MaxPower, b.MaxPowerHF)
	}
	for module, max := range map[string]int{"RF95": 20, "sx1280": 13, "LLCC68": 22} {
		b, err := ParseBoard([]byte("Lora:\n  Module: " + module + "\n"))
		if err != nil || b.MaxPower != max {
			t.Errorf("%s: %v, max power %d want %d", module, err, b.MaxPower, max)
		}
	}
}

func TestParseRFSwitchTable(t *testing.T) {
	b, err := ParseBoard([]byte(`
Lora:
  Module: lr1121
  rfswitch_table:
    pins: [DIO5, DIO6]
    MODE_STBY: [LOW, LOW]
    MODE_RX: [HIGH, LOW]
    MODE_TX: [LOW, HIGH]
    MODE_TX_HP: [LOW, HIGH]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := lrRFSwitchArgs(b.RFSwitch)
	want := []byte{0x03, 0x00, 0x01, 0x02, 0x02, 0x00, 0x00, 0x00}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SetDioAsRfSwitch %x, want %x", got, want)
	}
}

func TestParseRejects(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"lr2021":  {"Lora:\n  Module: lr2021\n", "lr2021"},
		"sim":     {"Lora:\n  Module: sim\n", "simulated"},
		"no lora": {"Meta:\n  name: x\n", "Lora"},
		"dio":     {"Lora:\n  Module: lr1121\n  rfswitch_table:\n    pins: [DIO9]\n", "DIO9"},
	} {
		if _, err := ParseBoard([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error about %s", name, err, tc.want)
		}
	}
	if _, err := ParseBoard([]byte("Lora:\n  Module: auto\n")); !errors.Is(err, errAutoModule) {
		t.Errorf("auto: %v", err)
	}
}

func TestChipPowerWithPAGain(t *testing.T) {
	b, err := ParseBoard([]byte("Lora:\n  Module: sx1262\n  Busy: 1\n  SX126X_MAX_POWER: 22\n" +
		"  TX_GAIN_LORA: [12, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 10, 10, 9, 8, 8, 7]\n"))
	if err != nil {
		t.Fatal(err)
	}
	// uMesh 30 dBm table, EU_868's 27 dBm: chip 19 dBm + 8 dB = 27; chip 20 + 8 would be 28.
	if got := b.ChipPower(27, -9, 22); got != 19 {
		t.Fatalf("27 dBm at the antenna: chip %d, want 19", got)
	}
	if got := (Board{TxGain: []int{8}}).ChipPower(30, -9, 22); got != 22 {
		t.Fatalf("single gain: chip %d, want 22", got)
	}
	if got := (Board{TxGain: []int{8}}).ChipPower(20, -9, 22); got != 12 {
		t.Fatalf("single gain: chip %d, want 12", got)
	}
	if got := (Board{}).ChipPower(27, -9, 22); got != 22 {
		t.Fatalf("no PA: chip %d, want 22", got)
	}
	if got := boardLimit(22, 8); got != 8 {
		t.Fatalf("NebraHat 2W limit: %d", got)
	}
}

// Every built-in meshtasticd board file parses, including one that repeats a key.
func TestKnownBoards(t *testing.T) {
	boards := KnownBoards()
	if len(boards) < 60 {
		t.Fatalf("only %d built-in boards", len(boards))
	}
	for _, k := range boards {
		if k.Err != nil {
			t.Errorf("%s: %v", k.File, k.Err)
		}
		if k.File == "lora-lyra-picocalc-wio-sx1262.yaml" && k.Board.Busy.Chip != 0 {
			t.Errorf("picocalc: first gpiochip should win, got %+v", k.Board.Busy)
		}
	}
	b, src, err := findBoard("MeshAdv-900M30S")
	if err != nil || b.Module != ModuleSX1262 || !strings.Contains(src, "lora-MeshAdv-900M30S.yaml") {
		t.Fatalf("find MeshAdv: %v %q %+v", err, src, b)
	}
	if _, _, err := findBoard("no-such-board"); err == nil {
		t.Fatal("found a board that doesn't exist")
	}
}

func TestAutoconfNames(t *testing.T) {
	if got := autoconfName("lora-usb-" + "MESHSTICK 1262" + ".yaml"); got != "lora-usb-meshstick-1262.yaml" {
		t.Fatalf("usb name %q", got)
	}
	for product, file := range autoconfProducts {
		if _, _, err := findBoard(file); err != nil {
			t.Errorf("%s → %s: %v", product, file, err)
		}
	}
	body := "RAK6421-13300-S1:aabbcc123456:5ba85807d92138b7519cfb60460573af"
	raw := fmt.Sprintf("%s:%08x", body, crc32.ChecksumIEEE([]byte(body)))
	if model, err := parseRAKEEPROM(append([]byte(raw), 0xFF, 0xFF)); err != nil || model != "RAK6421-13300-S1" {
		t.Fatalf("EEPROM: %q %v", model, err)
	}
	if _, err := parseRAKEEPROM([]byte(body + ":00000000")); err == nil {
		t.Fatal("accepted a bad EEPROM checksum")
	}
}

func TestCH341Encoding(t *testing.T) {
	if reverseBits(0x01) != 0x80 || reverseBits(0xC0) != 0x03 {
		t.Fatal("bit reversal")
	}
	tx := make([]byte, 40)
	tx[0], tx[31] = 0x80, 0x01
	p := ch341SPIPackets(tx)
	if len(p) != 2 || len(p[0]) != 32 || len(p[1]) != 10 || p[0][0] != 0xA8 || p[0][1] != 0x01 || p[1][1] != 0x80 {
		t.Fatalf("packets %x", p)
	}
	if got := ch341UIO(0x01, 0x2F); !reflect.DeepEqual(got, []byte{0xAB, 0x81, 0x6F, 0x20}) {
		t.Fatalf("UIO %x", got)
	}
	if in := ch341Inputs([]byte{0x50, 0x04, 0x00}); in&(1<<4) == 0 || in&(1<<6) == 0 || in&(1<<10) == 0 {
		t.Fatalf("inputs %b", in)
	}
}
