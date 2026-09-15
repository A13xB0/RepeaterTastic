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
	r := New(func(ctx context.Context, device string) (radio.Radio, error) {
		mu.Lock()
		tried = append(tried, device)
		mu.Unlock()
		if device == "/dev/good" {
			return null.New(), nil
		}
		return nil, errors.New("no such device")
	}, radio.Info{Driver: "kiss", Device: "/dev/bad"}, time.Hour, func(string, ...any) {})
	defer r.Close()

	if !r.Retarget("/dev/good") {
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
	if r.Retarget("/dev/other") {
		t.Fatal("retarget accepted after the radio opened")
	}
}
