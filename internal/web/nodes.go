package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/nodes"
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
		if _, err := rm.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: id.ChannelCopy(i)}}); err != nil {
			return fmt.Errorf("saved here, but meshtasticd didn't take channel %d: %w", i, err)
		}
	}
	return nil
}

// nodeKind is "board" or "hosted" for an identity a real node stands for, else "".
func nodeKind(id *mesh.Identity) string {
	if k, ok := id.Remote().(interface{ Kind() string }); ok {
		return k.Kind()
	}
	return ""
}

func (s *Server) getHosted(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.Lock()
	hc := s.cfg.Hosted
	s.cfgMu.Unlock()
	var inst []HostedInstance
	if s.opt.Hosted != nil {
		inst = s.opt.Hosted()
	}
	if inst == nil {
		inst = []HostedInstance{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"persona": hc.Persona, "meshtasticd": hc.Meshtasticd, "docker_image": hc.DockerImage,
		"port_base": hc.HostedPortBase(), "min_version": nodes.MinFirmware, "instances": inst,
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
	// Check a launcher that will run: better to say so now than after the restart.
	if req.Persona {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		var l nodes.Launcher = nodes.ExecLauncher{Binary: req.Meshtasticd}
		if req.DockerImage != "" {
			l = nodes.DockerLauncher{Image: req.DockerImage}
		}
		v, err := l.Version(ctx)
		switch {
		case err != nil:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s can't run: %v", l.Describe(), err))
			return
		case !nodes.VersionAtLeast(v, nodes.MinFirmware):
			writeError(w, http.StatusBadRequest, fmt.Sprintf("meshtasticd %s is too old: hosted nodes need %s or newer", v, nodes.MinFirmware))
			return
		}
	}
	s.cfgMu.Lock()
	s.cfg.Hosted = req
	s.cfgMu.Unlock()
	if err := s.saveIfPath(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("hosted meshtasticd settings changed", "persona", req.Persona, "docker_image", req.DockerImage)
	s.getHosted(w, r)
}
