// Package lazy wraps a radio that may not be available at start-up (modem unplugged, wrong port):
// it keeps trying to open it in the background so the rest of the daemon (web GUI, client APIs,
// UDP link) runs regardless.
package lazy

import (
	"context"
	"sync"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

type Radio struct {
	open  func(ctx context.Context, driver, device string) (radio.Radio, error)
	info  radio.Info // info.Driver and info.Device are what's being tried
	logf  func(string, ...any)
	every time.Duration
	wake  chan struct{}

	mu      sync.Mutex
	inner   radio.Radio
	lastErr error
	cfg     *radio.Config // the last settings, applied again when the radio is reopened
	opened  int           // times the radio was opened

	frames chan radio.Frame
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New starts opening info.Device with info.Driver in the background. info describes the radio
// until it is open.
func New(open func(ctx context.Context, driver, device string) (radio.Radio, error), info radio.Info, every time.Duration,
	logf func(string, ...any)) *Radio {
	r := &Radio{open: open, info: info, logf: logf, every: every, frames: make(chan radio.Frame, 64),
		done: make(chan struct{}), wake: make(chan struct{}, 1)}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	go r.loop()
	return r
}

func (r *Radio) loop() {
	defer close(r.done)
	for {
		r.mu.Lock()
		driver, device := r.info.Driver, r.info.Device
		r.mu.Unlock()
		inner, err := r.open(r.ctx, driver, device)
		var again bool
		if err == nil {
			again = r.serve(inner)
		} else {
			again = r.waitRetry(err)
		}
		if !again {
			return
		}
	}
}

// serve configures a newly opened radio and forwards its frames until it closes. It returns false
// on shutdown, true when the radio was lost and should be opened again.
func (r *Radio) serve(inner radio.Radio) bool {
	r.mu.Lock()
	cfg := r.cfg
	r.opened++
	reopened := r.opened > 1
	r.mu.Unlock()
	if cfg != nil {
		if err := inner.Configure(r.ctx, *cfg); err != nil {
			r.logf("radio reopened but not configured: %v", err)
		}
	}
	if reopened {
		r.logf("radio open again")
	}
	r.mu.Lock()
	r.inner, r.lastErr = inner, nil
	r.mu.Unlock()
	for f := range inner.Frames() {
		select {
		case r.frames <- f:
		default:
		}
	}
	// The radio closed. On shutdown that's the end; otherwise it was lost: open it again.
	r.mu.Lock()
	r.inner = nil
	r.mu.Unlock()
	if r.ctx.Err() != nil {
		return false
	}
	_ = inner.Close()
	r.logf("radio lost; opening it again")
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(time.Second):
	}
	return true
}

// waitRetry records why the radio didn't open, logging only the first failure, and waits for the
// next try. It returns false on shutdown.
func (r *Radio) waitRetry(err error) bool {
	r.mu.Lock()
	first := r.lastErr == nil
	r.lastErr = err
	r.mu.Unlock()
	if first {
		r.logf("radio not available yet, retrying every %s: %v", r.every, err)
	}
	select {
	case <-r.ctx.Done():
		return false
	case <-r.wake:
	case <-time.After(r.every):
	}
	return true
}

// Retarget switches to another driver or device while the radio still isn't open, and tries it at
// once. It reports false once a device is open: changing it then needs a restart.
func (r *Radio) Retarget(driver, device string) bool {
	r.mu.Lock()
	if r.inner != nil {
		r.mu.Unlock()
		return false
	}
	changed := r.info.Driver != driver || r.info.Device != device
	r.info.Driver, r.info.Device = driver, device
	r.lastErr = nil // log the next failure: it's about the new device
	r.mu.Unlock()
	if changed {
		r.logf("trying %s radio on %s", driver, device)
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	return true
}

func (r *Radio) get() radio.Radio {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner
}

func (r *Radio) Configure(ctx context.Context, c radio.Config) error {
	if in := r.get(); in != nil {
		err := in.Configure(ctx, c)
		if err == nil {
			r.mu.Lock()
			r.cfg = &c
			r.mu.Unlock()
		}
		return err
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
	r.mu.Lock()
	defer r.mu.Unlock()
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
