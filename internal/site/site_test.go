package site

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func host(t *testing.T, preset pb.Config_LoRaConfig_ModemPreset, id string) *mesh.Host {
	t.Helper()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: phy.Preset(preset), RadioID: id, StateDir: t.TempDir()}, null.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// LongFast and MediumFast share 869.525 MHz in EU_868, so one must wait for the other.
func TestOverlappingRadiosTakeTurns(t *testing.T) {
	s := New(0)
	lf, mf := host(t, pb.Config_LoRaConfig_LONG_FAST, "lf"), host(t, pb.Config_LoRaConfig_MEDIUM_FAST, "mf")
	s.Add(lf)
	s.Add(mf)
	if len(s.Overlaps(lf)) != 1 {
		t.Fatalf("LongFast should overlap MediumFast")
	}

	release, err := s.Acquire(context.Background(), lf)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		rel, err := s.Acquire(context.Background(), mf)
		if err == nil {
			rel()
		}
		got <- err
	}()
	select {
	case <-got:
		t.Fatal("MediumFast keyed up while LongFast was on air")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MediumFast never got its turn")
	}
}

// LongModerate (125 kHz at 869.5875) and LongSlow (125 kHz at 869.4625) don't overlap.
func TestSeparateChannelsDontBlock(t *testing.T) {
	s := New(0)
	a, b := host(t, pb.Config_LoRaConfig_LONG_MODERATE, "a"), host(t, pb.Config_LoRaConfig_LONG_SLOW, "b")
	s.Add(a)
	s.Add(b)
	ra, err := s.Acquire(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer ra()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	rb, err := s.Acquire(ctx, b)
	if err != nil {
		t.Fatalf("non-overlapping radio was blocked: %v", err)
	}
	rb()
}

func TestSiteDutyBudget(t *testing.T) {
	s := New(1)
	h := host(t, pb.Config_LoRaConfig_LONG_FAST, "lf")
	s.Add(h)
	h.Air.AddTx(time.Now(), 40_000, 0) // 40 s of the last hour ≈ 1.1 %
	if _, err := s.Acquire(context.Background(), h); !errors.Is(err, mesh.ErrSiteDutyCycle) {
		t.Fatalf("Acquire over budget = %v, want ErrSiteDutyCycle", err)
	}
}
