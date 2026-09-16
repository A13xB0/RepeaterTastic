package spi

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// flakyHAL fails one SPI transaction (failAt, counting from 0; -1 = none), one BUSY read
// (busyFailAt, counting from 1; 0 = none), or every BUSY read (busyErr).
type flakyHAL struct {
	hal
	failAt     int
	n          int
	busyFailAt int
	busyN      int
	busyErr    error
}

func (f *flakyHAL) Transfer(tx []byte) ([]byte, error) {
	i := f.n
	f.n++
	if i == f.failAt {
		return nil, errBoom
	}
	return f.hal.Transfer(tx)
}

func (f *flakyHAL) Busy() (bool, error) {
	f.busyN++
	if f.busyN == f.busyFailAt {
		return false, errBoom
	}
	if f.busyErr != nil {
		return false, f.busyErr
	}
	return f.hal.Busy()
}

// chipCase builds a chip on a fresh model behind h, and primes the model with a received packet.
type chipCase struct {
	name  string
	cfg   radio.Config
	power int
	// tolerated and toleratedBusy are how many single SPI or BUSY failures the driver deliberately
	// ignores (best-effort reads and clean-ups).
	tolerated, toleratedBusy int
	build                    func(t *testing.T, wrap func(hal) hal) (c chip, prime func())
}

func sx126xCase() chipCase {
	return chipCase{name: "sx126x", cfg: longFast, power: 14, tolerated: 1, toleratedBusy: 1,
		build: func(t *testing.T, wrap func(hal) hal) (chip, func()) {
			fc := newFakeChip()
			h := wrap(fc)
			_ = fc.Reset()
			c := &sx126x{bus: bus{h: h}, board: Board{Module: ModuleSX1262, TCXOVolt: 1.8, DIO2RF: true}, log: t.Logf}
			return c, func() {
				copy(fc.buf[:], "abc")
				fc.rxLen, fc.irq = 3, irqRxDone
			}
		}}
}

func sx127xCase(bw uint32, power int) chipCase {
	cfg := longFast
	cfg.BandwidthHz = bw
	return chipCase{name: fmt.Sprintf("sx127x %d Hz %d dBm", bw, power), cfg: cfg, power: power, tolerated: 1,
		build: func(_ *testing.T, wrap func(hal) hal) (chip, func()) {
			m := &rf95Model{}
			m.reg[rfRegVersion] = 0x12
			h := wrap(newFakeHAL(m.xfer, false))
			return &sx127x{bus: bus{h: h}, board: Board{Module: ModuleRF95}}, func() {
				copy(m.fifo[:], "abc")
				m.reg[rfRegRxNbBytes], m.reg[rfRegFifoRxCurrent] = 3, 0
				m.reg[rfRegHopChannel] = 0x40
				m.reg[rfRegIrqFlags] = rfIrqRxDone
			}
		}}
}

var wide = radio.Config{FrequencyHz: 2_403_000_000, BandwidthHz: 812_500, SF: 11, CR: 5, SyncWord: 0x2B, Preamble: 16}

func sx128xCase() chipCase {
	return chipCase{name: "sx128x", cfg: wide, power: 10, tolerated: 0,
		build: func(_ *testing.T, wrap func(hal) hal) (chip, func()) {
			m := newSX1280Model()
			h := wrap(newFakeHAL(m.xfer, true))
			return &sx128x{bus: bus{h: h}, board: Board{Module: ModuleSX1280}}, func() {
				copy(m.buf[:], "abc")
				m.rxLen, m.irq = 3, s8IrqRxDone
			}
		}}
}

func newSX1280Model() *sx1280Model {
	m := &sx1280Model{regs: map[uint16]byte{}, cmds: map[byte][]byte{}}
	for i, c := range []byte("SX1280 V3B") {
		m.regs[s8RegVersionString+uint16(i)] = c
	}
	return m
}

var lrSwitch = &RFSwitchTable{Pins: []string{"DIO5", "DIO6"}, Modes: map[string][]bool{"MODE_RX": {true, false}}}

func lrCase() chipCase {
	return chipCase{name: "lr11x0", cfg: longFast, power: 20, tolerated: 3, toleratedBusy: 5,
		build: func(t *testing.T, wrap func(hal) hal) (chip, func()) {
			m := &lr1121Model{cmds: map[uint16][]byte{}}
			h := wrap(newFakeHAL(m.xfer, true))
			b := Board{Module: ModuleLR1121, TCXOVolt: 1.8, RFSwitch: lrSwitch}
			return &lr11x0{bus: bus{h: h}, board: b, log: t.Logf}, func() {
				m.rx, m.irq = []byte("abc"), lrIrqRxDone
			}
		}}
}

// exercise runs a chip through its whole life, returning the first error.
func exercise(c chip, cfg radio.Config, power int, prime func()) error {
	steps := []func() error{
		c.init,
		func() error { return c.configure(cfg, power) },
		c.startRx,
		func() error { return c.transmit([]byte("tx")) },
		c.standby,
		c.startRx,
		func() error { prime(); _, err := c.events(); return err },
		func() error { _, _, _, err := c.packet(); return err },
		func() error { _, err := c.rssi(); return err },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// cleanRun exercises tc once without failures and returns the HAL's transaction and BUSY counts.
func cleanRun(t *testing.T, tc chipCase) *flakyHAL {
	t.Helper()
	var clean *flakyHAL
	c, prime := tc.build(t, func(h hal) hal { clean = &flakyHAL{hal: h, failAt: -1}; return clean })
	if err := exercise(c, tc.cfg, tc.power, prime); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	return clean
}

// unreported runs tc n times, the i-th time (from 0) behind flaky(i), and counts the runs that
// succeeded despite the injected failure. Any other error than the injected one fails the test.
func unreported(t *testing.T, tc chipCase, n int, flaky func(h hal, i int) *flakyHAL) int {
	t.Helper()
	ignored := 0
	for i := range n {
		c, prime := tc.build(t, func(h hal) hal { return flaky(h, i) })
		err := exercise(c, tc.cfg, tc.power, prime)
		if err == nil {
			ignored++
		} else if !errors.Is(err, errBoom) {
			t.Errorf("failure %d: unexpected error %v", i, err)
		}
	}
	return ignored
}

// Every SPI failure along the way is reported, except the few the drivers deliberately ignore.
func TestChipsReportSPIFailures(t *testing.T) {
	for _, tc := range []chipCase{sx126xCase(), sx127xCase(250_000, 14), sx127xCase(500_000, 20), sx128xCase(), lrCase()} {
		t.Run(tc.name, func(t *testing.T) {
			clean := cleanRun(t, tc)
			if clean.n < 20 {
				t.Fatalf("only %d transactions", clean.n)
			}
			ignored := unreported(t, tc, clean.n, func(h hal, i int) *flakyHAL { return &flakyHAL{hal: h, failAt: i} })
			if ignored != tc.tolerated {
				t.Fatalf("%d SPI failures went unreported, want %d", ignored, tc.tolerated)
			}
		})
	}
}

// The same for failures reading the BUSY line.
func TestChipsReportBusyFailures(t *testing.T) {
	for _, tc := range []chipCase{sx126xCase(), sx128xCase(), lrCase()} {
		t.Run(tc.name, func(t *testing.T) {
			clean := cleanRun(t, tc)
			ignored := unreported(t, tc, clean.busyN, func(h hal, i int) *flakyHAL {
				return &flakyHAL{hal: h, failAt: -1, busyFailAt: i + 1}
			})
			if ignored != tc.toleratedBusy {
				t.Fatalf("%d BUSY failures went unreported, want %d", ignored, tc.toleratedBusy)
			}
		})
	}
}

func TestChipsBusyLineFailure(t *testing.T) {
	for _, tc := range []chipCase{sx126xCase(), sx128xCase(), lrCase()} {
		c, _ := tc.build(t, func(h hal) hal { return &flakyHAL{hal: h, failAt: -1, busyErr: errBoom} })
		if err := c.init(); !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "after reset") {
			t.Errorf("%s: init = %v", tc.name, err)
		}
	}
}

// ------------------------------------------------------------------------------------ SX126x

func TestSX126xBandwidths(t *testing.T) {
	want := map[uint32]byte{7_800: 0x00, 10_400: 0x08, 15_600: 0x01, 20_800: 0x09, 31_250: 0x02, 41_700: 0x0A,
		62_500: 0x03, 125_000: 0x04, 250_000: 0x05, 500_000: 0x06}
	for hz, code := range want {
		if got, ok := sx126xBandwidth(hz); !ok || got != code {
			t.Errorf("%d Hz: 0x%02x %v, want 0x%02x", hz, got, ok, code)
		}
	}
	if _, ok := sx126xBandwidth(100_000); ok {
		t.Error("100 kHz accepted")
	}
}

func TestImageBands(t *testing.T) {
	cases := map[uint32][2]byte{
		928_000_000: {0xE1, 0xE9},
		868_000_000: {0xD7, 0xDB},
		800_000_000: {0xC1, 0xC5},
		490_000_000: {0x75, 0x81},
		433_000_000: {0x6B, 0x6F},
		150_000_000: {36, 38},
	}
	for hz, want := range cases {
		if got := imageBand(hz); got != want {
			t.Errorf("%d Hz: %x, want %x", hz, got, want)
		}
	}
}

func TestSX126xFrequencyRange(t *testing.T) {
	r, _ := openFake(t, Board{})
	for _, hz := range []uint32{149_000_000, 961_000_000} {
		c := longFast
		c.FrequencyHz = hz
		if err := r.Configure(t.Context(), c); !errors.Is(err, radio.ErrUnsupported) {
			t.Errorf("%d Hz: %v", hz, err)
		}
	}
}

// 500 kHz clears the modulation-quality bit; staying in a band doesn't recalibrate the image.
func TestSX126x500kAndSameBand(t *testing.T) {
	r, chip := openFake(t, Board{})
	c := longFast
	c.BandwidthHz = 500_000
	if err := r.Configure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	c.FrequencyHz = 868_100_000
	if err := r.Configure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	chip.mu.Lock()
	defer chip.mu.Unlock()
	if chip.regs[regTxModulation]&0x04 != 0 {
		t.Error("modulation-quality bit set at 500 kHz")
	}
	calibrations := 0
	for _, tx := range chip.log {
		if tx[0] == cmdCalibrateImage {
			calibrations++
		}
	}
	if calibrations != 1 {
		t.Errorf("%d image calibrations, want 1", calibrations)
	}
}

type logLines struct{ lines []string }

func (l *logLines) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestSX126xDeviceErrorsLoggedAtInit(t *testing.T) {
	fc := newFakeChip()
	_ = fc.Reset()
	fc.devErrs = 0x0020
	logs := &logLines{}
	c := &sx126x{bus: bus{h: fc}, board: Board{Module: ModuleSX1262}, log: logs.logf}
	if err := c.init(); err != nil {
		t.Fatal(err)
	}
	if len(logs.lines) != 1 || !strings.Contains(logs.lines[0], "0x0020") {
		t.Fatalf("logs %q", logs.lines)
	}
}

func TestSX126xDiagnostics(t *testing.T) {
	fc := newFakeChip()
	fc.status, fc.devErrs = 5<<4, 0x0120 // RX; XOSC start and PA ramp
	c := &sx126x{bus: bus{h: fc}}
	got := strings.Join(c.diagnostics(), "\n")
	want := "status:     0x50 (RX)\ndev errors: 0x0120 (XOSC start (check the TCXO voltage), PA ramp)"
	if got != want {
		t.Fatalf("diagnostics:\n%s\nwant\n%s", got, want)
	}

	fc.status, fc.devErrs = 7<<4, 0
	if got := c.diagnostics(); len(got) != 2 || !strings.Contains(got[0], "unexpected mode 7") || !strings.HasSuffix(got[1], "(none)") {
		t.Fatalf("diagnostics %q", got)
	}
	for i, want := range []string{"status: boom", "dev errors: boom"} {
		c.h = &flakyHAL{hal: fc, failAt: i}
		if got := c.diagnostics(); got[len(got)-1] != want {
			t.Errorf("failure %d: %q", i, got)
		}
	}
}

func TestFlagNames(t *testing.T) {
	names := []string{"a", "", "c"}
	cases := map[uint32]string{0: " (none)", 2: " (none)", 1: " (a)", 5: " (a, c)", 0xFF: " (a, c)"}
	for v, want := range cases {
		if got := flagNames(v, names); got != want {
			t.Errorf("flagNames(%b) = %q, want %q", v, got, want)
		}
	}
}

func TestSX126xEmptyPacketAndNoEvents(t *testing.T) {
	fc := newFakeChip()
	c := &sx126x{bus: bus{h: fc}}
	if ev, err := c.events(); ev != 0 || err != nil {
		t.Fatalf("events = %v, %v", ev, err)
	}
	if data, _, _, err := c.packet(); data != nil || err != nil {
		t.Fatalf("packet = %v, %v", data, err)
	}
	if v, err := c.rssi(); v != -110 || err != nil {
		t.Fatalf("rssi = %d, %v", v, err)
	}
}

// ------------------------------------------------------------------------------------ SX127x

func newRF95(t *testing.T) (*sx127x, *rf95Model) {
	t.Helper()
	m := &rf95Model{}
	m.reg[rfRegVersion] = 0x12
	c := &sx127x{bus: bus{h: newFakeHAL(m.xfer, false)}, board: Board{Module: ModuleRF95}}
	if err := c.init(); err != nil {
		t.Fatal(err)
	}
	return c, m
}

func TestSX127xConfigRejects(t *testing.T) {
	c, _ := newRF95(t)
	cases := map[string]radio.Config{
		"bandwidth": {FrequencyHz: 868_000_000, BandwidthHz: 41_700, SF: 7, CR: 5},
		"SF6":       {FrequencyHz: 868_000_000, BandwidthHz: 125_000, SF: 6, CR: 5},
		"too low":   {FrequencyHz: 136_000_000, BandwidthHz: 125_000, SF: 7, CR: 5},
		"too high":  {FrequencyHz: 1_021_000_000, BandwidthHz: 125_000, SF: 7, CR: 5},
	}
	for name, cfg := range cases {
		if err := c.configure(cfg, 10); !errors.Is(err, radio.ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for hz, bits := range map[uint32]byte{62_500: 0x60, 125_000: 0x70, 250_000: 0x80, 500_000: 0x90} {
		if got, err := sx127xBandwidth(hz); err != nil || got != bits {
			t.Errorf("%d Hz: 0x%02x %v", hz, got, err)
		}
	}
}

func TestSX127xPower(t *testing.T) {
	cases := []struct {
		power     int
		pa, paDac byte
	}{
		{20, 0xFF, 0x07},
		{19, 0xFF, 0x04}, // not settable: 17
		{5, 0xF3, 0x04},
	}
	for _, tc := range cases {
		c, m := newRF95(t)
		m.reg[rfRegPaDac] = 0x80
		if err := c.setPower(tc.power); err != nil {
			t.Fatal(err)
		}
		if m.reg[rfRegPaConfig] != tc.pa || m.reg[rfRegPaDac] != 0x80|tc.paDac {
			t.Errorf("%d dBm: PA 0x%02x DAC 0x%02x", tc.power, m.reg[rfRegPaConfig], m.reg[rfRegPaDac])
		}
	}
}

func TestSX127xErrata(t *testing.T) {
	cases := []struct {
		name          string
		freq, bw      uint32
		r36, r3A, r2F byte
		r31high       bool
	}{
		{"868 500k", 868_000_000, 500_000, 0x02, 0x64, 0, true},
		{"433 500k", 433_000_000, 500_000, 0x02, 0x7F, 0, true},
		{"169 500k", 169_000_000, 500_000, 0, 0, 0, true},
		{"868 250k", 868_000_000, 250_000, 0, 0, 0x40, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, m := newRF95(t)
			m.reg[0x31] = 0x80
			c.freqHz, c.bwHz = tc.freq, tc.bw
			if err := c.errata(); err != nil {
				t.Fatal(err)
			}
			if m.reg[0x36] != tc.r36 || m.reg[0x3A] != tc.r3A || m.reg[0x2F] != tc.r2F || (m.reg[0x31]&0x80 != 0) != tc.r31high {
				t.Fatalf("0x36=%02x 0x3A=%02x 0x2F=%02x 0x31=%02x", m.reg[0x36], m.reg[0x3A], m.reg[0x2F], m.reg[0x31])
			}
		})
	}
}

func TestSX127xInitRejects(t *testing.T) {
	m := &rf95Model{}
	m.reg[rfRegVersion] = 0x22
	c := &sx127x{bus: bus{h: newFakeHAL(m.xfer, false)}}
	if err := c.init(); err == nil || !strings.Contains(err.Error(), "version register 0x22") {
		t.Fatalf("init = %v", err)
	}
	// A chip that never leaves FSK mode.
	m = &rf95Model{}
	m.reg[rfRegVersion] = 0x11
	stuck := func(tx []byte) []byte {
		if tx[0] == rfRegOpMode|0x80 {
			return make([]byte, len(tx))
		}
		return m.xfer(tx)
	}
	c = &sx127x{bus: bus{h: newFakeHAL(stuck, false)}}
	if err := c.init(); err == nil || !strings.Contains(err.Error(), "didn't enter LoRa mode") {
		t.Fatalf("init = %v", err)
	}
}

func TestSX127xReceiveDetails(t *testing.T) {
	c, m := newRF95(t)
	if err := c.configure(radio.Config{FrequencyHz: 433_000_000, BandwidthHz: 125_000, SF: 9, CR: 5}, 10); err != nil {
		t.Fatal(err)
	}
	if ev, err := c.events(); ev != 0 || err != nil {
		t.Fatalf("idle events = %v, %v", ev, err)
	}
	if data, _, _, err := c.packet(); data != nil || err != nil {
		t.Fatalf("empty packet = %v, %v", data, err)
	}
	// RxDone from a header without the payload CRC flag counts as damaged.
	m.reg[rfRegIrqFlags], m.reg[rfRegHopChannel] = rfIrqRxDone, 0
	if ev, err := c.events(); err != nil || ev != evRxDone|evCrcErr || m.reg[rfRegIrqFlags] != 0 {
		t.Fatalf("events = %v, %v (flags 0x%02x)", ev, err, m.reg[rfRegIrqFlags])
	}
	// Positive SNR leaves the RSSI alone; 433 MHz uses the low-band offset.
	copy(m.fifo[:], "xy")
	m.reg[rfRegRxNbBytes], m.reg[rfRegPktSnr], m.reg[rfRegPktRssi] = 2, 8, 50
	data, rssi, snr, err := c.packet()
	if err != nil || string(data) != "xy" || rssi != -164+50 || snr != 2 {
		t.Fatalf("packet = %q %d %g %v", data, rssi, snr, err)
	}
	m.reg[rfRegRssi] = 40
	if v, err := c.rssi(); v != -124 || err != nil {
		t.Fatalf("rssi = %d, %v", v, err)
	}
}

func TestSX127xReceivingAndDiagnostics(t *testing.T) {
	c, m := newRF95(t)
	for stat, want := range map[byte]bool{0x00: false, 0x01: true, 0x02: true, 0x08: true, 0x04: false} {
		m.reg[rfRegModemStat] = stat
		if got, err := c.receiving(); got != want || err != nil {
			t.Errorf("modem stat 0x%02x: %v %v", stat, got, err)
		}
	}
	m.reg[rfRegOpMode] = rfLongRangeMode | rfModeRxCont
	got := strings.Join(c.diagnostics(), "\n")
	if got != "version:    0x12\nop mode:    0x85 (RX continuous, LoRa true)" {
		t.Fatalf("diagnostics %q", got)
	}
	c.h = &flakyHAL{hal: c.h, failAt: 0}
	if got := c.diagnostics(); len(got) != 1 {
		t.Fatalf("diagnostics after a failed read %q", got)
	}
	c.h = &flakyHAL{hal: c.h, failAt: 0}
	if _, err := c.receiving(); !errors.Is(err, errBoom) {
		t.Fatalf("receiving = %v", err)
	}
}

// The Radio probes an RF95 for a packet in progress.
func TestSX127xChannelBusy(t *testing.T) {
	m := &rf95Model{}
	m.reg[rfRegVersion] = 0x12
	h := newFakeHAL(m.xfer, false)
	r, err := newRadio(h, Board{Module: ModuleRF95}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h.mu.Lock()
	m.reg[rfRegModemStat] = 0x01
	h.mu.Unlock()
	if busy, err := r.ChannelBusy(t.Context()); !busy || err != nil {
		t.Fatalf("ChannelBusy = %v, %v", busy, err)
	}
	if d := r.Diagnostics(); len(d) != 2 {
		t.Fatalf("Diagnostics %q", d)
	}
}

// ------------------------------------------------------------------------------------ SX1280

func newSX1280(t *testing.T) (*sx128x, *sx1280Model) {
	t.Helper()
	m := newSX1280Model()
	c := &sx128x{bus: bus{h: newFakeHAL(m.xfer, true)}, board: Board{Module: ModuleSX1280}}
	if err := c.init(); err != nil {
		t.Fatal(err)
	}
	return c, m
}

func TestSX128xConfigure(t *testing.T) {
	c, m := newSX1280(t)
	for bw, code := range map[uint32]byte{203_125: 0x34, 406_250: 0x26, 812_500: 0x18, 1_625_000: 0x0A} {
		cfg := wide
		cfg.BandwidthHz = bw
		if err := c.configure(cfg, 0); err != nil {
			t.Fatal(err)
		}
		if got := m.cmds[s8CmdSetModulation][1]; got != code {
			t.Errorf("%d Hz: 0x%02x, want 0x%02x", bw, got, code)
		}
	}
	for sf, want := range map[uint8]byte{5: 0x1E, 6: 0x1E, 7: 0x37, 8: 0x37, 9: 0x32, 12: 0x32} {
		cfg := wide
		cfg.SF = sf
		if err := c.configure(cfg, -18); err != nil {
			t.Fatal(err)
		}
		if m.regs[s8RegLoRaSFConfig] != want || m.regs[s8RegFreqErrorCorr]&1 == 0 || m.cmds[s8CmdSetTxParams][0] != 0 {
			t.Errorf("SF%d: SF config 0x%02x", sf, m.regs[s8RegLoRaSFConfig])
		}
	}
	rejects := []radio.Config{
		{FrequencyHz: 2_403_000_000, BandwidthHz: 500_000, SF: 7, CR: 5},
		{FrequencyHz: 2_399_000_000, BandwidthHz: 812_500, SF: 7, CR: 5},
		{FrequencyHz: 2_501_000_000, BandwidthHz: 812_500, SF: 7, CR: 5},
	}
	for _, cfg := range rejects {
		if err := c.configure(cfg, 0); !errors.Is(err, radio.ErrUnsupported) {
			t.Errorf("%+v: %v", cfg, err)
		}
	}
}

func TestSX128xPreamble(t *testing.T) {
	cases := map[uint16]byte{0: 0x11, 2: 0x11, 3: 0x12, 12: 0x16, 16: 0x18, 30: 0x1F, 31: 0x28, 32: 0x28, 60000: 0xCF}
	for n, want := range cases {
		got := sx128xPreamble(n)
		if got != want {
			t.Errorf("preamble %d: 0x%02x, want 0x%02x", n, got, want)
		}
		if m, e := uint(got&0x0F), uint(got>>4); m<<e < uint(n) {
			t.Errorf("preamble %d encoded as %d", n, m<<e)
		}
	}
}

func TestSX128xInitRejects(t *testing.T) {
	m := &sx1280Model{regs: map[uint16]byte{s8RegVersionString: 'X'}, cmds: map[byte][]byte{}}
	c := &sx128x{bus: bus{h: newFakeHAL(m.xfer, true)}}
	if err := c.init(); err == nil || !strings.Contains(err.Error(), `version string "X"`) {
		t.Fatalf("init = %v", err)
	}
}

func TestSX128xReceiveDetails(t *testing.T) {
	c, m := newSX1280(t)
	if ev, err := c.events(); ev != 0 || err != nil {
		t.Fatalf("idle events = %v, %v", ev, err)
	}
	if data, _, _, err := c.packet(); data != nil || err != nil {
		t.Fatalf("empty packet = %v, %v", data, err)
	}
	// Negative SNR raises the reported RSSI (RadioLib's getRSSI).
	copy(m.buf[:], "q")
	m.rxLen, m.pkt = 1, [2]byte{180, 0xF0} // -90 dBm, SNR -4
	data, rssi, snr, err := c.packet()
	if err != nil || string(data) != "q" || snr != -4 || rssi != -86 {
		t.Fatalf("packet = %q %d %g %v", data, rssi, snr, err)
	}
	if v, err := c.rssi(); v != 0 || err != nil {
		t.Fatalf("rssi = %d, %v", v, err)
	}
	m.irq = s8IrqPreamble | s8IrqHeaderValid | s8IrqTimeout
	if ev, err := c.events(); err != nil || ev != evPreamble|evHeaderValid|evTimeout || m.irq != 0 {
		t.Fatalf("events = %v, %v", ev, err)
	}
}

func TestSX128xDiagnostics(t *testing.T) {
	c, m := newSX1280(t)
	m.status = 5 << 5
	if got := strings.Join(c.diagnostics(), "\n"); got != "version:    SX1280 V3B\nstatus:     0xa0 (RX)" {
		t.Fatalf("diagnostics %q", got)
	}
	c.h = &flakyHAL{hal: c.h, failAt: 0}
	if got := c.diagnostics(); len(got) != 1 {
		t.Fatalf("diagnostics after failure %q", got)
	}
}

// ------------------------------------------------------------------------------------ LR11x0

func newLR(t *testing.T, b Board, m *lr1121Model) *lr11x0 {
	t.Helper()
	if m.cmds == nil {
		m.cmds = map[uint16][]byte{}
	}
	if b.Module == "" {
		b.Module = ModuleLR1121
	}
	return &lr11x0{bus: bus{h: newFakeHAL(m.xfer, true)}, board: b, log: t.Logf}
}

func TestLRCheckVersion(t *testing.T) {
	cases := []struct {
		name    string
		module  string
		version []byte
		want    string // error, or log line
	}{
		{"bootloader", ModuleLR1121, []byte{0x22, lrDeviceBoot, 0, 0}, "bootloader"},
		{"not an LR11x0", ModuleLR1121, []byte{0, 0x42, 0, 0}, "didn't answer as an LR11x0"},
		{"mismatch", ModuleLR1110, []byte{0x22, lrDeviceLR1120, 0, 0}, "board says lr1110 but the chip reports device 0x02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &lr1121Model{version: tc.version}
			logs := &logLines{}
			l := newLR(t, Board{Module: tc.module}, m)
			l.log = logs.logf
			err := l.init()
			got := strings.Join(logs.lines, "\n")
			if err != nil {
				got = err.Error()
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("init = %v, logs %q", err, logs.lines)
			}
		})
	}
}

func TestLRTCXOClearsXOSCError(t *testing.T) {
	m := &lr1121Model{errs: []byte{0, 0x20}}
	l := newLR(t, Board{TCXOVolt: 3.3}, m)
	if err := l.init(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.cmds[lrCmdClearErrors]; !ok {
		t.Fatal("HF XOSC error not cleared")
	}
	if got := m.cmds[lrCmdSetTcxoMode]; got[0] != 7 {
		t.Fatalf("TCXO %x", got)
	}
	if got := m.cmds[lrCmdSetDioAsRfSwitch]; got != nil {
		t.Fatalf("RF switch set without a table: %x", got)
	}
}

func TestLRDIOIndex(t *testing.T) {
	for name, want := range map[string]int{"DIO5": 0, "dio6": 1, "DIO7": 2, "DIO8": 3, "DIO10": 4, "DIO9": -1} {
		if got := lrDIOIndex(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
}

func TestLRPowerRange(t *testing.T) {
	l := &lr11x0{board: Board{MaxPower: 20, MaxPowerHF: 10}}
	if lo, hi := l.powerRange(868_000_000); lo != -17 || hi != 20 {
		t.Errorf("sub-GHz range %d..%d", lo, hi)
	}
	if lo, hi := l.powerRange(2_400_000_000); lo != -18 || hi != 10 {
		t.Errorf("2.4 GHz range %d..%d", lo, hi)
	}
}

func TestLRConfigure24GHz(t *testing.T) {
	m := &lr1121Model{}
	l := newLR(t, Board{}, m)
	if err := l.init(); err != nil {
		t.Fatal(err)
	}
	if err := l.configure(wide, 13); err != nil {
		t.Fatal(err)
	}
	if got := m.cmds[lrCmdSetModulation]; !bytes.Equal(got, []byte{11, 0x0F, 1, 1}) {
		t.Errorf("modulation %x", got)
	}
	if got := m.cmds[lrCmdSetPaConfig]; got[0] != 0x02 || got[1] != 0x00 {
		t.Errorf("PA %x", got)
	}
	if _, ok := m.cmds[lrCmdCalibImage]; ok {
		t.Error("image calibration at 2.4 GHz")
	}
	// Low power sub-GHz: the LP PA. A small move doesn't recalibrate.
	c := longFast
	if err := l.configure(c, 10); err != nil {
		t.Fatal(err)
	}
	delete(m.cmds, lrCmdCalibImage)
	c.FrequencyHz += 5_000_000
	if err := l.configure(c, 10); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.cmds[lrCmdCalibImage]; ok {
		t.Error("recalibrated for a 5 MHz move")
	}
	if got := m.cmds[lrCmdSetPaConfig]; got[0] != 0x00 || got[1] != 0x00 {
		t.Errorf("PA %x", got)
	}
}

func TestLRConfigureRejects(t *testing.T) {
	cases := []struct {
		name   string
		module string
		cfg    radio.Config
		want   string
	}{
		{"LR1110 at 2.4 GHz", ModuleLR1110, wide, "no 2.4 GHz radio"},
		{"out of band", ModuleLR1121, radio.Config{FrequencyHz: 1_000_000_000, BandwidthHz: 125_000, SF: 7, CR: 5}, "outside"},
		{"sub-GHz bandwidth", ModuleLR1121, radio.Config{FrequencyHz: 868_000_000, BandwidthHz: 812_500, SF: 7, CR: 5}, "bandwidth 812500"},
		{"2.4 GHz bandwidth", ModuleLR1120, radio.Config{FrequencyHz: 2_450_000_000, BandwidthHz: 125_000, SF: 7, CR: 5}, "bandwidth 125000"},
	}
	for _, tc := range cases {
		l := newLR(t, Board{Module: tc.module}, &lr1121Model{})
		err := l.configure(tc.cfg, 10)
		if !errors.Is(err, radio.ErrUnsupported) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// 1.9–2.2 GHz is an LR1120/LR1121 band too.
	l := newLR(t, Board{Module: ModuleLR1120}, &lr1121Model{})
	if err := l.configure(radio.Config{FrequencyHz: 2_000_000_000, BandwidthHz: 203_125, SF: 7, CR: 5}, 5); err != nil {
		t.Fatalf("2 GHz: %v", err)
	}
}

func TestLRReceiveDetails(t *testing.T) {
	m := &lr1121Model{}
	l := newLR(t, Board{}, m)
	if ev, err := l.events(); ev != 0 || err != nil {
		t.Fatalf("idle events = %v, %v", ev, err)
	}
	m.rx = []byte{}
	if data, _, _, err := l.packet(); data != nil || err != nil {
		t.Fatalf("empty packet = %v, %v", data, err)
	}
	if v, err := l.rssi(); v != 0 || err != nil {
		t.Fatalf("rssi = %d, %v", v, err)
	}
	m.irq = lrIrqPreamble | lrIrqHeaderErr | 1<<31 // an IRQ outside the mask is ignored
	if ev, err := l.events(); err != nil || ev != evPreamble|evHeaderErr {
		t.Fatalf("events = %v, %v", ev, err)
	}
}

func TestLRDiagnostics(t *testing.T) {
	m := &lr1121Model{errs: []byte{0x01, 0x21}}
	l := newLR(t, Board{}, m)
	if err := l.init(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(l.diagnostics(), "\n")
	want := "version:    LR1121, hardware 0x22, firmware 1.3\nerrors:     0x0121 (LF RC calibration, HF XOSC start (check the TCXO voltage), RX ADC offset)"
	if got != want {
		t.Fatalf("diagnostics:\n%s\nwant\n%s", got, want)
	}
	l.h = &flakyHAL{hal: l.h, failAt: 0}
	if got := l.diagnostics(); got[1] != "errors:     boom" {
		t.Fatalf("diagnostics after failure %q", got)
	}
}
