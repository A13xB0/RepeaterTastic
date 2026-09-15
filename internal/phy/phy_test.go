package phy

import (
	"math"
	"testing"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
)

func TestFrequencySlots(t *testing.T) {
	cases := []struct {
		region string
		slots  int
		slot   int
		freq   float64
	}{
		{"EU_868", 1, 0, 869.525},
		{"US", 104, 19, 906.875},
		{"EU_433", 4, 3, 433.875},
	}
	for _, c := range cases {
		rp, err := Resolve(Options{Region: c.region, Preset: pb.Config_LoRaConfig_LONG_FAST})
		if err != nil {
			t.Fatal(err)
		}
		if rp.NumSlots != c.slots || rp.Slot != c.slot || math.Abs(rp.FrequencyMHz-c.freq) > 1e-6 {
			t.Errorf("%s: slots=%d slot=%d freq=%.4f", c.region, rp.NumSlots, rp.Slot, rp.FrequencyMHz)
		}
	}
}

func TestTiming(t *testing.T) {
	rp, _ := Resolve(Options{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST})
	if s := rp.SlotTimeMs(); s < 28 || s > 28.2 {
		t.Fatalf("slot %.3f", s)
	}
	if a := rp.AirtimeMs(100); a < 950 || a > 1100 {
		t.Fatalf("airtime %.1f", a)
	}
	if CWSizeForSNR(-20) != 3 || CWSizeForSNR(10) != 8 {
		t.Fatal("cw map")
	}
	if rp.TxPowerDBm != 27 || rp.FrequencyHz() != 869525000 || rp.BwHz() != 250000 {
		t.Fatalf("%+v", rp)
	}
}
