package spi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

var errBoom = errors.New("boom")

// stubHAL is a hal with scriptable failures and an IRQ line the test raises.
type stubHAL struct {
	mu        sync.Mutex
	noIRQ     bool
	resetErr  error
	switchErr error
	waitErr   error // returned by WaitIRQ (once, if waitOnce)
	waitOnce  bool
	waits     int
	switches  []bool
	closed    int
	irq       chan struct{}
}

func newStubHAL() *stubHAL { return &stubHAL{irq: make(chan struct{}, 8)} }

func (h *stubHAL) Transfer(tx []byte) ([]byte, error) { return make([]byte, len(tx)), nil }
func (h *stubHAL) Reset() error                       { return h.resetErr }
func (h *stubHAL) HasBusy() bool                      { return true }
func (h *stubHAL) Busy() (bool, error)                { return false, nil }
func (h *stubHAL) HasIRQ() bool                       { return !h.noIRQ }

func (h *stubHAL) WaitIRQ(timeout time.Duration) (bool, error) {
	h.mu.Lock()
	h.waits++
	err := h.waitErr
	if h.waitOnce {
		h.waitErr = nil
	}
	h.mu.Unlock()
	if err != nil {
		return false, err
	}
	select {
	case <-h.irq:
		return true, nil
	case <-time.After(timeout):
		return false, nil
	}
}

func (h *stubHAL) SetRFSwitch(tx bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.switches = append(h.switches, tx)
	return h.switchErr
}

func (h *stubHAL) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed++
	return nil
}

func (h *stubHAL) setSwitchErr(err error) {
	h.mu.Lock()
	h.switchErr = err
	h.mu.Unlock()
}

func (h *stubHAL) waitCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waits
}

// stubChip is a chip whose results the test sets. The Radio serialises calls under its mutex; the
// test uses set to change things safely while the loop runs.
type stubChip struct {
	mu sync.Mutex

	initErr, configureErr, startRxErr, transmitErr, standbyErr, eventsErr, packetErr, rssiErr error

	pending   []event // returned by events, one per call
	data      []byte
	rssiValue int16
	busy      bool
	busyErr   error

	configured []int // power passed to configure
	starts     int
	transmits  int
	standbys   int
	rssiCalls  int
}

func (c *stubChip) set(fn func(*stubChip)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
}

func (c *stubChip) get(fn func(*stubChip)) { c.set(fn) }

func (c *stubChip) init() error { return c.initErr }

func (c *stubChip) configure(_ radio.Config, power int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configured = append(c.configured, power)
	return c.configureErr
}

func (c *stubChip) powerRange(uint32) (int, int) { return 0, 20 }

func (c *stubChip) startRx() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts++
	return c.startRxErr
}

func (c *stubChip) transmit([]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transmits++
	return c.transmitErr
}

func (c *stubChip) standby() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.standbys++
	return c.standbyErr
}

func (c *stubChip) events() (event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eventsErr != nil {
		return 0, c.eventsErr
	}
	if len(c.pending) == 0 {
		return 0, nil
	}
	ev := c.pending[0]
	c.pending = c.pending[1:]
	return ev, nil
}

func (c *stubChip) packet() ([]byte, int16, float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data, -80, 6.5, c.packetErr
}

func (c *stubChip) rssi() (int16, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rssiCalls++
	return c.rssiValue, c.rssiErr
}

func (c *stubChip) diagnostics() []string { return []string{"stub chip"} }

// busyChip adds the optional receiving() probe.
type busyChip struct{ *stubChip }

func (c busyChip) receiving() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy, c.busyErr
}

// startStub runs a Radio over the stubs, as newRadio does, without a chip driver.
func startStub(t *testing.T, h *stubHAL, c chip, b Board) *Radio {
	t.Helper()
	r := &Radio{hal: h, chip: c, board: b, log: t.Logf, frames: make(chan radio.Frame, 2), txDone: make(chan event, 1),
		stop: make(chan struct{}), done: make(chan struct{})}
	go r.loop()
	t.Cleanup(func() { r.Close() })
	return r
}

// raise queues events and fires the IRQ line.
func raise(h *stubHAL, c *stubChip, evs ...event) {
	c.set(func(c *stubChip) { c.pending = append(c.pending, evs...) })
	h.irq <- struct{}{}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func configured(t *testing.T, r *Radio) {
	t.Helper()
	if err := r.Configure(context.Background(), longFast); err != nil {
		t.Fatal(err)
	}
}

func TestNewChipUnknownModule(t *testing.T) {
	if _, err := newRadio(newStubHAL(), Board{Module: "cc1101"}, t.Logf); err == nil || !strings.Contains(err.Error(), `no driver for module "cc1101"`) {
		t.Fatalf("newRadio = %v", err)
	}
	for _, m := range []string{ModuleSX1268, ModuleLLCC68, ModuleLR1110, ModuleLR1120} {
		if _, err := newChip(newStubHAL(), Board{Module: m}, t.Logf); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}

func TestNewRadioResetAndInitErrors(t *testing.T) {
	h := newStubHAL()
	h.resetErr = errBoom
	if _, err := newRadio(h, Board{Module: ModuleSX1262}, t.Logf); !errors.Is(err, errBoom) || !strings.HasPrefix(err.Error(), "reset: ") {
		t.Fatalf("reset failure: %v", err)
	}
	// The stub HAL answers zeros: not an SX127x.
	if _, err := newRadio(newStubHAL(), Board{Module: ModuleRF95}, t.Logf); err == nil || !strings.Contains(err.Error(), "SX127x") {
		t.Fatalf("init failure: %v", err)
	}
}

func TestConfigureErrors(t *testing.T) {
	cases := []struct {
		name  string
		cfg   radio.Config
		setup func(*stubHAL, *stubChip)
		want  error
	}{
		{"SF too low", radio.Config{SF: 4, CR: 5}, nil, radio.ErrUnsupported},
		{"SF too high", radio.Config{SF: 13, CR: 5}, nil, radio.ErrUnsupported},
		{"CR too low", radio.Config{SF: 7, CR: 4}, nil, radio.ErrUnsupported},
		{"CR too high", radio.Config{SF: 7, CR: 9}, nil, radio.ErrUnsupported},
		{"standby", longFast, func(_ *stubHAL, c *stubChip) { c.standbyErr = errBoom }, errBoom},
		{"configure", longFast, func(_ *stubHAL, c *stubChip) { c.configureErr = errBoom }, errBoom},
		{"rf switch", longFast, func(h *stubHAL, _ *stubChip) { h.switchErr = errBoom }, errBoom},
		{"start rx", longFast, func(_ *stubHAL, c *stubChip) { c.startRxErr = errBoom }, errBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, c := newStubHAL(), &stubChip{}
			if tc.setup != nil {
				tc.setup(h, c)
			}
			r := startStub(t, h, c, Board{})
			if err := r.Configure(context.Background(), tc.cfg); !errors.Is(err, tc.want) {
				t.Fatalf("Configure = %v, want %v", err, tc.want)
			}
		})
	}
}

// Configure passes the board-limited power to the chip.
func TestConfigurePower(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{TxGain: []int{10}})
	configured(t, r) // 27 dBm - 10 dB gain = 17, within the stub's 0..20
	c.get(func(c *stubChip) {
		if len(c.configured) != 1 || c.configured[0] != 17 {
			t.Fatalf("configured with %v", c.configured)
		}
	})
}

func TestSendRejectsFrameSize(t *testing.T) {
	r := startStub(t, newStubHAL(), &stubChip{}, Board{})
	for _, n := range []int{0, 256} {
		if err := r.Send(context.Background(), make([]byte, n)); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("frame of %d bytes", n)) {
			t.Errorf("Send(%d bytes) = %v", n, err)
		}
	}
}

// A failure starting the transmission counts an error and goes back to receiving.
func TestSendStartFailures(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*stubHAL, *stubChip)
		starts int // startRx calls: configure, and the recovery unless the RF switch fails first
	}{
		{"standby", func(_ *stubHAL, c *stubChip) { c.standbyErr = errBoom }, 2},
		{"rf switch", func(h *stubHAL, _ *stubChip) { h.setSwitchErr(errBoom) }, 1},
		{"transmit", func(_ *stubHAL, c *stubChip) { c.transmitErr = errBoom }, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, c := newStubHAL(), &stubChip{}
			r := startStub(t, h, c, Board{})
			configured(t, r)
			c.get(func(c *stubChip) { tc.setup(h, c) })
			if err := r.Send(context.Background(), []byte{1}); !errors.Is(err, errBoom) {
				t.Fatalf("Send = %v", err)
			}
			if st := r.Stats(context.Background()); st.Errors != 1 || st.TxPackets != 0 {
				t.Fatalf("stats %+v", st)
			}
			c.get(func(c *stubChip) {
				if c.starts != tc.starts {
					t.Fatalf("startRx called %d times, want %d", c.starts, tc.starts)
				}
			})
		})
	}
}

// sendWith sends a frame and answers the transmission with ev once the chip is transmitting.
func sendWith(t *testing.T, r *Radio, h *stubHAL, c *stubChip, ctx context.Context, evs ...event) error {
	t.Helper()
	go func() {
		eventually(t, "transmit", func() bool {
			var n int
			c.get(func(c *stubChip) { n = c.transmits })
			return n > 0
		})
		if len(evs) > 0 {
			raise(h, c, evs...)
		}
	}()
	return r.Send(ctx, []byte("frame"))
}

func TestSendOutcomes(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{})
	configured(t, r)
	// A stale TxDone left from before is discarded, not taken as this frame's.
	r.txDone <- evTxDone
	if err := sendWith(t, r, h, c, context.Background(), evTxDone); err != nil {
		t.Fatalf("Send = %v", err)
	}
	c.set(func(c *stubChip) { c.transmits = 0 })
	if err := sendWith(t, r, h, c, context.Background(), evTimeout); !errors.Is(err, radio.ErrTxFailed) {
		t.Fatalf("Send after timeout = %v", err)
	}
	st := r.Stats(context.Background())
	if st.TxPackets != 1 || st.Errors != 1 {
		t.Fatalf("stats %+v", st)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// configure: RX; send 1: TX then RX; send 2: TX then RX.
	want := []bool{false, true, false, true, false}
	if fmt.Sprint(h.switches) != fmt.Sprint(want) {
		t.Fatalf("RF switch %v, want %v", h.switches, want)
	}
}

// Going back to RX after TxDone can fail too.
func TestSendRxRestartFails(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{})
	configured(t, r)
	c.set(func(c *stubChip) { c.startRxErr = errBoom })
	if err := sendWith(t, r, h, c, context.Background(), evTxDone); !errors.Is(err, errBoom) {
		t.Fatalf("Send = %v", err)
	}
}

func TestSendContextEnds(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{})
	configured(t, r)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := sendWith(t, r, h, c, ctx)
	if !errors.Is(err, radio.ErrTxFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send = %v", err)
	}
	c.get(func(c *stubChip) {
		if c.standbys < 3 { // configure, send, abort
			t.Fatalf("standby called %d times", c.standbys)
		}
	})
}

func TestSendClosedWhileWaiting(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{})
	configured(t, r)
	go func() {
		eventually(t, "transmit", func() bool {
			var n int
			c.get(func(c *stubChip) { n = c.transmits })
			return n > 0
		})
		r.Close()
	}()
	if err := r.Send(context.Background(), []byte{1}); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("Send = %v", err)
	}
}

func TestReceiveHandling(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{})
	configured(t, r)

	c.set(func(c *stubChip) { c.data = []byte("pkt") })
	raise(h, c, evRxDone|evHeaderValid)
	f := waitFrame(t, r)
	if string(f.Data) != "pkt" || f.RSSI != -80 || f.SNR != 6.5 || f.At.IsZero() {
		t.Fatalf("frame %+v", f)
	}

	// A header error, a CRC error, a failed read and an empty packet: errors, no frames.
	raise(h, c, evHeaderErr)
	raise(h, c, evRxDone|evCrcErr)
	eventually(t, "crc error", func() bool { return r.Stats(context.Background()).Errors == 2 })
	c.set(func(c *stubChip) { c.packetErr = errBoom })
	raise(h, c, evRxDone)
	eventually(t, "packet error", func() bool { return r.Stats(context.Background()).Errors == 3 })
	c.set(func(c *stubChip) { c.packetErr, c.data = nil, nil })
	raise(h, c, evRxDone)
	eventually(t, "empty packet", func() bool {
		var n int
		c.get(func(c *stubChip) { n = len(c.pending) })
		return n == 0
	})
	c.get(func(c *stubChip) {
		if c.starts != 2 { // configure, and the CRC error's FIFO reset
			t.Fatalf("startRx called %d times", c.starts)
		}
	})
	st := r.Stats(context.Background())
	if st.RxPackets != 1 || st.Errors != 3 {
		t.Fatalf("stats %+v", st)
	}
}

// A consumer that doesn't read loses frames, counted as errors.
func TestReceiveFramesFull(t *testing.T) {
	h, c := newStubHAL(), &stubChip{data: []byte{7}}
	r := startStub(t, h, c, Board{})
	configured(t, r)
	for range 3 { // the stub radio buffers 2
		raise(h, c, evRxDone)
	}
	eventually(t, "drop", func() bool { return r.Stats(context.Background()).Errors == 1 })
	if st := r.Stats(context.Background()); st.RxPackets != 3 {
		t.Fatalf("stats %+v", st)
	}
}

func TestEventsErrorCounted(t *testing.T) {
	h, c := newStubHAL(), &stubChip{eventsErr: errBoom}
	r := startStub(t, h, c, Board{})
	h.irq <- struct{}{}
	eventually(t, "error", func() bool { return r.Stats(context.Background()).Errors >= 1 })
}

// Without an IRQ line the loop polls, and samples the noise floor while idle.
func TestPollingAndNoiseFloor(t *testing.T) {
	h, c := newStubHAL(), &stubChip{rssiValue: -117}
	h.noIRQ = true
	r := startStub(t, h, c, Board{})
	// Not configured: no noise sampling.
	eventually(t, "polling", func() bool { return h.waitCount() >= 3 })
	c.get(func(c *stubChip) {
		if c.rssiCalls != 0 {
			t.Fatalf("sampled noise before configure")
		}
	})
	configured(t, r)
	eventually(t, "noise floor", func() bool { return r.Stats(context.Background()).NoiseFloorDBm == -117 })
}

func TestNoiseFloorErrorKeepsTrying(t *testing.T) {
	h, c := newStubHAL(), &stubChip{rssiErr: errBoom}
	h.noIRQ = true
	r := startStub(t, h, c, Board{})
	configured(t, r)
	eventually(t, "retries", func() bool {
		var n int
		c.get(func(c *stubChip) { n = c.rssiCalls })
		return n >= 2
	})
	if st := r.Stats(context.Background()); st.NoiseFloorDBm != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// A WaitIRQ error that doesn't mean the adapter is gone is logged and the loop carries on.
func TestWaitIRQErrorContinues(t *testing.T) {
	h, c := newStubHAL(), &stubChip{data: []byte{1}}
	h.waitErr, h.waitOnce = errBoom, true
	r := startStub(t, h, c, Board{})
	configured(t, r)
	raise(h, c, evRxDone)
	waitFrame(t, r)
	if !r.Stats(context.Background()).Connected {
		t.Fatal("disconnected after a transient error")
	}
}

func TestChannelBusy(t *testing.T) {
	ctx := context.Background()
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{})
	if busy, err := r.ChannelBusy(ctx); busy || err != nil {
		t.Fatalf("idle: %v %v", busy, err)
	}
	// A preamble seen long ago no longer counts.
	r.receiving.Store(time.Now().Add(-4 * time.Second).UnixNano())
	if busy, _ := r.ChannelBusy(ctx); busy {
		t.Fatal("stale preamble counted")
	}

	h2, bc := newStubHAL(), busyChip{&stubChip{busy: true}}
	r2 := startStub(t, h2, bc, Board{})
	if busy, err := r2.ChannelBusy(ctx); !busy || err != nil {
		t.Fatalf("chip probe: %v %v", busy, err)
	}
	bc.set(func(c *stubChip) { c.busy, c.busyErr = false, errBoom })
	if _, err := r2.ChannelBusy(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("probe error: %v", err)
	}
}

func TestInfoAndDiagnostics(t *testing.T) {
	cases := []struct {
		board Board
		want  radio.Info
	}{
		{Board{Module: ModuleSX1262, SPIDev: "spidev0.0"}, radio.Info{Driver: "spi", Device: "/dev/spidev0.0", Firmware: "sx1262", Name: "sx1262"}},
		{Board{Name: "Stick", Module: ModuleLR1121, USB: &USBID{VID: 0x1A86, PID: 0x5512}},
			radio.Info{Driver: "spi", Device: "usb:1a86:5512", Firmware: "lr1121", Name: "Stick"}},
	}
	for _, tc := range cases {
		r := startStub(t, newStubHAL(), &stubChip{}, tc.board)
		if got := r.Info(); got != tc.want {
			t.Errorf("Info = %+v, want %+v", got, tc.want)
		}
		if d := r.Diagnostics(); len(d) != 1 || d[0] != "stub chip" {
			t.Errorf("Diagnostics = %q", d)
		}
	}
}

func TestCloseOnce(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	r := startStub(t, h, c, Board{})
	for range 2 {
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := <-r.Frames(); ok {
		t.Fatal("frames open after Close")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed != 1 {
		t.Fatalf("HAL closed %d times", h.closed)
	}
}

// busyHAL has a BUSY line that the test controls.
type busyHAL struct {
	stubHAL
	busyFor int // Busy reports true this many times
	busyErr error
	calls   int
}

func (h *busyHAL) Busy() (bool, error) {
	h.calls++
	if h.busyErr != nil {
		return false, h.busyErr
	}
	if h.calls <= h.busyFor {
		return true, nil
	}
	return false, nil
}

type noBusyHAL struct{ stubHAL }

func (*noBusyHAL) HasBusy() bool { return false }

func TestWaitBusy(t *testing.T) {
	h := &busyHAL{busyFor: 3}
	if err := (bus{h: h}).waitBusy(time.Second); err != nil || h.calls != 4 {
		t.Fatalf("waitBusy = %v after %d polls", err, h.calls)
	}
	h = &busyHAL{busyFor: 1 << 30}
	if err := (bus{h: h}).waitBusy(5 * time.Millisecond); !errors.Is(err, errStayedBusy) {
		t.Fatalf("stuck BUSY: %v", err)
	}
	h = &busyHAL{busyErr: errBoom}
	if _, err := (bus{h: h}).get(0x12, 2); !errors.Is(err, errBoom) {
		t.Fatalf("BUSY read error: %v", err)
	}
	// Without a BUSY line: a fixed delay of timeout/50, at least 2 ms.
	start := time.Now()
	if err := (bus{h: &noBusyHAL{}}).waitBusy(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 2*time.Millisecond {
		t.Fatalf("waited only %v", d)
	}
}
