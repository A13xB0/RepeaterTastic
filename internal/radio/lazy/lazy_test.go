package lazy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
)

// A radio that hasn't opened follows a new device at once; once open, it doesn't.
func TestRetarget(t *testing.T) {
	var mu sync.Mutex
	tried := []string{}
	r := New(func(ctx context.Context, driver, device string) (radio.Radio, error) {
		mu.Lock()
		tried = append(tried, device)
		mu.Unlock()
		if device == "/dev/good" {
			return null.New(), nil
		}
		return nil, errors.New("no such device")
	}, radio.Info{Driver: "kiss", Device: "/dev/bad"}, time.Hour, func(string, ...any) {})
	defer r.Close()

	if !r.Retarget("kiss", "/dev/good") {
		t.Fatal("retarget refused before the radio opened")
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Inner() == nil {
		if time.Now().After(deadline) {
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("never opened the new device; tried %v", tried)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.Retarget("kiss", "/dev/other") {
		t.Fatal("retarget accepted after the radio opened")
	}
}

// configRadio is a null radio that remembers its settings.
type configRadio struct {
	*null.Radio
	mu  sync.Mutex
	cfg *radio.Config
}

func (c *configRadio) Configure(_ context.Context, cfg radio.Config) error {
	c.mu.Lock()
	c.cfg = &cfg
	c.mu.Unlock()
	return nil
}

func (c *configRadio) configured() *radio.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// A radio that goes away (its frames close) is opened again and given its settings back.
func TestReopenAfterLoss(t *testing.T) {
	var mu sync.Mutex
	var opened []*configRadio
	r := New(func(ctx context.Context, driver, device string) (radio.Radio, error) {
		c := &configRadio{Radio: null.New()}
		mu.Lock()
		opened = append(opened, c)
		mu.Unlock()
		return c, nil
	}, radio.Info{Driver: "spi", Device: "auto"}, time.Hour, func(string, ...any) {})
	defer r.Close()
	wait := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out: %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait("first open", func() bool { return r.Inner() != nil })
	cfg := radio.Config{FrequencyHz: 869525000, SF: 11}
	if err := r.Configure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := opened[0]
	mu.Unlock()
	_ = first.Close() // the adapter vanished
	wait("reopened", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(opened) == 2 && r.Inner() == radio.Radio(opened[1])
	})
	mu.Lock()
	second := opened[1]
	mu.Unlock()
	if c := second.configured(); c == nil || c.FrequencyHz != cfg.FrequencyHz {
		t.Fatalf("reopened radio not configured: %+v", c)
	}
}
