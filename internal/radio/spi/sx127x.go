package spi

import (
	"fmt"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// SX1276/SX1278 (RF95) LoRa registers (SX1276 datasheet, section 4.4; RadioLib SX127x/SX1278).
const (
	rfRegFifo           = 0x00
	rfRegOpMode         = 0x01
	rfRegFrfMsb         = 0x06
	rfRegPaConfig       = 0x09
	rfRegOcp            = 0x0B
	rfRegLna            = 0x0C
	rfRegFifoAddrPtr    = 0x0D
	rfRegFifoTxBase     = 0x0E
	rfRegFifoRxBase     = 0x0F
	rfRegFifoRxCurrent  = 0x10
	rfRegIrqFlags       = 0x12
	rfRegRxNbBytes      = 0x13
	rfRegModemStat      = 0x18
	rfRegPktSnr         = 0x19
	rfRegPktRssi        = 0x1A
	rfRegRssi           = 0x1B
	rfRegHopChannel     = 0x1C
	rfRegModemConfig1   = 0x1D
	rfRegModemConfig2   = 0x1E
	rfRegPreambleMsb    = 0x20
	rfRegPayloadLength  = 0x22
	rfRegHopPeriod      = 0x24
	rfRegModemConfig3   = 0x26
	rfRegDetectOptimize = 0x31
	rfRegInvertIQ       = 0x33
	rfRegDetectThresh   = 0x37
	rfRegSyncWord       = 0x39
	rfRegInvertIQ2      = 0x3B
	rfRegDioMapping1    = 0x40
	rfRegVersion        = 0x42
	rfRegPaDac          = 0x4D

	rfModeSleep     = 0x00
	rfModeStandby   = 0x01
	rfModeTx        = 0x03
	rfModeRxCont    = 0x05
	rfLongRangeMode = 0x80

	rfIrqCadDone     = 0x04
	rfIrqTxDone      = 0x08
	rfIrqValidHeader = 0x10
	rfIrqCrcErr      = 0x20
	rfIrqRxDone      = 0x40
	rfIrqRxTimeout   = 0x80
)

type sx127x struct {
	bus
	board   Board
	freqHz  uint32
	bwHz    uint32
	version byte
}

func (s *sx127x) read(reg byte) (byte, error) {
	rx, err := s.h.Transfer([]byte{reg & 0x7F, 0x00})
	if err != nil {
		return 0, err
	}
	return rx[1], nil
}

func (s *sx127x) write(reg byte, data ...byte) error {
	_, err := s.h.Transfer(append([]byte{reg | 0x80}, data...))
	return err
}

// update rewrites bits hi..lo of a register.
func (s *sx127x) update(reg byte, hi, lo uint, value byte) error {
	v, err := s.read(reg)
	if err != nil {
		return err
	}
	mask := byte((0xFF << lo) & (0xFF >> (7 - hi)))
	return s.write(reg, (v&^mask)|(value&mask))
}

func (s *sx127x) setMode(mode byte) error { return s.update(rfRegOpMode, 2, 0, mode) }

func (s *sx127x) init() error {
	v, err := s.read(rfRegVersion)
	if err != nil {
		return err
	}
	if v != 0x12 && v != 0x11 { // SX1276/77/78/79, RFM9x
		return fmt.Errorf("the chip didn't answer as an SX127x (version register 0x%02x, want 0x12): check spidev, CS and wiring", v)
	}
	s.version = v
	// LoRa mode can only be selected in sleep.
	if err := s.write(rfRegOpMode, rfModeSleep); err != nil {
		return err
	}
	if err := s.write(rfRegOpMode, rfLongRangeMode|rfModeSleep); err != nil {
		return err
	}
	if err := s.write(rfRegOpMode, rfLongRangeMode|rfModeStandby); err != nil {
		return err
	}
	if m, err := s.read(rfRegOpMode); err != nil {
		return err
	} else if m&rfLongRangeMode == 0 {
		return fmt.Errorf("the SX127x didn't enter LoRa mode (op mode 0x%02x)", m)
	}
	steps := []struct{ reg, value byte }{
		{rfRegHopPeriod, 0x00},  // no frequency hopping
		{rfRegOcp, 0x20 | 0x0B}, // 100 mA over-current limit, as Meshtastic's RF95
		{rfRegFifoTxBase, 0x00},
		{rfRegFifoRxBase, 0x00},
		{rfRegInvertIQ2, 0x1D}, // standard IQ
	}
	for _, st := range steps {
		if err := s.write(st.reg, st.value); err != nil {
			return err
		}
	}
	// Standard IQ: RX path not inverted, TX path bit set (RadioLib invertIQ(false)).
	if err := s.update(rfRegInvertIQ, 6, 6, 0x00); err != nil {
		return err
	}
	if err := s.update(rfRegInvertIQ, 0, 0, 0x01); err != nil {
		return err
	}
	// AGC on, LNA boost.
	if err := s.update(rfRegModemConfig3, 2, 2, 0x04); err != nil {
		return err
	}
	return s.update(rfRegLna, 1, 0, 0x03)
}

func (s *sx127x) powerRange(uint32) (int, int) { return 2, boardLimit(20, s.board.MaxPower) }

func (s *sx127x) configure(c radio.Config, power int) error {
	var bw byte
	switch c.BandwidthHz {
	case 62_500:
		bw = 0x60
	case 125_000:
		bw = 0x70
	case 250_000:
		bw = 0x80
	case 500_000:
		bw = 0x90
	default:
		return fmt.Errorf("%w: bandwidth %d Hz on SX127x (62.5, 125, 250 or 500 kHz)", radio.ErrUnsupported, c.BandwidthHz)
	}
	if c.SF < 7 { // SF6 needs implicit headers, which Meshtastic doesn't use
		return fmt.Errorf("%w: SF%d on SX127x", radio.ErrUnsupported, c.SF)
	}
	if c.FrequencyHz < 137_000_000 || c.FrequencyHz > 1_020_000_000 {
		return fmt.Errorf("%w: %d Hz is outside the SX127x's range", radio.ErrUnsupported, c.FrequencyHz)
	}
	s.freqHz, s.bwHz = c.FrequencyHz, c.BandwidthHz
	frf := uint32(uint64(c.FrequencyHz) * (1 << 19) / 32_000_000)
	if err := s.write(rfRegFrfMsb, byte(frf>>16), byte(frf>>8), byte(frf)); err != nil {
		return err
	}
	// Modem config 1: bandwidth, coding rate, explicit header.
	if err := s.write(rfRegModemConfig1, bw|(c.CR-4)<<1); err != nil {
		return err
	}
	// Modem config 2: spreading factor, single TX, RX CRC on.
	if err := s.update(rfRegModemConfig2, 7, 2, c.SF<<4|0x04); err != nil {
		return err
	}
	if err := s.update(rfRegDetectOptimize, 2, 0, 0x03); err != nil {
		return err
	}
	if err := s.write(rfRegDetectThresh, 0x0A); err != nil {
		return err
	}
	if err := s.update(rfRegModemConfig3, 3, 3, ldro(c)<<3); err != nil {
		return err
	}
	if err := s.write(rfRegPreambleMsb, byte(c.Preamble>>8), byte(c.Preamble)); err != nil {
		return err
	}
	if err := s.write(rfRegSyncWord, c.SyncWord); err != nil {
		return err
	}
	// PA_BOOST: 2–17 dBm, or 20 dBm with the high-power DAC. 18 and 19 aren't settable.
	if power > 17 && power < 20 {
		power = 17
	}
	if power == 20 {
		if err := s.write(rfRegPaConfig, 0x80|0x70|0x0F); err != nil {
			return err
		}
		return s.update(rfRegPaDac, 2, 0, 0x07)
	}
	if err := s.write(rfRegPaConfig, 0x80|0x70|byte(power-2)); err != nil {
		return err
	}
	return s.update(rfRegPaDac, 2, 0, 0x04)
}

// errata applies the SX1276 errata note 2.1 settings RadioLib uses (SX1278::errataFix).
func (s *sx127x) errata() error {
	mhz := s.freqHz / 1_000_000
	if s.bwHz == 500_000 {
		switch {
		case mhz >= 862 && mhz <= 1020:
			if err := s.write(0x36, 0x02); err != nil {
				return err
			}
			if err := s.write(0x3A, 0x64); err != nil {
				return err
			}
		case mhz >= 410 && mhz <= 525:
			if err := s.write(0x36, 0x02); err != nil {
				return err
			}
			if err := s.write(0x3A, 0x7F); err != nil {
				return err
			}
		}
		return s.update(0x31, 7, 7, 0x80)
	}
	if err := s.update(0x31, 7, 7, 0x00); err != nil {
		return err
	}
	if err := s.write(0x2F, 0x40); err != nil {
		return err
	}
	return s.write(0x30, 0x00)
}

func (s *sx127x) standby() error { return s.setMode(rfModeStandby) }

func (s *sx127x) startRx() error {
	if err := s.setMode(rfModeStandby); err != nil {
		return err
	}
	if err := s.update(rfRegDioMapping1, 7, 6, 0x00); err != nil { // DIO0 = RxDone
		return err
	}
	if err := s.errata(); err != nil {
		return err
	}
	if err := s.write(rfRegIrqFlags, 0xFF); err != nil {
		return err
	}
	if err := s.write(rfRegFifoRxBase, 0x00); err != nil {
		return err
	}
	if err := s.write(rfRegFifoAddrPtr, 0x00); err != nil {
		return err
	}
	return s.setMode(rfModeRxCont)
}

func (s *sx127x) transmit(frame []byte) error {
	if err := s.update(rfRegDioMapping1, 7, 6, 0x40); err != nil { // DIO0 = TxDone
		return err
	}
	if err := s.errata(); err != nil {
		return err
	}
	for _, st := range []struct{ reg, value byte }{
		{rfRegIrqFlags, 0xFF}, {rfRegPayloadLength, byte(len(frame))}, {rfRegFifoTxBase, 0x00}, {rfRegFifoAddrPtr, 0x00},
	} {
		if err := s.write(st.reg, st.value); err != nil {
			return err
		}
	}
	if err := s.write(rfRegFifo, frame...); err != nil {
		return err
	}
	return s.setMode(rfModeTx)
}

func (s *sx127x) events() (event, error) {
	flags, err := s.read(rfRegIrqFlags)
	if err != nil {
		return 0, err
	}
	flags &= rfIrqTxDone | rfIrqRxDone | rfIrqValidHeader | rfIrqCrcErr | rfIrqRxTimeout
	if flags == 0 {
		return 0, nil
	}
	ev := mapEvents(uint32(flags), map[uint32]event{rfIrqTxDone: evTxDone, rfIrqRxDone: evRxDone,
		rfIrqValidHeader: evHeaderValid, rfIrqCrcErr: evCrcErr, rfIrqRxTimeout: evTimeout})
	if flags&rfIrqRxDone != 0 && flags&rfIrqCrcErr == 0 {
		// No payload CRC in the header (CrcOnPayload clear): damaged, as RadioLib treats it.
		if hc, err := s.read(rfRegHopChannel); err == nil && hc&0x40 == 0 {
			ev |= evCrcErr
		}
	}
	return ev, s.write(rfRegIrqFlags, flags)
}

func (s *sx127x) packet() ([]byte, int16, float32, error) {
	n, err := s.read(rfRegRxNbBytes)
	if err != nil || n == 0 {
		return nil, 0, 0, err
	}
	addr, err := s.read(rfRegFifoRxCurrent)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := s.write(rfRegFifoAddrPtr, addr); err != nil {
		return nil, 0, 0, err
	}
	rx, err := s.h.Transfer(append([]byte{rfRegFifo}, make([]byte, n)...))
	if err != nil {
		return nil, 0, 0, err
	}
	snrRaw, err := s.read(rfRegPktSnr)
	if err != nil {
		return nil, 0, 0, err
	}
	rssiRaw, err := s.read(rfRegPktRssi)
	if err != nil {
		return nil, 0, 0, err
	}
	snr := float32(int8(snrRaw)) / 4
	rssi := s.rssiOffset() + int16(rssiRaw)
	if snr < 0 {
		rssi += int16(snr)
	}
	return rx[1:], rssi, snr, nil
}

// rssiOffset is -157 dBm on the high-frequency port (779 MHz and up), -164 on the low one
// (datasheet 5.5.5).
func (s *sx127x) rssiOffset() int16 {
	if s.freqHz >= 779_000_000 {
		return -157
	}
	return -164
}

func (s *sx127x) rssi() (int16, error) {
	v, err := s.read(rfRegRssi)
	if err != nil {
		return 0, err
	}
	return s.rssiOffset() + int16(v), nil
}

// receiving reports a signal being demodulated (MODEM_STAT: detected, synchronised or header
// valid), as Meshtastic's RadioLibRF95::isReceiving.
func (s *sx127x) receiving() (bool, error) {
	v, err := s.read(rfRegModemStat)
	return v&0x0B != 0, err
}

func (s *sx127x) diagnostics() []string {
	out := []string{fmt.Sprintf("version:    0x%02x", s.version)}
	if m, err := s.read(rfRegOpMode); err == nil {
		modes := map[byte]string{0: "sleep", 1: "standby", 2: "FS TX", 3: "TX", 4: "FS RX", 5: "RX continuous", 6: "RX single", 7: "CAD"}
		out = append(out, fmt.Sprintf("op mode:    0x%02x (%s, LoRa %v)", m, modes[m&7], m&rfLongRangeMode != 0))
	}
	return out
}
