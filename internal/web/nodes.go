package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// MirrorNodeConfig records the settings a Meshtastic node reported for one of its radios, so the
// GUI shows (and later saves) what the node really runs.
func (s *Server) MirrorNodeConfig(radioID string, mc mesh.Config) {
	apply := func(m *config.Mesh, relay *config.Relay) bool {
		next := *m
		next.Region, next.Preset, next.PrimaryChannel = mc.Region, mc.Preset.String(), mc.PrimaryChannel
		next.ChannelNum, next.OverrideFreqMHz, next.FreqOffsetMHz = mc.ChannelNum, mc.OverrideFreqMHz, mc.FreqOffsetMHz
		next.TxPowerDBm, next.HopLimit = mc.TxPowerDBm, mc.HopLimit
		changed := next != *m || relay.Role != mc.RelayRole
		*m, relay.Role = next, mc.RelayRole
		return changed
	}
	rc := s.radioByID(radioID)
	if rc == nil {
		return
	}
	s.cfgMu.Lock()
	changed := false
	if rc == s.radios[0] {
		changed = apply(&s.cfg.Mesh, &s.cfg.Relay)
	} else {
		for i := range s.cfg.Radios {
			if s.cfg.Radios[i].ID == radioID {
				changed = apply(&s.cfg.Radios[i].Mesh, &s.cfg.Radios[i].Relay)
			}
		}
		if rc.cfg != nil {
			apply(&rc.cfg.Mesh, &rc.cfg.Relay)
		}
	}
	s.cfgMu.Unlock()
	if !changed {
		return
	}
	if err := s.saveIfPath(); err != nil {
		s.log.Warn("settings from the Meshtastic node not saved to the config file", "radio", radioID, "err", err)
	}
}

// probeNode is the setup probe for driver meshtastic: it connects to the board or meshtasticd,
// reads its configuration and disconnects. A node a running radio already uses is reported, not
// opened twice (a serial board takes one client).
func (s *Server) probeNode(w http.ResponseWriter, r *http.Request, device string) {
	res := map[string]any{"ok": false, "driver": nodes.Driver, "firmware": "", "name": "", "sync_word_ok": false, "error": "", "details": []string{}}
	for _, rc := range s.radios {
		c := s.radioConfig(rc).Radio
		a, ok := rc.host.Radio().(*nodes.Attached)
		if c.Driver != nodes.Driver || !ok || (device != "" && c.Device != device) {
			continue
		}
		fillNodeProbe(res, a.Client().Snapshot())
		if !a.Client().Snapshot().Connected {
			res["error"] = "the node isn't answering: " + c.Device
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if device == "" {
		writeError(w, http.StatusBadRequest, "device must be the board's serial port or a meshtasticd address (host or host:port)")
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
			writeError(w, http.StatusBadRequest, "until a password is set, a meshtasticd address must be on this machine or the local network")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	c := mtclient.New(mtclient.Options{Address: device, ConfigTimeout: 15 * time.Second})
	if err := c.Start(ctx); err != nil {
		res["error"] = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	defer c.Close()
	if err := c.Wait(ctx); err != nil {
		res["error"] = "no Meshtastic node answered on " + device + ": " + err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	fillNodeProbe(res, c.Snapshot())
	writeJSON(w, http.StatusOK, res)
}

func fillNodeProbe(res map[string]any, s mtclient.Snapshot) {
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
	}
	if lora.GetRegion() == pb.Config_LoRaConfig_UNSET {
		details = append(details, "the region isn't set yet: it will be set to the one chosen here")
	}
	res["region"], res["preset"] = lora.GetRegion().String(), lora.GetModemPreset().String()
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

// pushOwner writes a node identity's names to the node.
func pushOwner(ctx context.Context, id *mesh.Identity) error {
	rm := id.Remote()
	if rm == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := rm.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: id.UserCopy()}}); err != nil {
		return fmt.Errorf("saved here, but the Meshtastic node didn't take the new name: %w", err)
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
		if _, err := rm.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: id.ChannelCopy(i)}}); err != nil {
			return fmt.Errorf("saved here, but the Meshtastic node didn't take channel %d: %w", i, err)
		}
	}
	return nil
}
