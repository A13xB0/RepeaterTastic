package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// pushOwner writes a node identity's names to the node.
func pushOwner(ctx context.Context, id *mesh.Identity) error {
	rm := id.Remote()
	if rm == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := rm.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: id.UserCopy()}}); err != nil {
		return fmt.Errorf("saved here, but meshtasticd didn't take the new name: %w", err)
	}
	return nil
}

// pushChannels writes a node identity's channel slots to the node.
func pushChannels(ctx context.Context, id *mesh.Identity, slots ...int) error {
	rm := id.Remote()
	if rm == nil || len(slots) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, i := range slots {
		ch := id.ChannelCopy(i)
		if p, ok := rm.(interface{ PrepareChannel(*pb.Channel) }); ok {
			p.PrepareChannel(ch) // what the node needs on every channel (a board's MQTT proxy)
		}
		if _, err := rm.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: ch}}); err != nil {
			return fmt.Errorf("saved here, but meshtasticd didn't take channel %d: %w", i, err)
		}
	}
	return nil
}

// hostedInstances lists every radio's meshtasticd instances.
func (s *Server) hostedInstances() []HostedInstance {
	out := []HostedInstance{}
	for _, rc := range s.radios {
		x := s.opt.Hosting[rc.id]
		if x == nil {
			continue
		}
		for _, hn := range x.Nodes() {
			out = append(out, HostedInstance{Radio: rc.id, Role: hn.Role, HostedStatus: hn.HostedStatus})
		}
	}
	return out
}

// nodesHealth is how meshtasticd is doing across the site: the worst radio's state, and every
// radio's problems.
func (s *Server) nodesHealth() map[string]any {
	rank := map[string]int{nodes.HealthOK: 0, nodes.HealthStarting: 1, nodes.HealthWarning: 2, nodes.HealthError: 3}
	state, total, up := nodes.HealthOK, 0, 0
	problems := []string{}
	var version, launcher string
	for _, rc := range s.radios {
		x := s.opt.Hosting[rc.id]
		if x == nil {
			continue
		}
		h := x.Health()
		if rank[h.State] > rank[state] {
			state = h.State
		}
		total, up = total+h.Nodes, up+h.Up
		for _, p := range h.Problems {
			if len(s.radios) > 1 {
				p = rc.name + ": " + p
			}
			problems = append(problems, p)
		}
		if version == "" {
			version = h.Version
		}
		launcher = h.Launcher
	}
	return map[string]any{"state": state, "nodes": total, "up": up, "problems": problems, "version": version, "launcher": launcher}
}

func (s *Server) getHosted(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.Lock()
	hc := s.cfg.Hosted
	s.cfgMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"meshtasticd": hc.Meshtasticd, "docker_image": hc.DockerImage,
		"port_base": hc.HostedPortBase(), "min_version": nodes.MinFirmware, "instances": s.hostedInstances(),
		"restart_required": len(s.restartReasons()) > 0})
}

func (s *Server) putHosted(w http.ResponseWriter, r *http.Request) {
	var req config.Hosted
	if !readJSON(w, r, &req) {
		return
	}
	req.Meshtasticd = strings.TrimSpace(req.Meshtasticd)
	req.DockerImage = strings.TrimSpace(req.DockerImage)
	if req.PortBase == 4500 {
		req.PortBase = 0 // the default stays implicit in the file
	}
	s.cfgMu.Lock()
	next := *s.cfg
	next.Hosted = req
	err := next.Validate()
	s.cfgMu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Check the launcher will run: better to say so now than after the restart.
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if _, err := nodes.CheckLauncher(ctx, nodes.LauncherFor(req.Meshtasticd, req.DockerImage)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfgMu.Lock()
	s.cfg.Hosted = req
	s.cfgMu.Unlock()
	if err := s.saveIfPath(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("meshtasticd settings changed", "meshtasticd", req.Meshtasticd, "docker_image", req.DockerImage)
	s.getHosted(w, r)
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
