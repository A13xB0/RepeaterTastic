// The meshtasticd nodes: their settings, health, and identity changes pushed to them.

package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
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

// hostedLog is GET /hosted/{name}/log: the last lines an instance's meshtasticd printed.
func (s *Server) hostedLog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	for _, x := range s.opt.Hosting {
		if x == nil {
			continue
		}
		if lines, ok := x.InstanceLog(name); ok {
			writeJSON(w, http.StatusOK, lines)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no meshtasticd "+name)
}
