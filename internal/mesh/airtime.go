package mesh

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// StatBucket is the resolution airtime statistics are stored at beyond the
// rolling hour: asking the API for anything finer over a longer window would be
// inventing detail that was never recorded.
const StatBucket = 10 * time.Minute

// FineBucket is the resolution airtime is kept at over the recent past, for
// charts that want to show bursts rather than averages. Ten-minute buckets
// flatten a two-minute spree to a fifth of its height.
const FineBucket = time.Minute

const (
	minuteSlots = 60 // rolling hour, 1-minute resolution (duty cycle)
	statBucket  = StatBucket
	statSlots   = 7 * 24 * 6 // 7 days of 10-minute buckets
	fineSlots   = 48 * 60    // 2 days of 1-minute buckets

	// FineWindow is how far back the per-minute ring can answer for.
	FineWindow = fineSlots * FineBucket
)

type minuteSlot struct {
	start time.Time
	txMs  float64
	rxMs  float64
}

// AirtimeBucket is one 10-minute statistics bucket.
type AirtimeBucket struct {
	Start      time.Time
	TxMs       float64
	RxMs       float64
	RelayMs    float64
	ByIdentity map[uint32]float64
}

// Airtime tracks transmit/receive airtime for the duty-cycle budget and statistics.
type Airtime struct {
	mu      sync.Mutex
	minutes [minuteSlots]minuteSlot
	stats   []AirtimeBucket // ring, newest at statHead
	head    int
	fine     []AirtimeBucket
	fineHead int
	recent  []struct { // for channel utilisation over the last 60 s
		at time.Time
		ms float64
	}
}

func NewAirtime() *Airtime {
	return &Airtime{stats: make([]AirtimeBucket, statSlots), fine: make([]AirtimeBucket, fineSlots)}
}

func (a *Airtime) minute(now time.Time) *minuteSlot {
	start := now.Truncate(time.Minute)
	s := &a.minutes[start.Unix()/60%minuteSlots]
	if !s.start.Equal(start) {
		*s = minuteSlot{start: start}
	}
	return s
}

func (a *Airtime) bucket(now time.Time) *AirtimeBucket {
	start := now.Truncate(statBucket)
	b := &a.stats[a.head]
	if !b.Start.Equal(start) {
		if !b.Start.IsZero() {
			a.head = (a.head + 1) % len(a.stats)
			b = &a.stats[a.head]
		}
		*b = AirtimeBucket{Start: start, ByIdentity: map[uint32]float64{}}
	}
	return b
}

func (a *Airtime) fineBucket(now time.Time) *AirtimeBucket {
	start := now.Truncate(FineBucket)
	b := &a.fine[a.fineHead]
	if !b.Start.Equal(start) {
		if !b.Start.IsZero() {
			a.fineHead = (a.fineHead + 1) % len(a.fine)
			b = &a.fine[a.fineHead]
		}
		*b = AirtimeBucket{Start: start, ByIdentity: map[uint32]float64{}}
	}
	return b
}

// FineBuckets returns per-minute buckets within window, oldest first.
func (a *Airtime) FineBuckets(now time.Time, window time.Duration) []AirtimeBucket {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AirtimeBucket, 0, len(a.fine))
	for i := 1; i <= len(a.fine); i++ {
		b := a.fine[(a.fineHead+i)%len(a.fine)]
		if b.Start.IsZero() || now.Sub(b.Start) > window {
			continue
		}
		cp := b
		cp.ByIdentity = make(map[uint32]float64, len(b.ByIdentity))
		for k, v := range b.ByIdentity {
			cp.ByIdentity[k] = v
		}
		out = append(out, cp)
	}
	return out
}

// AddTx records a transmission; origin 0 means a relay.
func (a *Airtime) AddTx(now time.Time, ms float64, origin uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.minute(now).txMs += ms
	b := a.bucket(now)
	b.TxMs += ms
	f := a.fineBucket(now)
	f.TxMs += ms
	if origin == 0 {
		b.RelayMs += ms
		f.RelayMs += ms
	} else {
		b.ByIdentity[origin] += ms
		f.ByIdentity[origin] += ms
	}
	a.addRecent(now, ms)
}

func (a *Airtime) AddRx(now time.Time, ms float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.minute(now).rxMs += ms
	a.bucket(now).RxMs += ms
	a.fineBucket(now).RxMs += ms
	a.addRecent(now, ms)
}

func (a *Airtime) addRecent(now time.Time, ms float64) {
	a.recent = append(a.recent, struct {
		at time.Time
		ms float64
	}{now, ms})
	cut := 0
	for cut < len(a.recent) && now.Sub(a.recent[cut].at) > time.Minute {
		cut++
	}
	a.recent = a.recent[cut:]
}

// HourTotals returns TX and RX milliseconds over the last hour.
func (a *Airtime) HourTotals(now time.Time) (txMs, rxMs float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.minutes {
		if !s.start.IsZero() && now.Sub(s.start) < time.Hour {
			txMs += s.txMs
			rxMs += s.rxMs
		}
	}
	return
}

// TxPercent is the hourly TX utilisation compared against the region duty cycle.
func (a *Airtime) TxPercent(now time.Time) float64 {
	tx, _ := a.HourTotals(now)
	return tx / 3600000 * 100
}

// ChannelUtilPercent is on-air time (ours + heard) over the last minute.
func (a *Airtime) ChannelUtilPercent(now time.Time) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	var ms float64
	for _, r := range a.recent {
		if now.Sub(r.at) <= time.Minute {
			ms += r.ms
		}
	}
	return ms / 60000 * 100
}

// IdentityHourMs is one identity's TX airtime in the last hour (10-minute resolution).
func (a *Airtime) IdentityHourMs(now time.Time, id uint32) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	var ms float64
	for _, b := range a.stats {
		if !b.Start.IsZero() && now.Sub(b.Start) < time.Hour {
			ms += b.ByIdentity[id]
		}
	}
	return ms
}

// Buckets returns stats buckets within window, oldest first.
func (a *Airtime) Buckets(now time.Time, window time.Duration) []AirtimeBucket {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []AirtimeBucket
	for i := 1; i <= len(a.stats); i++ {
		b := a.stats[(a.head+i)%len(a.stats)]
		if b.Start.IsZero() || now.Sub(b.Start) > window {
			continue
		}
		cp := b
		cp.ByIdentity = make(map[uint32]float64, len(b.ByIdentity))
		for k, v := range b.ByIdentity {
			cp.ByIdentity[k] = v
		}
		out = append(out, cp)
	}
	return out
}

// Airtime lives in memory, so every restart used to cost the charts their
// history — a Pi that reboots at 03:00 showed a blank day until the evening.
// The rings are small enough to write out whole: two days of per-minute
// totals and a week of ten-minute ones.

// AirtimeSnapshot is the saved form of the rings.
type AirtimeSnapshot struct {
	Saved time.Time       `json:"saved"`
	Stats []AirtimeBucket `json:"stats"`
	Fine  []AirtimeBucket `json:"fine"`
}

// Snapshot returns the buckets worth keeping across a restart, oldest first.
func (a *Airtime) Snapshot(now time.Time) AirtimeSnapshot {
	snap := AirtimeSnapshot{Saved: now}
	snap.Stats = a.Buckets(now, time.Duration(statSlots)*statBucket)
	snap.Fine = a.FineBuckets(now, FineWindow)
	return snap
}

// Restore puts saved buckets back, dropping any that have since aged out.
// Buckets recorded after the snapshot (a clock that went backwards, or a stale
// file) are ignored rather than trusted.
func (a *Airtime) Restore(snap AirtimeSnapshot, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, b := range snap.Stats {
		if b.Start.IsZero() || b.Start.After(now) || now.Sub(b.Start) > time.Duration(statSlots)*statBucket {
			continue
		}
		if b.ByIdentity == nil {
			b.ByIdentity = map[uint32]float64{}
		}
		a.head = (a.head + 1) % len(a.stats)
		a.stats[a.head] = b
	}
	for _, b := range snap.Fine {
		if b.Start.IsZero() || b.Start.After(now) || now.Sub(b.Start) > FineWindow {
			continue
		}
		if b.ByIdentity == nil {
			b.ByIdentity = map[uint32]float64{}
		}
		a.fineHead = (a.fineHead + 1) % len(a.fine)
		a.fine[a.fineHead] = b
	}
}

// SaveTo writes the rings to path.
func (a *Airtime) SaveTo(path string, now time.Time) error {
	return writeJSONAtomic(path, a.Snapshot(now))
}

// LoadFrom restores the rings from path. A missing file is not an error: it is
// simply the first run after this was added, or a fresh install.
func (a *Airtime) LoadFrom(path string, now time.Time) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snap AirtimeSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return err
	}
	a.Restore(snap, now)
	return nil
}
