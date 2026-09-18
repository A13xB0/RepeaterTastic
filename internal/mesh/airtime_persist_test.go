package mesh

import (
	"path/filepath"
	"testing"
	"time"
)

// Per-minute airtime is what lets a chart show a burst rather than an average.
func TestFineBucketsKeepEachMinuteApart(t *testing.T) {
	a := NewAirtime()
	base := time.Now().Truncate(time.Minute).Add(-5 * time.Minute)
	for i := 0; i < 5; i++ {
		a.AddTx(base.Add(time.Duration(i)*time.Minute), float64(100*(i+1)), 42)
	}
	fine := a.FineBuckets(time.Now(), time.Hour)
	if len(fine) != 5 {
		t.Fatalf("fine buckets = %d, want one a minute for 5 minutes", len(fine))
	}
	for i, b := range fine {
		if want := float64(100 * (i + 1)); b.TxMs != want {
			t.Errorf("minute %d tx = %v, want %v (a burst must not be averaged away)", i, b.TxMs, want)
		}
	}
	// The same traffic in the coarse ring lands in one or two ten-minute buckets.
	if coarse := a.Buckets(time.Now(), time.Hour); len(coarse) > 2 {
		t.Errorf("coarse buckets = %d, want the same five minutes grouped", len(coarse))
	}
}

// A reboot used to cost the charts their history.
func TestAirtimeSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airtime.json")
	now := time.Now()

	before := NewAirtime()
	before.AddTx(now.Add(-30*time.Minute), 250, 7)
	before.AddRx(now.Add(-30*time.Minute), 900)
	before.AddTx(now.Add(-3*time.Hour), 500, 7) // still inside the two-day fine window
	if err := before.SaveTo(path, now); err != nil {
		t.Fatalf("save: %v", err)
	}

	after := NewAirtime()
	if err := after.LoadFrom(path, now); err != nil {
		t.Fatalf("load: %v", err)
	}
	fine := after.FineBuckets(now, 4*time.Hour)
	if len(fine) != 2 {
		t.Fatalf("restored fine buckets = %d, want 2", len(fine))
	}
	var totalTx, totalRx float64
	for _, b := range fine {
		totalTx += b.TxMs
		totalRx += b.RxMs
	}
	if totalTx != 750 || totalRx != 900 {
		t.Errorf("restored tx/rx = %v/%v, want 750/900", totalTx, totalRx)
	}
	coarse := after.Buckets(now, 4*time.Hour)
	if len(coarse) == 0 {
		t.Error("the ten-minute ring should have been restored too")
	}
}

// A stale file must not outrank what is happening now.
func TestRestoreDropsWhatHasAgedOutOrIsFromTheFuture(t *testing.T) {
	now := time.Now()
	a := NewAirtime()
	a.Restore(AirtimeSnapshot{
		Saved: now,
		Fine: []AirtimeBucket{
			{Start: now.Add(-3 * FineWindow), TxMs: 1}, // long aged out
			{Start: now.Add(time.Hour), TxMs: 2},       // stamped in the future
			{Start: now.Add(-time.Minute), TxMs: 3},    // good
		},
	}, now)
	fine := a.FineBuckets(now, FineWindow)
	if len(fine) != 1 || fine[0].TxMs != 3 {
		t.Fatalf("restored %d buckets %v, want only the recent one", len(fine), fine)
	}
}

// Missing state is the normal first run, not a failure.
func TestLoadingAMissingFileIsNotAnError(t *testing.T) {
	a := NewAirtime()
	if err := a.LoadFrom(filepath.Join(t.TempDir(), "nope.json"), time.Now()); err != nil {
		t.Errorf("LoadFrom(missing) = %v, want nil", err)
	}
}
