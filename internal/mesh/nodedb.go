package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// NodeEntry is what we know about a node heard on the mesh (or one of our own identities).
type NodeEntry struct {
	Num       uint32
	User      *pb.User
	Position  *pb.Position
	Metrics   *pb.DeviceMetrics
	LastHeard time.Time
	SNR       float32
	RSSI      int32
	HopsAway  int // -1 unknown
	ViaMQTT   bool
	NextHop   uint8
	Favorite  bool
	Ignored   bool
	Channel   uint32
	Local     bool
}

func (e *NodeEntry) PublicKey() []byte {
	if e == nil || e.User == nil || len(e.User.PublicKey) != 32 {
		return nil
	}
	return e.User.PublicKey
}

// NodeDB is shared by every identity on the host: they all sit in the same RF spot.
type NodeDB struct {
	mu    sync.RWMutex
	nodes map[uint32]*NodeEntry
	dirty bool
}

func NewNodeDB() *NodeDB { return &NodeDB{nodes: map[uint32]*NodeEntry{}} }

func (db *NodeDB) Get(num uint32) (NodeEntry, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	e, ok := db.nodes[num]
	if !ok {
		return NodeEntry{}, false
	}
	return *e, true
}

func (db *NodeDB) entry(num uint32) *NodeEntry {
	e, ok := db.nodes[num]
	if !ok {
		e = &NodeEntry{Num: num, HopsAway: -1}
		db.nodes[num] = e
	}
	return e
}

// Update applies fn to the node, creating it if needed.
func (db *NodeDB) Update(num uint32, fn func(e *NodeEntry)) {
	db.mu.Lock()
	defer db.mu.Unlock()
	fn(db.entry(num))
	db.dirty = true
}

func (db *NodeDB) Delete(num uint32) {
	db.mu.Lock()
	defer db.mu.Unlock()
	delete(db.nodes, num)
	db.dirty = true
}

// UpdateFromPacket mirrors NodeDB::updateFrom for a decoded or opaque received packet.
func (db *NodeDB) UpdateFromPacket(p *pb.MeshPacket, now time.Time) {
	if p.From == 0 {
		return
	}
	db.Update(p.From, func(e *NodeEntry) {
		e.LastHeard = now
		e.ViaMQTT = p.ViaMqtt
		if h := wire.HopsAway(p); h >= 0 {
			e.HopsAway = h
			if h == 0 && p.TransportMechanism == pb.MeshPacket_TRANSPORT_LORA {
				e.SNR = p.RxSnr
				if p.RxRssi != nil {
					e.RSSI = *p.RxRssi
				}
			}
		}
		if d, ok := p.PayloadVariant.(*pb.MeshPacket_Decoded); ok {
			e.Channel = p.Channel
			_ = d
		}
	})
}

// SetUser stores a NodeInfo user. Keys are trust-on-first-use: a different key for a known node is
// ignored (returns false) so a spoofed NodeInfo can't redirect DMs.
func (db *NodeDB) SetUser(num uint32, u *pb.User) (changed bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	e := db.entry(num)
	nu := proto.Clone(u).(*pb.User)
	nu.Id = wire.NodeID(num)
	if old := e.PublicKey(); old != nil && len(nu.PublicKey) == 32 && string(old) != string(nu.PublicKey) {
		nu.PublicKey = old
	}
	if old := e.PublicKey(); old != nil && len(nu.PublicKey) == 0 {
		nu.PublicKey = old
	}
	changed = e.User == nil || !proto.Equal(e.User, nu)
	e.User = nu
	db.dirty = true
	return changed
}

// ResolveLastByte finds the unique node whose last byte matches (optionally only direct neighbours).
func (db *NodeDB) ResolveLastByte(b uint8, directOnly bool) (uint32, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var found uint32
	n := 0
	for num, e := range db.nodes {
		if wire.LastByte(num) != b {
			continue
		}
		if directOnly && e.HopsAway != 0 && !e.Local {
			continue
		}
		found = num
		n++
	}
	return found, n == 1
}

// LastByteCollision reports another known node using the same last byte (0 = none).
func (db *NodeDB) LastByteCollision(num uint32) uint32 {
	db.mu.RLock()
	defer db.mu.RUnlock()
	for other := range db.nodes {
		if other != num && wire.LastByte(other) == wire.LastByte(num) {
			return other
		}
	}
	return 0
}

func (db *NodeDB) Snapshot() []NodeEntry {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make([]NodeEntry, 0, len(db.nodes))
	for _, e := range db.nodes {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastHeard.After(out[j].LastHeard) })
	return out
}

// NodeInfo converts an entry to what the client API sends.
func (e NodeEntry) NodeInfo() *pb.NodeInfo {
	ni := &pb.NodeInfo{
		Num:        e.Num,
		User:       e.User,
		Position:   e.Position,
		Snr:        e.SNR,
		LastHeard:  uint32(e.LastHeard.Unix()),
		ViaMqtt:    e.ViaMQTT,
		Channel:    e.Channel,
		IsFavorite: e.Favorite,
		IsIgnored:  e.Ignored,
	}
	if e.LastHeard.IsZero() {
		ni.LastHeard = 0
	}
	if e.Metrics != nil {
		ni.DeviceMetrics = e.Metrics
	}
	if e.HopsAway >= 0 {
		h := uint32(e.HopsAway)
		ni.HopsAway = &h
	}
	return ni
}

// ---------------------------------------------------------------------------------- persistence

type nodeRecord struct {
	Num       uint32  `json:"num"`
	User      []byte  `json:"user,omitempty"`
	Position  []byte  `json:"position,omitempty"`
	Metrics   []byte  `json:"metrics,omitempty"`
	LastHeard int64   `json:"last_heard"`
	SNR       float32 `json:"snr"`
	RSSI      int32   `json:"rssi"`
	HopsAway  int     `json:"hops_away"`
	ViaMQTT   bool    `json:"via_mqtt,omitempty"`
	NextHop   uint8   `json:"next_hop,omitempty"`
	Favorite  bool    `json:"favorite,omitempty"`
	Ignored   bool    `json:"ignored,omitempty"`
	Channel   uint32  `json:"channel,omitempty"`
}

func marshalOpt(m proto.Message) []byte {
	if m == nil || !m.ProtoReflect().IsValid() {
		return nil
	}
	b, _ := proto.Marshal(m)
	return b
}

// Save writes the DB atomically when it changed.
func (db *NodeDB) Save(path string) error {
	db.mu.Lock()
	if !db.dirty {
		db.mu.Unlock()
		return nil
	}
	recs := make([]nodeRecord, 0, len(db.nodes))
	for _, e := range db.nodes {
		if e.Local {
			continue
		}
		recs = append(recs, nodeRecord{
			Num: e.Num, User: marshalOpt(e.User), Position: marshalOpt(e.Position), Metrics: marshalOpt(e.Metrics),
			LastHeard: e.LastHeard.UnixMilli(), SNR: e.SNR, RSSI: e.RSSI, HopsAway: e.HopsAway, ViaMQTT: e.ViaMQTT,
			NextHop: e.NextHop, Favorite: e.Favorite, Ignored: e.Ignored, Channel: e.Channel,
		})
	}
	db.dirty = false
	db.mu.Unlock()
	return writeJSONAtomic(path, recs)
}

func (db *NodeDB) Load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var recs []nodeRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, r := range recs {
		e := &NodeEntry{Num: r.Num, SNR: r.SNR, RSSI: r.RSSI, HopsAway: r.HopsAway, ViaMQTT: r.ViaMQTT,
			NextHop: r.NextHop, Favorite: r.Favorite, Ignored: r.Ignored, Channel: r.Channel}
		if r.LastHeard > 0 {
			e.LastHeard = time.UnixMilli(r.LastHeard)
		}
		if len(r.User) > 0 {
			e.User = &pb.User{}
			_ = proto.Unmarshal(r.User, e.User)
		}
		if len(r.Position) > 0 {
			e.Position = &pb.Position{}
			_ = proto.Unmarshal(r.Position, e.Position)
		}
		if len(r.Metrics) > 0 {
			e.Metrics = &pb.DeviceMetrics{}
			_ = proto.Unmarshal(r.Metrics, e.Metrics)
		}
		db.nodes[r.Num] = e
	}
	return nil
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
