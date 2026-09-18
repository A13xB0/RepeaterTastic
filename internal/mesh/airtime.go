package mesh

import (
	"sync"
	"time"
)

// StatBucket is the resolution airtime statistics are stored at beyond the
// rolling hour: asking the API for anything finer over a longer window would be
// inventing detail that was never recorded.
const StatBucket = 10 * time.Minute

const (
	minuteSlots = 60 // rolling hour, 1-minute resolution (duty cycle)
	statBucket  = StatBucket
	statSlots   = 7 * 24 * 6 // 7 days of 10-minute buckets
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
	recent  []struct { // for channel utilisation over the last 60 s
		at time.Time
		ms float64
	}
}

func NewAirtime() *Airtime { return &Airtime{stats: make([]AirtimeBucket, statSlots)} }

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

// AddTx records a transmission; origin 0 means a relay.
func (a *Airtime) AddTx(now time.Time, ms float64, origin uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.minute(now).txMs += ms
	b := a.bucket(now)
	b.TxMs += ms
	if origin == 0 {
		b.RelayMs += ms
	} else {
		b.ByIdentity[origin] += ms
	}
	a.addRecent(now, ms)
}

func (a *Airtime) AddRx(now time.Time, ms float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.minute(now).rxMs += ms
	a.bucket(now).RxMs += ms
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
