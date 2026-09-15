package kiss

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/radio"
)

// ------------------------------------------------------------------------------- fake modem

// fakeModem speaks the patched MeshCore KISS protocol over net.Pipe. Every dial is a "boot":
// PHY state resets to firmware defaults, as a real modem does on reconnect.
type fakeModem struct {
	mu sync.Mutex

	// behaviour knobs
	version     byte
	name        string
	unknownSync bool          // answer SetSyncWord with UnknownCmd despite version
	txDelay     time.Duration // Data -> TxDone
	txResult    byte
	busyOnData  bool // answer Data with TxBusy and never TxDone
	dropReplies int  // replace the next N command replies with TxBusy (full output queue)
	chanBusy    bool
	unplugged   bool

	// state
	conn                            net.Conn
	dials                           int
	freq, bw                        uint32
	sf, cr                          byte
	power                           int8
	sync                            byte
	preamble                        uint16
	kissTxDelay, persist            byte
	txPending                       bool
	dataWhilePending, sent, cmdSeen int
	cmdCount                        map[byte]int
}

func newFake() *fakeModem {
	return &fakeModem{version: 2, name: "Heltec V3", txDelay: 10 * time.Millisecond, txResult: 1}
}

func (f *fakeModem) dial() (io.ReadWriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unplugged {
		return nil, errors.New("open /dev/ttyUSB0: no such file or directory")
	}
	host, dev := net.Pipe()
	f.conn = dev
	f.dials++
	f.freq, f.bw, f.sf, f.cr, f.power = 0, 0, 0, 0, 0
	f.sync, f.preamble, f.kissTxDelay, f.persist = MeshCoreSyncWord, 0, 50, 63
	f.txPending = false
	f.cmdCount = map[byte]int{}
	go f.serve(dev)
	return host, nil
}

func (f *fakeModem) unplug() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unplugged = true
	if f.conn != nil {
		f.conn.Close()
	}
}

func (f *fakeModem) plug() {
	f.mu.Lock()
	f.unplugged = false
	f.mu.Unlock()
}

func (f *fakeModem) send(c net.Conn, typ byte, parts ...[]byte) {
	_, _ = c.Write(appendFrame(nil, typ, parts...)) // net.Pipe writes are atomic per call
}

func (f *fakeModem) hw(c net.Conn, sub byte, data ...byte) {
	f.send(c, typeSetHardware, []byte{sub}, data)
}

// receive injects an over-the-air packet, optionally followed by RxMeta.
func (f *fakeModem) receive(data []byte, meta bool, snrQuarter, rssi int8) {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	f.send(c, typeData, data)
	if meta {
		f.hw(c, respRxMeta, byte(snrQuarter), byte(rssi))
	}
}

func (f *fakeModem) serve(c net.Conn) {
	var d decoder
	buf := make([]byte, 256)
	for {
		n, err := c.Read(buf)
		for _, b := range buf[:n] {
			if fr := d.feed(b); fr != nil {
				f.handle(c, bytes.Clone(fr))
			}
		}
		if err != nil {
			return
		}
	}
}

func (f *fakeModem) handle(c net.Conn, fr []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data := fr[1:]
	switch fr[0] {
	case typeData:
		if f.txPending || f.busyOnData {
			if f.txPending {
				f.dataWhilePending++
			}
			go f.hw(c, respError, ErrCodeTxBusy)
			return
		}
		f.txPending = true
		result := f.txResult
		time.AfterFunc(f.txDelay, func() {
			f.mu.Lock()
			f.txPending = false
			f.sent++
			f.mu.Unlock()
			f.hw(c, respTxDone, result)
		})
	case typeTxDelay:
		f.kissTxDelay = data[0]
	case typePersistence:
		f.persist = data[0]
	case typeSetHardware:
		cmd, arg := data[0], data[1:]
		f.cmdCount[cmd]++
		reply, ok := f.command(cmd, arg)
		if f.dropReplies > 0 {
			f.dropReplies--
			reply, ok = []byte{respError, ErrCodeTxBusy}, true
		}
		if ok {
			go f.hw(c, reply[0], reply[1:]...) // off the read goroutine, like the modem's output queue
		}
	}
}

func (f *fakeModem) command(cmd byte, a []byte) ([]byte, bool) {
	ok := []byte{respOK}
	u32 := binary.LittleEndian.AppendUint32
	switch cmd {
	case cmdPing:
		return []byte{0x97}, true
	case cmdGetVersion:
		return []byte{0x91, f.version, 0}, true
	case cmdGetDeviceName:
		return append([]byte{0x96}, f.name...), true
	case cmdSetRadio:
		f.freq, f.bw = binary.LittleEndian.Uint32(a), binary.LittleEndian.Uint32(a[4:])
		f.sf, f.cr = a[8], a[9]
		return ok, true
	case cmdGetRadio:
		r := u32(u32([]byte{0x8B}, f.freq), f.bw)
		return append(r, f.sf, f.cr), true
	case cmdSetTxPower:
		f.power = int8(a[0])
		return ok, true
	case cmdGetTxPower:
		return []byte{0x8C, byte(f.power)}, true
	case cmdIsChannelBusy:
		b := byte(0)
		if f.chanBusy {
			b = 1
		}
		return []byte{0x8E, b}, true
	case cmdGetNoiseFloor:
		return binary.LittleEndian.AppendUint16([]byte{0x90}, uint16(0xFF92)), true // -110
	case cmdGetStats:
		return u32(u32(u32([]byte{0x92}, 7), uint32(f.sent)), 1), true
	}
	if f.version < 2 || (cmd == cmdSetSyncWord && f.unknownSync) {
		return []byte{respError, ErrCodeUnknownCmd}, true
	}
	switch cmd {
	case cmdSetSyncWord:
		f.sync = a[0]
		return ok, true
	case cmdSetPreamble:
		f.preamble = binary.LittleEndian.Uint16(a)
		return ok, true
	case cmdGetPhyExtra:
		p := f.preamble
		if p == 0 {
			p = meshCorePreamble(f.sf)
		}
		return binary.LittleEndian.AppendUint16([]byte{0x9D, f.sync}, p), true
	}
	return []byte{respError, ErrCodeUnknownCmd}, true
}

func (f *fakeModem) get(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// ------------------------------------------------------------------------------------ helpers

var longFast = radio.Config{FrequencyHz: 869525000, BandwidthHz: 250000, SF: 11, CR: 5, SyncWord: 0x2B, Preamble: 16, TxPowerDBm: 27}

// fast is a PHY with ~0.1 s worst-case airtime, so TxDone timeouts stay short.
var fast = radio.Config{FrequencyHz: 869525000, BandwidthHz: 500000, SF: 7, CR: 5, SyncWord: 0x2B, Preamble: 16, TxPowerDBm: 10}

func openFake(t *testing.T, f *fakeModem, mod ...func(*Options)) *Modem {
	t.Helper()
	o := Options{Device: "fake", Dial: f.dial, ReconnectInterval: 20 * time.Millisecond,
		CommandTimeout: 200 * time.Millisecond, HandshakeTimeout: time.Second, RxMetaWait: 50 * time.Millisecond,
		TxMargin: 100 * time.Millisecond, Logf: t.Logf}
	for _, fn := range mod {
		fn(&o)
	}
	m, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func nextFrame(t *testing.T, m *Modem) radio.Frame {
	t.Helper()
	select {
	case fr, ok := <-m.Frames():
		if !ok {
			t.Fatal("frames channel closed")
		}
		return fr
	case <-time.After(2 * time.Second):
		t.Fatal("no frame")
	}
	return radio.Frame{}
}

var ctx = context.Background()

// -------------------------------------------------------------------------------------- tests

func TestFraming(t *testing.T) {
	payload := []byte{0x01, fend, 0x02, fesc, fesc, fend, tfend, tfesc}
	enc := appendFrame(nil, typeData, payload)
	if bytes.Count(enc, []byte{fend}) != 2 {
		t.Fatalf("unescaped FEND in %x", enc)
	}
	var d decoder
	var got [][]byte
	// Garbage before the first FEND, a back-to-back empty frame and a bad escape are ignored.
	stream := append([]byte{0x55, 0xAA}, enc...)
	stream = append(stream, fend, fend, typeSetHardware, fesc, 0x42, 0x97, fend)
	for _, b := range stream {
		if fr := d.feed(b); fr != nil {
			got = append(got, bytes.Clone(fr))
		}
	}
	if len(got) != 2 || !bytes.Equal(got[0], append([]byte{typeData}, payload...)) || !bytes.Equal(got[1], []byte{typeSetHardware, 0x97}) {
		t.Fatalf("decoded %x", got)
	}
	// README example bytes.
	if e := appendFrame(nil, typeSetHardware, []byte{cmdSetRadio}, []byte{0x08, 0xE6, 0xD3, 0x33, 0x90, 0xD0, 0x03, 0x00, 0x0B, 0x05}); !bytes.Equal(e,
		[]byte{0xC0, 0x06, 0x09, 0x08, 0xE6, 0xD3, 0x33, 0x90, 0xD0, 0x03, 0x00, 0x0B, 0x05, 0xC0}) {
		t.Fatalf("SetRadio frame %x", e)
	}
}

func TestOpenConfigure(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	if in := m.Info(); in.Driver != "kiss" || in.Name != "Heltec V3" || in.Firmware != "MeshCore KISS v2" || m.Version() != 2 {
		t.Fatalf("info %+v", in)
	}
	if err := m.Configure(ctx, longFast); err != nil {
		t.Fatal(err)
	}
	f.get(func() {
		if f.freq != 869525000 || f.bw != 250000 || f.sf != 11 || f.cr != 5 || f.power != 27 || f.sync != 0x2B ||
			f.preamble != 16 || f.kissTxDelay != 0 || f.persist != 255 || f.cmdCount[cmdGetPhyExtra] != 1 {
			t.Fatalf("fake state %+v", f)
		}
	})
	got, err := m.ModemConfig(ctx)
	if err != nil || got != longFast {
		t.Fatalf("ModemConfig %+v %v", got, err)
	}
	if err := m.Configure(ctx, radio.Config{SF: 11, CR: 5, Preamble: 3, FrequencyHz: 1, BandwidthHz: 1}); err == nil {
		t.Fatal("bad preamble accepted")
	}
}

func TestConfigureUnpatched(t *testing.T) {
	f := newFake()
	f.version = 1
	m := openFake(t, f)
	err := m.Configure(ctx, longFast)
	if !errors.Is(err, radio.ErrUnsupported) || !strings.Contains(err.Error(), "Heltec_v3_kiss_modem-factory.bin") {
		t.Fatalf("got %v", err)
	}
	f.get(func() {
		if f.cmdCount[cmdSetRadio] != 0 {
			t.Fatal("unpatched modem was partially configured")
		}
	})
	// Stock MeshCore PHY is still usable.
	mc := longFast
	mc.SyncWord = MeshCoreSyncWord
	if err := m.Configure(ctx, mc); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureUnknownSyncCmd(t *testing.T) {
	f := newFake()
	f.unknownSync = true
	m := openFake(t, f)
	if err := m.Configure(ctx, longFast); !errors.Is(err, radio.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestSend(t *testing.T) {
	f := newFake()
	f.txDelay = 30 * time.Millisecond
	m := openFake(t, f)
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- m.Send(ctx, []byte{byte(i), fend, fesc})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.get(func() {
		if f.sent != 5 || f.dataWhilePending != 0 {
			t.Fatalf("sent %d, data while pending %d", f.sent, f.dataWhilePending)
		}
	})

	f.get(func() { f.txResult = 0 })
	if err := m.Send(ctx, []byte{1}); !errors.Is(err, radio.ErrTxFailed) {
		t.Fatalf("got %v", err)
	}
	if err := m.Send(ctx, make([]byte, 256)); err == nil {
		t.Fatal("256-byte frame accepted")
	}
}

func TestSendCancelKeepsSingleFlight(t *testing.T) {
	f := newFake()
	f.txDelay = 150 * time.Millisecond
	m := openFake(t, f)
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := m.Send(c, []byte{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if err := m.Send(ctx, []byte{2}); err != nil {
		t.Fatal(err)
	}
	f.get(func() {
		if f.sent != 2 || f.dataWhilePending != 0 {
			t.Fatalf("sent %d, data while pending %d", f.sent, f.dataWhilePending)
		}
	})
}

func TestSendTxBusy(t *testing.T) {
	f := newFake()
	f.busyOnData = true
	m := openFake(t, f)
	if err := m.Configure(ctx, fast); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := m.Send(ctx, []byte{1, 2, 3}); !errors.Is(err, radio.ErrBusy) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if m.TxBusyErrors() != 1 {
		t.Fatalf("tx busy count %d", m.TxBusyErrors())
	}
}

func TestRxMetaPairing(t *testing.T) {
	f := newFake()
	m := openFake(t, f)

	f.receive([]byte{0xAA, fend}, true, -30, -97) // SNR -7.5 dB
	fr := nextFrame(t, m)
	if !bytes.Equal(fr.Data, []byte{0xAA, fend}) || fr.SNR != -7.5 || fr.RSSI != -97 {
		t.Fatalf("frame %+v", fr)
	}

	start := time.Now()
	f.receive([]byte{0xBB}, false, 0, 0)
	fr = nextFrame(t, m)
	if !bytes.Equal(fr.Data, []byte{0xBB}) || fr.SNR != 0 || fr.RSSI != 0 || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("frame %+v after %v", fr, time.Since(start))
	}

	// Meta lost for the first of two back-to-back frames: first is flushed unpaired, second pairs.
	f.receive([]byte{0x01}, false, 0, 0)
	f.receive([]byte{0x02}, true, 22, -50)
	a, b := nextFrame(t, m), nextFrame(t, m)
	if a.Data[0] != 1 || a.RSSI != 0 || b.Data[0] != 2 || b.RSSI != -50 || b.SNR != 5.5 {
		t.Fatalf("frames %+v %+v", a, b)
	}
}

func TestFramesDropOldest(t *testing.T) {
	f := newFake()
	m := openFake(t, f, func(o *Options) { o.FrameBuffer = 2 })
	for i := range 5 {
		f.receive([]byte{byte(i)}, true, 0, -80)
	}
	waitFor(t, "drops", func() bool { return m.Dropped() == 3 })
	if a, b := nextFrame(t, m), nextFrame(t, m); a.Data[0] != 3 || b.Data[0] != 4 {
		t.Fatalf("kept %v %v", a.Data, b.Data)
	}
}

func TestCommandsAndLostReply(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	f.get(func() { f.chanBusy = true; f.dropReplies = 1 })
	busy, err := m.ChannelBusy(ctx)
	if err != nil || !busy {
		t.Fatalf("busy %v %v", busy, err)
	}
	f.get(func() {
		if f.cmdCount[cmdIsChannelBusy] != 2 {
			t.Fatalf("IsChannelBusy sent %d times, want a retry", f.cmdCount[cmdIsChannelBusy])
		}
	})
	st := m.Stats(ctx)
	if !st.Connected || st.RxPackets != 7 || st.Errors != 1 || st.NoiseFloorDBm != -110 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReconnect(t *testing.T) {
	f := newFake()
	m := openFake(t, f)
	if err := m.Configure(ctx, longFast); err != nil {
		t.Fatal(err)
	}
	f.unplug()
	waitFor(t, "disconnect", func() bool { return !m.Stats(ctx).Connected })
	if err := m.Send(ctx, []byte{1}); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("send while unplugged: %v", err)
	}
	if _, err := m.ChannelBusy(ctx); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("busy while unplugged: %v", err)
	}
	// A config change while unplugged is kept and applied on reconnect.
	want := longFast
	want.TxPowerDBm = 14
	if err := m.Configure(ctx, want); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("configure while unplugged: %v", err)
	}
	time.Sleep(60 * time.Millisecond) // a few failed dials
	f.plug()
	waitFor(t, "reconnect", func() bool { st := m.Stats(ctx); return st.Connected && st.Reconnects == 1 })
	f.get(func() {
		if f.dials != 2 || f.sync != 0x2B || f.preamble != 16 || f.freq != longFast.FrequencyHz || f.power != 14 || f.persist != 255 {
			t.Fatalf("config not re-applied: %+v", f)
		}
	})
	f.receive([]byte{0x42}, true, 4, -60)
	if fr := nextFrame(t, m); fr.Data[0] != 0x42 || fr.SNR != 1 {
		t.Fatalf("frame %+v", fr)
	}
	if err := m.Send(ctx, []byte{1}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailsAndClose(t *testing.T) {
	f := newFake()
	f.unplugged = true
	if _, err := Open(ctx, Options{Dial: f.dial, Logf: t.Logf}); err == nil {
		t.Fatal("open succeeded without device")
	}
	f.plug()
	m := openFake(t, f)
	m.Close()
	if _, ok := <-m.Frames(); ok {
		t.Fatal("frames not closed")
	}
	if err := m.Send(ctx, []byte{1}); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("send after close: %v", err)
	}
}
