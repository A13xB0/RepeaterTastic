// Chats: conversations, messages and read marks.

package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	key, err := url.PathUnescape(r.PathValue("key"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad conversation key")
		return
	}
	s.hostFor(r).Messages.MarkRead(id.NodeNum, key)
	s.hostFor(r).Bus.Publish(mesh.Event{Type: "identity", Data: id.NodeID()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	convs := s.hostFor(r).Messages.Conversations(id.NodeNum, id.NodeID())
	// Every enabled channel is a conversation even before anything is said on it:
	// the chat page only opens listed conversations, so a new identity could
	// otherwise send DMs but never post in a channel.
	listed := map[string]bool{}
	for _, c := range convs {
		listed[c.Key] = true
	}
	for i := 0; i < mesh.MaxChannels; i++ {
		ch := id.ChannelCopy(i)
		key := "ch:" + strconv.Itoa(i)
		if ch == nil || ch.Role == pb.Channel_DISABLED || listed[key] {
			continue
		}
		convs = append(convs, mesh.ConversationSummary{Key: key})
	}
	for i := range convs {
		convs[i].Title = s.conversationTitle(id, convs[i].Key)
	}
	// Channels without history sort after every conversation that has some, in slot order.
	sort.SliceStable(convs, func(i, j int) bool { return convs[i].LastTime > convs[j].LastTime })
	writeJSON(w, http.StatusOK, convs)
}

func (s *Server) conversationTitle(id *mesh.Identity, key string) string {
	if strings.HasPrefix(key, "ch:") {
		idx, _ := strconv.Atoi(key[3:])
		if ch := id.ChannelCopy(idx); ch != nil {
			if n := ch.GetSettings().GetName(); n != "" {
				return n
			}
		}
		return s.radioOf(id).host.RadioParams().PresetName()
	}
	num, err := wire.ParseNodeID(strings.TrimPrefix(key, "dm:"))
	if err == nil {
		if e, ok := s.radioOf(id).host.DB.Get(num); ok && e.User != nil {
			return e.User.LongName
		}
	}
	return strings.TrimPrefix(key, "dm:")
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	writeJSON(w, http.StatusOK, s.hostFor(r).Messages.List(id.NodeNum, id.NodeID(), q.Get("conversation"), before, limit))
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	id := s.identityParam(w, r)
	if id == nil {
		return
	}
	var req struct {
		To      string `json:"to"`
		Channel int    `json:"channel"`
		Text    string `json:"text"`
		WantAck *bool  `json:"want_ack"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	to := wire.Broadcast
	if req.To != "" {
		n, err := wire.ParseNodeID(req.To)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be a node id like !a1c40e07")
			return
		}
		to = n
	}
	wantAck := true
	if req.WantAck != nil {
		wantAck = *req.WantAck
	}
	pid, err := s.hostFor(r).SendText(id, to, req.Channel, req.Text, wantAck)
	if err != nil && pid == 0 {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, m := range s.hostFor(r).Messages.List(id.NodeNum, id.NodeID(), "", 0, 20) {
		if m.ID == pid && m.Direction == "out" {
			writeJSON(w, http.StatusAccepted, m)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": pid})
}
