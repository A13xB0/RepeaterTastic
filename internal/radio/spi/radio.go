// Package spi drives a Semtech LoRa chip directly, the way meshtasticd does: SX1262/SX1268/LLCC68,
// SX1276/SX1278 (RF95), SX1280 and LR1110/LR1120/LR1121, on a Linux board's SPI bus (spidev and
// GPIO character devices) or a CH341 USB-to-SPI adapter. Boards are described with meshtasticd's
// own config.d YAML; a copy of meshtasticd's board files is built in.
//
// DRAFT: written from the Semtech datasheets and RadioLib's command sequences, not yet run on
// hardware. See docs/spi-radio-testing.md.
package spi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// hal is the hardware under a chip: SPI transactions framed by chip select, and the
// RESET/BUSY/IRQ lines.
type hal interface {
	// Transfer runs one chip-select-framed full-duplex transaction.
	Transfer(tx []byte) ([]byte, error)
	Reset() error
	HasBusy() bool
	Busy() (bool, error)
	HasIRQ() bool
	// WaitIRQ waits for the IRQ line to be (or go) high, or for timeout (false).
	WaitIRQ(timeout time.Duration) (bool, error)
	// SetRFSwitch drives the board's TXen/RXen lines, if it has them.
	SetRFSwitch(tx bool) error
	Close() error
}

// event is a chip interrupt, normalised across chip families.
type event uint16

const (
	evTxDone event = 1 << iota
	evRxDone
	evPreamble
	evHeaderValid
	evHeaderErr
	evCrcErr
	evTimeout
)

// chip is one Semtech chip family. The Radio serialises all calls.
type chip interface {
	// init runs after a hardware reset: check the chip answers, then set what doesn't change
	// with the PHY.
	init() error
	// configure tunes the chip; power is the chip's output setting in dBm (already limited).
	configure(c radio.Config, power int) error
	// powerRange is the chip's output range at a frequency.
	powerRange(freqHz uint32) (min, max int)
	startRx() error
	// transmit loads a frame and starts sending it.
	transmit(frame []byte) error
	standby() error
	// events reads and clears pending interrupts.
	events() (event, error)
	// packet reads the frame behind an RxDone.
	packet() (data []byte, rssi int16, snr float32, err error)
	// rssi is the instantaneous RSSI while receiving.
	rssi() (int16, error)
	// diagnostics describes the chip for bench tools.
	diagnostics() []string
}

var _ radio.Radio = (*Radio)(nil)

// Radio is a LoRa chip driven over SPI. It implements radio.Radio.
type Radio struct {
	hal   hal
	chip  chip
	board Board
	log   func(string, ...any)

	mu      sync.Mutex // one SPI conversation at a time
	cfg     radio.Config
	haveCfg bool

	frames    chan radio.Frame
	txDone    chan event
	receiving atomic.Int64 // unix nanos when a preamble/header was seen, 0 = idle
	noise     atomic.Int32
	rx, tx    atomic.Uint32
	errs      atomic.Uint32

	stop chan struct{}
	done chan struct{}
	once sync.Once
	lost atomic.Bool // the adapter went away: the radio closes itself
}

// Open opens the board's bus, resets the chip, checks it answers and starts listening for
// interrupts. Configure before receiving or sending.
func Open(ctx context.Context, b Board, logf func(string, ...any)) (*Radio, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var (
		h   hal
		err error
	)
	if b.USB != nil {
		h, err = openCH341(b, logf)
	} else {
		h, err = openHAL(b, logf)
	}
	if err != nil {
		return nil, err
	}
	r, err := newRadio(h, b, logf)
	if err != nil {
		h.Close()
		return nil, err
	}
	return r, nil
}

func newChip(h hal, b Board, logf func(string, ...any)) (chip, error) {
	bus := bus{h: h}
	switch b.Module {
	case ModuleSX1262, ModuleSX1268, ModuleLLCC68:
		return &sx126x{bus: bus, board: b, log: logf}, nil
	case ModuleRF95:
		return &sx127x{bus: bus, board: b}, nil
	case ModuleSX1280:
		return &sx128x{bus: bus, board: b}, nil
	case ModuleLR1110, ModuleLR1120, ModuleLR1121:
		return &lr11x0{bus: bus, board: b, log: logf}, nil
	}
	return nil, fmt.Errorf("spi: no driver for module %q", b.Module)
}

func newRadio(h hal, b Board, logf func(string, ...any)) (*Radio, error) {
	c, err := newChip(h, b, logf)
	if err != nil {
		return nil, err
	}
	r := &Radio{hal: h, chip: c, board: b, log: logf, frames: make(chan radio.Frame, 64), txDone: make(chan event, 1),
		stop: make(chan struct{}), done: make(chan struct{})}
	if err := h.Reset(); err != nil {
		return nil, fmt.Errorf("reset: %w", err)
	}
	if err := c.init(); err != nil {
		return nil, err
	}
	go r.loop()
	return r, nil
}

// Configure tunes the chip to the PHY and starts receiving.
func (r *Radio) Configure(ctx context.Context, c radio.Config) error {
	if c.SF < 5 || c.SF > 12 || c.CR < 5 || c.CR > 8 {
		return fmt.Errorf("%w: SF%d CR4/%d", radio.ErrUnsupported, c.SF, c.CR)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.chip.standby(); err != nil {
		return err
	}
	min, max := r.chip.powerRange(c.FrequencyHz)
	power := r.board.ChipPower(int(c.TxPowerDBm), min, max)
	if err := r.chip.configure(c, power); err != nil {
		return err
	}
	r.cfg, r.haveCfg = c, true
	return r.startRx()
}

func (r *Radio) startRx() error {
	if err := r.hal.SetRFSwitch(false); err != nil {
		return err
	}
	return r.chip.startRx()
}

// Send transmits one frame and returns after TxDone.
func (r *Radio) Send(ctx context.Context, frame []byte) error {
	if len(frame) == 0 || len(frame) > 255 {
		return fmt.Errorf("spi: frame of %d bytes", len(frame))
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
	err := r.chip.standby()
	if err == nil {
		err = r.hal.SetRFSwitch(true)
	}
	if err == nil {
		r.receiving.Store(0)
		err = r.chip.transmit(frame)
	}
	r.mu.Unlock()
	if err != nil {
		r.errs.Add(1)
		r.mu.Lock()
		_ = r.startRx()
		r.mu.Unlock()
		return err
	}
	select {
	case ev := <-r.txDone:
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.startRx(); err != nil {
			return err
		}
		if ev&evTxDone == 0 {
			r.errs.Add(1)
			return radio.ErrTxFailed
		}
		r.tx.Add(1)
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		defer r.mu.Unlock()
		r.errs.Add(1)
		_ = r.chip.standby()
		_ = r.startRx()
		return fmt.Errorf("%w: no TxDone: %w", radio.ErrTxFailed, ctx.Err())
	case <-r.stop:
		return radio.ErrNotConnected
	}
}

// loop handles interrupts (or polls the chip when there's no IRQ line).
func (r *Radio) loop() {
	defer close(r.done)
	var lastNoise time.Time
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		wait := 100 * time.Millisecond
		if !r.hal.HasIRQ() {
			wait = 10 * time.Millisecond
		}
		if _, err := r.hal.WaitIRQ(wait); err != nil {
			if deviceGone(err) {
				r.log("spi: the adapter is gone (%v); closing so it can be opened again", err)
				r.lost.Store(true)
				go r.Close() // Close waits for this loop to end
				return
			}
			r.log("spi: waiting for IRQ: %v", err)
			time.Sleep(wait)
		}
		r.mu.Lock()
		ev, err := r.chip.events()
		if err != nil {
			r.mu.Unlock()
			r.errs.Add(1)
			time.Sleep(wait)
			continue
		}
		if ev != 0 {
			r.handle(ev)
		} else if r.haveCfg && r.receiving.Load() == 0 && time.Since(lastNoise) > 5*time.Second {
			if v, err := r.chip.rssi(); err == nil {
				r.noise.Store(int32(v))
				lastNoise = time.Now()
			}
		}
		r.mu.Unlock()
	}
}

func (r *Radio) handle(ev event) {
	now := time.Now()
	if ev&(evPreamble|evHeaderValid) != 0 && ev&evRxDone == 0 {
		r.receiving.Store(now.UnixNano())
	}
	if ev&(evTxDone|evTimeout) != 0 && ev&evRxDone == 0 {
		select {
		case r.txDone <- ev:
		default:
		}
		return
	}
	if ev&evHeaderErr != 0 {
		r.receiving.Store(0)
		r.errs.Add(1)
	}
	if ev&evRxDone == 0 {
		return
	}
	r.receiving.Store(0)
	if ev&evCrcErr != 0 {
		r.errs.Add(1)
		_ = r.startRx() // some chips (RF95) need the FIFO pointer reset
		return
	}
	data, rssi, snr, err := r.chip.packet()
	if err != nil {
		r.errs.Add(1)
		return
	}
	if len(data) == 0 {
		return
	}
	r.rx.Add(1)
	select {
	case r.frames <- radio.Frame{Data: data, RSSI: rssi, SNR: snr, At: now}:
	default:
		r.errs.Add(1) // nobody reading fast enough
	}
}

// ChannelBusy reports a packet being received right now (preamble or header seen, no RxDone yet).
// DRAFT: Meshtastic firmware uses CAD for this; preamble/header detection is a first
// approximation.
func (r *Radio) ChannelBusy(ctx context.Context) (bool, error) {
	since := r.receiving.Load()
	if since != 0 && time.Since(time.Unix(0, since)) < 3*time.Second {
		return true, nil
	}
	if s, ok := r.chip.(interface{ receiving() (bool, error) }); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		return s.receiving()
	}
	return false, nil
}

func (r *Radio) Frames() <-chan radio.Frame { return r.frames }

func (r *Radio) Info() radio.Info {
	name := r.board.Name
	if name == "" {
		name = r.board.Module
	}
	dev := "/dev/" + r.board.SPIDev
	if r.board.USB != nil {
		dev = fmt.Sprintf("usb:%04x:%04x", r.board.USB.VID, r.board.USB.PID)
	}
	return radio.Info{Driver: "spi", Device: dev, Firmware: r.board.Module, Name: name}
}

func (r *Radio) Stats(ctx context.Context) radio.Stats {
	return radio.Stats{RxPackets: r.rx.Load(), TxPackets: r.tx.Load(), Errors: r.errs.Load(),
		NoiseFloorDBm: int16(r.noise.Load()), Connected: !r.lost.Load()}
}

// Diagnostics describes the chip (version, mode, error flags) for bench tools.
func (r *Radio) Diagnostics() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chip.diagnostics()
}

func (r *Radio) Close() error {
	r.once.Do(func() {
		close(r.stop)
		<-r.done
		r.mu.Lock()
		_ = r.chip.standby()
		r.mu.Unlock()
		r.hal.Close()
		close(r.frames)
	})
	return nil
}

// ------------------------------------------------------------------------------------ bus

// bus is the SPI access chips share: BUSY handshaking around each transaction.
type bus struct {
	h hal
}

var errStayedBusy = errors.New("the chip stayed BUSY")

// waitBusy waits for BUSY to fall. Without a BUSY line it waits a fixed time instead, scaled to
// how long the caller expects the chip to take.
func (b bus) waitBusy(timeout time.Duration) error {
	if !b.h.HasBusy() {
		d := timeout / 50
		if d < 2*time.Millisecond {
			d = 2 * time.Millisecond
		}
		time.Sleep(d)
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		busy, err := b.h.Busy()
		if err != nil {
			return err
		}
		if !busy {
			return nil
		}
		if time.Now().After(deadline) {
			return errStayedBusy
		}
		time.Sleep(time.Millisecond)
	}
}

func (b bus) transfer(tx []byte) ([]byte, error) {
	if err := b.waitBusy(100 * time.Millisecond); err != nil {
		return nil, err
	}
	return b.h.Transfer(tx)
}

// cmd sends an opcode and its arguments (SX126x/SX128x style).
func (b bus) cmd(op byte, args ...byte) error {
	_, err := b.transfer(append([]byte{op}, args...))
	return err
}

// get runs an SX126x/SX128x Get command: opcode, status, then n data bytes.
func (b bus) get(op byte, n int) ([]byte, error) {
	rx, err := b.transfer(append([]byte{op, 0x00}, make([]byte, n)...))
	if err != nil {
		return nil, err
	}
	return rx[2:], nil
}

// boardLimit is the chip's maximum power, lowered by the board's limit when it has one.
func boardLimit(chipMax, boardMax int) int {
	if boardMax > 0 && boardMax < chipMax {
		return boardMax
	}
	return chipMax
}
