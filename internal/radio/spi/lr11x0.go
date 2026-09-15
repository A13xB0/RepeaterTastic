package spi

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// LR1110/LR1120/LR1121 commands (LR11xx user manual; RadioLib LR11x0).
const (
	lrCmdGetVersion        = 0x0101
	lrCmdGetErrors         = 0x010D
	lrCmdClearErrors       = 0x010E
	lrCmdCalibrate         = 0x010F
	lrCmdSetRegMode        = 0x0110
	lrCmdCalibImage        = 0x0111
	lrCmdSetDioAsRfSwitch  = 0x0112
	lrCmdSetDioIrqParams   = 0x0113
	lrCmdClearIrq          = 0x0114
	lrCmdSetTcxoMode       = 0x0117
	lrCmdSetStandby        = 0x011C
	lrCmdWriteBuffer       = 0x0109
	lrCmdReadBuffer        = 0x010A
	lrCmdClearRxBuffer     = 0x010B
	lrCmdGetRxBufferStatus = 0x0203
	lrCmdGetPacketStatus   = 0x0204
	lrCmdGetRssiInst       = 0x0205
	lrCmdSetRx             = 0x0209
	lrCmdSetTx             = 0x020A
	lrCmdSetRfFrequency    = 0x020B
	lrCmdSetPacketType     = 0x020E
	lrCmdSetModulation     = 0x020F
	lrCmdSetPacketParams   = 0x0210
	lrCmdSetTxParams       = 0x0211
	lrCmdSetRxTxFallback   = 0x0213
	lrCmdSetPaConfig       = 0x0215
	lrCmdSetRxBoosted      = 0x0227
	lrCmdSetLoRaSyncWord   = 0x022B

	lrIrqTxDone      = 1 << 2
	lrIrqRxDone      = 1 << 3
	lrIrqPreamble    = 1 << 4
	lrIrqHeaderValid = 1 << 5
	lrIrqHeaderErr   = 1 << 6
	lrIrqCrcErr      = 1 << 7
	lrIrqTimeout     = 1 << 10
	lrIrqAll         = 0x1BF80FFC

	lrDeviceLR1110 = 0x01
	lrDeviceLR1120 = 0x02
	lrDeviceLR1121 = 0x03
	lrDeviceBoot   = 0xDF
)

type lr11x0 struct {
	bus
	board    Board
	log      func(string, ...any)
	preamble uint16
	calFreq  uint32 // frequency of the last image calibration
	version  [4]byte
}

// write runs a command with no response.
func (l *lr11x0) write(op uint16, args ...byte) error {
	_, err := l.transfer(append([]byte{byte(op >> 8), byte(op)}, args...))
	if err != nil {
		return err
	}
	return l.waitBusy(100 * time.Millisecond)
}

// read runs a command, then reads the response in a second transaction: status, then n bytes.
func (l *lr11x0) read(op uint16, n int, args ...byte) ([]byte, error) {
	if err := l.write(op, args...); err != nil {
		return nil, err
	}
	rx, err := l.transfer(make([]byte, 1+n))
	if err != nil {
		return nil, err
	}
	return rx[1:], nil
}

func be32(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }

func (l *lr11x0) init() error {
	if err := l.waitBusy(time.Second); err != nil {
		return fmt.Errorf("after reset: %w (check the Busy and Reset pins)", err)
	}
	v, err := l.read(lrCmdGetVersion, 4)
	if err != nil {
		return err
	}
	copy(l.version[:], v)
	want := map[string]byte{ModuleLR1110: lrDeviceLR1110, ModuleLR1120: lrDeviceLR1120, ModuleLR1121: lrDeviceLR1121}[l.board.Module]
	switch v[1] {
	case lrDeviceLR1110, lrDeviceLR1120, lrDeviceLR1121:
		if v[1] != want {
			l.log("lr11x0: board says %s but the chip reports device 0x%02x", l.board.Module, v[1])
		}
	case lrDeviceBoot:
		return fmt.Errorf("the LR11x0 is in its bootloader (no transceiver firmware): flash it with meshtasticd or Semtech's updater first")
	default:
		return fmt.Errorf("the chip didn't answer as an LR11x0 (version %x): check spidev, CS and wiring", v)
	}
	if err := l.write(lrCmdSetStandby, 0x00); err != nil {
		return err
	}
	if l.board.TCXOVolt > 0 {
		if e, err := l.read(lrCmdGetErrors, 2); err == nil && e[1]&0x20 != 0 { // HF XOSC start
			_ = l.write(lrCmdClearErrors)
		}
		// 5 ms start-up in 30.52 µs steps, as RadioLib's default.
		if err := l.write(lrCmdSetTcxoMode, tcxoCode(l.board.TCXOVolt), 0x00, 0x00, 163); err != nil {
			return fmt.Errorf("TCXO: %w", err)
		}
	}
	steps := []struct {
		op   uint16
		args []byte
	}{
		{lrCmdSetRxTxFallback, []byte{0x01}}, // standby RC after RX/TX
		{lrCmdClearIrq, be32(lrIrqAll)},
		{lrCmdSetDioIrqParams, make([]byte, 8)},
		{lrCmdCalibrate, []byte{0x3F}},
	}
	for _, st := range steps {
		if err := l.write(st.op, st.args...); err != nil {
			return err
		}
	}
	time.Sleep(5 * time.Millisecond)
	if err := l.waitBusy(time.Second); err != nil {
		return fmt.Errorf("calibrate: %w", err)
	}
	if err := l.write(lrCmdSetPacketType, 0x02); err != nil { // LoRa
		return err
	}
	if err := l.write(lrCmdSetRegMode, 0x01); err != nil { // DC-DC, as meshtasticd
		return err
	}
	if t := l.board.RFSwitch; t != nil {
		if err := l.write(lrCmdSetDioAsRfSwitch, lrRFSwitchArgs(t)...); err != nil {
			return err
		}
	}
	if err := l.write(lrCmdSetRxBoosted, 0x01); err != nil {
		return err
	}
	mask := uint32(lrIrqTxDone | lrIrqRxDone | lrIrqPreamble | lrIrqHeaderValid | lrIrqHeaderErr | lrIrqCrcErr | lrIrqTimeout)
	return l.write(lrCmdSetDioIrqParams, append(be32(mask), 0, 0, 0, 0)...)
}

// lrDIOIndex is an rfswitch_table pin's bit: DIO5 = 0 … DIO8 = 3, DIO10 = 4.
func lrDIOIndex(name string) int {
	switch strings.ToUpper(name) {
	case "DIO5":
		return 0
	case "DIO6":
		return 1
	case "DIO7":
		return 2
	case "DIO8":
		return 3
	case "DIO10":
		return 4
	}
	return -1
}

// lrRFSwitchArgs builds SetDioAsRfSwitch from an rfswitch_table, as RadioLib's
// LR11x0::setRfSwitchTable: an enable mask of DIOs, then one level mask per mode.
func lrRFSwitchArgs(t *RFSwitchTable) []byte {
	var enable byte
	for _, p := range t.Pins {
		enable |= 1 << lrDIOIndex(p)
	}
	args := []byte{enable}
	for _, mode := range []string{"MODE_STBY", "MODE_RX", "MODE_TX", "MODE_TX_HP", "MODE_TX_HF", "MODE_GNSS", "MODE_WIFI"} {
		var m byte
		for j, high := range t.Modes[mode] {
			if high && j < len(t.Pins) {
				m |= 1 << j
			}
		}
		args = append(args, m)
	}
	return args
}

func (l *lr11x0) powerRange(freqHz uint32) (int, int) {
	if freqHz > 1_000_000_000 {
		return -18, boardLimit(13, l.board.MaxPowerHF)
	}
	return -17, boardLimit(22, l.board.MaxPower)
}

func (l *lr11x0) configure(c radio.Config, power int) error {
	high := c.FrequencyHz > 1_000_000_000
	if l.board.Module == ModuleLR1110 && high {
		return fmt.Errorf("%w: the LR1110 has no 2.4 GHz radio", radio.ErrUnsupported)
	}
	mhz := float64(c.FrequencyHz) / 1e6
	if !(mhz >= 150 && mhz <= 960) && !(mhz >= 1900 && mhz <= 2200) && !(mhz >= 2400 && mhz <= 2500) {
		return fmt.Errorf("%w: %.3f MHz is outside the LR11x0's bands", radio.ErrUnsupported, mhz)
	}
	bw, ok := map[uint32]byte{62_500: 0x03, 125_000: 0x04, 250_000: 0x05, 500_000: 0x06}[c.BandwidthHz]
	if high {
		bw, ok = map[uint32]byte{203_125: 0x0D, 406_250: 0x0E, 812_500: 0x0F}[c.BandwidthHz]
	}
	if !ok {
		return fmt.Errorf("%w: bandwidth %d Hz on LR11x0 at %.3f MHz", radio.ErrUnsupported, c.BandwidthHz, mhz)
	}
	if diff := int64(c.FrequencyHz) - int64(l.calFreq); l.calFreq == 0 || diff >= 20_000_000 || diff <= -20_000_000 {
		// Image rejection calibration over ±4 MHz, in 4 MHz steps (RadioLib calibrateImageRejection).
		lo, hi := math.Floor((mhz-4-1)/4), math.Ceil((mhz+4+1)/4)
		if hi > 255 { // 2.4 GHz: the chip calibrates itself; the command takes sub-GHz steps only
			lo, hi = 0, 0
		}
		if hi > 0 {
			if err := l.write(lrCmdCalibImage, byte(lo), byte(hi)); err != nil {
				return err
			}
			if err := l.waitBusy(time.Second); err != nil {
				return fmt.Errorf("image calibration: %w", err)
			}
		}
		l.calFreq = c.FrequencyHz
	}
	if err := l.write(lrCmdSetRfFrequency, be32(c.FrequencyHz)...); err != nil {
		return err
	}
	lowRate := ldro(c)
	if high && c.SF > 10 { // RadioLib: SX128x-compatible LDRO at 2.4 GHz
		lowRate = 1
	}
	if err := l.write(lrCmdSetModulation, c.SF, bw, c.CR-4, lowRate); err != nil {
		return err
	}
	l.preamble = c.Preamble
	if err := l.packetParams(0xFF); err != nil {
		return err
	}
	if err := l.write(lrCmdSetLoRaSyncWord, c.SyncWord); err != nil {
		return err
	}
	// PA: the HF PA at 2.4 GHz; the high-power PA (from VBAT) above 14 dBm; otherwise low power.
	paSel, supply := byte(0x00), byte(0x00)
	switch {
	case high:
		paSel = 0x02
	case power > 14:
		paSel, supply = 0x01, 0x01
	}
	if err := l.write(lrCmdSetPaConfig, paSel, supply, 0x04, 0x07); err != nil {
		return err
	}
	return l.write(lrCmdSetTxParams, byte(int8(power)), 0x02) // 48 µs ramp
}

func (l *lr11x0) packetParams(length byte) error {
	// Explicit header, CRC on, standard IQ.
	return l.write(lrCmdSetPacketParams, byte(l.preamble>>8), byte(l.preamble), 0x00, length, 0x01, 0x00)
}

func (l *lr11x0) standby() error { return l.write(lrCmdSetStandby, 0x00) }

func (l *lr11x0) startRx() error {
	if err := l.packetParams(0xFF); err != nil {
		return err
	}
	if err := l.write(lrCmdClearIrq, be32(lrIrqAll)...); err != nil {
		return err
	}
	return l.write(lrCmdSetRx, 0xFF, 0xFF, 0xFF) // continuous
}

func (l *lr11x0) transmit(frame []byte) error {
	if err := l.packetParams(byte(len(frame))); err != nil {
		return err
	}
	if err := l.write(lrCmdWriteBuffer, frame...); err != nil {
		return err
	}
	if err := l.write(lrCmdClearIrq, be32(lrIrqAll)...); err != nil {
		return err
	}
	return l.write(lrCmdSetTx, 0x00, 0x00, 0x00)
}

func (l *lr11x0) events() (event, error) {
	// A bare NOP transaction returns the two status bytes and the 32-bit IRQ status.
	rx, err := l.transfer(make([]byte, 6))
	if err != nil {
		return 0, err
	}
	flags := uint32(rx[2])<<24 | uint32(rx[3])<<16 | uint32(rx[4])<<8 | uint32(rx[5])
	flags &= lrIrqAll
	if flags == 0 {
		return 0, nil
	}
	if err := l.write(lrCmdClearIrq, be32(flags)...); err != nil {
		return 0, err
	}
	return mapEvents(flags, map[uint32]event{lrIrqTxDone: evTxDone, lrIrqRxDone: evRxDone, lrIrqPreamble: evPreamble,
		lrIrqHeaderValid: evHeaderValid, lrIrqHeaderErr: evHeaderErr, lrIrqCrcErr: evCrcErr, lrIrqTimeout: evTimeout}), nil
}

func (l *lr11x0) packet() ([]byte, int16, float32, error) {
	st, err := l.read(lrCmdGetRxBufferStatus, 2)
	if err != nil {
		return nil, 0, 0, err
	}
	n, start := st[0], st[1]
	if n == 0 {
		return nil, 0, 0, nil
	}
	data, err := l.read(lrCmdReadBuffer, int(n), start, n)
	if err != nil {
		return nil, 0, 0, err
	}
	ps, err := l.read(lrCmdGetPacketStatus, 3)
	if err != nil {
		return nil, 0, 0, err
	}
	_ = l.write(lrCmdClearRxBuffer)
	return data, -int16(ps[0]) / 2, float32(int8(ps[1])) / 4, nil
}

func (l *lr11x0) rssi() (int16, error) {
	v, err := l.read(lrCmdGetRssiInst, 1)
	if err != nil {
		return 0, err
	}
	return -int16(v[0]) / 2, nil
}

func (l *lr11x0) diagnostics() []string {
	devices := map[byte]string{lrDeviceLR1110: "LR1110", lrDeviceLR1120: "LR1120", lrDeviceLR1121: "LR1121"}
	out := []string{fmt.Sprintf("version:    %s, hardware 0x%02x, firmware %d.%d", devices[l.version[1]], l.version[0], l.version[2], l.version[3])}
	e, err := l.read(lrCmdGetErrors, 2)
	if err != nil {
		return append(out, "errors:     "+err.Error())
	}
	names := []string{"LF RC calibration", "HF RC calibration", "ADC calibration", "PLL calibration", "image calibration",
		"HF XOSC start (check the TCXO voltage)", "LF XOSC start", "PLL lock", "RX ADC offset"}
	v := uint32(e[0])<<8 | uint32(e[1])
	return append(out, fmt.Sprintf("errors:     0x%04x%s", v, flagNames(v, names)))
}
