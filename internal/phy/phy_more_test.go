package phy

import (
	"math"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/pb"
)

func TestResolveErrors(t *testing.T) {
	if _, err := Resolve(Options{Region: "MARS", Preset: pb.Config_LoRaConfig_LONG_FAST}); err == nil || !strings.Contains(err.Error(), "unknown region") {
		t.Fatalf("region err = %v", err)
	}
	if _, err := Resolve(Options{Region: "EU_868", Preset: Preset(999)}); err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("preset err = %v", err)
	}
}

func TestResolveRegionIsCaseInsensitive(t *testing.T) {
	rp, err := Resolve(Options{Region: "eu_868", Preset: pb.Config_LoRaConfig_LONG_FAST})
	if err != nil {
		t.Fatal(err)
	}
	if rp.Region.Code != pb.Config_LoRaConfig_EU_868 {
		t.Fatalf("region = %v", rp.Region.Code)
	}
}

func TestResolveSlotSelection(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		slot int
		freq float64
	}{
		{"channel override", Options{Region: "US", Preset: pb.Config_LoRaConfig_LONG_FAST, ChannelNum: 1}, 0, 902.125},
		{"region fixed slot", Options{Region: "EU_N_868", Preset: pb.Config_LoRaConfig_NARROW_FAST}, 0, 869.4417},
		{"channel name hash", Options{Region: "US", Preset: pb.Config_LoRaConfig_LONG_FAST, PrimaryChannelName: "LongFast"}, 19, 906.875},
		{"frequency override", Options{Region: "US", Preset: pb.Config_LoRaConfig_LONG_FAST, OverrideFreqMHz: 915.5}, -1, 915.5},
		{"offset", Options{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, FreqOffsetMHz: 0.01}, 0, 869.535},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rp, err := Resolve(c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if rp.Slot != c.slot || math.Abs(rp.FrequencyMHz-c.freq) > 1e-6 {
				t.Fatalf("slot=%d freq=%.5f, want %d %.5f", rp.Slot, rp.FrequencyMHz, c.slot, c.freq)
			}
		})
	}
}

// withRegion installs a synthetic region for the duration of a test.
func withRegion(t *testing.T, ri RegionInfo) {
	t.Helper()
	Regions[ri.Name] = ri
	t.Cleanup(func() { delete(Regions, ri.Name) })
}

func TestResolveSyntheticRegions(t *testing.T) {
	presetHash := r(pb.Config_LoRaConfig_US, 902.0, 928.0, 100, 30)
	presetHash.Name, presetHash.OverrideSlot = "TEST_PRESET_HASH", -1
	withRegion(t, presetHash)
	noSlots := r(pb.Config_LoRaConfig_US, 900.0, 900.0, 100, 30)
	noSlots.Name = "TEST_NO_SLOTS"
	withRegion(t, noSlots)

	// Preset-name hash ignores the channel name.
	rp, err := Resolve(Options{Region: "TEST_PRESET_HASH", Preset: pb.Config_LoRaConfig_LONG_FAST, PrimaryChannelName: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if want := int(DJB2("LongFast") % uint32(rp.NumSlots)); rp.Slot != want {
		t.Fatalf("preset hash slot = %d, want %d", rp.Slot, want)
	}
	rp, err = Resolve(Options{Region: "TEST_NO_SLOTS", Preset: pb.Config_LoRaConfig_LONG_FAST})
	if err != nil {
		t.Fatal(err)
	}
	if rp.NumSlots != 0 || rp.Slot != 0 {
		t.Fatalf("no slots: %+v", rp)
	}
}

func TestResolveWideLoRaAndPower(t *testing.T) {
	rp, err := Resolve(Options{Region: "LORA_24", Preset: pb.Config_LoRaConfig_LONG_FAST, TxPowerDBm: 50})
	if err != nil {
		t.Fatal(err)
	}
	if rp.Preamble != 12 || rp.TxPowerDBm != 10 {
		t.Fatalf("preamble=%d power=%d", rp.Preamble, rp.TxPowerDBm)
	}
	rp, err = Resolve(Options{Region: "US", Preset: pb.Config_LoRaConfig_LONG_FAST, TxPowerDBm: 17})
	if err != nil {
		t.Fatal(err)
	}
	if rp.Preamble != PreambleSymbols || rp.TxPowerDBm != 17 || rp.SyncWord != SyncWord {
		t.Fatalf("preamble=%d power=%d sync=%x", rp.Preamble, rp.TxPowerDBm, rp.SyncWord)
	}
}

func TestPresetName(t *testing.T) {
	rp := RadioParams{Preset: pb.Config_LoRaConfig_MEDIUM_SLOW}
	if got := rp.PresetName(); got != "MediumSlow" {
		t.Fatalf("PresetName = %q", got)
	}
}

func TestAirtimeLowSF(t *testing.T) {
	// SF6 uses the 6.25-symbol preamble tail and no header bits.
	got := AirtimeMs(10, 6, 125, 5, 8)
	tSym := 64.0 / 125
	num := 8*10 + 16 - 24
	nPayload := 8 + int(math.Ceil(float64(num)/24))*5
	want := (8 + 6.25 + float64(nPayload)) * tSym
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("AirtimeMs = %v, want %v", got, want)
	}
	// Low data rate optimisation (tSym >= 16ms) shrinks the denominator.
	if AirtimeMs(10, 12, 125, 8, 16) <= AirtimeMs(10, 11, 125, 8, 16) {
		t.Fatal("SF12 should take longer than SF11")
	}
}

func TestFloodDelayBounds(t *testing.T) {
	const slot = 10.0
	for range 50 {
		// SNR 10 => cw 8. Router: [0, 16) slots.
		if d := FloodDelayMs(10, slot, true); d < 0 || d >= 16*slot {
			t.Fatalf("router delay %v", d)
		}
		// Client: 2*CWMax slots plus [0, 256) slots.
		if d := FloodDelayMs(10, slot, false); d < 16*slot || d >= (16+256)*slot {
			t.Fatalf("client delay %v", d)
		}
	}
	if d := FloodDelayMs(-20, slot, false); d < 16*slot || d >= (16+8)*slot {
		t.Fatalf("low snr client delay %v", d)
	}
}

func TestOwnTxDelayBounds(t *testing.T) {
	for range 50 {
		if d := OwnTxDelayMs(0, 5); d < 0 || d >= 8*5 || math.Mod(d, 5) != 0 {
			t.Fatalf("idle delay %v", d)
		}
		if d := OwnTxDelayMs(100, 5); d < 0 || d >= 256*5 {
			t.Fatalf("busy delay %v", d)
		}
	}
}

func TestRetransmissionMs(t *testing.T) {
	rp, err := Resolve(Options{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST})
	if err != nil {
		t.Fatal(err)
	}
	got := RetransmissionMs(50, rp, 0)
	want := 2*rp.AirtimeMs(50) + float64(8+16+32)*rp.SlotTimeMs() + ProcessingMs
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("RetransmissionMs = %v, want %v", got, want)
	}
	if RetransmissionMs(50, rp, 100) <= got {
		t.Fatal("busier channel should back off longer")
	}
}
