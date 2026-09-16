package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient/mtclienttest"
)

// fakeMeshtasticd writes a script that reports a supported version and fails to host anything.
func fakeMeshtasticd(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "meshtasticd")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'meshtasticd 2.8.1.abcdef'; exit 0; fi\nexit 3\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// quiet sends the process's stderr (where run logs) to /dev/null and restores the default logger.
func quiet(t *testing.T) {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldErr, oldLog := os.Stderr, slog.Default()
	os.Stderr = devNull
	t.Cleanup(func() {
		os.Stderr = oldErr
		slog.SetDefault(oldLog)
		_ = devNull.Close()
	})
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// serveBoard runs a fake Meshtastic board on 127.0.0.1 and returns its address.
func serveBoard(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go mtclienttest.New(0x0b0a4d01).Serve(l)
	return l.Addr().String()
}

// writeConfig writes a two-radio config (no radio hardware; the second radio is a fake board
// on TCP) with the web server on 127.0.0.1:webPort, plugins and a local MQTT link.
func writeConfig(t *testing.T, stateDir string, webPort int, extra string) string {
	t.Helper()
	cfg := fmt.Sprintf(`radio:
  driver: none
state_dir: %s
log_level: debug
mdns:
  enabled: false
web:
  enabled: true
  bind: 127.0.0.1
  port: 1
plugins:
  enabled: true
  dir: %s
hosted:
  meshtasticd: %s
  port_base: %d
links:
  mqtt:
    - name: local
      enabled: true
      address: 127.0.0.1:1
    - name: off
      enabled: false
      address: 127.0.0.1:1
identities:
  - long_name: Base Camp
    short_name: BASE
    api_port: %d
%s`, stateDir, filepath.Join(stateDir, "plugins"), fakeMeshtasticd(t), freeTCPPort(t), freeTCPPort(t), extra)
	p := filepath.Join(t.TempDir(), "repeatertastic.yaml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPEATERTASTIC_WEB_PORT", strconv.Itoa(webPort))
	return p
}

// runUntilHealthy runs run(cfgPath) until the web server answers, then interrupts it.
func runUntilHealthy(t *testing.T, cfgPath string) {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- run(cfgPath) }()
	deadline := time.Now().Add(15 * time.Second)
	for healthcheck() != 0 {
		select {
		case err := <-res:
			t.Fatalf("run ended early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("web server never became healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
	p, _ := os.FindProcess(os.Getpid())
	if err := p.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run did not stop on interrupt")
	}
}

func TestRunStartsAndStops(t *testing.T) {
	quiet(t)
	state := t.TempDir()
	board := serveBoard(t)
	extra := fmt.Sprintf(`radios:
  - id: board
    radio:
      driver: meshtastic
      device: %s
    mesh:
      region: EU_868
      preset: MEDIUM_FAST
`, board)
	cfgPath := writeConfig(t, state, freeTCPPort(t), extra)
	// A staged config restore is applied first, and staged identities for each radio.
	if err := os.Rename(cfgPath, cfgPath+".restore"); err != nil {
		t.Fatal(err)
	}
	stagedIDs := filepath.Join(state, "identities.json.restore")
	if err := os.WriteFile(stagedIDs, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	runUntilHealthy(t, cfgPath)

	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("staged config not applied: %v", err)
	}
	recs, err := mesh.LoadIdentityRecords(state)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRecord(recs, "RepeaterTastic Relay", true) || !hasRecord(recs, "Base Camp", false) {
		t.Fatalf("saved identities %+v", recs)
	}
	if _, err := os.Stat(filepath.Join(state, "radios", "board")); err != nil {
		t.Fatalf("board radio state dir: %v", err)
	}

	// A second start restores the saved identities rather than creating new ones.
	runUntilHealthy(t, cfgPath)
	again, err := mesh.LoadIdentityRecords(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(recs) || again[0].PrivateKey != recs[0].PrivateKey {
		t.Fatalf("identities changed on restart: %d -> %d", len(recs), len(again))
	}
}

func hasRecord(recs []mesh.IdentityRecord, long string, relay bool) bool {
	for _, r := range recs {
		if r.LongName == long && r.IsRelay == relay {
			return true
		}
	}
	return false
}
