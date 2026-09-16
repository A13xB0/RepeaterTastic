package spi

import (
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detectFixture points board detection and the installed board directories at a temporary tree.
type detectFixture struct {
	usb, hat, boards string
	eeprom           string
	eepromErr        error
}

func newDetectFixture(t *testing.T) *detectFixture {
	t.Helper()
	root := t.TempDir()
	f := &detectFixture{usb: filepath.Join(root, "usb"), hat: filepath.Join(root, "hat"), boards: filepath.Join(root, "config.d"),
		eepromErr: errors.New("no such device")}
	for _, d := range []string{f.usb, f.boards} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldUSB, oldHat, oldDev, oldRead, oldDirs := usbDevicesDir, hatDir, rakI2CDev, readEEPROM, boardDirs
	usbDevicesDir, hatDir, rakI2CDev, boardDirs = f.usb, f.hat, "/dev/i2c-test", []string{f.boards}
	readEEPROM = func(dev string) (string, error) {
		if dev != "/dev/i2c-test" {
			return "", fmt.Errorf("unexpected device %s", dev)
		}
		return f.eeprom, f.eepromErr
	}
	t.Cleanup(func() {
		usbDevicesDir, hatDir, rakI2CDev, readEEPROM, boardDirs = oldUSB, oldHat, oldDev, oldRead, oldDirs
	})
	return f
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *detectFixture) addUSB(t *testing.T, name, vid, pid, product string) {
	t.Helper()
	d := filepath.Join(f.usb, name)
	writeFile(t, filepath.Join(d, "idVendor"), vid+"\n")
	writeFile(t, filepath.Join(d, "idProduct"), pid+"\n")
	if product != "" {
		writeFile(t, filepath.Join(d, "product"), product+"\n")
	}
}

func (f *detectFixture) setHAT(t *testing.T, vendor, product string) {
	t.Helper()
	writeFile(t, filepath.Join(f.hat, "vendor"), vendor+"\x00")
	writeFile(t, filepath.Join(f.hat, "product"), product+"\x00\n")
}

func TestDetectNothing(t *testing.T) {
	f := newDetectFixture(t)
	f.addUSB(t, "1-1", "046d", "c52b", "Receiver") // not a CH341
	_, _, err := Resolve("auto")
	if err == nil {
		t.Fatal("detected a board with nothing attached")
	}
	for _, want := range []string{"no CH341 USB radio", "no Pi HAT+ EEPROM", "no RAK EEPROM: no such device", "set radio.device"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestDetectUSBKnownProduct(t *testing.T) {
	f := newDetectFixture(t)
	f.addUSB(t, "1-2", "1a86", "5512", "MESHSTICK")
	b, src, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if b.USB == nil || src != "CH341 USB MESHSTICK → built-in lora-meshstick-1262.yaml" {
		t.Fatalf("board %+v from %q", b, src)
	}
}

// An unknown USB product names a file by meshtasticd's rule; when that doesn't exist the HAT is
// tried next.
func TestDetectUSBUnknownThenHAT(t *testing.T) {
	f := newDetectFixture(t)
	f.addUSB(t, "1-3", "1a86", "5512", "Mystery Stick")
	f.setHAT(t, "Frequency Labs", "MESHADV-PI")
	b, src, err := Resolve("AUTO")
	if err != nil {
		t.Fatal(err)
	}
	if b.Module != ModuleSX1262 || src != "Pi HAT+ Frequency Labs MESHADV-PI → built-in lora-MeshAdv-900M30S.yaml" {
		t.Fatalf("board %+v from %q", b, src)
	}
}

func TestDetectHATByVendorName(t *testing.T) {
	f := newDetectFixture(t)
	f.setHAT(t, "RAK", "6421 Pi Hat")
	_, src, err := Resolve("auto")
	if err != nil {
		t.Fatal(err)
	}
	if src != "Pi HAT+ RAK 6421 Pi Hat → built-in lora-hat-rak-6421-pi-hat.yaml" {
		t.Fatalf("source %q", src)
	}
}

func TestDetectRAKEEPROM(t *testing.T) {
	f := newDetectFixture(t)
	f.setHAT(t, "Nobody", "Unknown Hat") // named file doesn't exist: fall through to the EEPROM
	f.eeprom, f.eepromErr = "RAK6421-13300-S2", nil
	_, src, err := Resolve("auto")
	if err != nil {
		t.Fatal(err)
	}
	if src != "EEPROM RAK6421-13300-S2 → built-in lora-RAK6421-13300-slot2.yaml" {
		t.Fatalf("source %q", src)
	}

	f.eeprom = "RAK9999"
	_, _, err = Resolve("auto")
	if err == nil || !strings.Contains(err.Error(), "EEPROM model RAK9999 isn't known") ||
		!strings.Contains(err.Error(), "lora-hat-nobody-unknown-hat.yaml") {
		t.Fatalf("error %v", err)
	}
}

func TestResolvePaths(t *testing.T) {
	f := newDetectFixture(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	writeFile(t, file, "Lora:\n  Module: sx1280\n")
	b, src, err := Resolve("  " + file + " ")
	if err != nil || b.Module != ModuleSX1280 || src != file {
		t.Fatalf("Resolve(file) = %+v %q %v", b, src, err)
	}

	// Module: auto in a config file means detect.
	auto := filepath.Join(dir, "auto.yaml")
	writeFile(t, auto, "Lora:\n  Module: auto\n")
	f.addUSB(t, "1-4", "1a86", "5512", "MESHTOAD")
	if _, src, err := Resolve(auto); err != nil || !strings.HasPrefix(src, "CH341 USB MESHTOAD") {
		t.Fatalf("Resolve(auto file) = %q %v", src, err)
	}

	if _, _, err := Resolve(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("resolved a missing file")
	}
}

// Installed board files win over the built-in copies, whatever form of the name is given.
func TestResolveInstalledBoard(t *testing.T) {
	f := newDetectFixture(t)
	installed := filepath.Join(f.boards, "lora-MeshAdv-900M30S.yaml")
	writeFile(t, installed, "Meta:\n  name: Local copy\nLora:\n  Module: llcc68\n")
	for _, name := range []string{"MeshAdv-900M30S", "MeshAdv-900M30S.yaml", "lora-MeshAdv-900M30S.yaml"} {
		b, src, err := Resolve(name)
		if err != nil || src != installed || b.Name != "Local copy" || b.Module != ModuleLLCC68 {
			t.Errorf("Resolve(%s) = %+v %q %v", name, b, src, err)
		}
	}
	broken := filepath.Join(f.boards, "lora-broken.yaml")
	writeFile(t, broken, "Lora:\n  Module: sim\n")
	if _, src, err := Resolve("broken"); err == nil || src != broken {
		t.Fatalf("Resolve(broken) = %q %v", src, err)
	}
	// A directory with a board's name isn't a board file.
	if err := os.Mkdir(filepath.Join(f.boards, "lora-dir.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve("dir"); err == nil || !strings.Contains(err.Error(), f.boards) {
		t.Fatalf("Resolve(dir) = %v", err)
	}
}

func TestResolveBuiltinCaseInsensitive(t *testing.T) {
	newDetectFixture(t)
	_, src, err := Resolve("meshadv-mini-900m22s")
	if err != nil || src != "built-in lora-MeshAdv-Mini-900M22S.yaml" {
		t.Fatalf("Resolve = %q %v", src, err)
	}
}

func TestUSBProductMatchesIDs(t *testing.T) {
	f := newDetectFixture(t)
	f.addUSB(t, "usb1", "1a86", "7523", "Serial")
	f.addUSB(t, "1-5", "1A86", "5512", "") // upper case IDs don't match sysfs's lower case
	if got := usbProduct(0x1A86, 0x5512); got != "" {
		t.Fatalf("usbProduct = %q", got)
	}
	f.addUSB(t, "1-6", "1a86", "5512", "  uMesh  ")
	if got := usbProduct(0x1A86, 0x5512); got != "uMesh" {
		t.Fatalf("usbProduct = %q", got)
	}
}

func TestReadDT(t *testing.T) {
	p := filepath.Join(t.TempDir(), "product")
	writeFile(t, p, "Board Name \x00\n")
	if got := readDT(p); got != "Board Name" {
		t.Fatalf("readDT = %q", got)
	}
	if got := readDT(p + ".missing"); got != "" {
		t.Fatalf("readDT(missing) = %q", got)
	}
}

func TestParseRAKEEPROMRejects(t *testing.T) {
	body := "RAK6421-13300-S1:aabbcc:id"
	cases := map[string][]byte{
		"too few fields":  []byte("RAK:1234abcd"),
		"short checksum":  []byte(body + ":1234"),
		"not hex":         []byte(body + ":zzzzzzzz"),
		"blank EEPROM":    {0xFF, 0xFF, 0xFF},
		"checksum digits": []byte(fmt.Sprintf("%s:%08x", body, crc32.ChecksumIEEE([]byte(body))+1)),
	}
	for name, raw := range cases {
		if model, err := parseRAKEEPROM(raw); err == nil {
			t.Errorf("%s: accepted, model %q", name, model)
		}
	}
}

func TestCH341InputsShort(t *testing.T) {
	if got := ch341Inputs([]byte{0xFF, 0xFF}); got != 0 {
		t.Fatalf("short reply decoded to %b", got)
	}
	if got := ch341Inputs([]byte{0x00, 0x10, 0x7F}); got != 0 {
		t.Fatalf("masked bits decoded to %b", got)
	}
}
