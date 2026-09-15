package spi

import (
	"fmt"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// SX1262/SX1268/LLCC68 commands (SX1261/2 datasheet, section 13).
const (
	cmdSetStandby          = 0x80
	cmdSetRx               = 0x82
	cmdSetTx               = 0x83
	cmdSetRfFrequency      = 0x86
	cmdSetPacketType       = 0x8A
	cmdSetModulationParams = 0x8B
	cmdSetPacketParams     = 0x8C
	cmdSetTxParams         = 0x8E
	cmdSetBufferBaseAddr   = 0x8F
	cmdSetPaConfig         = 0x95
	cmdSetRegulatorMode    = 0x96
	cmdSetDIO3AsTcxoCtrl   = 0x97
	cmdCalibrateImage      = 0x98
	cmdCalibrate           = 0x89
	cmdSetDIO2AsRfSwitch   = 0x9D
	cmdSetDioIrqParams     = 0x08
	cmdClearIrqStatus      = 0x02
	cmdClearDeviceErrors   = 0x07
	cmdWriteRegister       = 0x0D
	cmdWriteBuffer         = 0x0E
	cmdGetIrqStatus        = 0x12
	cmdGetRxBufferStatus   = 0x13
	cmdGetPacketStatus     = 0x14
	cmdGetRssiInst         = 0x15
	cmdGetDeviceErrors     = 0x17
	cmdReadRegister        = 0x1D
	cmdReadBuffer          = 0x1E
	cmdGetStatus           = 0xC0
)

// SX126x registers.
const (
	regSyncWord        = 0x0740
	regIQPolarity      = 0x0736 // errata 15.4
	regTxModulation    = 0x0889 // errata 15.1
	regTxClampConfig   = 0x08D8 // errata 15.2
	regOCPConfig       = 0x08E7
	regRxGain          = 0x08AC
	rxGainBoosted      = 0x96
	loraSyncWordReset1 = 0x14 // the LoRa sync word register after reset: 0x1424
	loraSyncWordReset2 = 0x24
)

// SX126x IRQ bits.
const (
	irqTxDone        = 1 << 0
	irqRxDone        = 1 << 1
	irqPreamble      = 1 << 2
	irqSyncWordValid = 1 << 3
	irqHeaderValid   = 1 << 4
	irqHeaderErr     = 1 << 5
	irqCrcErr        = 1 << 6
	irqTimeout       = 1 << 9
)

type sx126x struct {
	bus
	board     Board
	log       func(string, ...any)
	imageBand [2]byte
	preamble  uint16
}

func (s *sx126x) init() error {
	if err := s.waitBusy(time.Second); err != nil {
		return fmt.Errorf("after reset: %w (check the Busy and Reset pins)", err)
	}
	if err := s.cmd(cmdSetStandby, 0x00); err != nil {
		return fmt.Errorf("standby: %w", err)
	}
	if err := s.cmd(cmdSetPacketType, 0x01); err != nil {
		return fmt.Errorf("LoRa packet type: %w", err)
	}
	// The LoRa sync word register reads 0x1424 after reset: proof that SPI, CS and BUSY work.
	sw, err := s.readRegister(regSyncWord, 2)
	if err != nil {
		return err
	}
	if sw[0] != loraSyncWordReset1 || sw[1] != loraSyncWordReset2 {
		return fmt.Errorf("the chip didn't answer as an SX126x (sync word register %02x%02x, want 1424): check spidev, CS and wiring", sw[0], sw[1])
	}
	if s.board.TCXOVolt > 0 {
		// 5 ms start-up (160 × 31.25 µs), then recalibrate everything with the TCXO running.
		if err := s.cmd(cmdSetDIO3AsTcxoCtrl, tcxoCode(s.board.TCXOVolt), 0x00, 0x00, 0xA0); err != nil {
			return fmt.Errorf("TCXO: %w", err)
		}
		if err := s.cmd(cmdClearDeviceErrors, 0x00, 0x00); err != nil {
			return err
		}
		if err := s.cmd(cmdCalibrate, 0x7F); err != nil {
			return fmt.Errorf("calibrate: %w", err)
		}
		if err := s.waitBusy(time.Second); err != nil {
			return fmt.Errorf("calibrate: %w", err)
		}
	}
	if err := s.cmd(cmdSetRegulatorMode, 0x01); err != nil { // DC-DC
		return err
	}
	if s.board.DIO2RF {
		if err := s.cmd(cmdSetDIO2AsRfSwitch, 0x01); err != nil {
			return err
		}
	}
	if err := s.cmd(cmdSetBufferBaseAddr, 0x00, 0x00); err != nil {
		return err
	}
	mask := uint16(irqTxDone | irqRxDone | irqPreamble | irqHeaderValid | irqHeaderErr | irqCrcErr | irqTimeout)
	if err := s.cmd(cmdSetDioIrqParams, byte(mask>>8), byte(mask), byte(mask>>8), byte(mask), 0, 0, 0, 0); err != nil {
		return err
	}
	if errs, err := s.deviceErrors(); err == nil && errs != 0 {
		s.log("sx126x: device errors after init: 0x%04x", errs)
	}
	return nil
}

func (s *sx126x) powerRange(uint32) (int, int) { return -9, boardLimit(22, s.board.MaxPower) }

func (s *sx126x) configure(c radio.Config, power int) error {
	bw, ok := sx126xBandwidth(c.BandwidthHz)
	if !ok {
		return fmt.Errorf("%w: bandwidth %d Hz", radio.ErrUnsupported, c.BandwidthHz)
	}
	if c.FrequencyHz < 150_000_000 || c.FrequencyHz > 960_000_000 {
		return fmt.Errorf("%w: %d Hz is outside the SX126x's 150–960 MHz", radio.ErrUnsupported, c.FrequencyHz)
	}
	frf := uint32(uint64(c.FrequencyHz) * (1 << 25) / 32_000_000)
	if err := s.cmd(cmdSetRfFrequency, byte(frf>>24), byte(frf>>16), byte(frf>>8), byte(frf)); err != nil {
		return err
	}
	if band := imageBand(c.FrequencyHz); band != s.imageBand {
		if err := s.cmd(cmdCalibrateImage, band[0], band[1]); err != nil {
			return err
		}
		if err := s.waitBusy(time.Second); err != nil {
			return fmt.Errorf("image calibration: %w", err)
		}
		s.imageBand = band
	}
	if err := s.cmd(cmdSetModulationParams, c.SF, bw, c.CR-4, ldro(c)); err != nil {
		return err
	}
	s.preamble = c.Preamble
	if err := s.packetParams(0xFF); err != nil {
		return err
	}
	// Errata 15.1: modulation quality at 500 kHz.
	if err := s.updateRegister(regTxModulation, func(v byte) byte {
		if c.BandwidthHz == 500_000 {
			return v &^ 0x04
		}
		return v | 0x04
	}); err != nil {
		return err
	}
	// Errata 15.4: standard IQ.
	if err := s.updateRegister(regIQPolarity, func(v byte) byte { return v | 0x04 }); err != nil {
		return err
	}
	// Sync word 0x2B becomes 0x24B4 (RadioLib setSyncWord with control bits 0x44).
	if err := s.writeRegister(regSyncWord, syncWordPair(c.SyncWord)...); err != nil {
		return err
	}
	// High-power PA, optimal for +22 dBm (datasheet table 13-21); same for SX1268 and LLCC68.
	if err := s.cmd(cmdSetPaConfig, 0x04, 0x07, 0x00, 0x01); err != nil {
		return err
	}
	if err := s.cmd(cmdSetTxParams, byte(int8(power)), 0x04); err != nil { // 200 µs ramp
		return err
	}
	// Errata 15.2: better tolerance of antenna mismatch.
	if err := s.updateRegister(regTxClampConfig, func(v byte) byte { return v | 0x1E }); err != nil {
		return err
	}
	if err := s.writeRegister(regOCPConfig, 0x38); err != nil { // 140 mA, as Meshtastic sets for SX126x
		return err
	}
	return s.writeRegister(regRxGain, rxGainBoosted)
}

func (s *sx126x) standby() error { return s.cmd(cmdSetStandby, 0x00) }

func (s *sx126x) startRx() error {
	if err := s.packetParams(0xFF); err != nil {
		return err
	}
	if err := s.cmd(cmdClearIrqStatus, 0x03, 0xFF); err != nil {
		return err
	}
	return s.cmd(cmdSetRx, 0xFF, 0xFF, 0xFF) // continuous
}

func (s *sx126x) transmit(frame []byte) error {
	if err := s.cmd(cmdWriteBuffer, append([]byte{0x00}, frame...)...); err != nil {
		return err
	}
	if err := s.packetParams(byte(len(frame))); err != nil {
		return err
	}
	if err := s.cmd(cmdClearIrqStatus, 0x03, 0xFF); err != nil {
		return err
	}
	return s.cmd(cmdSetTx, 0x00, 0x00, 0x00) // no chip timeout: the caller's context bounds it
}

func (s *sx126x) packetParams(length byte) error {
	// Explicit header, CRC on, standard IQ.
	return s.cmd(cmdSetPacketParams, byte(s.preamble>>8), byte(s.preamble), 0x00, length, 0x01, 0x00)
}

func (s *sx126x) events() (event, error) {
	v, err := s.get(cmdGetIrqStatus, 2)
	if err != nil {
		return 0, err
	}
	flags := uint16(v[0])<<8 | uint16(v[1])
	if flags == 0 {
		return 0, nil
	}
	if err := s.cmd(cmdClearIrqStatus, byte(flags>>8), byte(flags)); err != nil {
		return 0, err
	}
	return mapEvents(uint32(flags), map[uint32]event{irqTxDone: evTxDone, irqRxDone: evRxDone, irqPreamble: evPreamble,
		irqHeaderValid: evHeaderValid, irqHeaderErr: evHeaderErr, irqCrcErr: evCrcErr, irqTimeout: evTimeout}), nil
}

func (s *sx126x) packet() ([]byte, int16, float32, error) {
	st, err := s.get(cmdGetRxBufferStatus, 2)
	if err != nil {
		return nil, 0, 0, err
	}
	n, start := st[0], st[1]
	if n == 0 {
		return nil, 0, 0, nil
	}
	rx, err := s.transfer(append([]byte{cmdReadBuffer, start, 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, 0, 0, err
	}
	ps, err := s.get(cmdGetPacketStatus, 3)
	if err != nil {
		return nil, 0, 0, err
	}
	return rx[3:], -int16(ps[0]) / 2, float32(int8(ps[1])) / 4, nil
}

func (s *sx126x) rssi() (int16, error) {
	v, err := s.get(cmdGetRssiInst, 1)
	if err != nil {
		return 0, err
	}
	return -int16(v[0]) / 2, nil
}

func (s *sx126x) diagnostics() []string {
	var out []string
	rx, err := s.transfer([]byte{cmdGetStatus, 0x00})
	if err != nil {
		return []string{"status: " + err.Error()}
	}
	modes := map[byte]string{2: "standby RC", 3: "standby XOSC", 4: "FS", 5: "RX", 6: "TX"}
	mode, ok := modes[rx[1]>>4&7]
	if !ok {
		mode = fmt.Sprintf("unexpected mode %d", rx[1]>>4&7)
	}
	out = append(out, fmt.Sprintf("status:     0x%02x (%s)", rx[1], mode))
	e, err := s.deviceErrors()
	if err != nil {
		return append(out, "dev errors: "+err.Error())
	}
	names := []string{"RC64K calibration", "RC13M calibration", "PLL calibration", "ADC calibration",
		"image calibration", "XOSC start (check the TCXO voltage)", "PLL lock", "", "PA ramp"}
	return append(out, fmt.Sprintf("dev errors: 0x%04x%s", e, flagNames(uint32(e), names)))
}

func (s *sx126x) deviceErrors() (uint16, error) {
	v, err := s.get(cmdGetDeviceErrors, 2)
	if err != nil {
		return 0, err
	}
	return uint16(v[0])<<8 | uint16(v[1]), nil
}

func (s *sx126x) readRegister(addr uint16, n int) ([]byte, error) {
	rx, err := s.transfer(append([]byte{cmdReadRegister, byte(addr >> 8), byte(addr), 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, err
	}
	return rx[4:], nil
}

func (s *sx126x) writeRegister(addr uint16, data ...byte) error {
	return s.cmd(cmdWriteRegister, append([]byte{byte(addr >> 8), byte(addr)}, data...)...)
}

func (s *sx126x) updateRegister(addr uint16, fn func(byte) byte) error {
	v, err := s.readRegister(addr, 1)
	if err != nil {
		return err
	}
	return s.writeRegister(addr, fn(v[0]))
}

// ------------------------------------------------------------------------------------ shared

// ldro is whether low data rate optimisation is needed: symbols of 16 ms or more (LongSlow,
// VeryLongSlow).
func ldro(c radio.Config) byte {
	if float64(uint32(1)<<c.SF)/float64(c.BandwidthHz)*1000 >= 16 {
		return 1
	}
	return 0
}

// syncWordPair is the two-register form of a LoRa sync word on SX126x/SX128x: 0x2B → 0x24 0xB4.
func syncWordPair(sw uint8) []byte {
	return []byte{(sw & 0xF0) | 0x04, ((sw & 0x0F) << 4) | 0x04}
}

func mapEvents(flags uint32, m map[uint32]event) event {
	var ev event
	for bit, e := range m {
		if flags&bit != 0 {
			ev |= e
		}
	}
	return ev
}

func flagNames(v uint32, names []string) string {
	var out []string
	for i, n := range names {
		if v&(1<<i) != 0 && n != "" {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return " (none)"
	}
	return " (" + strings.Join(out, ", ") + ")"
}

func sx126xBandwidth(hz uint32) (byte, bool) {
	switch hz {
	case 7_800:
		return 0x00, true
	case 10_400:
		return 0x08, true
	case 15_600:
		return 0x01, true
	case 20_800:
		return 0x09, true
	case 31_250:
		return 0x02, true
	case 41_700:
		return 0x0A, true
	case 62_500:
		return 0x03, true
	case 125_000:
		return 0x04, true
	case 250_000:
		return 0x05, true
	case 500_000:
		return 0x06, true
	}
	return 0, false
}

// imageBand is the CalibrateImage pair for a frequency (datasheet table 9-2, RadioLib otherwise).
func imageBand(hz uint32) [2]byte {
	mhz := float64(hz) / 1e6
	switch {
	case mhz >= 902:
		return [2]byte{0xE1, 0xE9}
	case mhz >= 863:
		return [2]byte{0xD7, 0xDB}
	case mhz >= 779:
		return [2]byte{0xC1, 0xC5}
	case mhz >= 470:
		return [2]byte{0x75, 0x81}
	case mhz >= 430:
		return [2]byte{0x6B, 0x6F}
	}
	return [2]byte{byte((mhz - 4) / 4), byte((mhz + 4) / 4)}
}

// tcxoCode is the SX126x/LR11x0 TCXO voltage code nearest to v volts.
func tcxoCode(v float64) byte {
	codes := []float64{1.6, 1.7, 1.8, 2.2, 2.4, 2.7, 3.0, 3.3}
	best := 0
	for i, c := range codes {
		if abs(c-v) < abs(codes[best]-v) {
			best = i
		}
	}
	return byte(best)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
