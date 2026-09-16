package lazy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// feedRadio is a controllable radio: the test pushes frames and closes it at will.
type feedRadio struct {
	frames    chan radio.Frame
	once      sync.Once
	configErr error
	mu        sync.Mutex
	sent      [][]byte
	closed    bool
}

func newFeedRadio() *feedRadio { return &feedRadio{frames: make(chan radio.Frame, 4)} }

func (f *feedRadio) Configure(context.Context, radio.Config) error { return f.configErr }
func (f *feedRadio) Send(_ context.Context, b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, b)
	return nil
}
func (f *feedRadio) Frames() <-chan radio.Frame                { return f.frames }
func (f *feedRadio) ChannelBusy(context.Context) (bool, error) { return true, nil }
func (f *feedRadio) Info() radio.Info                          { return radio.Info{Driver: "feed", Name: "feeder"} }
func (f *feedRadio) Stats(context.Context) radio.Stats {
	return radio.Stats{Connected: true, RxPackets: 7}
}
func (f *feedRadio) Close() error {
	f.once.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.frames)
	})
	return nil
}

func (f *feedRadio) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// logRecorder collects log lines safely.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Until the radio opens, every call reports not connected and Info describes the target.
func TestNotConnectedYet(t *testing.T) {
	var attempts sync.WaitGroup
	attempts.Add(3)
	var n int
	var mu sync.Mutex
	openErr := errors.New("no device")
	logs := &logRecorder{}
	r := New(func(context.Context, string, string) (radio.Radio, error) {
		mu.Lock()
		n++
		if n <= 3 {
			attempts.Done()
		}
		mu.Unlock()
		return nil, openErr
	}, radio.Info{Driver: "kiss", Device: "/dev/ttyX"}, time.Millisecond, logs.logf)

	attempts.Wait()
	ctx := context.Background()
	if err := r.Configure(ctx, radio.Config{}); !errors.Is(err, radio.ErrNotConnected) {
		t.Errorf("Configure = %v", err)
	}
	if err := r.Send(ctx, []byte{1}); !errors.Is(err, radio.ErrNotConnected) {
		t.Errorf("Send = %v", err)
	}
	if busy, err := r.ChannelBusy(ctx); busy || !errors.Is(err, radio.ErrNotConnected) {
		t.Errorf("ChannelBusy = %v, %v", busy, err)
	}
	if info := r.Info(); info.Driver != "kiss" || info.Device != "/dev/ttyX" {
		t.Errorf("Info = %+v", info)
	}
	if st := r.Stats(ctx); st != (radio.Stats{}) {
		t.Errorf("Stats = %+v", st)
	}
	if !errors.Is(r.LastError(), openErr) {
		t.Errorf("LastError = %v", r.LastError())
	}
	if r.Inner() != nil {
		t.Error("Inner should be nil")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Only the first failure is logged, however many retries happened.
	if got := logs.count(); got != 1 {
		t.Errorf("logged %d lines, want 1", got)
	}
	if _, ok := <-r.Frames(); ok {
		t.Error("Frames should be closed after Close")
	}
}

// Once open, calls go to the inner radio and its frames are forwarded.
func TestDelegatesWhenOpen(t *testing.T) {
	inner := newFeedRadio()
	r := New(func(context.Context, string, string) (radio.Radio, error) { return inner, nil },
		radio.Info{Driver: "feed"}, time.Hour, func(string, ...any) {})
	waitFor(t, "open", func() bool { return r.Inner() != nil })
	ctx := context.Background()

	if err := r.Send(ctx, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if busy, err := r.ChannelBusy(ctx); !busy || err != nil {
		t.Errorf("ChannelBusy = %v, %v", busy, err)
	}
	if info := r.Info(); info.Name != "feeder" {
		t.Errorf("Info = %+v", info)
	}
	if st := r.Stats(ctx); st.RxPackets != 7 {
		t.Errorf("Stats = %+v", st)
	}
	if r.LastError() != nil {
		t.Errorf("LastError = %v", r.LastError())
	}
	inner.frames <- radio.Frame{Data: []byte{9}, RSSI: -80}
	select {
	case f := <-r.Frames():
		if f.RSSI != -80 || len(f.Data) != 1 || f.Data[0] != 9 {
			t.Errorf("frame = %+v", f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("frame not forwarded")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !inner.isClosed() {
		t.Error("inner radio not closed")
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.sent) != 1 || string(inner.sent[0]) != "hi" {
		t.Errorf("sent = %q", inner.sent)
	}
}

// A failed Configure isn't remembered for the next reopen.
func TestConfigureErrorNotRemembered(t *testing.T) {
	inner := newFeedRadio()
	inner.configErr = radio.ErrUnsupported
	r := New(func(context.Context, string, string) (radio.Radio, error) { return inner, nil },
		radio.Info{}, time.Hour, func(string, ...any) {})
	defer r.Close()
	waitFor(t, "open", func() bool { return r.Inner() != nil })
	if err := r.Configure(context.Background(), radio.Config{SF: 7}); !errors.Is(err, radio.ErrUnsupported) {
		t.Fatalf("Configure = %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg != nil {
		t.Fatalf("cfg remembered after failure: %+v", r.cfg)
	}
}

// Retarget to the same device is accepted without waking the loop or logging.
func TestRetargetSameDevice(t *testing.T) {
	logs := &logRecorder{}
	r := New(func(ctx context.Context, _, _ string) (radio.Radio, error) {
		return nil, errors.New("absent")
	}, radio.Info{Driver: "kiss", Device: "/dev/a"}, time.Hour, logs.logf)
	defer r.Close()
	waitFor(t, "first failure", func() bool { return logs.count() == 1 })
	if !r.Retarget("kiss", "/dev/a") {
		t.Fatal("Retarget refused")
	}
	if got := logs.count(); got != 1 {
		t.Fatalf("logged %d lines, want 1", got)
	}
	if r.LastError() != nil {
		t.Fatal("Retarget should clear the last error")
	}
}

// A radio lost with a failing reconfigure is still reopened, and a shutdown during the back-off
// ends the loop promptly.
func TestReopenConfigureFailsThenShutdown(t *testing.T) {
	var mu sync.Mutex
	var opened []*feedRadio
	logs := &logRecorder{}
	r := New(func(context.Context, string, string) (radio.Radio, error) {
		f := newFeedRadio()
		mu.Lock()
		if len(opened) > 0 {
			f.configErr = errors.New("bad settings")
		}
		opened = append(opened, f)
		mu.Unlock()
		return f, nil
	}, radio.Info{}, time.Hour, logs.logf)
	waitFor(t, "open", func() bool { return r.Inner() != nil })
	if err := r.Configure(context.Background(), radio.Config{SF: 9}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := opened[0]
	mu.Unlock()
	_ = first.Close()
	waitFor(t, "reopen", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(opened) == 2 && r.Inner() != nil
	})
	// lost, configure failure, open again
	waitFor(t, "logs", func() bool { return logs.count() == 3 })

	// Lose it again and shut down during the one-second back-off.
	mu.Lock()
	second := opened[1]
	mu.Unlock()
	_ = second.Close()
	waitFor(t, "lost", func() bool { return r.Inner() == nil })
	start := time.Now()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("Close took %v", d)
	}
}
