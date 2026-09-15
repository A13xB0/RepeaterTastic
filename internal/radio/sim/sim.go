// Package sim is an in-memory LoRa "air" for tests: radios attached to a Hub hear each other's
// frames after the frame's time on air, when tuned to the same frequency, bandwidth, SF and sync word.
package sim

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// Link is what a receiver sees of a sender.
type Link struct {
	RSSI int16
	SNR  float32
	Drop float64 // probability 0..1 that a frame is lost
}

// Hub is the shared air.
type Hub struct {
	scale float64

	mu     sync.Mutex
	radios map[*Radio]struct{}
	links  map[[2]string]Link
	def    Link
	rng    *rand.Rand
}

// NewHub creates an empty air. timeScale multiplies airtime: 1 is real time, 0.01 runs 100x
// faster, 0 delivers instantly (ChannelBusy is then never true).
func NewHub(timeScale float64) *Hub {
	return &Hub{scale: timeScale, radios: map[*Radio]struct{}{}, links: map[[2]string]Link{},
		def: Link{RSSI: -80, SNR: 8}, rng: rand.New(rand.NewPCG(1, 2))}
}

// SetSeed makes drop decisions reproducible.
func (h *Hub) SetSeed(seed uint64) {
	h.mu.Lock()
	h.rng = rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	h.mu.Unlock()
}

// SetDefaultLink applies to every pair without an explicit link (default RSSI -80, SNR 8, no drops).
func (h *Hub) SetDefaultLink(l Link) {
	h.mu.Lock()
	h.def = l
	h.mu.Unlock()
}

// SetLink sets what radio "to" sees of radio "from" (directional).
func (h *Hub) SetLink(from, to string, l Link) {
	h.mu.Lock()
	h.links[[2]string{from, to}] = l
	h.mu.Unlock()
}

// Attach adds a radio. It hears nothing until configured.
func (h *Hub) Attach(name string, bufSize int) *Radio {
	if bufSize <= 0 {
		bufSize = 64
	}
	r := &Radio{hub: h, name: name, frames: make(chan radio.Frame, bufSize), txSem: make(chan struct{}, 1)}
	h.mu.Lock()
	h.radios[r] = struct{}{}
	h.mu.Unlock()
	return r
}

// Radio is one simulated modem. It implements radio.Radio.
type Radio struct {
	hub    *Hub
	name   string
	frames chan radio.Frame
	txSem  chan struct{}

	// guarded by hub.mu
	cfg              radio.Config
	configured       bool
	closed           bool
	txStart, txEnd   time.Time // current or last transmission
	rxCount, txCount uint32
	dropped          uint64
}

var _ radio.Radio = (*Radio)(nil)

// ErrNotConfigured is returned by Send before Configure.
var ErrNotConfigured = errors.New("sim: radio not configured")

func sameChannel(a, b radio.Config) bool {
	return a.FrequencyHz == b.FrequencyHz && a.BandwidthHz == b.BandwidthHz && a.SF == b.SF && a.SyncWord == b.SyncWord
}

func (r *Radio) Configure(_ context.Context, c radio.Config) error {
	h := r.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.closed {
		return radio.ErrNotConnected
	}
	r.cfg, r.configured = c, true
	return nil
}

// Airtime is the frame's scaled time on air under the radio's config.
func (r *Radio) Airtime(n int) time.Duration {
	h := r.hub
	h.mu.Lock()
	c := r.cfg
	h.mu.Unlock()
	pre := int(c.Preamble)
	if pre == 0 {
		pre = phy.PreambleSymbols
	}
	ms := phy.AirtimeMs(n, int(c.SF), float64(c.BandwidthHz)/1000, int(c.CR), pre)
	return time.Duration(ms * h.scale * float64(time.Millisecond))
}

// Send occupies the air for the frame's airtime, then delivers it to every other radio on the
// same channel that was not itself transmitting meanwhile (half duplex). Cancelling ctx aborts it.
func (r *Radio) Send(ctx context.Context, frame []byte) error {
	if len(frame) == 0 || len(frame) > 255 {
		return errors.New("sim: frame length outside 1..255")
	}
	select {
	case r.txSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.txSem }()
	h := r.hub
	d := r.Airtime(len(frame))
	h.mu.Lock()
	if r.closed || !r.configured {
		h.mu.Unlock()
		if r.closed {
			return radio.ErrNotConnected
		}
		return ErrNotConfigured
	}
	start := time.Now()
	r.txStart, r.txEnd = start, start.Add(d)
	h.mu.Unlock()

	if d > 0 {
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			h.mu.Lock()
			r.txEnd = time.Now()
			h.mu.Unlock()
			return ctx.Err()
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	r.txEnd = now
	if r.closed {
		return radio.ErrNotConnected
	}
	r.txCount++
	for o := range h.radios {
		if o == r || o.closed || !o.configured || !sameChannel(r.cfg, o.cfg) {
			continue
		}
		if o.txStart.Before(now) && o.txEnd.After(start) {
			continue // receiver was transmitting
		}
		l, ok := h.links[[2]string{r.name, o.name}]
		if !ok {
			l = h.def
		}
		if l.Drop > 0 && h.rng.Float64() < l.Drop {
			continue
		}
		o.rxCount++
		o.emitLocked(radio.Frame{Data: bytes.Clone(frame), RSSI: l.RSSI, SNR: l.SNR, At: now})
	}
	return nil
}

func (r *Radio) emitLocked(f radio.Frame) {
	select {
	case r.frames <- f:
		return
	default:
	}
	select {
	case <-r.frames:
		r.dropped++
	default:
	}
	r.frames <- f
}

func (r *Radio) Frames() <-chan radio.Frame { return r.frames }

// ChannelBusy is true while another radio on the same channel is transmitting.
func (r *Radio) ChannelBusy(context.Context) (bool, error) {
	h := r.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.closed {
		return false, radio.ErrNotConnected
	}
	now := time.Now()
	for o := range h.radios {
		if o != r && !o.closed && o.configured && sameChannel(r.cfg, o.cfg) && o.txEnd.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func (r *Radio) Info() radio.Info { return radio.Info{Driver: "sim", Device: r.name, Name: r.name} }

func (r *Radio) Stats(context.Context) radio.Stats {
	h := r.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	return radio.Stats{RxPackets: r.rxCount, TxPackets: r.txCount, NoiseFloorDBm: -120, Connected: !r.closed}
}

// Dropped counts frames evicted because the Frames consumer was too slow.
func (r *Radio) Dropped() uint64 {
	h := r.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	return r.dropped
}

// Close detaches the radio and closes Frames.
func (r *Radio) Close() error {
	h := r.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if !r.closed {
		r.closed = true
		delete(h.radios, r)
		close(r.frames)
	}
	return nil
}
