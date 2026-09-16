package kiss

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// inject writes a raw frame from the fake modem to the host.
func (f *fakeModem) inject(typ byte, parts ...[]byte) {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	f.send(c, typ, parts...)
}

// logSink records log lines so tests can wait for one.
type logSink struct {
	t     *testing.T
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.t.Log(msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg)
}

func (l *logSink) has(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func TestModemErrorText(t *testing.T) {
	cases := []struct {
		code byte
		want string
	}{
		{ErrCodeInvalidLength, "invalid length"},
		{ErrCodeInvalidParam, "invalid parameter"},
		{ErrCodeNoCallback, "no callback"},
		{ErrCodeUnknownCmd, "unknown command"},
		{ErrCodeTxBusy, "tx busy"},
		{0x42, "modem error (0x42)"},
	}
	for _, c := range cases {
		got := (&ModemError{Cmd: cmdSetRadio, Code: c.code}).Error()
		if !strings.HasPrefix(got, "kiss: command 0x09: ") || !strings.Contains(got, c.want) {
			t.Errorf("code 0x%02x: %q", c.code, got)
		}
	}
}

func TestDecoderOversizeFrame(t *testing.T) {
	var d decoder
	d.feed(fend)
	for range maxFrame {
		if d.feed(0x01) != nil {
			t.Fatal("frame ended early")
		}
	}
	// One byte too many discards the frame; bytes until the next FEND are ignored.
	d.feed(0x02)
	d.feed(0x03)
	if fr := d.feed(fend); fr != nil {
		t.Fatalf("oversize frame delivered (%d bytes)", len(fr))
	}
	d.feed(0x04)
	if fr := d.feed(fend); !bytes.Equal(fr, []byte{0x04}) {
		t.Fatalf("next frame = %x", fr)
	}
}

func TestOpenWithoutDevice(t *testing.T) {
	if _, err := Open(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "no device") {
		t.Fatalf("Open = %v", err)
	}
	// The default dialer opens the serial port, which doesn't exist here.
	if _, err := Open(ctx, Options{Device: t.TempDir() + "/ttyNONE", Logf: t.Logf}); err == nil {
		t.Fatal("Open of a missing serial device succeeded")
	}
}

func TestHandshakeTimeout(t *testing.T) {
	f := newFake()
	f.silent[cmdPing] = true
	start := time.Now()
	_, err := Open(ctx, Options{Device: "fake", Dial: f.dial, CommandTimeout: 30 * time.Millisecond,
		HandshakeTimeout: 150 * time.Millisecond, Logf: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "did not answer ping") {
		t.Fatalf("Open = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestHandshakeCancelled(t *testing.T) {
	f := newFake()
	f.silent[cmdPing] = true
	c, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err := Open(c, Options{Device: "fake", Dial: f.dial, CommandTimeout: time.Second, HandshakeTimeout: 5 * time.Second, Logf: t.Logf})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open = %v", err)
	}
}

func TestHandshakeBadVersionReply(t *testing.T) {
	f := newFake()
	f.replies[cmdGetVersion] = []byte{0x91}
	_, err := Open(ctx, Options{Device: "fake", Dial: f.dial, CommandTimeout: 100 * time.Millisecond, Logf: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "short reply") {
		t.Fatalf("Open = %v", err)
	}
}

// A modem without a device name still opens, with an empty name.
func TestHandshakeNoName(t *testing.T) {
	f := newFake()
	f.replies[cmdGetDeviceName] = []byte{respError, ErrCodeUnknownCmd}
	m := openFake(t, f)
	if in := m.Info(); in.Name != "" || in.Firmware != "Mesh KISS v2" {
		t.Fatalf("Info = %+v", in)
	}
}

func TestCommandNoReply(t *testing.T) {
	f := newFake()
	m := openFake(t, f, func(o *Options) { o.CommandTimeout = 20 * time.Millisecond })
	f.get(func() { f.silent[cmdIsChannelBusy] = true })
	_, err := m.ChannelBusy(ctx)
	if err == nil || !strings.Contains(err.Error(), "no reply after 3 attempts") {
		t.Fatalf("ChannelBusy = %v", err)
	}
	f.get(func() {
		if f.cmdCount[cmdIsChannelBusy] != 3 {
			t.Fatalf("sent %d times", f.cmdCount[cmdIsChannelBusy])
		}
	})
}

func TestCommandContextCancelled(t *testing.T) {
	f := newFake()
	m := openFake(t, f, func(o *Options) { o.CommandTimeout = time.Second })
	f.get(func() { f.silent[cmdIsChannelBusy] = true })
	c, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := m.ChannelBusy(c); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ChannelBusy = %v", err)
	}
}

// A TxBusy in place of a reply, then cancellation while waiting for the genuine reply.
func TestCommandBusyThenCancelled(t *testing.T) {
	f := newFake()
	m := openFake(t, f, func(o *Options) { o.CommandTimeout = time.Second })
	f.get(func() { f.silent[cmdIsChannelBusy] = true })
	c, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := m.ChannelBusy(c)
		errc <- err
	}()
	waitFor(t, "command sent", func() bool {
		var n int
		f.get(func() { n = f.cmdCount[cmdIsChannelBusy] })
		return n == 1
	})
	f.inject(typeSetHardware, []byte{respError, ErrCodeTxBusy})
	waitFor(t, "busy noted", func() bool { return m.TxBusyErrors() == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("ChannelBusy = %v", err)
	}
}

// A TxBusy in place of a reply, then the link drops.
func TestCommandBusyThenDisconnect(t *testing.T) {
	f := newFake()
	m := openFake(t, f, func(o *Options) { o.CommandTimeout = time.Second })
	f.get(func() { f.silent[cmdGetRadio] = true })
	errc := make(chan error, 1)
	go func() {
		_, err := m.ModemConfig(ctx)
		errc <- err
	}()
	waitFor(t, "command sent", func() bool {
		var n int
		f.get(func() { n = f.cmdCount[cmdGetRadio] })
		return n == 1
	})
	f.inject(typeSetHardware, []byte{respError, ErrCodeTxBusy})
	waitFor(t, "busy noted", func() bool { return m.TxBusyErrors() == 1 })
	f.unplug()
	if err := <-errc; !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("ModemConfig = %v", err)
	}
}

func TestCommandDisconnectWhileWaiting(t *testing.T) {
	f := newFake()
	m := openFake(t, f, func(o *Options) { o.CommandTimeout = time.Second })
	f.get(func() { f.silent[cmdGetAirtime] = true })
	errc := make(chan error, 1)
	go func() {
		_, err := m.Airtime(ctx, 10)
		errc <- err
	}()
	waitFor(t, "command sent", func() bool {
		var n int
		f.get(func() { n = f.cmdCount[cmdGetAirtime] })
		return n == 1
	})
	f.unplug()
	if err := <-errc; !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("Airtime = %v", err)
	}
}

// Unsolicited or malformed hardware frames are ignored and don't disturb later commands.
func TestUnsolicitedFramesIgnored(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	f.inject(typeSetHardware)                              // too short
	f.inject(typeSetHardware, []byte{respOK})              // no waiter
	f.inject(typeSetHardware, []byte{respTxDone, 1})       // no TX in flight
	f.inject(typeSetHardware, []byte{respRxMeta, 1})       // short meta
	f.inject(typeSetHardware, []byte{respRxMeta, 1, 0xB0}) // meta without a frame
	f.inject(typeTxDelay, []byte{5})                       // not sent by modems
	f.get(func() { f.chanBusy = true })
	if busy, err := m.ChannelBusy(ctx); err != nil || !busy {
		t.Fatalf("ChannelBusy = %v, %v", busy, err)
	}
	select {
	case fr := <-m.Frames():
		t.Fatalf("unexpected frame %+v", fr)
	default:
	}
}

func TestAirtime(t *testing.T) {
	f := newFake()
	f.replies[cmdGetAirtime] = binary.LittleEndian.AppendUint32([]byte{cmdGetAirtime | 0x80}, 1234)
	m := openFake(t, f)
	d, err := m.Airtime(ctx, 50)
	if err != nil || d != 1234*time.Millisecond {
		t.Fatalf("Airtime = %v, %v", d, err)
	}
	f.get(func() { f.replies[cmdGetAirtime] = []byte{respError, ErrCodeInvalidParam} })
	var me *ModemError
	if _, err := m.Airtime(ctx, 50); !errors.As(err, &me) || me.Code != ErrCodeInvalidParam || me.Cmd != cmdGetAirtime {
		t.Fatalf("Airtime error = %v", err)
	}
}

func TestDisconnectedCalls(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	before := m.Stats(ctx)
	f.unplug()
	waitFor(t, "disconnect", func() bool { return m.session() == nil })
	if _, err := m.Airtime(ctx, 1); !errors.Is(err, radio.ErrNotConnected) {
		t.Errorf("Airtime = %v", err)
	}
	if _, err := m.ModemConfig(ctx); !errors.Is(err, radio.ErrNotConnected) {
		t.Errorf("ModemConfig = %v", err)
	}
	st := m.Stats(ctx)
	if st.Connected || st.RxPackets != before.RxPackets || st.NoiseFloorDBm != before.NoiseFloorDBm {
		t.Errorf("Stats while disconnected = %+v, before %+v", st, before)
	}
}

func TestModemConfigUnpatched(t *testing.T) {
	f := newFake()
	f.version = 1
	m := openFake(t, f)
	stock := fast
	stock.SyncWord, stock.Preamble = MeshCoreSyncWord, 0
	if err := m.Configure(ctx, stock); err != nil {
		t.Fatal(err)
	}
	got, err := m.ModemConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != stock {
		t.Fatalf("ModemConfig = %+v, want %+v", got, stock)
	}
}

func TestModemConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		cmd  byte
	}{
		{"radio", cmdGetRadio},
		{"power", cmdGetTxPower},
		{"phy extra", cmdGetPhyExtra},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.replies[c.cmd] = []byte{respError, ErrCodeNoCallback}
			m := openFake(t, f)
			var me *ModemError
			if _, err := m.ModemConfig(ctx); !errors.As(err, &me) || me.Cmd != c.cmd {
				t.Fatalf("ModemConfig = %v", err)
			}
		})
	}
}

func TestChannelBusyError(t *testing.T) {
	f := newFake()
	f.replies[cmdIsChannelBusy] = []byte{cmdIsChannelBusy | 0x80} // no data
	m := openFake(t, f)
	if _, err := m.ChannelBusy(ctx); err == nil || !strings.Contains(err.Error(), "short reply") {
		t.Fatalf("ChannelBusy = %v", err)
	}
}

func TestConfigureValidation(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	bad := []radio.Config{
		{FrequencyHz: 1, BandwidthHz: 1, SF: 4, CR: 5},
		{FrequencyHz: 1, BandwidthHz: 1, SF: 13, CR: 5},
		{FrequencyHz: 1, BandwidthHz: 1, SF: 7, CR: 4},
		{FrequencyHz: 1, BandwidthHz: 1, SF: 7, CR: 9},
		{FrequencyHz: 1, SF: 7, CR: 5},
		{BandwidthHz: 1, SF: 7, CR: 5},
	}
	for _, c := range bad {
		if err := m.Configure(ctx, c); err == nil || !strings.Contains(err.Error(), "invalid config") {
			t.Errorf("Configure(%+v) = %v", c, err)
		}
	}
	f.get(func() {
		if f.cmdCount[cmdSetRadio] != 0 {
			t.Fatal("invalid config reached the modem")
		}
	})
}

// Configure and Send wait for the transmitter; a context ending first abandons the wait.
func TestTxSemContextCancelled(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	m.txSem <- struct{}{} // an in-flight transmission
	defer func() { <-m.txSem }()
	c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := m.Configure(c, fast); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Configure = %v", err)
	}
	if err := m.Send(c, []byte{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send = %v", err)
	}
}

func TestConfigureModemErrors(t *testing.T) {
	cases := []struct {
		name string
		cmd  byte
	}{
		{"set radio", cmdSetRadio},
		{"set power", cmdSetTxPower},
		{"set sync", cmdSetSyncWord},
		{"set preamble", cmdSetPreamble},
		{"read back", cmdGetPhyExtra},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.replies[c.cmd] = []byte{respError, ErrCodeInvalidParam}
			m := openFake(t, f)
			var me *ModemError
			if err := m.Configure(ctx, fast); !errors.As(err, &me) || me.Cmd != c.cmd {
				t.Fatalf("Configure = %v", err)
			}
		})
	}
}

func TestConfigureReadBackMismatch(t *testing.T) {
	f := newFake()
	f.replies[cmdGetPhyExtra] = []byte{cmdGetPhyExtra | 0x80, 0x34, 8, 0}
	m := openFake(t, f)
	err := m.Configure(ctx, fast)
	if err == nil || !strings.Contains(err.Error(), "sync 0x34 preamble 8") {
		t.Fatalf("Configure = %v", err)
	}
}

// Patched firmware that lacks SetSyncWord still takes a stock MeshCore PHY, without read-back.
func TestConfigureUnknownSyncStockPHY(t *testing.T) {
	f := newFake()
	f.unknownSync = true
	m := openFake(t, f)
	stock := fast
	stock.SyncWord, stock.Preamble = MeshCoreSyncWord, 0
	if err := m.Configure(ctx, stock); err != nil {
		t.Fatal(err)
	}
	// Persistence has no reply: wait for the fake to see it.
	waitFor(t, "persistence", func() bool {
		var p byte
		f.get(func() { p = f.persist })
		return p == 255
	})
	f.get(func() {
		if f.cmdCount[cmdGetPhyExtra] != 0 || f.cmdCount[cmdSetPreamble] != 0 {
			t.Fatalf("unexpected commands %v", f.cmdCount)
		}
	})
}

// Leaving the preamble at 0 expects the MeshCore default back.
func TestConfigureDefaultPreamble(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	c := fast
	c.Preamble = 0
	if err := m.Configure(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got := m.txTimeout(10); got <= m.opts.TxMargin {
		t.Fatalf("txTimeout = %v", got)
	}
}

func TestTxTimeoutUnconfigured(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	if got, want := m.txTimeout(10), 20*time.Second+m.opts.TxMargin; got != want {
		t.Fatalf("txTimeout = %v, want %v", got, want)
	}
}

func TestSendNoTxDone(t *testing.T) {
	f := newFake()
	f.txDelay = time.Hour
	m := openFake(t, f)
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	err := m.Send(ctx, []byte{1})
	if !errors.Is(err, radio.ErrTxFailed) || !strings.Contains(err.Error(), "no TxDone") {
		t.Fatalf("Send = %v", err)
	}
	if err := m.Send(ctx, nil); err == nil {
		t.Fatal("empty frame accepted")
	}
}

func TestSendDisconnectDuringTx(t *testing.T) {
	f := newFake()
	f.txDelay = time.Hour
	m := openFake(t, f)
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- m.Send(ctx, []byte{1}) }()
	waitFor(t, "tx pending", func() bool {
		var p bool
		f.get(func() { p = f.txPending })
		return p
	})
	f.unplug()
	if err := <-errc; !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("Send = %v", err)
	}
}

// A frame whose RxMeta is still awaited is delivered as soon as the link drops.
func TestPendingFrameFlushedOnDisconnect(t *testing.T) {
	f := newFake()
	m := openFake(t, f, func(o *Options) { o.RxMetaWait = time.Hour })
	f.receive([]byte{0x5A}, false, 0, 0)
	waitFor(t, "pending", func() bool {
		m.rxMu.Lock()
		defer m.rxMu.Unlock()
		return m.havePending
	})
	f.unplug()
	if fr := nextFrame(t, m); !bytes.Equal(fr.Data, []byte{0x5A}) || fr.RSSI != 0 {
		t.Fatalf("frame %+v", fr)
	}
}

// Firmware downgraded while unplugged: the settings it can't take are logged, and the modem
// still reconnects.
func TestReconnectUnsupportedConfigLogged(t *testing.T) {
	f := newFake()
	logs := &logSink{t: t}
	m := openFake(t, f, func(o *Options) { o.Logf = logs.logf })
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	f.unplug()
	waitFor(t, "disconnect", func() bool { return m.session() == nil })
	f.get(func() { f.version = 1 })
	f.plug()
	waitFor(t, "reconnect", func() bool { return m.Stats(ctx).Reconnects == 1 })
	if !logs.has("cannot set sync word/preamble") {
		t.Fatalf("unsupported config not logged: %v", logs.lines)
	}
	if m.Version() != 1 {
		t.Fatalf("Version = %d", m.Version())
	}
}

// A config the modem rejects on reconnect keeps it disconnected (logged once) until it accepts.
func TestReconnectReapplyFails(t *testing.T) {
	f := newFake()
	logs := &logSink{t: t}
	m := openFake(t, f, func(o *Options) { o.Logf = logs.logf })
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	f.unplug()
	waitFor(t, "disconnect", func() bool { return m.session() == nil })
	f.get(func() { f.replies[cmdSetRadio] = []byte{respError, ErrCodeInvalidParam} })
	f.plug()
	waitFor(t, "reapply failure", func() bool { return logs.has("re-applying config") })
	waitFor(t, "several attempts", func() bool {
		var n int
		f.get(func() { n = f.dials })
		return n >= 4
	})
	if m.Stats(ctx).Connected {
		t.Fatal("connected despite rejected config")
	}
	f.get(func() { delete(f.replies, cmdSetRadio) })
	waitFor(t, "reconnect", func() bool { return m.Stats(ctx).Connected })
	logs.mu.Lock()
	defer logs.mu.Unlock()
	n := 0
	for _, l := range logs.lines {
		if strings.Contains(l, "re-applying config") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reapply failure logged %d times", n)
	}
}

// Closing while the modem is away stops the reconnect loop.
func TestCloseWhileReconnecting(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	f.unplug()
	waitFor(t, "disconnect", func() bool { return m.session() == nil })
	done := make(chan struct{})
	go func() {
		_ = m.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}
