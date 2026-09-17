package mesh

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// fakeRadio is a radio the test drives: frames are injected, sends recorded, and channel-busy,
// configure and send results set per test.
type fakeRadio struct {
	frames chan radio.Frame

	mu           sync.Mutex
	sent         [][]byte
	configs      []radio.Config
	busy         bool
	busyErr      error
	sendErr      error
	configureErr error
	name         string
	sentCh       chan []byte
}

func newFakeRadio() *fakeRadio {
	return &fakeRadio{frames: make(chan radio.Frame, 16), sentCh: make(chan []byte, 16)}
}

func (r *fakeRadio) Configure(_ context.Context, c radio.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs = append(r.configs, c)
	return r.configureErr
}

func (r *fakeRadio) Send(_ context.Context, frame []byte) error {
	r.mu.Lock()
	err := r.sendErr
	if err == nil {
		r.sent = append(r.sent, append([]byte(nil), frame...))
	}
	r.mu.Unlock()
	if err == nil {
		select {
		case r.sentCh <- frame:
		default:
		}
	}
	return err
}

func (r *fakeRadio) Frames() <-chan radio.Frame { return r.frames }

func (r *fakeRadio) ChannelBusy(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy, r.busyErr
}

func (r *fakeRadio) Info() radio.Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	return radio.Info{Driver: "fake", Name: r.name}
}

func (r *fakeRadio) Stats(context.Context) radio.Stats { return radio.Stats{Connected: true} }
func (r *fakeRadio) Close() error                      { return nil }

func (r *fakeRadio) sentFrames() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.sent...)
}

func (r *fakeRadio) configureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.configs)
}

func (r *fakeRadio) set(fn func(r *fakeRadio)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// virtualHost is a host on a fake radio with one virtual relay identity (so the host decodes
// frames itself).
func virtualHost(t *testing.T, cfg Config) (*Host, *fakeRadio, *Identity) {
	t.Helper()
	if cfg.Region == "" {
		cfg.Region = "EU_868"
		cfg.Preset = pb.Config_LoRaConfig_LONG_FAST
	}
	r := newFakeRadio()
	h, err := NewHost(cfg, r, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewIdentity(nil, "Relay", "RLY")
	if err != nil {
		t.Fatal(err)
	}
	relay.IsRelay = true
	if err := h.AddIdentity(relay); err != nil {
		t.Fatal(err)
	}
	return h, r, relay
}

// recordingTap is an AirTap (and LinkTap) that remembers what it was given.
type recordingTap struct {
	mu          sync.Mutex
	heard       int
	transmitted []uint32
	linkHeard   int
}

func (t *recordingTap) Heard(radio.Frame) {
	t.mu.Lock()
	t.heard++
	t.mu.Unlock()
}

func (t *recordingTap) Transmitted(_ []byte, _ *pb.MeshPacket, origin uint32) {
	t.mu.Lock()
	t.transmitted = append(t.transmitted, origin)
	t.mu.Unlock()
}

func (t *recordingTap) LinkHeard(*pb.MeshPacket) {
	t.mu.Lock()
	t.linkHeard++
	t.mu.Unlock()
}

func (t *recordingTap) counts() (heard, tx, link int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.heard, len(t.transmitted), t.linkHeard
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// nextEvent returns the first bus event of the given type.
func nextEvent(t *testing.T, ch <-chan Event, typ string) Event {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == typ {
				return e
			}
		case <-deadline:
			t.Fatalf("no %s event", typ)
			return Event{}
		}
	}
}
