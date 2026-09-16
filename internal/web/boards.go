package web

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ScotMesh/RepeaterTastic/internal/radio/spi"
)

// meshtasticdDirs are where meshtasticd keeps board files; config.d holds the ones in use.
var meshtasticdDirs = []string{"/etc/meshtasticd/config.d", "/etc/meshtasticd/available.d"}

// boardEntry is one choice for radio.device with driver spi (GET /boards).
type boardEntry struct {
	ID        string `json:"id"` // what to put in radio.device
	Name      string `json:"name"`
	Module    string `json:"module"`
	Bus       string `json:"bus"`    // spidev0.0, usb
	Source    string `json:"source"` // auto, config.d, available.d, built-in
	Supported bool   `json:"supported"`
	Error     string `json:"error"`
}

// boards lists the LoRa boards the spi driver can use: meshtasticd's installed board files, then
// the built-in copies, with "auto" first.
func (s *Server) boards(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listBoards())
}

func listBoards() []boardEntry {
	out := []boardEntry{{ID: "auto", Name: "Detect automatically", Source: "auto", Supported: true}}
	for _, dir := range meshtasticdDirs {
		files, _ := filepath.Glob(filepath.Join(dir, "lora-*.yaml"))
		sort.Strings(files)
		for _, p := range files {
			b, err := spi.LoadBoard(p)
			out = append(out, boardEntryFor(p, filepath.Base(p), filepath.Base(dir), b, err))
		}
	}
	var builtin []boardEntry
	for _, kb := range spi.KnownBoards() {
		builtin = append(builtin, boardEntryFor(kb.File, kb.File, "built-in", kb.Board, kb.Err))
	}
	sort.SliceStable(builtin, func(i, j int) bool { return strings.ToLower(builtin[i].Name) < strings.ToLower(builtin[j].Name) })
	return append(out, builtin...)
}

func boardEntryFor(id, file, source string, b spi.Board, err error) boardEntry {
	e := boardEntry{ID: id, Name: b.Name, Module: b.Module, Source: source, Supported: err == nil}
	if e.Name == "" {
		e.Name = strings.TrimSuffix(strings.TrimPrefix(file, "lora-"), ".yaml")
	}
	switch {
	case b.USB != nil:
		e.Bus = "usb"
	case b.SPIDev != "":
		e.Bus = strings.TrimPrefix(b.SPIDev, "/dev/")
	}
	if err != nil {
		e.Error = err.Error()
	}
	return e
}

// boardRef limits the setup probe (reachable before a password is set) to board names, auto and
// meshtasticd's own board files, so it can't be used to read other files.
func boardRef(device string) bool {
	if !strings.ContainsRune(device, '/') {
		return true
	}
	p := filepath.Clean(device)
	for _, dir := range meshtasticdDirs {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}
