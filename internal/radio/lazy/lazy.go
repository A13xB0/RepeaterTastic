// Package lazy wraps a radio that may not be available at start-up (modem unplugged, wrong port):
// it keeps trying to open it in the background so the rest of the daemon (web GUI, client APIs,
// UDP link) runs regardless.
package lazy

import (
	"context"
	"sync"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/radio"
)

type Radio struct {
	open  func(ctx context.Context) (radio.Radio, error)
	info  radio.Info
	logf  func(string, ...any)
	every time.Duration

	mu      sync.Mutex
	inner   radio.Radio
	lastErr error

	frames chan radio.Frame
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New starts opening in the background. info describes the radio until it is open.
func New(open func(ctx context.Context) (radio.Radio, error), info radio.Info, every time.Duration,
	logf func(string, ...any)) *Radio {
	r := &Radio{open: open, info: info, logf: logf, every: every, frames: make(chan radio.Frame, 64),
		done: make(chan struct{})}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	go r.loop()
	return r
}

func (r *Radio) loop() {
	defer close(r.done)
	for {
		inner, err := r.open(r.ctx)
		if err == nil {
			r.mu.Lock()
			r.inner, r.lastErr = inner, nil
			r.mu.Unlock()
			for f := range inner.Frames() {
				select {
				case r.frames <- f:
				default:
				}
			}
			return
		}
		r.mu.Lock()
		first := r.lastErr == nil
		r.lastErr = err
		r.mu.Unlock()
		if first {
			r.logf("radio not available yet, retrying every %s: %v", r.every, err)
		}
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(r.every):
		}
	}
}

func (r *Radio) get() radio.Radio {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner
}

func (r *Radio) Configure(ctx context.Context, c radio.Config) error {
	if in := r.get(); in != nil {
		return in.Configure(ctx, c)
	}
	return radio.ErrNotConnected
}

func (r *Radio) Send(ctx context.Context, frame []byte) error {
	if in := r.get(); in != nil {
		return in.Send(ctx, frame)
	}
	return radio.ErrNotConnected
}

func (r *Radio) Frames() <-chan radio.Frame { return r.frames }

func (r *Radio) ChannelBusy(ctx context.Context) (bool, error) {
	if in := r.get(); in != nil {
		return in.ChannelBusy(ctx)
	}
	return false, radio.ErrNotConnected
}

func (r *Radio) Info() radio.Info {
	if in := r.get(); in != nil {
		return in.Info()
	}
	return r.info
}

func (r *Radio) Stats(ctx context.Context) radio.Stats {
	if in := r.get(); in != nil {
		return in.Stats(ctx)
	}
	return radio.Stats{}
}

// LastError is why the radio isn't open yet (nil once open).
func (r *Radio) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

// Inner returns the opened radio, or nil.
func (r *Radio) Inner() radio.Radio { return r.get() }

func (r *Radio) Close() error {
	r.cancel()
	var err error
	if in := r.get(); in != nil {
		err = in.Close()
	}
	<-r.done
	close(r.frames)
	return err
}
