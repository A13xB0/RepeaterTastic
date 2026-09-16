// First-time setup: the wizard, and probing serial ports, boards and meshtasticd.

package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/kiss"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/spi"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"`
		Driver string `json:"driver"` // kiss (default), spi, meshtastic, or auto (find out what's on a serial port)
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Device = strings.TrimSpace(req.Device)
	switch req.Driver {
	case "", "kiss":
	case "spi":
		s.probeSPI(w, r, req.Device)
		return
	case nodes.BoardDriver:
		s.probeBoard(w, r, req.Device)
		return
	case "auto":
		s.detect(w, r, req.Device)
		return
	default:
		writeError(w, http.StatusBadRequest, "driver must be kiss, spi, meshtastic or auto")
		return
	}
	res := map[string]any{"ok": false, "driver": "kiss", "firmware": "", "name": "", "sync_word_ok": false, "error": ""}
	// The running modem already owns this port: report it rather than opening it twice.
	if req.Device == "" || req.Device == s.cfg.Radio.Device {
		info := s.hostFor(r).Radio().Info()
		st := s.radioStats(r.Context())
		res["driver"], res["firmware"], res["name"] = info.Driver, info.Firmware, info.Name
		res["ok"] = st.Connected
		res["sync_word_ok"] = s.hostFor(r).RadioConfigured()
		if !st.Connected {
			res["error"] = "the modem isn't answering on " + s.cfg.Radio.Device
		} else if !s.hostFor(r).RadioConfigured() {
			res["error"] = "the modem answers but rejected Meshtastic's sync word: flash the RepeaterTastic KISS firmware"
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if !serialPath(req.Device) {
		writeError(w, http.StatusBadRequest, "device must be a serial port such as /dev/ttyUSB0 or /dev/serial/by-id/…")
		return
	}
	writeJSON(w, http.StatusOK, probeKISS(r.Context(), req.Device, res))
}

// probeKISS pings a KISS modem on a serial port and fills res.
func probeKISS(ctx context.Context, device string, res map[string]any) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	m, err := kiss.Open(ctx, kiss.Options{Device: device, HandshakeTimeout: 5 * time.Second, Logf: func(string, ...any) {}})
	if err != nil {
		res["error"] = "no modem answered on " + device + ": " + err.Error()
		return res
	}
	defer m.Close()
	info := m.Info()
	res["ok"], res["firmware"], res["name"] = true, info.Firmware, info.Name
	res["sync_word_ok"] = m.Version() >= kiss.PatchedVersion
	if m.Version() < kiss.PatchedVersion {
		res["error"] = "stock MeshCore KISS firmware can't use Meshtastic's sync word: flash the RepeaterTastic build"
	}
	return res
}

// detect finds out what is on a serial port: a KISS modem, or a board running Meshtastic firmware.
// The KISS ping goes first: Meshtastic firmware ignores it, while a Meshtastic handshake could
// look like a frame to send to a KISS modem. A port a running radio uses is reported, not opened.
func (s *Server) detect(w http.ResponseWriter, r *http.Request, device string) {
	if !serialPath(device) {
		writeError(w, http.StatusBadRequest, "device must be a serial port such as /dev/ttyUSB0 or /dev/serial/by-id/…")
		return
	}
	for _, rc := range s.radios {
		info := rc.host.Radio().Info()
		if info.Device != device {
			continue
		}
		if info.Driver == nodes.BoardDriver {
			s.probeBoard(w, r, device)
			return
		}
		if info.Driver == "kiss" && s.radioStats(r.Context()).Connected {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "driver": "kiss", "firmware": info.Firmware, "name": info.Name,
				"sync_word_ok": rc.host.RadioConfigured(), "error": "", "details": []string{"this radio already uses the modem"}})
			return
		}
	}
	res := probeKISS(r.Context(), device, map[string]any{"ok": false, "driver": "kiss", "firmware": "", "name": "", "sync_word_ok": false, "error": ""})
	if res["ok"] == true {
		res["details"] = []string{"a KISS modem answered"}
		writeJSON(w, http.StatusOK, res)
		return
	}
	kissErr := res["error"]
	board := map[string]any{"ok": false, "driver": nodes.BoardDriver, "firmware": "", "name": "", "sync_word_ok": false, "error": "", "details": []string{}}
	s.boardProbe(r.Context(), device, board, 8*time.Second)
	if board["ok"] == true {
		writeJSON(w, http.StatusOK, board)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "driver": "", "firmware": "", "name": "", "sync_word_ok": false,
		"error":   "nothing on " + device + " answered as a KISS modem or a board running Meshtastic firmware",
		"details": []string{fmt.Sprint("KISS: ", kissErr), fmt.Sprint("Meshtastic: ", board["error"])}})
}

// probeSPI is the setup probe for driver spi: it opens the board, reads the chip's diagnostics
// and closes it again. A board a running radio already drives is reported, not opened twice.
func (s *Server) probeSPI(w http.ResponseWriter, r *http.Request, device string) {
	res := map[string]any{"ok": false, "driver": "spi", "firmware": "", "name": "", "sync_word_ok": false, "error": "", "details": []string{}}
	for _, rc := range s.radios {
		c := s.radioConfig(rc).Radio
		if c.Driver != "spi" || (device != "" && c.Device != device) {
			continue
		}
		info := rc.host.Radio().Info()
		st := rc.stats(r.Context())
		res["firmware"], res["name"] = info.Firmware, info.Name
		res["ok"], res["sync_word_ok"] = st.Connected, st.Connected
		if !st.Connected {
			res["error"] = "the board isn't answering: " + c.Device
		} else if d, ok := rc.host.Radio().(interface{ Diagnostics() []string }); ok {
			res["details"] = d.Diagnostics()
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if device == "" {
		writeError(w, http.StatusBadRequest, "device must be a board name, a board file under /etc/meshtasticd, or auto")
		return
	}
	if !boardRef(device) {
		writeError(w, http.StatusBadRequest, "a board file must be under /etc/meshtasticd/config.d or available.d (or give the board's name)")
		return
	}
	b, src, err := spi.Resolve(device)
	if err != nil {
		res["error"] = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	res["firmware"], res["name"] = b.Module, b.Name
	if b.Name == "" {
		res["name"] = b.Module
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	rad, err := spi.Open(ctx, b, func(string, ...any) {})
	if err != nil {
		res["error"] = "the board didn't answer (" + src + "): " + err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	defer rad.Close()
	res["ok"], res["sync_word_ok"] = true, true
	res["details"] = append([]string{"board " + src + ": " + b.Summary()}, rad.Diagnostics()...)
	writeJSON(w, http.StatusOK, res)
}

// setupOrAuth lets the setup wizard use a handler before a password exists.
func (s *Server) setupOrAuth(h http.HandlerFunc) http.HandlerFunc {
	authed := s.requireAuth(h)
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth.SetupNeeded() {
			h(w, r)
			return
		}
		authed(w, r)
	}
}

// serialPath limits the setup probe (reachable before a password is set) to serial devices.
func serialPath(p string) bool {
	p = filepath.Clean(p)
	for _, prefix := range []string{"/dev/tty", "/dev/serial/", "/dev/cu.", "/dev/rfcomm"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	// udev aliases such as /dev/rsn-meshtastic: allow a symlink that resolves to a serial device
	if target, err := filepath.EvalSymlinks(p); err == nil && target != p && strings.HasPrefix(p, "/dev/") {
		return serialPath(target)
	}
	return false
}

// checkMeshtasticd is the setup check for hosted nodes: which meshtasticd would run, and whether
// it can. Before a password exists only a meshtasticd program or an official image may be tried.
func (s *Server) checkMeshtasticd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Meshtasticd string `json:"meshtasticd"`
		DockerImage string `json:"docker_image"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Meshtasticd, req.DockerImage = strings.TrimSpace(req.Meshtasticd), strings.TrimSpace(req.DockerImage)
	if !nodes.MeshtasticdBinary(req.Meshtasticd) {
		writeError(w, http.StatusBadRequest, "the program must be meshtasticd (a path ending in /meshtasticd)")
		return
	}
	if req.DockerImage != "" && s.auth.SetupNeeded() && !nodes.OfficialImage(req.DockerImage) {
		writeError(w, http.StatusBadRequest, "until a password is set, only meshtastic/meshtasticd images can be checked")
		return
	}
	l := nodes.LauncherFor(req.Meshtasticd, req.DockerImage)
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	v, err := nodes.CheckLauncher(ctx, l)
	res := map[string]any{"ok": err == nil, "version": v, "min_version": nodes.MinFirmware, "launcher": l.Describe(), "error": ""}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

// probeBoard is the setup probe for driver meshtastic: it connects to the board, reads its
// configuration and disconnects. A board a running radio already uses is reported, not opened
// twice (a serial board takes one client).
func (s *Server) probeBoard(w http.ResponseWriter, r *http.Request, device string) {
	res := map[string]any{"ok": false, "driver": nodes.BoardDriver, "firmware": "", "name": "", "sync_word_ok": false, "error": "", "details": []string{}}
	for _, rc := range s.radios {
		b, ok := rc.host.Radio().(*nodes.BoardRadio)
		if !ok || (device != "" && b.Info().Device != device) {
			continue
		}
		snap := b.Node().Client().Snapshot()
		fillBoardProbe(res, snap)
		if !snap.Connected {
			res["error"] = "the board isn't answering: " + b.Info().Device
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if device == "" {
		writeError(w, http.StatusBadRequest, "device must be the board's serial port or its address (host or host:port)")
		return
	}
	if mtclient.IsSerial(device) {
		if !serialPath(device) {
			writeError(w, http.StatusBadRequest, "device must be a serial port such as /dev/ttyACM0 or /dev/serial/by-id/…")
			return
		}
	} else {
		addr, err := mtclient.TCPAddress(device)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Before a password exists anyone can call this: keep it to this machine and the LAN.
		if s.auth.SetupNeeded() && !lanAddr(r.Context(), addr) {
			writeError(w, http.StatusBadRequest, "until a password is set, a board's address must be on this machine or the local network")
			return
		}
	}
	s.boardProbe(r.Context(), device, res, 15*time.Second)
	writeJSON(w, http.StatusOK, res)
}

// boardProbe connects to a board, reads its settings into res and disconnects.
func (s *Server) boardProbe(ctx context.Context, device string, res map[string]any, wait time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, wait+5*time.Second)
	defer cancel()
	c := mtclient.New(mtclient.Options{Address: device, ConfigTimeout: wait, Logf: func(string, ...any) {}})
	if err := c.Start(ctx); err != nil {
		res["error"] = err.Error()
		return
	}
	defer c.Close()
	if err := c.Wait(ctx); err != nil {
		res["error"] = "no Meshtastic board answered on " + device + ": " + err.Error()
		return
	}
	fillBoardProbe(res, c.Snapshot())
}

func fillBoardProbe(res map[string]any, s mtclient.Snapshot) {
	if !s.Connected && s.MyInfo == nil {
		return
	}
	u := s.Self().GetUser()
	lora := s.Config.GetLora()
	res["ok"], res["sync_word_ok"] = s.Connected, s.Connected
	res["name"] = u.GetLongName()
	res["firmware"] = "Meshtastic " + s.Metadata.GetFirmwareVersion()
	details := []string{
		fmt.Sprintf("node %s %q (%s)", wire.NodeID(s.NodeNum()), u.GetLongName(), u.GetShortName()),
		fmt.Sprintf("hardware %s, role %s", s.Metadata.GetHwModel(), s.Config.GetDevice().GetRole()),
		fmt.Sprintf("region %s, preset %s, hop limit %d", lora.GetRegion(), lora.GetModemPreset(), lora.GetHopLimit()),
		fmt.Sprintf("%d nodes known", len(s.Nodes)),
		"its MQTT module will carry your identities (client proxy); its own MQTT connection stops",
	}
	if lora.GetRegion() == pb.Config_LoRaConfig_UNSET {
		details = append(details, "the region isn't set yet: it will be set to the one chosen here")
	}
	res["region"], res["preset"] = lora.GetRegion().String(), lora.GetModemPreset().String()
	res["role"] = s.Config.GetDevice().GetRole().String()
	res["node_id"] = wire.NodeID(s.NodeNum())
	res["details"] = details
}

// lanAddr reports whether host:port resolves only to loopback, private or link-local addresses.
func lanAddr(ctx context.Context, addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		found, err := net.DefaultResolver.LookupIP(lctx, "ip", host)
		if err != nil || len(found) == 0 {
			return false
		}
		ips = found
	}
	for _, ip := range ips {
		if !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
			return false
		}
	}
	return true
}

// runtimes reports what this machine can run hosted nodes with: an installed meshtasticd and
// Docker (with whether the image is already downloaded), for the configured program and image.
func (s *Server) runtimes(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.Lock()
	hc := s.cfg.Hosted
	s.cfgMu.Unlock()
	writeJSON(w, http.StatusOK, nodes.DetectRuntimes(r.Context(), hc.Meshtasticd, hc.DockerImage))
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"needed": s.auth.SetupNeeded()})
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.SetupNeeded() {
		writeError(w, http.StatusConflict, "setup has already been completed; sign in instead")
		return
	}
	var req struct {
		Password  string `json:"password"`
		Region    string `json:"region"`
		Preset    string `json:"preset"`
		Driver    string `json:"driver"` // kiss or spi; empty keeps the config's driver
		Device    string `json:"device"`
		RelayRole string `json:"relay_role"`
		// PrimaryChannel names the primary channel ("" = the preset's name), which picks the slot.
		PrimaryChannel *string `json:"primary_channel"`
		// Hosted says which meshtasticd runs the nodes; nil keeps the config's.
		Hosted *config.Hosted `json:"hosted"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Password) < 8 {
		// Checked before anything is saved, so a bad password leaves setup to be tried again.
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	s.cfgMu.Lock()
	next := *s.cfg
	if req.Region != "" {
		next.Mesh.Region = strings.ToUpper(req.Region)
	}
	if req.Preset != "" {
		next.Mesh.Preset = strings.ToUpper(req.Preset)
	}
	if req.RelayRole != "" {
		next.Relay.Role = mesh.NormalizeRelayRole(req.RelayRole)
	}
	if req.PrimaryChannel != nil {
		next.Mesh.PrimaryChannel = strings.TrimSpace(*req.PrimaryChannel)
	}
	if req.Hosted != nil {
		h := *req.Hosted
		h.Meshtasticd, h.DockerImage = strings.TrimSpace(h.Meshtasticd), strings.TrimSpace(h.DockerImage)
		if h.DockerImage != "" && !nodes.OfficialImage(h.DockerImage) {
			s.cfgMu.Unlock()
			writeError(w, http.StatusBadRequest, "setup only takes meshtastic/meshtasticd images; choose another under Configuration once signed in")
			return
		}
		next.Hosted = h
	}
	req.Device = strings.TrimSpace(req.Device)
	switch req.Driver {
	case "kiss", "spi", nodes.BoardDriver:
		if req.Driver != "kiss" && req.Device == "" {
			// Don't let a serial port left in the config pass as a board.
			s.cfgMu.Unlock()
			if req.Driver == "spi" {
				writeError(w, http.StatusBadRequest, "driver spi needs a device: a board from GET /boards, or auto")
			} else {
				writeError(w, http.StatusBadRequest, "driver meshtastic needs a device: the board's serial port or its address")
			}
			return
		}
		next.Radio.Driver = req.Driver
		if req.Device != "" {
			next.Radio.Device = req.Device
		}
	case "":
		// An older wizard only picks serial ports: don't write one over an SPI radio's board.
		if req.Device != "" && next.Radio.Driver == "kiss" {
			next.Radio.Device = req.Device
		}
	default:
		s.cfgMu.Unlock()
		writeError(w, http.StatusBadRequest, "driver must be kiss, spi or meshtastic")
		return
	}
	if err := next.Validate(); err != nil {
		s.cfgMu.Unlock()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfgMu.Unlock()
	// The radio settings first: if they can't be applied, no password is set and setup can be
	// run again.
	if err := s.applyConfig(r, &next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.auth.SetPassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok, exp := s.auth.IssueJWT()
	// A modem that hasn't opened yet switches to the chosen port at once (see followUnopenedDevices).
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires": exp.UnixMilli(), "restart_required": len(s.restartReasons()) > 0})
}

func (s *Server) serialPorts(w http.ResponseWriter, r *http.Request) {
	out := []map[string]string{}
	seen := map[string]bool{}
	byID, _ := filepath.Glob("/dev/serial/by-id/*")
	for _, p := range byID {
		target, _ := filepath.EvalSymlinks(p)
		seen[target] = true
		out = append(out, map[string]string{"path": p, "description": describePort(filepath.Base(p)), "device": target})
	}
	for _, pat := range []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/ttyAMA*", "/dev/cu.usb*"} {
		ms, _ := filepath.Glob(pat)
		for _, p := range ms {
			if !seen[p] {
				out = append(out, map[string]string{"path": p, "description": filepath.Base(p), "device": p})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func describePort(name string) string {
	name = strings.TrimPrefix(name, "usb-")
	if i := strings.LastIndex(name, "-if"); i > 0 {
		name = name[:i]
	}
	return strings.ReplaceAll(name, "_", " ")
}
