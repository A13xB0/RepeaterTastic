package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/kiss"
)

type exitCode int

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(&out, r)
	}()
	func() {
		defer func() { os.Stdout = old }()
		fn()
	}()
	_ = w.Close()
	<-done
	_ = r.Close()
	return out.String()
}

// runMain runs main with args and returns the exit code (-1 if main returned) and its stdout.
func runMain(t *testing.T, fm *fakeModem, args ...string) (code int, out string) {
	t.Helper()
	oldArgs, oldExit, oldDial, oldStderr := os.Args, exit, dialKISS, os.Stderr
	t.Cleanup(func() { os.Args, exit, dialKISS, os.Stderr = oldArgs, oldExit, oldDial, oldStderr })
	os.Args = append([]string{"kisstool"}, args...)
	exit = func(c int) { panic(exitCode(c)) }
	dialKISS = func() (io.ReadWriteCloser, error) { return nil, errors.New("no such device") }
	if fm != nil {
		dialKISS = fm.dial
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })
	os.Stderr = devNull
	code = -1
	out = captureStdout(t, func() {
		defer func() {
			if r := recover(); r != nil {
				c, ok := r.(exitCode)
				if !ok {
					panic(r)
				}
				code = int(c)
			}
		}()
		main()
	})
	return code, out
}

func TestMainExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no command", nil, 2},
		{"unknown command", []string{"frobnicate"}, 2},
		{"bad preset", []string{"listen", "--preset", "NOPE"}, 1},
		{"bad region", []string{"listen", "--region", "MARS"}, 1},
		{"send-text without text", []string{"send-text"}, 1},
		{"send-text bad from", []string{"send-text", "--from", "zzz", "hi"}, 1},
		{"open fails", []string{"info", "--dev", "/dev/nonexistent"}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := runMain(t, nil, tc.args...); code != tc.want {
				t.Fatalf("exit %d, want %d", code, tc.want)
			}
		})
	}
}

func TestMainBoards(t *testing.T) {
	code, out := runMain(t, nil, "boards")
	if code != -1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, ".yaml") || !strings.Contains(out, `"auto" to detect`) {
		t.Fatal(out)
	}
}

func TestMainInfo(t *testing.T) {
	code, out := runMain(t, newFakeModem(), "info")
	if code != -1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"name:       Heltec V3", "patch: yes", "noise:      -110 dBm", "rx 7, tx 0, rx errors 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestMainSendText(t *testing.T) {
	fm := newFakeModem()
	code, out := runMain(t, fm, "send-text", "hello", "--from", "!deadbe00", "world")
	if code != -1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "from !deadbe00") || !strings.Contains(out, "TxDone after") {
		t.Fatal(out)
	}
	sent := fm.sentFrames()
	if len(sent) != 1 {
		t.Fatalf("%d frames sent", len(sent))
	}
	var dec strings.Builder
	printFrame(&dec, radio.Frame{Data: sent[0]}, 0x08)
	if !strings.Contains(dec.String(), `"hello world"`) {
		t.Fatal(dec.String())
	}
	if fm.freq != 869525000 || fm.sf != 11 || fm.sync != 0x2B || fm.preamble != 16 || fm.power != 10 {
		t.Fatalf("modem PHY %d Hz SF%d sync %#x preamble %d power %d", fm.freq, fm.sf, fm.sync, fm.preamble, fm.power)
	}
}

func TestMainSendTextTxFails(t *testing.T) {
	fm := newFakeModem()
	fm.txResult = 0
	if code, _ := runMain(t, fm, "send-text", "hi"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestMainListenStopsOnInterrupt(t *testing.T) {
	fm := newFakeModem()
	// Dialing happens after main has registered for SIGINT; deliver a frame, then interrupt.
	fm.onDial = func() {
		go func() {
			time.Sleep(200 * time.Millisecond)
			frame, err := buildText(mustResolve(t), 0x11223344, 7, "ping")
			if err == nil {
				fm.receive(frame, 26, -80)
			}
			time.Sleep(200 * time.Millisecond)
			p, _ := os.FindProcess(os.Getpid())
			_ = p.Signal(os.Interrupt)
		}()
	}
	code, out := runMain(t, fm, "listen")
	if code != -1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"listening (default channel hash 0x08)", "RSSI -80 dBm  SNR 6.50 dB", `"ping"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func mustResolve(t *testing.T) phy.RadioParams {
	t.Helper()
	rp, err := resolve("EU_868", "LONG_FAST", 10)
	if err != nil {
		t.Error(err)
	}
	return rp
}

func openFake(t *testing.T, fm *fakeModem) *kiss.Modem {
	t.Helper()
	m, err := kiss.Open(context.Background(), kiss.Options{Device: "fake", Dial: fm.dial, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestInfoUnpatchedAndRadioError(t *testing.T) {
	fm := newFakeModem()
	fm.version = 1
	fm.failRadio = true
	m := openFake(t, fm)
	out := captureStdout(t, func() {
		if err := info(context.Background(), m); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "patch: no, flash") || !strings.Contains(out, "radio:      error:") {
		t.Fatal(out)
	}
}

func TestOpenErrors(t *testing.T) {
	old := dialKISS
	t.Cleanup(func() { dialKISS = old })
	dialKISS = func() (io.ReadWriteCloser, error) { return nil, errors.New("gone") }
	if _, err := open(context.Background(), "/dev/ttyX", 9600, ""); err == nil || !strings.Contains(err.Error(), "open /dev/ttyX: gone") {
		t.Fatalf("kiss open: %v", err)
	}
	if _, err := open(context.Background(), "", 0, "/nonexistent/board.yaml"); err == nil {
		t.Fatal("missing board file accepted")
	}
}

// fakeRadio is a radio.Radio whose Configure and Send can fail and whose Frames the test feeds.
type fakeRadio struct {
	cfgErr, sendErr error
	frames          chan radio.Frame
}

func (r *fakeRadio) Configure(context.Context, radio.Config) error { return r.cfgErr }
func (r *fakeRadio) Send(context.Context, []byte) error            { return r.sendErr }
func (r *fakeRadio) Frames() <-chan radio.Frame                    { return r.frames }
func (r *fakeRadio) ChannelBusy(context.Context) (bool, error)     { return false, nil }
func (r *fakeRadio) Info() radio.Info                              { return radio.Info{} }
func (r *fakeRadio) Stats(context.Context) radio.Stats             { return radio.Stats{} }
func (r *fakeRadio) Close() error                                  { return nil }

func TestListenAndSendErrors(t *testing.T) {
	rp := mustResolve(t)
	boom := errors.New("boom")
	ctx := context.Background()
	out := captureStdout(t, func() {
		if err := listen(ctx, &fakeRadio{cfgErr: boom}, rp); !errors.Is(err, boom) {
			t.Errorf("listen configure: %v", err)
		}
		if err := sendText(ctx, &fakeRadio{cfgErr: boom}, rp, 5, "x"); !errors.Is(err, boom) {
			t.Errorf("send configure: %v", err)
		}
		if err := sendText(ctx, &fakeRadio{sendErr: boom}, rp, 5, "x"); !errors.Is(err, boom) {
			t.Errorf("send: %v", err)
		}
		closed := make(chan radio.Frame)
		close(closed)
		if err := listen(ctx, &fakeRadio{frames: closed}, rp); err != nil {
			t.Errorf("listen on closed frames: %v", err)
		}
	})
	if !strings.Contains(out, "listening") {
		t.Fatal(out)
	}
}

func TestPrintFrameVariants(t *testing.T) {
	rp := mustResolve(t)
	frame, err := buildText(rp, 0xdeadbe00, 1, "x")
	if err != nil {
		t.Fatal(err)
	}
	garbled := append([]byte(nil), frame...)
	garbled[16] ^= 0xFF // corrupt the first ciphertext byte (the protobuf tag)
	tests := []struct {
		name string
		data []byte
		hash uint8
		want string
	}{
		{"too short", []byte{1, 2, 3}, 0x08, "not a Meshtastic frame"},
		{"other channel", frame, 0x09, "not the default-key primary channel"},
		{"undecodable", garbled, 0x08, "decrypt/parse failed"},
	}
	for _, tc := range tests {
		var out strings.Builder
		printFrame(&out, radio.Frame{Data: tc.data}, tc.hash)
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: %s", tc.name, out.String())
		}
	}
}

func TestParseAnywhere(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	n := fs.Int("n", 0, "")
	args := parseAnywhere(fs, []string{"a", "-n", "3", "b"})
	if *n != 3 || strings.Join(args, ",") != "a,b" {
		t.Fatalf("n=%d args=%v", *n, args)
	}
}
