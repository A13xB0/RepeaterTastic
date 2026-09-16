package sim

import (
	"errors"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

func TestInfoAndDefaultBuffer(t *testing.T) {
	h := NewHub(0)
	r := h.Attach("node1", 0)
	if cap(r.frames) != 64 {
		t.Fatalf("default buffer = %d, want 64", cap(r.frames))
	}
	if info := r.Info(); info.Driver != "sim" || info.Device != "node1" || info.Name != "node1" {
		t.Fatalf("Info = %+v", info)
	}
}

func TestClosedRadioRefuses(t *testing.T) {
	h := NewHub(0)
	r := h.Attach("gone", 1)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Configure(ctx, longFast); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("Configure = %v", err)
	}
	if _, err := r.ChannelBusy(ctx); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("ChannelBusy = %v", err)
	}
	if st := r.Stats(ctx); st.Connected {
		t.Fatalf("Stats = %+v", st)
	}
}
