package mesh

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

// Event is published on the host bus for the web UI and other observers.
type Event struct {
	Type string // packet, message, identity, node, traceroute, log
	Data any
}

// Bus is a non-blocking fan-out; slow subscribers miss events rather than stall the radio.
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

func (b *Bus) Subscribe(buf int) (<-chan Event, func()) {
	ch := make(chan Event, buf)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// PacketRecord is one line of the packet log (API shape, see docs/api.md).
type PacketRecord struct {
	Seq         uint64         `json:"seq"`
	Time        int64          `json:"time"`
	Direction   string         `json:"direction"`
	Kind        string         `json:"kind"`
	ID          uint32         `json:"id"`
	From        string         `json:"from"`
	To          string         `json:"to"`
	ChannelHash uint8          `json:"channel_hash"`
	Channel     string         `json:"channel,omitempty"`
	Port        string         `json:"port,omitempty"`
	HopLimit    uint32         `json:"hop_limit"`
	HopStart    uint32         `json:"hop_start"`
	WantAck     bool           `json:"want_ack"`
	ViaMQTT     bool           `json:"via_mqtt"`
	NextHop     uint32         `json:"next_hop"`
	RelayNode   uint32         `json:"relay_node"`
	RSSI        int32          `json:"rssi"`
	SNR         float32        `json:"snr"`
	Size        int            `json:"size"`
	AirtimeMs   float64        `json:"airtime_ms"`
	DecodedBy   string         `json:"decoded_by,omitempty"`
	PKI         bool           `json:"pki"`
	Summary     string         `json:"summary,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	Raw         string         `json:"raw,omitempty"`
	Transport   string         `json:"transport,omitempty"`
	Radio       string         `json:"radio_id,omitempty"` // the radio that heard or sent it

	// Mesh and Data travel on the bus only (plugins): the packet as heard or sent, and its
	// decoded payload when one of this radio's channels or keys could read it. Never logged.
	Mesh *pb.MeshPacket `json:"-"`
	Data *pb.Data       `json:"-"`
	// Holders are the identities that would hear this packet as a node does: those holding the
	// channel it decoded on (with the channel's index on each), or the recipient of a PKI DM
	// (index 0).
	Holders []ChannelHolder `json:"-"`
}

// ChannelHolder is an identity that holds a packet's channel, and the channel's index on it.
type ChannelHolder struct {
	NodeNum uint32
	Index   int
	Relay   bool
}

// PacketLog is a fixed-size ring of recent packets.
type PacketLog struct {
	mu   sync.Mutex
	buf  []PacketRecord
	next int
	full bool
	seq  uint64
}

func NewPacketLog(n int) *PacketLog { return &PacketLog{buf: make([]PacketRecord, n)} }

func (l *PacketLog) Add(r PacketRecord) PacketRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	r.Seq = l.seq
	if r.Time == 0 {
		r.Time = time.Now().UnixMilli()
	}
	l.buf[l.next] = r
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
	return r
}

// List returns newest-first records matching filter, up to limit, older than beforeMs (0 = now).
func (l *PacketLog) List(limit int, beforeMs int64, filter func(*PacketRecord) bool) []PacketRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.next
	if l.full {
		n = len(l.buf)
	}
	out := []PacketRecord{}
	for i := 0; i < n && len(out) < limit; i++ {
		idx := (l.next - 1 - i + len(l.buf)) % len(l.buf)
		r := &l.buf[idx]
		if beforeMs > 0 && r.Time >= beforeMs {
			continue
		}
		if filter != nil && !filter(r) {
			continue
		}
		out = append(out, *r)
	}
	return out
}

// Message is a chat message for the web UI.
type Message struct {
	ID        uint32  `json:"id"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Channel   int     `json:"channel"`
	Text      string  `json:"text"`
	Time      int64   `json:"time"`
	Direction string  `json:"direction"`
	Status    string  `json:"status"`
	Error     string  `json:"error,omitempty"`
	PKI       bool    `json:"pki"`
	RSSI      int32   `json:"rssi,omitempty"`
	SNR       float32 `json:"snr,omitempty"`
	Hops      int     `json:"hops"`
}

// Conversation key: "ch:<index>" or "dm:!nodeid".
func (m *Message) Conversation(self string) string {
	if m.To == "!ffffffff" {
		return "ch:" + itoa(m.Channel)
	}
	if m.From == self {
		return "dm:" + m.To
	}
	return "dm:" + m.From
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

// MessageStore keeps recent messages per identity.
type MessageStore struct {
	mu   sync.Mutex
	per  map[uint32][]*Message
	max  int
	read map[uint32]map[string]int64 // conversation → last read time
	// dirty avoids rewriting the file (SD card wear on a Pi) when nothing changed.
	dirty bool
}

func NewMessageStore(max int) *MessageStore {
	return &MessageStore{per: map[uint32][]*Message{}, max: max, read: map[uint32]map[string]int64{}}
}

// Take removes an identity's messages and read marks, to move them to another radio's store.
func (s *MessageStore) Take(identity uint32) ([]*Message, map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs, read := s.per[identity], s.read[identity]
	delete(s.per, identity)
	delete(s.read, identity)
	s.dirty = true
	return msgs, read
}

// Put adds messages and read marks taken from another store.
func (s *MessageStore) Put(identity uint32, msgs []*Message, read map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := append(s.per[identity], msgs...)
	if len(l) > s.max {
		l = l[len(l)-s.max:]
	}
	if len(l) > 0 {
		s.per[identity] = l
	}
	if len(read) > 0 {
		if s.read[identity] == nil {
			s.read[identity] = map[string]int64{}
		}
		for k, v := range read {
			s.read[identity][k] = v
		}
	}
	s.dirty = true
}

func (s *MessageStore) Add(identity uint32, m *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := append(s.per[identity], m)
	if len(l) > s.max {
		l = l[len(l)-s.max:]
	}
	s.per[identity] = l
	s.dirty = true
}

// SetStatus updates an outgoing message by packet id; returns the updated copy.
func (s *MessageStore) SetStatus(identity, id uint32, status, errText string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.per[identity]
	for i := len(l) - 1; i >= 0; i-- {
		if l[i].ID == id && l[i].Direction == "out" {
			if l[i].Status == "acked" && status != "acked" {
				return *l[i], false
			}
			l[i].Status, l[i].Error = status, errText
			s.dirty = true
			return *l[i], true
		}
	}
	return Message{}, false
}

func (s *MessageStore) List(identity uint32, self, conversation string, beforeMs int64, limit int) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Message{}
	l := s.per[identity]
	for i := len(l) - 1; i >= 0 && len(out) < limit; i-- {
		m := l[i]
		if conversation != "" && m.Conversation(self) != conversation {
			continue
		}
		if beforeMs > 0 && m.Time >= beforeMs {
			continue
		}
		out = append(out, *m)
	}
	// oldest first for display
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if conversation != "" {
		if s.read[identity] == nil {
			s.read[identity] = map[string]int64{}
		}
		s.read[identity][conversation] = time.Now().UnixMilli()
	}
	return out
}

// ConversationSummary is one entry of the conversations list.
type ConversationSummary struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	LastText string `json:"last_text"`
	LastTime int64  `json:"last_time"`
	Unread   int    `json:"unread"`
}

func (s *MessageStore) Conversations(identity uint32, self string) []ConversationSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := map[string]*ConversationSummary{}
	var order []string
	for _, m := range s.per[identity] {
		k := m.Conversation(self)
		c, ok := idx[k]
		if !ok {
			c = &ConversationSummary{Key: k}
			idx[k] = c
			order = append(order, k)
		}
		c.LastText, c.LastTime = m.Text, m.Time
		if m.Direction == "in" && m.Time > s.read[identity][k] {
			c.Unread++
		}
	}
	out := make([]ConversationSummary, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	return out
}

// MarkRead clears unread messages in one conversation.
func (s *MessageStore) MarkRead(identity uint32, conversation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read[identity] == nil {
		s.read[identity] = map[string]int64{}
	}
	s.read[identity][conversation] = time.Now().UnixMilli()
	s.dirty = true
}

// UnreadTotal counts unread incoming messages across all conversations of an identity.
func (s *MessageStore) UnreadTotal(identity uint32, self string) int {
	n := 0
	for _, c := range s.Conversations(identity, self) {
		n += c.Unread
	}
	return n
}

// Window returns an identity's messages newer than sinceMs.
func (s *MessageStore) Window(identity uint32, sinceMs int64) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.per[identity] {
		if m.Time >= sinceMs {
			out = append(out, *m)
		}
	}
	return out
}

type messageFile struct {
	Messages map[string][]*Message       `json:"messages"`
	Read     map[string]map[string]int64 `json:"read"`
}

// Save writes all messages and read markers to path.
func (s *MessageStore) Save(path string) error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.dirty = false
	f := messageFile{Messages: map[string][]*Message{}, Read: map[string]map[string]int64{}}
	for id, l := range s.per {
		cp := make([]*Message, len(l))
		for i, m := range l {
			mm := *m
			cp[i] = &mm
		}
		f.Messages[itoa(int(id))] = cp
	}
	for id, r := range s.read {
		m := map[string]int64{}
		for k, v := range r {
			m[k] = v
		}
		f.Read[itoa(int(id))] = m
	}
	s.mu.Unlock()
	return writeJSONAtomic(path, f)
}

// failUnacked marks outgoing messages still waiting for an acknowledgement as failed: the
// restart lost their retries.
func failUnacked(l []*Message) {
	for _, m := range l {
		if m.Direction == "out" && (m.Status == "queued" || m.Status == "sent") {
			m.Status = "failed"
			m.Error = "restarted before an acknowledgement arrived"
		}
	}
}

// Load restores messages saved by Save.
func (s *MessageStore) Load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f messageFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, l := range f.Messages {
		id, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			continue
		}
		failUnacked(l)
		s.per[uint32(id)] = l
	}
	for k, r := range f.Read {
		id, err := strconv.ParseUint(k, 10, 32)
		if err == nil {
			s.read[uint32(id)] = r
		}
	}
	return nil
}
