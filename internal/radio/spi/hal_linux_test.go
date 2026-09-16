//go:build linux

package spi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDeviceGone(t *testing.T) {
	for _, err := range []error{unix.ENODEV, unix.ESHUTDOWN, unix.ENOENT, unix.EBUSY, fmt.Errorf("usb: %w", unix.ENODEV)} {
		if !deviceGone(err) {
			t.Errorf("%v: not gone", err)
		}
	}
	for _, err := range []error{nil, unix.EIO, unix.ETIMEDOUT, errBoom} {
		if deviceGone(err) {
			t.Errorf("%v: gone", err)
		}
	}
}

// An adapter that disappears closes the radio, which then reports itself disconnected.
func TestAdapterGoneClosesRadio(t *testing.T) {
	h, c := newStubHAL(), &stubChip{}
	h.waitErr = fmt.Errorf("ch341 USB transfer: %w", unix.ENODEV)
	r := startStub(t, h, c, Board{})
	select {
	case _, ok := <-r.Frames():
		if ok {
			t.Fatal("unexpected frame")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("radio not closed after the adapter went away")
	}
	if r.Stats(context.Background()).Connected {
		t.Fatal("still connected")
	}
	if err := r.Send(context.Background(), []byte{1}); err == nil {
		t.Fatal("sent on a closed radio")
	}
}

// notADevice is an open regular file: every ioctl on it fails with ENOTTY.
func notADevice(t *testing.T) (string, int) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spidev9.9")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(p, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	return p, fd
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "spidev7.7")
	if _, err := Open(context.Background(), Board{Module: ModuleSX1262, SPIDev: missing}, nil); err == nil ||
		!strings.Contains(err.Error(), "open "+missing) || !errors.Is(err, unix.ENOENT) {
		t.Fatalf("missing spidev: %v", err)
	}
	// A bare spidev name is looked up in /dev.
	if _, err := Open(context.Background(), Board{Module: ModuleSX1262, SPIDev: "spidev-repeatertastic-test"}, nil); err == nil ||
		!strings.Contains(err.Error(), "/dev/spidev-repeatertastic-test") {
		t.Fatalf("bare name: %v", err)
	}
	p, fd := notADevice(t)
	unix.Close(fd)
	if _, err := Open(context.Background(), Board{Module: ModuleSX1262, SPIDev: p}, t.Logf); err == nil ||
		!strings.Contains(err.Error(), "spidev mode") || !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("not a spidev: %v", err)
	}
	usb := &USBID{VID: 0xFFFF, PID: 0xFFFE, Serial: "nope"}
	if _, err := Open(context.Background(), Board{Module: ModuleSX1262, USB: usb}, t.Logf); err == nil ||
		err.Error() != "no CH341 USB radio ffff:fffe with serial nope found (check lsusb)" {
		t.Fatalf("missing USB adapter: %v", err)
	}
}

func TestReadRAKEEPROMErrors(t *testing.T) {
	if _, err := readRAKEEPROM(filepath.Join(t.TempDir(), "i2c-9")); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("missing bus: %v", err)
	}
	p, fd := notADevice(t)
	unix.Close(fd)
	if _, err := readRAKEEPROM(p); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("not an I2C bus: %v", err)
	}
}

func TestOutputLine(t *testing.T) {
	if l, err := outputLine(Pin{}, "reset", true); l != nil || err != nil {
		t.Fatalf("unconnected pin: %v %v", l, err)
	}
	_, err := outputLine(Pin{Chip: 9999, Line: 3, Set: true}, "txen", false)
	if err == nil || !strings.HasPrefix(err.Error(), "txen (gpiochip9999 line 3): ") {
		t.Fatalf("missing gpiochip: %v", err)
	}
}

// A linuxHAL without GPIO lines: no BUSY or IRQ, no RF switch, no reset; SPI errors surface.
func TestLinuxHALWithoutLines(t *testing.T) {
	_, fd := notADevice(t)
	h := &linuxHAL{spi: fd, speed: 1_000_000, log: t.Logf}
	if h.HasBusy() || h.HasIRQ() {
		t.Fatal("lines reported without GPIO")
	}
	if err := h.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := h.SetRFSwitch(true); err != nil {
		t.Fatal(err)
	}
	if err := h.openCS(Pin{}); err != nil || h.cs != nil {
		t.Fatalf("openCS: %v", err)
	}
	if err := h.openEnablePins(nil); err != nil {
		t.Fatal(err)
	}
	if err := h.openLines(Board{}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if got, err := h.WaitIRQ(5 * time.Millisecond); got || err != nil || time.Since(start) < 5*time.Millisecond {
		t.Fatalf("WaitIRQ = %v, %v", got, err)
	}
	if _, err := h.Transfer([]byte{1, 2}); !errors.Is(err, unix.ENOTTY) || !strings.HasPrefix(err.Error(), "spi transfer: ") {
		t.Fatalf("Transfer = %v", err)
	}
	if err := h.Close(); err != nil || h.spi != -1 {
		t.Fatalf("Close: %v, fd %d", err, h.spi)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

// GPIO line failures stop the operations that need them.
func TestLinuxHALLineErrors(t *testing.T) {
	_, fd := notADevice(t)
	_, lineFD := notADevice(t)
	line := &gpioLine{fd: lineFD}
	h := &linuxHAL{spi: fd, cs: line, reset: line, txen: line, busy: line, log: t.Logf}
	if _, err := h.Transfer([]byte{1}); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("Transfer = %v", err)
	}
	if err := h.Reset(); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("Reset = %v", err)
	}
	if err := h.SetRFSwitch(false); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("SetRFSwitch = %v", err)
	}
	h.txen, h.rxen = nil, line
	if err := h.SetRFSwitch(false); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("SetRFSwitch rxen = %v", err)
	}
	if !h.HasBusy() {
		t.Fatal("no BUSY")
	}
	if _, err := h.Busy(); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("Busy = %v", err)
	}
	// A regular file always polls readable.
	if got, err := line.WaitEdge(time.Second); !got || err != nil {
		t.Fatalf("WaitEdge = %v, %v", got, err)
	}
	h.cs, h.reset, h.rxen, h.busy = nil, nil, nil, nil
	h.high = []*gpioLine{line}
	if err := h.Close(); err != nil || line.fd != -1 {
		t.Fatalf("Close: %v, line fd %d", err, line.fd)
	}
}

// A ch341HAL whose USB requests fail reports the failures.
func TestCH341HALErrors(t *testing.T) {
	_, fd := notADevice(t)
	h := &ch341HAL{fd: fd, cs: 0, irq: Pin{Line: 2, Set: true}, busy: Pin{Line: 4, Set: true},
		reset: Pin{Line: 1, Set: true}, txen: Pin{Line: 3, Set: true}, rxen: Pin{Line: 5, Set: true}}
	h.drain() // nothing to drain: returns at once
	if _, err := h.Transfer([]byte{0xAA}); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("Transfer = %v", err)
	}
	if err := h.Reset(); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("Reset = %v", err)
	}
	if !h.HasBusy() || !h.HasIRQ() {
		t.Fatal("pins not reported")
	}
	if _, err := h.Busy(); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("Busy = %v", err)
	}
	if _, err := h.WaitIRQ(time.Second); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("WaitIRQ = %v", err)
	}
	if err := h.SetRFSwitch(true); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("SetRFSwitch = %v", err)
	}
	h.txen = Pin{}
	if err := h.SetRFSwitch(true); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("SetRFSwitch rxen = %v", err)
	}
	if _, err := h.readIn(4); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("readIn = %v", err)
	}
	if err := h.Close(); err != nil || h.fd != -1 {
		t.Fatalf("Close: %v, fd %d", err, h.fd)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCH341HALWithoutPins(t *testing.T) {
	h := &ch341HAL{fd: -1}
	if h.HasBusy() || h.HasIRQ() {
		t.Fatal("pins reported")
	}
	if err := h.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := h.SetRFSwitch(true); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if got, err := h.WaitIRQ(5 * time.Millisecond); got || err != nil || time.Since(start) < 5*time.Millisecond {
		t.Fatalf("WaitIRQ = %v, %v", got, err)
	}
}
