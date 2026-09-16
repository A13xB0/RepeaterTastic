package null

import (
	"context"
	"errors"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

var _ radio.Radio = (*Radio)(nil)

func TestNullRadio(t *testing.T) {
	r := New()
	ctx := context.Background()
	if err := r.Configure(ctx, radio.Config{SF: 11}); err != nil {
		t.Fatalf("Configure = %v", err)
	}
	for range 3 {
		if err := r.Send(ctx, []byte{1, 2}); err != nil {
			t.Fatalf("Send = %v", err)
		}
	}
	if busy, err := r.ChannelBusy(ctx); busy || err != nil {
		t.Fatalf("ChannelBusy = %v, %v", busy, err)
	}
	if info := r.Info(); info.Driver != "none" || info.Name != "no radio" {
		t.Fatalf("Info = %+v", info)
	}
	if st := r.Stats(ctx); !st.Connected || st.TxPackets != 3 {
		t.Fatalf("Stats = %+v", st)
	}
}

func TestNullSendCancelled(t *testing.T) {
	r := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Send(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send = %v", err)
	}
	// The attempt still counts as transmitted.
	if st := r.Stats(context.Background()); st.TxPackets != 1 {
		t.Fatalf("TxPackets = %d", st.TxPackets)
	}
}

func TestNullCloseIdempotent(t *testing.T) {
	r := New()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-r.Frames(); ok {
		t.Fatal("Frames should be closed")
	}
}
