package spi

import (
	"embed"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// boardFiles are meshtasticd's LoRa board files (meshtastic/firmware bin/config.d, GPL-3.0), so
// boards work without meshtasticd installed. See boards/README.md.
//
//go:embed boards/*.yaml
var boardFiles embed.FS

// boardDirs are where meshtasticd installs board files; they win over the built-in copies.
var boardDirs = []string{"/etc/meshtasticd/config.d", "/etc/meshtasticd/available.d"}

// Where detectBoard looks for a radio; variables so tests can point them at fixtures.
var (
	usbDevicesDir = "/sys/bus/usb/devices"
	hatDir        = "/proc/device-tree/hat"
	rakI2CDev     = "/dev/i2c-1"
	readEEPROM    = readRAKEEPROM
)

// Resolve finds the board for a radio.device setting:
//   - a path to a meshtasticd board file or config.yaml (Module: auto detects the board);
//   - a board file name, with or without "lora-" and ".yaml" (lora-MeshAdv-900M30S.yaml,
//     MeshAdv-900M30S), looked up in /etc/meshtasticd then the built-in copies;
//   - "auto": detect a CH341 USB radio, a Pi HAT+ or a RAK EEPROM, as meshtasticd's autoconf.
//
// It returns the board and where it came from.
func Resolve(device string) (Board, string, error) {
	device = strings.TrimSpace(device)
	switch {
	case device == "" || strings.EqualFold(device, "auto"):
		return detectBoard()
	case strings.ContainsRune(device, '/'):
		b, err := LoadBoard(device)
		if errors.Is(err, errAutoModule) {
			return detectBoard()
		}
		return b, device, err
	}
	return findBoard(device)
}

// findBoard looks a board file name up in boardDirs, then in the built-in copies.
func findBoard(name string) (Board, string, error) {
	candidates := boardCandidates(name)
	if p := findInstalledBoard(candidates); p != "" {
		b, err := LoadBoard(p)
		return b, p, err
	}
	if file := findBuiltinBoard(candidates); file != "" {
		data, _ := boardFiles.ReadFile("boards/" + file)
		b, err := ParseBoard(data)
		if err != nil {
			return Board{}, "", fmt.Errorf("built-in %s: %w", file, err)
		}
		return b, "built-in " + file, nil
	}
	return Board{}, "", fmt.Errorf("no board file %q in %s or the built-in list (kisstool boards lists them)", name, strings.Join(boardDirs, ", "))
}

// boardCandidates are the file names name might mean, with and without ".yaml" and "lora-".
func boardCandidates(name string) []string {
	candidates := []string{name}
	if !strings.HasSuffix(name, ".yaml") {
		candidates = append(candidates, name+".yaml")
	}
	for _, c := range append([]string(nil), candidates...) {
		if !strings.HasPrefix(c, "lora-") {
			candidates = append(candidates, "lora-"+c)
		}
	}
	return candidates
}

// findInstalledBoard is the path of the first candidate in boardDirs, or "".
func findInstalledBoard(candidates []string) string {
	for _, dir := range boardDirs {
		for _, c := range candidates {
			if p := filepath.Join(dir, c); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

// findBuiltinBoard is the name of the first candidate among the built-in files (ignoring case),
// or "".
func findBuiltinBoard(candidates []string) string {
	entries, _ := fs.ReadDir(boardFiles, "boards")
	for _, c := range candidates {
		for _, e := range entries {
			if strings.EqualFold(e.Name(), c) {
				return e.Name()
			}
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// KnownBoard is a built-in board file and whether this driver supports it.
type KnownBoard struct {
	File  string
	Board Board
	Err   error
}

// KnownBoards lists the built-in board files.
func KnownBoards() []KnownBoard {
	entries, _ := fs.ReadDir(boardFiles, "boards")
	var out []KnownBoard
	for _, e := range entries {
		data, _ := boardFiles.ReadFile("boards/" + e.Name())
		b, err := ParseBoard(data)
		out = append(out, KnownBoard{File: e.Name(), Board: b, Err: err})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// autoconfProducts maps product strings to board files (meshtasticd's configProducts).
var autoconfProducts = map[string]string{
	"MESHTOAD":         "lora-usb-meshtoad-e22.yaml",
	"MESHSTICK":        "lora-meshstick-1262.yaml",
	"MESHADV-PI":       "lora-MeshAdv-900M30S.yaml",
	"MeshAdv Mini":     "lora-MeshAdv-Mini-900M22S.yaml",
	"POWERPI":          "lora-MeshAdv-900M30S.yaml",
	"RAK6421-13300-S1": "lora-RAK6421-13300-slot1.yaml",
	"RAK6421-13300-S2": "lora-RAK6421-13300-slot2.yaml",
}

// autoconfName is meshtasticd's cleanupNameForAutoconf: spaces to dashes, lower case.
func autoconfName(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, " ", "-"))
}

// detectBoard follows meshtasticd's autoconf order: a CH341 USB radio's product string, a Pi
// HAT+ EEPROM, then a RAK board EEPROM on I2C.
func detectBoard() (Board, string, error) {
	var tried []string
	for _, detect := range []func() (how, file, miss string){detectUSBBoard, detectHATBoard, detectRAKBoard} {
		how, file, miss := detect()
		if miss != "" {
			tried = append(tried, miss)
			continue
		}
		b, src, err := findBoard(file)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s → %s: %v", how, file, err))
			continue
		}
		return b, how + " → " + src, nil
	}
	return Board{}, "", fmt.Errorf("couldn't detect the radio board (%s): set radio.device to its board file", strings.Join(tried, "; "))
}

// detectUSBBoard names the board file for a CH341 USB radio's product string, or says why not
// (miss).
func detectUSBBoard() (how, file, miss string) {
	product := usbProduct(0x1A86, 0x5512)
	if product == "" {
		return "", "", "no CH341 USB radio"
	}
	file, ok := autoconfProducts[product]
	if !ok {
		file = autoconfName("lora-usb-" + product + ".yaml")
	}
	return "CH341 USB " + product, file, ""
}

// detectHATBoard names the board file for a Pi HAT+ EEPROM, or says why not (miss).
func detectHATBoard() (how, file, miss string) {
	product := readDT(filepath.Join(hatDir, "product"))
	if product == "" {
		return "", "", "no Pi HAT+ EEPROM"
	}
	vendor := readDT(filepath.Join(hatDir, "vendor"))
	file, ok := autoconfProducts[product]
	if !ok {
		file = autoconfName("lora-hat-" + vendor + "-" + product + ".yaml")
	}
	return "Pi HAT+ " + vendor + " " + product, file, ""
}

// detectRAKBoard names the board file for a RAK EEPROM on I2C, or says why not (miss).
func detectRAKBoard() (how, file, miss string) {
	model, err := readEEPROM(rakI2CDev)
	if err != nil {
		return "", "", "no RAK EEPROM: " + err.Error()
	}
	file, ok := autoconfProducts[model]
	if !ok {
		return "", "", "EEPROM model " + model + " isn't known"
	}
	return "EEPROM " + model, file, ""
}

func usbProduct(vid, pid uint16) string {
	dirs, _ := filepath.Glob(filepath.Join(usbDevicesDir, "*"))
	for _, d := range dirs {
		v, _ := os.ReadFile(filepath.Join(d, "idVendor"))
		p, _ := os.ReadFile(filepath.Join(d, "idProduct"))
		if strings.TrimSpace(string(v)) == fmt.Sprintf("%04x", vid) && strings.TrimSpace(string(p)) == fmt.Sprintf("%04x", pid) {
			name, _ := os.ReadFile(filepath.Join(d, "product"))
			return strings.TrimSpace(string(name))
		}
	}
	return ""
}

func readDT(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\x00\n ")
}

// parseRAKEEPROM checks a RAK autoconf EEPROM string, "<model>:<mac>:<device id>:<crc32>", and
// returns the model. The CRC32 covers everything before the last colon.
func parseRAKEEPROM(raw []byte) (string, error) {
	raw = cutPadding(raw)
	s := string(raw)
	parts := strings.Split(s, ":")
	if len(parts) != 4 || len(parts[3]) != 8 {
		return "", errors.New("not an autoconf EEPROM")
	}
	var want uint32
	if _, err := fmt.Sscanf(parts[3], "%08x", &want); err != nil {
		return "", err
	}
	if crc32.ChecksumIEEE([]byte(s[:strings.LastIndex(s, ":")])) != want {
		return "", errors.New("EEPROM checksum mismatch")
	}
	return parts[0], nil
}

// cutPadding cuts b at its first NUL or 0xFF byte (unwritten EEPROM or register padding).
func cutPadding(b []byte) []byte {
	for i, c := range b {
		if c == 0x00 || c == 0xff {
			return b[:i]
		}
	}
	return b
}
