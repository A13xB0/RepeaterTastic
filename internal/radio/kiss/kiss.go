// Package kiss drives a MeshCore KISS modem (with RepeaterTastic's sync word / preamble patch)
// as a radio.Radio over a serial link.
//
// One reader goroutine per connection deframes everything the modem sends. SetHardware requests
// are serialised and matched to their reply sub-command; TxDone, RxMeta and received Data frames
// are handled independently. Only one Data frame is ever in flight: Send waits for TxDone.
// If the link drops, a supervisor redials every ReconnectInterval and re-applies the last Config.
package kiss

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// PatchedVersion is the GetVersion value of firmware carrying the sync word / preamble patch.
const PatchedVersion = 2

// MeshCoreSyncWord is the sync word stock MeshCore firmware hardcodes.
const MeshCoreSyncWord = 0x12

const firmwareHint = "flash firmware/out/Heltec_v3_kiss_modem-factory.bin (see firmware/README.md)"

// Options configures Open. Zero values pick the defaults noted on each field.
type Options struct {
	Device string // serial device, e.g. /dev/ttyUSB0
	Baud   int    // default 115200

	// Dial opens the transport; default opens Device at Baud. Tests substitute a fake modem.
	Dial func() (io.ReadWriteCloser, error)

	ReconnectInterval time.Duration // default 2s
	CommandTimeout    time.Duration // per attempt, default 1s
	HandshakeTimeout  time.Duration // Ping retries after connecting (modem may reboot on open), default 5s
	RxMetaWait        time.Duration // how long a Data frame waits for its RxMeta, default 250ms
	TxMargin          time.Duration // added to the TxDone timeout, default 2s
	FrameBuffer       int           // Frames() capacity, default 64; oldest dropped when full

	Logf func(format string, args ...any) // default log.Printf
}

// ModemError is an Error (0xF1) reply to a SetHardware command.
type ModemError struct {
	Cmd, Code byte
}

func (e *ModemError) Error() string {
	name := "error"
	switch e.Code {
	case ErrCodeInvalidLength:
		name = "invalid length"
	case ErrCodeInvalidParam:
		name = "invalid parameter"
	case ErrCodeNoCallback:
		name = "no callback"
	case ErrCodeUnknownCmd:
		name = "unknown command"
	case ErrCodeTxBusy:
		name = "tx busy"
	}
	return fmt.Sprintf("kiss: command 0x%02x: modem %s (0x%02x)", e.Cmd, name, e.Code)
}

// Modem is a connected (or reconnecting) KISS modem. It implements radio.Radio.
type Modem struct {
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closer sync.Once

	frames chan radio.Frame
	txSem  chan struct{} // single-flight TX (and Configure, which must not race a TX)

	mu         sync.Mutex
	sess       *session // nil while disconnected
	cfg        *radio.Config
	cfgGen     int
	version    byte
	name       string
	reconnects int
	last       radio.Stats // last modem counters, served while disconnected

	rxMu        sync.Mutex
	pending     radio.Frame
	havePending bool
	metaTimer   *time.Timer
	rxClosed    bool

	dropped atomic.Uint64
	txBusy  atomic.Uint64
}

var _ radio.Radio = (*Modem)(nil)

// Open dials the modem, waits for it to answer Ping and reads its version and name.
// The PHY is not touched until Configure. Once open, the Modem reconnects by itself.
func Open(ctx context.Context, o Options) (*Modem, error) {
	if o.Baud == 0 {
		o.Baud = 115200
	}
	if o.Dial == nil {
		if o.Device == "" {
			return nil, errors.New("kiss: no device")
		}
		dev, baud := o.Device, o.Baud
		o.Dial = func() (io.ReadWriteCloser, error) { return dialSerial(dev, baud) }
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.ReconnectInterval, 2*time.Second)
	def(&o.CommandTimeout, time.Second)
	def(&o.HandshakeTimeout, 5*time.Second)
	def(&o.RxMetaWait, 250*time.Millisecond)
	def(&o.TxMargin, 2*time.Second)
	if o.FrameBuffer <= 0 {
		o.FrameBuffer = 64
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	m := &Modem{opts: o, frames: make(chan radio.Frame, o.FrameBuffer), txSem: make(chan struct{}, 1)}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	s, err := m.connect(ctx, false)
	if err != nil {
		m.cancel()
		m.wg.Wait()
		return nil, err
	}
	m.wg.Add(1)
	go m.supervise(s)
	return m, nil
}

// ---------------------------------------------------------------------------------- connection

type response struct {
	sub  byte
	data []byte
}

type waiter struct {
	expect byte
	ch     chan response
	busy   chan struct{}
}

type session struct {
	m    *Modem
	rwc  io.ReadWriteCloser
	done chan struct{}
	once sync.Once
	err  error

	wmu  sync.Mutex
	wbuf []byte

	cmdMu  sync.Mutex
	respMu sync.Mutex
	waiter *waiter

	txActive atomic.Bool
	txBusy   atomic.Bool
	txDone   chan byte
}

func (s *session) fail(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
		_ = s.rwc.Close()
	})
}

func (s *session) write(typ byte, parts ...[]byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	select {
	case <-s.done:
		return radio.ErrNotConnected
	default:
	}
	s.wbuf = appendFrame(s.wbuf[:0], typ, parts...)
	if _, err := s.rwc.Write(s.wbuf); err != nil {
		s.fail(err)
		return fmt.Errorf("%w: %w", radio.ErrNotConnected, err)
	}
	return nil
}

func (s *session) readLoop() {
	defer s.m.wg.Done()
	buf := make([]byte, 1024)
	var d decoder
	for {
		n, err := s.rwc.Read(buf)
		for _, b := range buf[:n] {
			if f := d.feed(b); f != nil {
				s.handle(f)
			}
		}
		if err != nil {
			s.fail(err)
			s.m.flushPending()
			return
		}
	}
}

func (s *session) handle(f []byte) {
	switch f[0] {
	case typeData:
		s.m.rxData(f[1:])
	case typeSetHardware:
		s.handleHardware(f)
	}
	// Other ports and KISS commands are not sent by the modem; ignore.
}

// handleHardware dispatches a SetHardware frame from the modem: RX metadata, TxDone, TxBusy or a
// command reply.
func (s *session) handleHardware(f []byte) {
	if len(f) < 2 {
		return
	}
	sub, data := f[1], f[2:]
	switch sub {
	case respRxMeta:
		s.m.rxMeta(data)
	case respTxDone:
		if len(data) > 0 && s.txActive.Load() {
			select {
			case s.txDone <- data[0]:
			default:
			}
		}
	case respError:
		if len(data) > 0 && data[0] == ErrCodeTxBusy {
			s.noteTxBusy()
			return
		}
		s.deliver(sub, data)
	default:
		s.deliver(sub, data)
	}
}

// noteTxBusy records a TxBusy error and tells a waiting command about it.
func (s *session) noteTxBusy() {
	// Ambiguous: a Data frame arrived while one was pending, or the modem's 2-frame host
	// output queue overflowed (possibly eating a command reply). Never a verdict by itself.
	s.m.txBusy.Add(1)
	s.txBusy.Store(true)
	s.respMu.Lock()
	if w := s.waiter; w != nil {
		select {
		case w.busy <- struct{}{}:
		default:
		}
	}
	s.respMu.Unlock()
}

func (s *session) deliver(sub byte, data []byte) {
	s.respMu.Lock()
	defer s.respMu.Unlock()
	w := s.waiter
	if w == nil || (sub != w.expect && sub != respError) {
		return // late reply to a timed-out command, or unsolicited
	}
	select {
	case w.ch <- response{sub, bytes.Clone(data)}:
	default:
	}
}

const cmdAttempts = 3

// command sends a SetHardware request and waits for expect (or Error). Requests are idempotent,
// so a lost reply (timeout, or TxBusy signalling a full modem output queue) is retried.
func (s *session) command(ctx context.Context, cmd, expect byte, arg []byte) ([]byte, error) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	w := &waiter{expect: expect, ch: make(chan response, 1), busy: make(chan struct{}, 1)}
	s.respMu.Lock()
	s.waiter = w
	s.respMu.Unlock()
	defer func() {
		s.respMu.Lock()
		s.waiter = nil
		s.respMu.Unlock()
	}()
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for attempt := 1; ; attempt++ {
		if err := s.write(typeSetHardware, []byte{cmd}, arg); err != nil {
			return nil, err
		}
		t.Reset(s.m.opts.CommandTimeout)
		select {
		case r := <-w.ch:
			return replyData(cmd, r)
		case <-w.busy:
			// Give a genuine reply a moment before re-asking.
			t.Reset(100 * time.Millisecond)
			select {
			case r := <-w.ch:
				return replyData(cmd, r)
			case <-t.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-s.done:
				return nil, radio.ErrNotConnected
			}
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.done:
			return nil, radio.ErrNotConnected
		}
		t.Stop() // go1.23+ timers: no stale tick after Stop/Reset
		if attempt >= cmdAttempts {
			return nil, fmt.Errorf("kiss: command 0x%02x: no reply after %d attempts", cmd, attempt)
		}
	}
}

func replyData(cmd byte, r response) ([]byte, error) {
	if r.sub == respError {
		code := byte(0)
		if len(r.data) > 0 {
			code = r.data[0]
		}
		return nil, &ModemError{Cmd: cmd, Code: code}
	}
	return r.data, nil
}

func (s *session) get(ctx context.Context, cmd byte, minLen int, arg []byte) ([]byte, error) {
	d, err := s.command(ctx, cmd, cmd|0x80, arg)
	if err == nil && len(d) < minLen {
		err = fmt.Errorf("kiss: command 0x%02x: short reply (%d bytes)", cmd, len(d))
	}
	return d, err
}

func (s *session) set(ctx context.Context, cmd byte, arg []byte) error {
	_, err := s.command(ctx, cmd, respOK, arg)
	return err
}

// connect dials, handshakes, applies the stored config and publishes the session.
func (m *Modem) connect(ctx context.Context, reconnect bool) (*session, error) {
	rwc, err := m.opts.Dial()
	if err != nil {
		return nil, err
	}
	s := &session{m: m, rwc: rwc, done: make(chan struct{}), txDone: make(chan byte, 1)}
	m.wg.Add(1)
	go s.readLoop()
	stop := context.AfterFunc(m.ctx, func() { s.fail(radio.ErrNotConnected) })
	defer stop()

	ver, name, err := m.handshake(ctx, s)
	if err != nil {
		s.fail(err)
		return nil, err
	}
	for gen := -1; ; {
		m.mu.Lock()
		if m.ctx.Err() != nil {
			m.mu.Unlock()
			s.fail(radio.ErrNotConnected)
			return nil, radio.ErrNotConnected
		}
		if gen == m.cfgGen || m.cfg == nil {
			m.sess, m.version, m.name = s, ver, name
			if reconnect {
				m.reconnects++
			}
			m.mu.Unlock()
			return s, nil
		}
		gen = m.cfgGen
		cfg := *m.cfg
		m.mu.Unlock()
		if err := m.reapply(ctx, s, ver, cfg); err != nil {
			s.fail(err)
			return nil, fmt.Errorf("kiss: re-applying config: %w", err)
		}
	}
}

// reapply applies the stored config to a new session. A setting the firmware can't take is
// logged rather than failing the connection.
func (m *Modem) reapply(ctx context.Context, s *session, ver byte, cfg radio.Config) error {
	err := m.apply(ctx, s, ver, cfg)
	if err == nil || !errors.Is(err, radio.ErrUnsupported) {
		return err
	}
	m.opts.Logf("kiss: %s: %v", m.opts.Device, err)
	return nil
}

func (m *Modem) handshake(ctx context.Context, s *session) (ver byte, name string, err error) {
	hctx, cancel := context.WithTimeout(ctx, m.opts.HandshakeTimeout)
	defer cancel()
	_ = s.write(typeReturn, nil) // flushes any partial frame on the modem side; otherwise ignored
	for {
		if _, err = s.get(hctx, cmdPing, 0, nil); err == nil {
			break
		}
		if hctx.Err() != nil || errors.Is(err, radio.ErrNotConnected) {
			if ctx.Err() == nil && hctx.Err() != nil {
				err = fmt.Errorf("kiss: modem did not answer ping within %v", m.opts.HandshakeTimeout)
			}
			return 0, "", err
		}
	}
	d, err := s.get(ctx, cmdGetVersion, 1, nil)
	if err != nil {
		return 0, "", err
	}
	if n, err := s.get(ctx, cmdGetDeviceName, 0, nil); err == nil {
		name = string(n)
	}
	return d[0], name, nil
}

func (m *Modem) supervise(s *session) {
	defer m.wg.Done()
	for {
		select {
		case <-s.done:
		case <-m.ctx.Done():
			return
		}
		m.mu.Lock()
		if m.sess == s {
			m.sess = nil
		}
		m.mu.Unlock()
		if m.ctx.Err() != nil {
			return
		}
		m.opts.Logf("kiss: %s disconnected: %v; retrying every %v", m.opts.Device, s.err, m.opts.ReconnectInterval)
		if s = m.reconnect(); s == nil {
			return
		}
	}
}

// reconnect retries every ReconnectInterval, logging each new error once, until it has a session
// or the modem is closed (nil).
func (m *Modem) reconnect() *session {
	t := time.NewTimer(m.opts.ReconnectInterval)
	for lastErr := ""; ; {
		select {
		case <-m.ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		ns, err := m.connect(m.ctx, true)
		if err == nil {
			m.opts.Logf("kiss: %s reconnected", m.opts.Device)
			return ns
		}
		if e := err.Error(); e != lastErr {
			m.opts.Logf("kiss: %s reconnect: %v", m.opts.Device, err)
			lastErr = e
		}
		t.Reset(m.opts.ReconnectInterval)
	}
}

func (m *Modem) session() *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sess
}

// ------------------------------------------------------------------------------------ receive

func (m *Modem) rxData(p []byte) {
	f := radio.Frame{Data: bytes.Clone(p), At: time.Now()}
	m.rxMu.Lock()
	defer m.rxMu.Unlock()
	if m.havePending {
		m.emitLocked(m.pending) // its RxMeta never came
	}
	m.pending, m.havePending = f, true
	if m.metaTimer == nil {
		m.metaTimer = time.AfterFunc(m.opts.RxMetaWait, m.metaExpired)
	} else {
		m.metaTimer.Reset(m.opts.RxMetaWait)
	}
}

func (m *Modem) rxMeta(d []byte) {
	if len(d) < 2 {
		return
	}
	m.rxMu.Lock()
	defer m.rxMu.Unlock()
	if !m.havePending {
		return
	}
	m.pending.SNR = float32(int8(d[0])) / 4
	m.pending.RSSI = int16(int8(d[1]))
	m.havePending = false
	m.metaTimer.Stop()
	m.emitLocked(m.pending)
}

func (m *Modem) metaExpired() {
	m.rxMu.Lock()
	defer m.rxMu.Unlock()
	// A fire that raced a Reset for a newer frame must not flush that frame early.
	if m.havePending && time.Since(m.pending.At) >= m.opts.RxMetaWait {
		m.havePending = false
		m.emitLocked(m.pending)
	}
}

func (m *Modem) flushPending() {
	m.rxMu.Lock()
	defer m.rxMu.Unlock()
	if m.havePending {
		m.havePending = false
		m.emitLocked(m.pending)
	}
}

// emitLocked delivers without blocking, evicting the oldest queued frame if the consumer lags.
func (m *Modem) emitLocked(f radio.Frame) {
	if m.rxClosed {
		return
	}
	select {
	case m.frames <- f:
		return
	default:
	}
	select {
	case <-m.frames:
		m.dropped.Add(1)
	default:
	}
	select {
	case m.frames <- f:
	default:
		m.dropped.Add(1)
	}
}

// ------------------------------------------------------------------------------- radio.Radio

// Frames delivers received packets; it stays open across reconnects and closes on Close.
func (m *Modem) Frames() <-chan radio.Frame { return m.frames }

// Dropped counts received frames evicted because the Frames consumer was too slow.
func (m *Modem) Dropped() uint64 { return m.dropped.Load() }

// TxBusyErrors counts TxBusy (0xF1 0x07) reports from the modem.
func (m *Modem) TxBusyErrors() uint64 { return m.txBusy.Load() }

// Version is the firmware's GetVersion value (PatchedVersion or higher supports sync word/preamble).
func (m *Modem) Version() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int(m.version)
}

func meshCorePreamble(sf uint8) uint16 {
	if sf <= 8 {
		return 32
	}
	return 16
}

func unsupported(ver byte, what string) error {
	return fmt.Errorf("%w: KISS modem firmware version %d cannot set %s (needs the RepeaterTastic patch, version %d); %s",
		radio.ErrUnsupported, ver, what, PatchedVersion, firmwareHint)
}

// Configure applies c and remembers it for reconnects. If the modem is currently disconnected
// the config is stored, applied on reconnect, and ErrNotConnected is returned.
func (m *Modem) Configure(ctx context.Context, c radio.Config) error {
	if c.SF < 5 || c.SF > 12 || c.CR < 5 || c.CR > 8 || c.BandwidthHz == 0 || c.FrequencyHz == 0 {
		return fmt.Errorf("kiss: invalid config %+v", c)
	}
	if c.Preamble != 0 && c.Preamble < 6 {
		return fmt.Errorf("kiss: preamble %d must be 0 (default) or >= 6", c.Preamble)
	}
	select {
	case m.txSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.txSem }()
	m.mu.Lock()
	m.cfg = &c
	m.cfgGen++
	s, ver := m.sess, m.version
	m.mu.Unlock()
	if s == nil {
		return radio.ErrNotConnected
	}
	return m.apply(ctx, s, ver, c)
}

func (m *Modem) apply(ctx context.Context, s *session, ver byte, c radio.Config) error {
	defPre := meshCorePreamble(c.SF)
	stockOK := c.SyncWord == MeshCoreSyncWord && (c.Preamble == 0 || c.Preamble == defPre)
	if ver < PatchedVersion && !stockOK {
		return unsupported(ver, "sync word/preamble")
	}
	r := make([]byte, 10)
	binary.LittleEndian.PutUint32(r[0:], c.FrequencyHz)
	binary.LittleEndian.PutUint32(r[4:], c.BandwidthHz)
	r[8], r[9] = c.SF, c.CR
	if err := s.set(ctx, cmdSetRadio, r); err != nil {
		return err
	}
	if err := s.set(ctx, cmdSetTxPower, []byte{byte(c.TxPowerDBm)}); err != nil {
		return err
	}
	patched, err := s.setPhyExtra(ctx, ver, c, stockOK)
	if err != nil {
		return err
	}
	// Standard KISS CSMA knobs (no reply): transmit as soon as the channel is clear.
	if err := s.write(typeTxDelay, []byte{0}); err != nil {
		return err
	}
	if err := s.write(typePersistence, []byte{255}); err != nil {
		return err
	}
	if !patched {
		return nil
	}
	return s.checkPhyExtra(ctx, c, defPre)
}

// setPhyExtra sets the sync word and preamble on patched firmware. It returns false when it
// didn't: stock firmware, or patched firmware without SetSyncWord and a stock PHY wanted.
func (s *session) setPhyExtra(ctx context.Context, ver byte, c radio.Config, stockOK bool) (bool, error) {
	if ver < PatchedVersion {
		return false, nil
	}
	err := s.set(ctx, cmdSetSyncWord, []byte{c.SyncWord})
	var me *ModemError
	if errors.As(err, &me) && me.Code == ErrCodeUnknownCmd {
		if !stockOK {
			return false, unsupported(ver, "sync word (SetSyncWord unknown)")
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.set(ctx, cmdSetPreamble, binary.LittleEndian.AppendUint16(nil, c.Preamble)); err != nil {
		return false, err
	}
	return true, nil
}

// checkPhyExtra reads back the sync word and preamble (defPre when c leaves it 0) the modem is
// using.
func (s *session) checkPhyExtra(ctx context.Context, c radio.Config, defPre uint16) error {
	d, err := s.get(ctx, cmdGetPhyExtra, 3, nil)
	if err != nil {
		return err
	}
	wantPre := c.Preamble
	if wantPre == 0 {
		wantPre = defPre
	}
	if gotSync, gotPre := d[0], binary.LittleEndian.Uint16(d[1:]); gotSync != c.SyncWord || gotPre != wantPre {
		return fmt.Errorf("kiss: modem reports sync 0x%02x preamble %d after configure, want 0x%02x/%d",
			gotSync, gotPre, c.SyncWord, wantPre)
	}
	return nil
}

// txTimeout covers the modem's worst case: it may defer a TX while receiving for up to 1.5x the
// airtime of a 255-byte packet, then allows 1.5x the packet's own airtime before reporting failure.
func (m *Modem) txTimeout(n int) time.Duration {
	m.mu.Lock()
	c := m.cfg
	m.mu.Unlock()
	if c == nil {
		return 20*time.Second + m.opts.TxMargin
	}
	pre := c.Preamble
	if pre == 0 {
		pre = meshCorePreamble(c.SF)
	}
	bw := float64(c.BandwidthHz) / 1000
	air := phy.AirtimeMs(n, int(c.SF), bw, int(c.CR), int(pre))
	busy := phy.AirtimeMs(MaxPacket, int(c.SF), bw, int(c.CR), int(pre))
	return time.Duration(1.5*(air+busy)*float64(time.Millisecond)) + m.opts.TxMargin
}

// Send transmits one frame and waits for TxDone. If ctx ends first, Send returns but the
// transmitter stays reserved until the modem reports TxDone (or the timeout passes).
func (m *Modem) Send(ctx context.Context, frame []byte) error {
	if len(frame) == 0 || len(frame) > MaxPacket {
		return fmt.Errorf("kiss: frame length %d outside 1..%d", len(frame), MaxPacket)
	}
	select {
	case m.txSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	s := m.session()
	if s == nil {
		<-m.txSem
		return radio.ErrNotConnected
	}
	release := func() {
		s.txActive.Store(false)
		<-m.txSem
	}
	select {
	case <-s.txDone: // stale
	default:
	}
	s.txBusy.Store(false)
	s.txActive.Store(true)
	timeout := m.txTimeout(len(frame))
	if err := s.write(typeData, frame); err != nil {
		release()
		return err
	}
	t := time.NewTimer(timeout)
	wait := func(cancel <-chan struct{}) (bool, error) {
		select {
		case st := <-s.txDone:
			if st == 1 {
				return false, nil
			}
			return false, fmt.Errorf("%w: modem reported TxDone failure", radio.ErrTxFailed)
		case <-t.C:
			if s.txBusy.Load() {
				return false, fmt.Errorf("%w: TxBusy and no TxDone within %v", radio.ErrBusy, timeout)
			}
			return false, fmt.Errorf("%w: no TxDone within %v", radio.ErrTxFailed, timeout)
		case <-s.done:
			return false, radio.ErrNotConnected
		case <-cancel:
			return true, nil
		}
	}
	cancelled, err := wait(ctx.Done())
	if cancelled {
		go func() {
			_, _ = wait(nil) // the modem still reports the transmission; only then is it free
			t.Stop()
			release()
		}()
		return ctx.Err()
	}
	t.Stop()
	release()
	return err
}

// ChannelBusy asks the modem whether it is currently receiving a LoRa packet.
func (m *Modem) ChannelBusy(ctx context.Context) (bool, error) {
	s := m.session()
	if s == nil {
		return false, radio.ErrNotConnected
	}
	d, err := s.get(ctx, cmdIsChannelBusy, 1, nil)
	if err != nil {
		return false, err
	}
	return d[0] != 0, nil
}

func (m *Modem) Info() radio.Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	fw := ""
	if m.version != 0 {
		fw = fmt.Sprintf("Mesh KISS v%d", m.version)
	}
	return radio.Info{Driver: "kiss", Device: m.opts.Device, Firmware: fw, Name: m.name}
}

// Stats queries the modem's counters and noise floor; while disconnected (or on error) the last
// known values are returned.
func (m *Modem) Stats(ctx context.Context) radio.Stats {
	m.mu.Lock()
	s, st := m.sess, m.last
	m.mu.Unlock()
	if s != nil {
		if d, err := s.get(ctx, cmdGetStats, 12, nil); err == nil {
			st.RxPackets = binary.LittleEndian.Uint32(d[0:])
			st.TxPackets = binary.LittleEndian.Uint32(d[4:])
			st.Errors = binary.LittleEndian.Uint32(d[8:])
		}
		if d, err := s.get(ctx, cmdGetNoiseFloor, 2, nil); err == nil {
			st.NoiseFloorDBm = int16(binary.LittleEndian.Uint16(d))
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = st
	st.Connected = m.sess != nil
	st.Reconnects = m.reconnects
	return st
}

// ModemConfig reads back what the modem reports: GetRadio, GetTxPower and (patched firmware) GetPhyExtra.
func (m *Modem) ModemConfig(ctx context.Context) (radio.Config, error) {
	var c radio.Config
	s := m.session()
	if s == nil {
		return c, radio.ErrNotConnected
	}
	d, err := s.get(ctx, cmdGetRadio, 10, nil)
	if err != nil {
		return c, err
	}
	c.FrequencyHz = binary.LittleEndian.Uint32(d[0:])
	c.BandwidthHz = binary.LittleEndian.Uint32(d[4:])
	c.SF, c.CR = d[8], d[9]
	if d, err = s.get(ctx, cmdGetTxPower, 1, nil); err != nil {
		return c, err
	}
	c.TxPowerDBm = int8(d[0])
	if m.Version() < PatchedVersion {
		c.SyncWord = MeshCoreSyncWord
		return c, nil
	}
	if d, err = s.get(ctx, cmdGetPhyExtra, 3, nil); err != nil {
		return c, err
	}
	c.SyncWord, c.Preamble = d[0], binary.LittleEndian.Uint16(d[1:])
	return c, nil
}

// Airtime asks the modem's own time-on-air estimate for an n-byte packet.
func (m *Modem) Airtime(ctx context.Context, n int) (time.Duration, error) {
	s := m.session()
	if s == nil {
		return 0, radio.ErrNotConnected
	}
	d, err := s.get(ctx, cmdGetAirtime, 4, []byte{byte(n)})
	if err != nil {
		return 0, err
	}
	return time.Duration(binary.LittleEndian.Uint32(d)) * time.Millisecond, nil
}

// Close stops reconnecting, closes the link and closes Frames.
func (m *Modem) Close() error {
	m.closer.Do(func() {
		m.cancel()
		m.mu.Lock()
		s := m.sess
		m.sess = nil
		m.mu.Unlock()
		if s != nil {
			s.fail(radio.ErrNotConnected)
		}
		m.wg.Wait()
		m.rxMu.Lock()
		m.rxClosed = true
		if m.metaTimer != nil {
			m.metaTimer.Stop()
		}
		close(m.frames)
		m.rxMu.Unlock()
	})
	return nil
}
