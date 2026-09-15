// Package site coordinates several radios on one mast.
//
// Each radio runs its own mesh.Host. Radios whose channels overlap in frequency
// (LongFast and MediumFast both sit on 869.525 MHz in EU_868) must never transmit
// at the same time: with antennas a metre apart, one keying up blocks the other's
// receiver and both packets can be lost. The Site is the mesh.TxGate every host
// asks before transmitting. It also enforces an optional site-wide duty-cycle
// budget on top of each radio's own, for sites where the regulator (or good
// manners) counts the mast rather than each board.
package site

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
)

// Site is a mesh.TxGate shared by every host on one site.
type Site struct {
	mu      sync.Mutex
	changed chan struct{} // closed and replaced whenever a transmission ends
	hosts   []*mesh.Host
	onAir   map[*mesh.Host]span
	dutyPct atomic.Uint64 // math.Float64bits of the budget; 0 = no site-wide budget
	now     func() time.Time
}

type span struct{ lo, hi float64 } // MHz

// New returns a site. dutyPct is the site-wide airtime budget in percent of the last
// hour summed over all radios; 0 disables it.
func New(dutyPct float64) *Site {
	s := &Site{changed: make(chan struct{}), onAir: map[*mesh.Host]span{}, now: time.Now}
	s.SetDutyCyclePct(dutyPct)
	return s
}

// Add registers a host and installs the site as its transmit gate.
func (s *Site) Add(h *mesh.Host) {
	s.mu.Lock()
	s.hosts = append(s.hosts, h)
	s.mu.Unlock()
	h.SetTxGate(s)
}

// Hosts returns the registered hosts in the order they were added.
func (s *Site) Hosts() []*mesh.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*mesh.Host(nil), s.hosts...)
}

// DutyCyclePct is the site-wide budget (0 = none).
func (s *Site) DutyCyclePct() float64 { return math.Float64frombits(s.dutyPct.Load()) }

// SetDutyCyclePct changes the site-wide budget while running.
func (s *Site) SetDutyCyclePct(pct float64) { s.dutyPct.Store(math.Float64bits(pct)) }

// TxPercent is the site's summed transmit airtime over the last hour.
func (s *Site) TxPercent() float64 {
	now := s.now()
	total := 0.0
	for _, h := range s.Hosts() {
		total += h.Air.TxPercent(now)
	}
	return total
}

// Overlaps reports the other hosts whose channel overlaps h's in frequency.
func (s *Site) Overlaps(h *mesh.Host) []*mesh.Host {
	mine := spanOf(h)
	var out []*mesh.Host
	for _, o := range s.Hosts() {
		if o != h && mine.overlaps(spanOf(o)) {
			out = append(out, o)
		}
	}
	return out
}

// Acquire implements mesh.TxGate.
func (s *Site) Acquire(ctx context.Context, h *mesh.Host) (func(), error) {
	if limit := s.DutyCyclePct(); limit > 0 && s.TxPercent() >= limit {
		return nil, mesh.ErrSiteDutyCycle
	}
	mine := spanOf(h)
	for {
		s.mu.Lock()
		busy := false
		for o, sp := range s.onAir {
			if o != h && mine.overlaps(sp) {
				busy = true
				break
			}
		}
		if !busy {
			s.onAir[h] = mine
			s.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { s.release(h) }) }, nil
		}
		wait := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (s *Site) release(h *mesh.Host) {
	s.mu.Lock()
	delete(s.onAir, h)
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func spanOf(h *mesh.Host) span {
	rp := h.RadioParams()
	half := rp.BwKHz / 2000
	return span{rp.FrequencyMHz - half, rp.FrequencyMHz + half}
}

func (a span) overlaps(b span) bool { return a.lo < b.hi && b.lo < a.hi }
