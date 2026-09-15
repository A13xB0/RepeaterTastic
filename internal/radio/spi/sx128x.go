package spi

import (
	"fmt"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// SX1280 (2.4 GHz) commands and registers (SX1280 datasheet section 11; RadioLib SX128x).
const (
	s8CmdGetStatus         = 0xC0
	s8CmdWriteRegister     = 0x18
	s8CmdReadRegister      = 0x19
	s8CmdWriteBuffer       = 0x1A
	s8CmdReadBuffer        = 0x1B
	s8CmdSetStandby        = 0x80
	s8CmdSetTx             = 0x83
	s8CmdSetRx             = 0x82
	s8CmdSetPacketType     = 0x8A
	s8CmdSetRfFrequency    = 0x86
	s8CmdSetTxParams       = 0x8E
	s8CmdSetCadParams      = 0x88
	s8CmdSetBufferBase     = 0x8F
	s8CmdSetModulation     = 0x8B
	s8CmdSetPacketParams   = 0x8C
	s8CmdGetRxBufferStatus = 0x17
	s8CmdGetPacketStatus   = 0x1D
	s8CmdGetRssiInst       = 0x1F
	s8CmdSetDioIrqParams   = 0x8D
	s8CmdGetIrqStatus      = 0x15
	s8CmdClearIrqStatus    = 0x97
	s8CmdSetRegulatorMode  = 0x96

	s8RegVersionString   = 0x01F0
	s8RegLoRaSFConfig    = 0x0925
	s8RegFreqErrorCorr   = 0x093C
	s8RegLoRaSyncWordMsb = 0x0944

	s8IrqTxDone      = 0x0001
	s8IrqRxDone      = 0x0002
	s8IrqHeaderValid = 0x0010
	s8IrqHeaderErr   = 0x0020
	s8IrqCrcErr      = 0x0040
	s8IrqTimeout     = 0x4000
	s8IrqPreamble    = 0x8000
)

type sx128x struct {
	bus
	board    Board
	preamble byte // mantissa/exponent form
	version  string
}

func (s *sx128x) readRegister(addr uint16, n int) ([]byte, error) {
	rx, err := s.transfer(append([]byte{s8CmdReadRegister, byte(addr >> 8), byte(addr), 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, err
	}
	return rx[4:], nil
}

func (s *sx128x) writeRegister(addr uint16, data ...byte) error {
	return s.cmd(s8CmdWriteRegister, append([]byte{byte(addr >> 8), byte(addr)}, data...)...)
}

func (s *sx128x) init() error {
	if err := s.waitBusy(time.Second); err != nil {
		return fmt.Errorf("after reset: %w (check the Busy and Reset pins)", err)
	}
	if err := s.cmd(s8CmdSetStandby, 0x00); err != nil {
		return err
	}
	// The firmware version string starts "SX1280": proof that SPI, CS and BUSY work.
	v, err := s.readRegister(s8RegVersionString, 16)
	if err != nil {
		return err
	}
	s.version = strings.TrimRight(string(v), "\x00\xff")
	if !strings.HasPrefix(s.version, "SX1280") {
		return fmt.Errorf("the chip didn't answer as an SX1280 (version string %q): check spidev, CS and wiring", s.version)
	}
	for _, c := range [][]byte{
		{s8CmdSetBufferBase, 0x00, 0x00},
		{s8CmdSetPacketType, 0x01}, // LoRa
		{s8CmdSetCadParams, 0x60},  // 8 symbols
		{s8CmdSetRegulatorMode, 0x01},
	} {
		if err := s.cmd(c[0], c[1:]...); err != nil {
			return err
		}
	}
	mask := uint16(s8IrqTxDone | s8IrqRxDone | s8IrqHeaderValid | s8IrqHeaderErr | s8IrqCrcErr | s8IrqTimeout | s8IrqPreamble)
	return s.cmd(s8CmdSetDioIrqParams, byte(mask>>8), byte(mask), byte(mask>>8), byte(mask), 0, 0, 0, 0)
}

func (s *sx128x) powerRange(uint32) (int, int) { return -18, boardLimit(13, s.board.MaxPower) }

func (s *sx128x) configure(c radio.Config, power int) error {
	var bw byte
	switch c.BandwidthHz {
	case 203_125:
		bw = 0x34
	case 406_250:
		bw = 0x26
	case 812_500:
		bw = 0x18
	case 1_625_000:
		bw = 0x0A
	default:
		return fmt.Errorf("%w: bandwidth %d Hz on SX1280 (203.125, 406.25, 812.5 or 1625 kHz)", radio.ErrUnsupported, c.BandwidthHz)
	}
	if c.FrequencyHz < 2_400_000_000 || c.FrequencyHz > 2_500_000_000 {
		return fmt.Errorf("%w: %d Hz is outside the SX1280's 2.4 GHz band", radio.ErrUnsupported, c.FrequencyHz)
	}
	frf := uint32(uint64(c.FrequencyHz) * (1 << 18) / 52_000_000)
	if err := s.cmd(s8CmdSetRfFrequency, byte(frf>>16), byte(frf>>8), byte(frf)); err != nil {
		return err
	}
	if err := s.cmd(s8CmdSetModulation, c.SF<<4, bw, c.CR-4); err != nil {
		return err
	}
	// Datasheet 14.4.1: SF-dependent register, and frequency error compensation on.
	sfCfg := byte(0x32)
	switch {
	case c.SF <= 6:
		sfCfg = 0x1E
	case c.SF <= 8:
		sfCfg = 0x37
	}
	if err := s.writeRegister(s8RegLoRaSFConfig, sfCfg); err != nil {
		return err
	}
	fe, err := s.readRegister(s8RegFreqErrorCorr, 1)
	if err != nil {
		return err
	}
	if err := s.writeRegister(s8RegFreqErrorCorr, fe[0]|0x01); err != nil {
		return err
	}
	s.preamble = sx128xPreamble(c.Preamble)
	if err := s.packetParams(0xFF); err != nil {
		return err
	}
	if err := s.writeRegister(s8RegLoRaSyncWordMsb, syncWordPair(c.SyncWord)...); err != nil {
		return err
	}
	return s.cmd(s8CmdSetTxParams, byte(power+18), 0x80) // 10 µs ramp
}

// sx128xPreamble encodes a preamble length as the SX1280's mantissa × 2^exponent, rounding up.
func sx128xPreamble(n uint16) byte {
	for e := uint(1); e <= 15; e++ {
		for m := uint(1); m <= 15; m++ {
			if m<<e >= uint(n) {
				return byte(e<<4 | m)
			}
		}
	}
	return 0xFF
}

func (s *sx128x) packetParams(length byte) error {
	// Explicit header, CRC on, standard IQ.
	return s.cmd(s8CmdSetPacketParams, s.preamble, 0x00, length, 0x20, 0x40, 0x00, 0x00)
}

func (s *sx128x) standby() error { return s.cmd(s8CmdSetStandby, 0x00) }

func (s *sx128x) startRx() error {
	if err := s.packetParams(0xFF); err != nil {
		return err
	}
	if err := s.cmd(s8CmdClearIrqStatus, 0xFF, 0xFF); err != nil {
		return err
	}
	return s.cmd(s8CmdSetRx, 0x00, 0xFF, 0xFF) // continuous
}

func (s *sx128x) transmit(frame []byte) error {
	if err := s.packetParams(byte(len(frame))); err != nil {
		return err
	}
	if err := s.cmd(s8CmdWriteBuffer, append([]byte{0x00}, frame...)...); err != nil {
		return err
	}
	if err := s.cmd(s8CmdClearIrqStatus, 0xFF, 0xFF); err != nil {
		return err
	}
	return s.cmd(s8CmdSetTx, 0x00, 0x00, 0x00)
}

func (s *sx128x) events() (event, error) {
	v, err := s.get(s8CmdGetIrqStatus, 2)
	if err != nil {
		return 0, err
	}
	flags := uint16(v[0])<<8 | uint16(v[1])
	if flags == 0 {
		return 0, nil
	}
	if err := s.cmd(s8CmdClearIrqStatus, byte(flags>>8), byte(flags)); err != nil {
		return 0, err
	}
	return mapEvents(uint32(flags), map[uint32]event{s8IrqTxDone: evTxDone, s8IrqRxDone: evRxDone, s8IrqPreamble: evPreamble,
		s8IrqHeaderValid: evHeaderValid, s8IrqHeaderErr: evHeaderErr, s8IrqCrcErr: evCrcErr, s8IrqTimeout: evTimeout}), nil
}

func (s *sx128x) packet() ([]byte, int16, float32, error) {
	st, err := s.get(s8CmdGetRxBufferStatus, 2)
	if err != nil {
		return nil, 0, 0, err
	}
	n, start := st[0], st[1]
	if n == 0 {
		return nil, 0, 0, nil
	}
	rx, err := s.transfer(append([]byte{s8CmdReadBuffer, start, 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, 0, 0, err
	}
	ps, err := s.get(s8CmdGetPacketStatus, 5)
	if err != nil {
		return nil, 0, 0, err
	}
	snr := float32(int8(ps[1])) / 4
	rssi := -int16(ps[0]) / 2
	if snr <= 0 { // RadioLib SX128x::getRSSI
		rssi -= int16(snr)
	}
	return rx[3:], rssi, snr, nil
}

func (s *sx128x) rssi() (int16, error) {
	v, err := s.get(s8CmdGetRssiInst, 1)
	if err != nil {
		return 0, err
	}
	return -int16(v[0]) / 2, nil
}

func (s *sx128x) diagnostics() []string {
	out := []string{"version:    " + s.version}
	if rx, err := s.transfer([]byte{s8CmdGetStatus}); err == nil {
		modes := map[byte]string{2: "standby RC", 3: "standby XOSC", 4: "FS", 5: "RX", 6: "TX"}
		out = append(out, fmt.Sprintf("status:     0x%02x (%s)", rx[0], modes[rx[0]>>5&7]))
	}
	return out
}
