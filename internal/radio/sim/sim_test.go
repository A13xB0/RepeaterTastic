package sim

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/radio"
)

var (
	ctx      = context.Background()
	longFast = radio.Config{FrequencyHz: 869525000, BandwidthHz: 250000, SF: 11, CR: 5, SyncWord: 0x2B, Preamble: 16, TxPowerDBm: 27}
)

func attach(t *testing.T, h *Hub, name string, c radio.Config) *Radio {
	t.Helper()
	r := h.Attach(name, 16)
	if err := r.Configure(ctx, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func pending(r *Radio) int { return len(r.frames) }

func TestDeliveryAndChannels(t *testing.T) {
	h := NewHub(0)
	a := attach(t, h, "a", longFast)
	b := attach(t, h, "b", longFast)
	c := attach(t, h, "c", longFast)
	otherSync := longFast
	otherSync.SyncWord = 0x12
	otherFreq := longFast
	otherFreq.FrequencyHz += 250000
	otherSF := longFast
	otherSF.SF = 12
	x := attach(t, h, "x", otherSync)
	y := attach(t, h, "y", otherFreq)
	z := attach(t, h, "z", otherSF)
	h.SetLink("a", "c", Link{RSSI: -120, SNR: -12.5})

	if err := a.Send(ctx, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	fb := <-b.Frames()
	fc := <-c.Frames()
	if string(fb.Data) != "hi" || fb.RSSI != -80 || fb.SNR != 8 || fc.RSSI != -120 || fc.SNR != -12.5 {
		t.Fatalf("b %+v c %+v", fb, fc)
	}
	if pending(a)+pending(x)+pending(y)+pending(z) != 0 {
		t.Fatal("frame heard by sender or a radio on another channel")
	}
	if st := a.Stats(ctx); st.TxPackets != 1 || !st.Connected {
		t.Fatalf("stats %+v", st)
	}
	// Mutating the sent buffer must not affect what was delivered.
	buf := []byte{1}
	_ = a.Send(ctx, buf)
	buf[0] = 9
	if f := <-b.Frames(); f.Data[0] != 1 {
		t.Fatal("frame aliases sender buffer")
	}
}

func TestAirtimeAndChannelBusy(t *testing.T) {
	h := NewHub(0.2) // LongFast 20 bytes ~ 436 ms real, ~87 ms scaled
	a := attach(t, h, "a", longFast)
	b := attach(t, h, "b", longFast)
	want := a.Airtime(20)
	if want < 60*time.Millisecond || want > 120*time.Millisecond {
		t.Fatalf("scaled airtime %v", want)
	}
	done := make(chan time.Duration)
	start := time.Now()
	go func() {
		_ = a.Send(ctx, make([]byte, 20))
		done <- time.Since(start)
	}()
	time.Sleep(want / 3)
	if busy, _ := b.ChannelBusy(ctx); !busy {
		t.Fatal("b: channel not busy during a's transmission")
	}
	if busy, _ := a.ChannelBusy(ctx); busy {
		t.Fatal("a: own transmission reported as busy")
	}
	if pending(b) != 0 {
		t.Fatal("delivered before airtime elapsed")
	}
	if took := <-done; took < want {
		t.Fatalf("send returned after %v, airtime %v", took, want)
	}
	if pending(b) != 1 {
		t.Fatal("not delivered")
	}
	if busy, _ := b.ChannelBusy(ctx); busy {
		t.Fatal("busy after transmission")
	}
}

func TestHalfDuplexAndCancel(t *testing.T) {
	h := NewHub(0.1)
	a := attach(t, h, "a", longFast)
	b := attach(t, h, "b", longFast)
	errs := make(chan error, 2)
	go func() { errs <- a.Send(ctx, make([]byte, 30)) }()
	time.Sleep(5 * time.Millisecond)
	go func() { errs <- b.Send(ctx, make([]byte, 30)) }()
	<-errs
	<-errs
	if pending(a)+pending(b) != 0 {
		t.Fatal("overlapping transmitters heard each other")
	}

	c, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer cancel()
	if err := a.Send(c, make([]byte, 30)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if pending(b) != 0 {
		t.Fatal("aborted frame delivered")
	}
	if busy, _ := b.ChannelBusy(ctx); busy {
		t.Fatal("busy after abort")
	}
}

func TestDropsAndBuffer(t *testing.T) {
	h := NewHub(0)
	h.SetSeed(42)
	h.SetDefaultLink(Link{RSSI: -100, SNR: 0, Drop: 0.5})
	a := attach(t, h, "a", longFast)
	b := h.Attach("b", 1000)
	defer b.Close()
	if err := b.Send(ctx, []byte{1}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured send: %v", err)
	}
	_ = b.Configure(ctx, longFast)
	for range 1000 {
		_ = a.Send(ctx, []byte{1})
	}
	if n := pending(b); n < 400 || n > 600 {
		t.Fatalf("received %d of 1000 at 50%% drop", n)
	}

	small := h.Attach("small", 2)
	defer small.Close()
	_ = small.Configure(ctx, longFast)
	h.SetLink("a", "small", Link{})
	for i := range 5 {
		_ = a.Send(ctx, []byte{byte(i)})
	}
	if small.Dropped() != 3 || (<-small.Frames()).Data[0] != 3 {
		t.Fatalf("dropped %d", small.Dropped())
	}

	a.Close()
	if _, ok := <-a.Frames(); ok {
		t.Fatal("frames open after close")
	}
	if err := a.Send(ctx, []byte{1}); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("send after close: %v", err)
	}
}
