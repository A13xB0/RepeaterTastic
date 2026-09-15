// Package null is a radio that transmits into the void and never receives. It lets a host run on
// links only (UDP multicast, tests) without LoRa hardware.
package null

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

type Radio struct {
	frames chan radio.Frame
	once   sync.Once
	tx     atomic.Uint32
}

func New() *Radio { return &Radio{frames: make(chan radio.Frame)} }

func (r *Radio) Configure(context.Context, radio.Config) error { return nil }
func (r *Radio) Send(ctx context.Context, frame []byte) error {
	r.tx.Add(1)
	return ctx.Err()
}
func (r *Radio) Frames() <-chan radio.Frame                { return r.frames }
func (r *Radio) ChannelBusy(context.Context) (bool, error) { return false, nil }
func (r *Radio) Info() radio.Info                          { return radio.Info{Driver: "none", Name: "no radio"} }
func (r *Radio) Stats(context.Context) radio.Stats {
	return radio.Stats{Connected: true, TxPackets: r.tx.Load()}
}
func (r *Radio) Close() error { r.once.Do(func() { close(r.frames) }); return nil }
