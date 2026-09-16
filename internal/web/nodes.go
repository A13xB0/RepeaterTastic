// The node DB: nodes, sightings, traceroutes and NodeInfo requests.

package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

type traceWait struct {
	mu      sync.Mutex
	pending map[string]time.Time // identity|target → deadline
}

func (s *Server) watchTraceroutes(ctx context.Context, rc *radioCtx) {
	events, unsub := rc.host.Bus.Subscribe(64)
	defer unsub()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			if tr, ok := e.Data.(mesh.TracerouteResult); ok && e.Type == "traceroute" {
				s.traces.mu.Lock()
				delete(s.traces.pending, tr.Identity+"|"+tr.Target)
				s.traces.mu.Unlock()
			}
		case now := <-t.C:
			s.traces.mu.Lock()
			for k, deadline := range s.traces.pending {
				if now.After(deadline) {
					parts := strings.SplitN(k, "|", 2)
					if num, err := wire.ParseNodeID(parts[0]); err != nil || rc.host.Identity(num) == nil {
						continue // another radio's traceroute
					}
					delete(s.traces.pending, k)
					rc.host.Bus.Publish(mesh.Event{Type: "traceroute", Data: map[string]any{
						"identity": parts[0], "target": parts[1], "route": []string{}, "snr_towards": []float64{},
						"route_back": []string{}, "snr_back": []float64{}, "error": "no response within 60 s"}})
				}
			}
			s.traces.mu.Unlock()
		}
	}
}

func (s *Server) expectTraceroute(from, target string) {
	s.traces.mu.Lock()
	s.traces.pending[from+"|"+target] = time.Now().Add(60 * time.Second)
	s.traces.mu.Unlock()
}

func nodeJSON(e mesh.NodeEntry, knownBy []string) map[string]any {
	n := map[string]any{"node_id": wire.NodeID(e.Num), "node_num": e.Num, "has_public_key": e.PublicKey() != nil,
		"snr": nil, "rssi": nil, "via_mqtt": e.ViaMQTT, "local": e.Local, "favorite": e.Favorite, "ignored": e.Ignored}
	// Signal is only measured for nodes heard directly (as in the firmware); for relayed,
	// MQTT and local nodes 0/0 means "unknown", not a 0 dB link.
	if !e.Local && e.HopsAway == 0 && e.RSSI != 0 {
		n["snr"], n["rssi"] = e.SNR, e.RSSI
	}
	if e.User != nil {
		n["long_name"], n["short_name"], n["hw_model"], n["role"] = e.User.LongName, e.User.ShortName, e.User.HwModel.String(), e.User.Role.String()
		n["has_user"] = true
	} else {
		// Heard but no NodeInfo yet: the firmware's own placeholder names, so every
		// node always has the fields the GUI sorts and filters on.
		id := wire.NodeID(e.Num)
		short := id[len(id)-4:]
		n["long_name"], n["short_name"], n["hw_model"], n["role"] = "Meshtastic "+short, short,
			pb.HardwareModel_UNSET.String(), pb.Config_DeviceConfig_CLIENT.String()
		n["has_user"] = false
	}
	if !e.LastHeard.IsZero() {
		n["last_heard"] = e.LastHeard.UnixMilli()
	} else {
		n["last_heard"] = nil
	}
	if e.HopsAway >= 0 {
		n["hops_away"] = e.HopsAway
	} else {
		n["hops_away"] = nil
	}
	if e.NextHop != 0 {
		n["next_hop"] = e.NextHop
	} else {
		n["next_hop"] = nil
	}
	if p := e.Position; p != nil && (p.GetLatitudeI() != 0 || p.GetLongitudeI() != 0) {
		n["position"] = map[string]any{"lat": float64(p.GetLatitudeI()) / 1e7, "lon": float64(p.GetLongitudeI()) / 1e7,
			"alt": p.GetAltitude(), "time": int64(p.Time) * 1000}
	} else {
		n["position"] = nil
	}
	if m := e.Metrics; m != nil {
		n["telemetry"] = map[string]any{"battery": m.GetBatteryLevel(), "voltage": m.GetVoltage(),
			"channel_util": m.GetChannelUtilization(), "air_util_tx": m.GetAirUtilTx()}
	} else {
		n["telemetry"] = nil
	}
	if e.Local {
		n["known_by"] = []string{}
	} else {
		n["known_by"] = knownBy
	}
	return n
}

func (s *Server) localIDs(h *mesh.Host) []string {
	var ids []string
	for _, id := range h.Identities() {
		if id.Enabled {
			ids = append(ids, id.NodeID())
		}
	}
	return ids
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	known := s.localIDs(s.hostFor(r))
	for _, e := range s.hostFor(r).DB.Snapshot() {
		out = append(out, nodeJSON(e, known))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) fromIdentity(w http.ResponseWriter, r *http.Request) (*mesh.Identity, uint32, bool) {
	target, err := wire.ParseNodeID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad node id")
		return nil, 0, false
	}
	var req struct {
		From string `json:"from"`
	}
	if !readJSON(w, r, &req) {
		return nil, 0, false
	}
	var from *mesh.Identity
	if req.From == "" {
		from = s.hostFor(r).Relay()
	} else if n, err := wire.ParseNodeID(req.From); err == nil {
		from = s.hostFor(r).Identity(n)
	}
	if from == nil {
		writeError(w, http.StatusBadRequest, "from must be one of this host's identities")
		return nil, 0, false
	}
	return from, target, true
}

func (s *Server) traceroute(w http.ResponseWriter, r *http.Request) {
	from, target, ok := s.fromIdentity(w, r)
	if !ok {
		return
	}
	if err := s.hostFor(r).Traceroute(from, target); err != nil {
		writeError(w, http.StatusTooManyRequests, "traceroute not sent: "+err.Error())
		return
	}
	s.expectTraceroute(from.NodeID(), wire.NodeID(target))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

func (s *Server) requestNodeInfo(w http.ResponseWriter, r *http.Request) {
	from, target, ok := s.fromIdentity(w, r)
	if !ok {
		return
	}
	s.hostFor(r).RequestNodeInfo(from, target)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	num, err := wire.ParseNodeID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad node id")
		return
	}
	if s.hostFor(r).Identity(num) != nil {
		writeError(w, http.StatusConflict, "that node is one of this host's identities; delete it under Identities")
		return
	}
	s.hostFor(r).DB.Delete(num)
	w.WriteHeader(http.StatusNoContent)
}

// nodeSightings is GET /nodes/{id}/sightings: what every radio knows about a node.
func (s *Server) nodeSightings(w http.ResponseWriter, r *http.Request) {
	num, err := wire.ParseNodeID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "node id must look like !a1c40e07")
		return
	}
	out := []map[string]any{}
	for _, sg := range s.radios[0].host.Sightings(num) {
		name := sg.Radio
		if rc := s.radioByID(sg.Radio); rc != nil {
			name = rc.name
		}
		out = append(out, map[string]any{"radio_id": sg.Radio, "radio_name": name, "last_heard": sg.LastHeard.UnixMilli(),
			"snr": sg.SNR, "rssi": sg.RSSI, "hops_away": sg.HopsAway, "via_mqtt": sg.ViaMQTT})
	}
	writeJSON(w, http.StatusOK, out)
}
