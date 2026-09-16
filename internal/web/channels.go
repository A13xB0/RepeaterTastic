// An identity's channel slots and channel URLs.

package web

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/pb"
	"google.golang.org/protobuf/proto"
)

func (s *Server) putChannel(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 || idx >= mesh.MaxChannels {
		writeError(w, http.StatusBadRequest, "channel index must be 0-7")
		return
	}
	var req struct {
		Name     string `json:"name"`
		PSK      string `json:"psk"`
		Role     string `json:"role"`
		Uplink   bool   `json:"uplink"`
		Downlink bool   `json:"downlink"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	psk, err := base64.StdEncoding.DecodeString(req.PSK)
	if err != nil {
		writeError(w, http.StatusBadRequest, "psk must be base64 (AQ== is the default key)")
		return
	}
	role := pb.Channel_SECONDARY
	switch strings.ToUpper(req.Role) {
	case "PRIMARY":
		role = pb.Channel_PRIMARY
	case "DISABLED":
		role = pb.Channel_DISABLED
	}
	if idx == 0 {
		if role != pb.Channel_PRIMARY {
			writeError(w, http.StatusConflict, "channel 0 is the shared primary channel; its role can't change")
			return
		}
		if req.Name != s.hostFor(r).Config().PrimaryChannel {
			writeError(w, http.StatusConflict,
				"the primary channel name is shared by every identity because it picks the frequency; change it with Edit under Configuration → Radios")
			return
		}
	}
	if len(req.Name) > 11 {
		writeError(w, http.StatusBadRequest, "channel names are at most 11 characters")
		return
	}
	old := id.ChannelCopy(idx)
	ch := &pb.Channel{Index: int32(idx), Role: role, //nolint:gosec // idx is 0-7, checked above
		Settings: &pb.ChannelSettings{Name: req.Name, Psk: psk,
			UplinkEnabled: req.Uplink, DownlinkEnabled: req.Downlink, ModuleSettings: old.GetSettings().GetModuleSettings()}}
	if err := s.hostFor(r).SetChannel(id, ch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := pushChannels(r.Context(), id, idx); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.saveIdentities()
	writeJSON(w, http.StatusOK, s.identityJSON(id))
}

func (s *Server) getChannelURL(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	host := s.hostFor(r)
	rp := host.RadioParams()
	set := &pb.ChannelSet{LoraConfig: &pb.Config_LoRaConfig{UsePreset: true, ModemPreset: rp.Preset, Region: rp.Region.Code,
		HopLimit: host.Config().HopLimit, TxEnabled: true}}
	for i := 0; i < mesh.MaxChannels; i++ {
		if ch := id.ChannelCopy(i); ch != nil && ch.Role != pb.Channel_DISABLED {
			set.Settings = append(set.Settings, ch.Settings)
		}
	}
	b, _ := proto.Marshal(set)
	writeJSON(w, http.StatusOK, map[string]string{"url": "https://meshtastic.org/e/#" + base64.RawURLEncoding.EncodeToString(b)})
}

func (s *Server) postChannelURL(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	frag := req.URL
	if i := strings.Index(frag, "#"); i >= 0 {
		frag = frag[i+1:]
	}
	frag = strings.TrimRight(strings.TrimSpace(frag), "=")
	frag = strings.NewReplacer("+", "-", "/", "_").Replace(frag)
	b, err := base64.RawURLEncoding.DecodeString(frag)
	set := &pb.ChannelSet{}
	if err != nil || proto.Unmarshal(b, set) != nil || len(set.Settings) == 0 {
		writeError(w, http.StatusBadRequest, "that doesn't look like a Meshtastic channel URL (https://meshtastic.org/e/#…)")
		return
	}
	primaryName := s.hostFor(r).Config().PrimaryChannel
	free := func() int { // secondary channels go into free slots; existing channels are kept
		for k := 1; k < mesh.MaxChannels; k++ {
			if c := id.ChannelCopy(k); c == nil || c.Role == pb.Channel_DISABLED {
				return k
			}
		}
		return -1
	}
	skipped := 0
	before := make([]*pb.Channel, mesh.MaxChannels)
	for i := range before {
		before[i] = id.ChannelCopy(i)
	}
	for i, st := range set.Settings {
		if i == 0 && (st.GetName() == primaryName || st.GetName() == "") {
			ch := id.ChannelCopy(0)
			ch.Settings.Psk = st.Psk
			if err := s.hostFor(r).SetChannel(id, ch); err != nil {
				writeError(w, http.StatusBadRequest, "primary channel: "+err.Error())
				return
			}
			continue
		}
		k := free()
		if k < 0 {
			skipped++
			continue
		}
		if err := s.hostFor(r).SetChannel(id, &pb.Channel{Index: int32(k), Role: pb.Channel_SECONDARY, Settings: st}); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("channel %q: %v", st.GetName(), err))
			return
		}
	}
	if skipped > 0 {
		s.log.Info("channel URL import: no free slot for some channels", "identity", id.NodeID(), "skipped", skipped)
	}
	var changed []int
	for i := range before {
		if !proto.Equal(before[i], id.ChannelCopy(i)) {
			changed = append(changed, i)
		}
	}
	if err := pushChannels(r.Context(), id, changed...); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.saveIdentities()
	writeJSON(w, http.StatusOK, s.identityJSON(id))
}
