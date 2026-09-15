// Package sx126x drives a Semtech SX1262/SX1268/LLCC68 LoRa chip wired to a Linux board over
// spidev and GPIO: the radios meshtasticd runs on Raspberry Pi HATs and similar boards. Boards are
// described with meshtasticd's own config.d YAML.
//
// DRAFT: written from the SX126x datasheet and RadioLib's command sequences, not yet run on
// hardware. See docs/spi-radio-testing.md.
package sx126x

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// hal is the hardware under the driver: SPI with chip select, and the RESET/BUSY/DIO1 lines.
type hal interface {
	Transfer(tx []byte) ([]byte, error)
	Reset() error
	Busy() (bool, error)
	HasIRQ() bool
	// WaitIRQ waits for DIO1 to rise, or for timeout (false).
	WaitIRQ(timeout time.Duration) (bool, error)
	SetRFSwitch(tx bool) error
	Close() error
}

// Commands (SX1261/2 datasheet, section 13).
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

// Registers.
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

// IRQ bits.
const (
	irqTxDone        = 1 << 0
	irqRxDone        = 1 << 1
	irqPreamble      = 1 << 2
	irqSyncWordValid = 1 << 3
	irqHeaderValid   = 1 << 4
	irqHeaderErr     = 1 << 5
	irqCrcErr        = 1 << 6
	irqTimeout       = 1 << 9
	irqAll           = 0x03FF
)

var _ radio.Radio = (*Radio)(nil)

// Radio is an SX126x LoRa chip.
type Radio struct {
	hal   hal
	board Board
	log   func(string, ...any)

	mu        sync.Mutex // one SPI conversation at a time
	cfg       radio.Config
	haveCfg   bool
	imageBand [2]byte

	frames    chan radio.Frame
	txDone    chan uint16
	receiving atomic.Int64 // unix nanos when a preamble/header was seen, 0 = idle
	noise     atomic.Int32
	rx, tx    atomic.Uint32
	errs      atomic.Uint32

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Open resets the chip, checks it answers over SPI and starts listening for interrupts. Configure
// before receiving or sending.
func Open(ctx context.Context, b Board, logf func(string, ...any)) (*Radio, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	h, err := openHAL(b, logf)
	if err != nil {
		return nil, err
	}
	r, err := newRadio(ctx, h, b, logf)
	if err != nil {
		h.Close()
		return nil, err
	}
	return r, nil
}

func newRadio(ctx context.Context, h hal, b Board, logf func(string, ...any)) (*Radio, error) {
	r := &Radio{hal: h, board: b, log: logf, frames: make(chan radio.Frame, 64), txDone: make(chan uint16, 1),
		stop: make(chan struct{}), done: make(chan struct{})}
	if err := r.init(ctx); err != nil {
		return nil, err
	}
	go r.loop()
	return r, nil
}

// init resets the chip and sets what doesn't change with the PHY (datasheet 14.2, RadioLib begin).
func (r *Radio) init(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.hal.Reset(); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	if err := r.waitBusy(time.Second); err != nil {
		return fmt.Errorf("after reset: %w (check the Busy and Reset pins)", err)
	}
	steps := []struct {
		what string
		fn   func() error
	}{
		{"standby", func() error { return r.cmd(cmdSetStandby, 0x00) }},
		{"LoRa packet type", func() error { return r.cmd(cmdSetPacketType, 0x01) }},
	}
	for _, s := range steps {
		if err := s.fn(); err != nil {
			return fmt.Errorf("%s: %w", s.what, err)
		}
	}
	// The LoRa sync word register reads 0x1424 after reset: proof that SPI, CS and BUSY work.
	sw, err := r.readRegister(regSyncWord, 2)
	if err != nil {
		return err
	}
	if sw[0] != loraSyncWordReset1 || sw[1] != loraSyncWordReset2 {
		return fmt.Errorf("the chip didn't answer as an SX126x (sync word register %02x%02x, want 1424): check spidev, CS and wiring", sw[0], sw[1])
	}
	if r.board.TCXOVolt > 0 {
		// 5 ms start-up (160 × 31.25 µs), then recalibrate everything with the TCXO running.
		if err := r.cmd(cmdSetDIO3AsTcxoCtrl, tcxoCode(r.board.TCXOVolt), 0x00, 0x00, 0xA0); err != nil {
			return fmt.Errorf("TCXO: %w", err)
		}
		if err := r.cmd(cmdClearDeviceErrors, 0x00, 0x00); err != nil {
			return err
		}
		if err := r.cmd(cmdCalibrate, 0x7F); err != nil {
			return fmt.Errorf("calibrate: %w", err)
		}
		if err := r.waitBusy(time.Second); err != nil {
			return fmt.Errorf("calibrate: %w", err)
		}
	}
	if err := r.cmd(cmdSetRegulatorMode, 0x01); err != nil { // DC-DC
		return err
	}
	if r.board.DIO2RF {
		if err := r.cmd(cmdSetDIO2AsRfSwitch, 0x01); err != nil {
			return err
		}
	}
	if err := r.cmd(cmdSetBufferBaseAddr, 0x00, 0x00); err != nil {
		return err
	}
	mask := uint16(irqTxDone | irqRxDone | irqPreamble | irqHeaderValid | irqHeaderErr | irqCrcErr | irqTimeout)
	if err := r.cmd(cmdSetDioIrqParams, byte(mask>>8), byte(mask), byte(mask>>8), byte(mask), 0, 0, 0, 0); err != nil {
		return err
	}
	errs, err := r.deviceErrors()
	if err == nil && errs != 0 {
		r.log("sx126x: device errors after init: 0x%04x", errs)
	}
	return nil
}

// Configure tunes the chip to the PHY and starts receiving.
func (r *Radio) Configure(ctx context.Context, c radio.Config) error {
	bw, ok := bandwidthCode(c.BandwidthHz)
	if !ok {
		return fmt.Errorf("%w: bandwidth %d Hz", radio.ErrUnsupported, c.BandwidthHz)
	}
	if c.SF < 5 || c.SF > 12 || c.CR < 5 || c.CR > 8 {
		return fmt.Errorf("%w: SF%d CR4/%d", radio.ErrUnsupported, c.SF, c.CR)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.cmd(cmdSetStandby, 0x00); err != nil {
		return err
	}
	frf := uint32(uint64(c.FrequencyHz) * (1 << 25) / 32_000_000)
	if err := r.cmd(cmdSetRfFrequency, byte(frf>>24), byte(frf>>16), byte(frf>>8), byte(frf)); err != nil {
		return err
	}
	if band := imageBand(c.FrequencyHz); band != r.imageBand {
		if err := r.cmd(cmdCalibrateImage, band[0], band[1]); err != nil {
			return err
		}
		if err := r.waitBusy(time.Second); err != nil {
			return fmt.Errorf("image calibration: %w", err)
		}
		r.imageBand = band
	}
	// Low data rate optimisation when a symbol lasts 16 ms or more (LongSlow, VeryLongSlow).
	ldro := byte(0)
	if float64(uint32(1)<<c.SF)/float64(c.BandwidthHz)*1000 >= 16 {
		ldro = 1
	}
	if err := r.cmd(cmdSetModulationParams, c.SF, bw, c.CR-4, ldro); err != nil {
		return err
	}
	if err := r.packetParams(c.Preamble, 0xFF); err != nil {
		return err
	}
	// Errata 15.1: modulation quality at 500 kHz.
	if err := r.updateRegister(regTxModulation, func(v byte) byte {
		if c.BandwidthHz == 500_000 {
			return v &^ 0x04
		}
		return v | 0x04
	}); err != nil {
		return err
	}
	// Errata 15.4: standard IQ.
	if err := r.updateRegister(regIQPolarity, func(v byte) byte { return v | 0x04 }); err != nil {
		return err
	}
	// Sync word 0x2B becomes 0x24B4 (RadioLib setSyncWord with control bits 0x44).
	sw := c.SyncWord
	if err := r.writeRegister(regSyncWord, (sw&0xF0)|0x04, ((sw&0x0F)<<4)|0x04); err != nil {
		return err
	}
	power := r.board.ChipPower(int(c.TxPowerDBm))
	// High-power PA, optimal for +22 dBm (datasheet table 13-21); same for SX1268 and LLCC68.
	if err := r.cmd(cmdSetPaConfig, 0x04, 0x07, 0x00, 0x01); err != nil {
		return err
	}
	if err := r.cmd(cmdSetTxParams, byte(int8(power)), 0x04); err != nil { // 200 µs ramp
		return err
	}
	// Errata 15.2: better tolerance of antenna mismatch.
	if err := r.updateRegister(regTxClampConfig, func(v byte) byte { return v | 0x1E }); err != nil {
		return err
	}
	if err := r.writeRegister(regOCPConfig, 0x38); err != nil { // 140 mA, as Meshtastic sets for SX126x
		return err
	}
	if err := r.writeRegister(regRxGain, rxGainBoosted); err != nil {
		return err
	}
	r.cfg, r.haveCfg = c, true
	return r.startRx()
}

// Send transmits one frame and returns after TxDone.
func (r *Radio) Send(ctx context.Context, frame []byte) error {
	if len(frame) == 0 || len(frame) > 255 {
		return fmt.Errorf("sx126x: frame of %d bytes", len(frame))
	}
	r.mu.Lock()
	if !r.haveCfg {
		r.mu.Unlock()
		return radio.ErrNotConnected
	}
	select { // a stale TxDone from an earlier timeout
	case <-r.txDone:
	default:
	}
	err := r.transmit(frame)
	r.mu.Unlock()
	if err != nil {
		r.errs.Add(1)
		return err
	}
	select {
	case flags := <-r.txDone:
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.startRx(); err != nil {
			return err
		}
		if flags&irqTimeout != 0 {
			r.errs.Add(1)
			return radio.ErrTxFailed
		}
		r.tx.Add(1)
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		defer r.mu.Unlock()
		r.errs.Add(1)
		_ = r.cmd(cmdSetStandby, 0x00)
		_ = r.startRx()
		return fmt.Errorf("%w: no TxDone: %v", radio.ErrTxFailed, ctx.Err())
	case <-r.stop:
		return radio.ErrNotConnected
	}
}

func (r *Radio) transmit(frame []byte) error {
	if err := r.cmd(cmdSetStandby, 0x00); err != nil {
		return err
	}
	if err := r.writeBuffer(0, frame); err != nil {
		return err
	}
	if err := r.packetParams(r.cfg.Preamble, byte(len(frame))); err != nil {
		return err
	}
	if err := r.cmd(cmdClearIrqStatus, 0x03, 0xFF); err != nil {
		return err
	}
	if err := r.hal.SetRFSwitch(true); err != nil {
		return err
	}
	r.receiving.Store(0)
	return r.cmd(cmdSetTx, 0x00, 0x00, 0x00) // no chip timeout: the caller's context bounds it
}

func (r *Radio) startRx() error {
	if err := r.packetParams(r.cfg.Preamble, 0xFF); err != nil {
		return err
	}
	if err := r.hal.SetRFSwitch(false); err != nil {
		return err
	}
	if err := r.cmd(cmdClearIrqStatus, 0x03, 0xFF); err != nil {
		return err
	}
	return r.cmd(cmdSetRx, 0xFF, 0xFF, 0xFF) // continuous
}

func (r *Radio) packetParams(preamble uint16, length byte) error {
	// Explicit header, CRC on, standard IQ.
	return r.cmd(cmdSetPacketParams, byte(preamble>>8), byte(preamble), 0x00, length, 0x01, 0x00)
}

// loop handles DIO1 interrupts (or polls the chip when there's no IRQ pin).
func (r *Radio) loop() {
	defer close(r.done)
	lastNoise := time.Time{}
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		wait := 100 * time.Millisecond
		if !r.hal.HasIRQ() {
			wait = 5 * time.Millisecond
		}
		if _, err := r.hal.WaitIRQ(wait); err != nil {
			r.log("sx126x: waiting for IRQ: %v", err)
			time.Sleep(wait)
		}
		r.mu.Lock()
		flags, err := r.irqStatus()
		if err != nil {
			r.mu.Unlock()
			r.errs.Add(1)
			continue
		}
		if flags != 0 {
			_ = r.cmd(cmdClearIrqStatus, byte(flags>>8), byte(flags))
			r.handle(flags)
		} else if r.haveCfg && r.receiving.Load() == 0 && time.Since(lastNoise) > 5*time.Second {
			if v, err := r.get(cmdGetRssiInst, 1); err == nil {
				r.noise.Store(-int32(v[0]) / 2)
				lastNoise = time.Now()
			}
		}
		r.mu.Unlock()
	}
}

func (r *Radio) handle(flags uint16) {
	now := time.Now()
	if flags&(irqPreamble|irqHeaderValid) != 0 && flags&irqRxDone == 0 {
		r.receiving.Store(now.UnixNano())
	}
	if flags&(irqTxDone|irqTimeout) != 0 && flags&irqRxDone == 0 {
		select {
		case r.txDone <- flags:
		default:
		}
		return
	}
	if flags&(irqHeaderErr) != 0 {
		r.receiving.Store(0)
		r.errs.Add(1)
	}
	if flags&irqRxDone == 0 {
		return
	}
	r.receiving.Store(0)
	if flags&irqCrcErr != 0 {
		r.errs.Add(1)
		return
	}
	st, err := r.get(cmdGetRxBufferStatus, 2)
	if err != nil {
		r.errs.Add(1)
		return
	}
	n, start := st[0], st[1]
	if n == 0 {
		return
	}
	data, err := r.readBuffer(start, int(n))
	if err != nil {
		r.errs.Add(1)
		return
	}
	ps, err := r.get(cmdGetPacketStatus, 3)
	if err != nil {
		r.errs.Add(1)
		return
	}
	f := radio.Frame{Data: data, RSSI: -int16(ps[0]) / 2, SNR: float32(int8(ps[1])) / 4, At: now}
	r.rx.Add(1)
	select {
	case r.frames <- f:
	default:
		r.errs.Add(1) // nobody reading fast enough
	}
}

// ChannelBusy reports a packet being received right now (preamble or header seen, no RxDone yet).
// DRAFT: Meshtastic firmware uses CAD for this; preamble/header detection is a first
// approximation.
func (r *Radio) ChannelBusy(ctx context.Context) (bool, error) {
	since := r.receiving.Load()
	return since != 0 && time.Since(time.Unix(0, since)) < 3*time.Second, nil
}

func (r *Radio) Frames() <-chan radio.Frame { return r.frames }

func (r *Radio) Info() radio.Info {
	name := r.board.Name
	if name == "" {
		name = r.board.Module
	}
	return radio.Info{Driver: "spi", Device: "/dev/" + r.board.SPIDev, Firmware: r.board.Module, Name: name}
}

func (r *Radio) Stats(ctx context.Context) radio.Stats {
	return radio.Stats{RxPackets: r.rx.Load(), TxPackets: r.tx.Load(), Errors: r.errs.Load(),
		NoiseFloorDBm: int16(r.noise.Load()), Connected: true}
}

// Diagnostics reads the chip's status and error register for bench tools.
func (r *Radio) Diagnostics() (status byte, deviceErrors uint16, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rx, err := r.transfer([]byte{cmdGetStatus, 0x00})
	if err != nil {
		return 0, 0, err
	}
	e, err := r.deviceErrors()
	return rx[1], e, err
}

func (r *Radio) Close() error {
	r.once.Do(func() {
		close(r.stop)
		<-r.done
		r.mu.Lock()
		_ = r.cmd(cmdSetStandby, 0x00)
		r.mu.Unlock()
		r.hal.Close()
		close(r.frames)
	})
	return nil
}

// ------------------------------------------------------------------------------------ SPI

func (r *Radio) waitBusy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		busy, err := r.hal.Busy()
		if err != nil {
			return err
		}
		if !busy {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the chip stayed BUSY")
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *Radio) transfer(tx []byte) ([]byte, error) {
	if err := r.waitBusy(100 * time.Millisecond); err != nil {
		return nil, err
	}
	return r.hal.Transfer(tx)
}

func (r *Radio) cmd(op byte, args ...byte) error {
	_, err := r.transfer(append([]byte{op}, args...))
	return err
}

// get runs a Get command: opcode, status, then n data bytes.
func (r *Radio) get(op byte, n int) ([]byte, error) {
	rx, err := r.transfer(append([]byte{op, 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, err
	}
	return rx[2:], nil
}

func (r *Radio) irqStatus() (uint16, error) {
	v, err := r.get(cmdGetIrqStatus, 2)
	if err != nil {
		return 0, err
	}
	return uint16(v[0])<<8 | uint16(v[1]), nil
}

func (r *Radio) deviceErrors() (uint16, error) {
	v, err := r.get(cmdGetDeviceErrors, 2)
	if err != nil {
		return 0, err
	}
	return uint16(v[0])<<8 | uint16(v[1]), nil
}

func (r *Radio) readRegister(addr uint16, n int) ([]byte, error) {
	rx, err := r.transfer(append([]byte{cmdReadRegister, byte(addr >> 8), byte(addr), 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, err
	}
	return rx[4:], nil
}

func (r *Radio) writeRegister(addr uint16, data ...byte) error {
	return r.cmd(cmdWriteRegister, append([]byte{byte(addr >> 8), byte(addr)}, data...)...)
}

func (r *Radio) updateRegister(addr uint16, fn func(byte) byte) error {
	v, err := r.readRegister(addr, 1)
	if err != nil {
		return err
	}
	return r.writeRegister(addr, fn(v[0]))
}

func (r *Radio) writeBuffer(offset byte, data []byte) error {
	return r.cmd(cmdWriteBuffer, append([]byte{offset}, data...)...)
}

func (r *Radio) readBuffer(offset byte, n int) ([]byte, error) {
	rx, err := r.transfer(append([]byte{cmdReadBuffer, offset, 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, err
	}
	return rx[3:], nil
}

// ------------------------------------------------------------------------------------ tables

func bandwidthCode(hz uint32) (byte, bool) {
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
