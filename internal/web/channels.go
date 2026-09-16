// An identity's channel slots and channel URLs.

package web

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
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
	set, ok := parseChannelURL(req.URL)
	if !ok {
		writeError(w, http.StatusBadRequest, "that doesn't look like a Meshtastic channel URL (https://meshtastic.org/e/#…)")
		return
	}
	host := s.hostFor(r)
	before := channelSnapshot(id)
	skipped, err := importChannels(host, id, set)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if skipped > 0 {
		s.log.Info("channel URL import: no free slot for some channels", "identity", id.NodeID(), "skipped", skipped)
	}
	if err := pushChannels(r.Context(), id, changedChannels(before, id)...); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.saveIdentities()
	writeJSON(w, http.StatusOK, s.identityJSON(id))
}

// parseChannelURL decodes the channel set in a Meshtastic channel URL, forgiving padding and
// standard base64; it reports false unless the URL holds at least one channel.
func parseChannelURL(raw string) (*pb.ChannelSet, bool) {
	frag := raw
	if i := strings.Index(frag, "#"); i >= 0 {
		frag = frag[i+1:]
	}
	frag = strings.TrimRight(strings.TrimSpace(frag), "=")
	frag = strings.NewReplacer("+", "-", "/", "_").Replace(frag)
	b, err := base64.RawURLEncoding.DecodeString(frag)
	set := &pb.ChannelSet{}
	if err != nil || proto.Unmarshal(b, set) != nil || len(set.Settings) == 0 {
		return nil, false
	}
	return set, true
}

// importChannels applies a channel set to an identity: a first channel matching the shared primary
// sets its key, and the rest go into free slots so existing channels are kept. It returns how many
// found no free slot.
func importChannels(host *mesh.Host, id *mesh.Identity, set *pb.ChannelSet) (int, error) {
	primaryName := host.Config().PrimaryChannel
	skipped := 0
	for i, st := range set.Settings {
		if i == 0 && (st.GetName() == primaryName || st.GetName() == "") {
			ch := id.ChannelCopy(0)
			ch.Settings.Psk = st.Psk
			if err := host.SetChannel(id, ch); err != nil {
				return skipped, errors.New("primary channel: " + err.Error())
			}
			continue
		}
		k := freeChannelSlot(id)
		if k < 0 {
			skipped++
			continue
		}
		if err := host.SetChannel(id, &pb.Channel{Index: int32(k), Role: pb.Channel_SECONDARY, Settings: st}); err != nil {
			return skipped, fmt.Errorf("channel %q: %w", st.GetName(), err)
		}
	}
	return skipped, nil
}

// freeChannelSlot returns the first unused secondary slot, or -1 if they're all in use.
func freeChannelSlot(id *mesh.Identity) int {
	for k := 1; k < mesh.MaxChannels; k++ {
		if c := id.ChannelCopy(k); c == nil || c.Role == pb.Channel_DISABLED {
			return k
		}
	}
	return -1
}

// channelSnapshot copies every channel slot so changes can be found afterwards.
func channelSnapshot(id *mesh.Identity) []*pb.Channel {
	before := make([]*pb.Channel, mesh.MaxChannels)
	for i := range before {
		before[i] = id.ChannelCopy(i)
	}
	return before
}

// changedChannels lists the slots that differ from a snapshot.
func changedChannels(before []*pb.Channel, id *mesh.Identity) []int {
	var changed []int
	for i := range before {
		if !proto.Equal(before[i], id.ChannelCopy(i)) {
			changed = append(changed, i)
		}
	}
	return changed
}
