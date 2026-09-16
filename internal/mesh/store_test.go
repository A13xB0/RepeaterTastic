package mesh

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// ---------------------------------------------------------------------------------- node DB

func TestNodeDBLastByte(t *testing.T) {
	db := NewNodeDB()
	db.Update(0x100000aa, func(e *NodeEntry) { e.HopsAway = 0 })
	db.Update(0x200000aa, func(e *NodeEntry) { e.HopsAway = 2 })
	db.Update(0x300000bb, func(e *NodeEntry) { e.Local = true })

	if _, ok := db.ResolveLastByte(0xaa, false); ok {
		t.Fatal("ambiguous last byte resolved")
	}
	if n, ok := db.ResolveLastByte(0xaa, true); !ok || n != 0x100000aa {
		t.Fatalf("direct neighbour = %08x, %v", n, ok)
	}
	if n, ok := db.ResolveLastByte(0xbb, true); !ok || n != 0x300000bb {
		t.Fatalf("local node = %08x, %v", n, ok)
	}
	if _, ok := db.ResolveLastByte(0xcc, false); ok {
		t.Fatal("unknown byte resolved")
	}
	if c := db.LastByteCollision(0x100000aa); c != 0x200000aa {
		t.Fatalf("collision = %08x", c)
	}
	if c := db.LastByteCollision(0x300000bb); c != 0 {
		t.Fatalf("collision for a unique byte = %08x", c)
	}
	db.Delete(0x200000aa)
	if _, ok := db.Get(0x200000aa); ok {
		t.Fatal("deleted node still there")
	}
}

func TestNodeDBSnapshotAndNodeInfo(t *testing.T) {
	db := NewNodeDB()
	db.Update(1000, func(e *NodeEntry) { e.LastHeard = t0 })
	db.Update(2000, func(e *NodeEntry) {
		e.LastHeard = t0.Add(time.Minute)
		e.HopsAway = 2
		e.Metrics = &pb.DeviceMetrics{BatteryLevel: proto.Uint32(80)}
		e.Favorite = true
	})
	db.Update(3000, func(*NodeEntry) {})
	snap := db.Snapshot()
	if len(snap) != 3 || snap[0].Num != 2000 || snap[1].Num != 1000 {
		t.Fatalf("snapshot order = %+v", snap)
	}
	ni := snap[0].NodeInfo()
	if ni.GetHopsAway() != 2 || ni.HopsAway == nil || ni.GetDeviceMetrics().GetBatteryLevel() != 80 ||
		!ni.IsFavorite || ni.LastHeard != uint32(t0.Add(time.Minute).Unix()) {
		t.Fatalf("node info = %v", ni)
	}
	unknown := snap[2].NodeInfo()
	if unknown.LastHeard != 0 || unknown.HopsAway != nil || unknown.DeviceMetrics != nil {
		t.Fatalf("never-heard node info = %v", unknown)
	}
	var nilEntry *NodeEntry
	if nilEntry.PublicKey() != nil || (&NodeEntry{User: &pb.User{PublicKey: []byte{1}}}).PublicKey() != nil {
		t.Fatal("public key from an entry without a valid key")
	}
}

func TestNodeDBSetUserKeepsKey(t *testing.T) {
	db := NewNodeDB()
	key := make([]byte, 32)
	key[0] = 1
	if !db.SetUser(5, &pb.User{LongName: "A", PublicKey: key}) {
		t.Fatal("new user not reported changed")
	}
	other := make([]byte, 32)
	other[0] = 2
	db.SetUser(5, &pb.User{LongName: "B", PublicKey: other})
	e, _ := db.Get(5)
	if string(e.PublicKey()) != string(key) || e.User.LongName != "B" || e.User.Id != wire.NodeID(5) {
		t.Fatalf("user after spoof = %v", e.User)
	}
	db.SetUser(5, &pb.User{LongName: "B"})
	if e, _ := db.Get(5); string(e.PublicKey()) != string(key) {
		t.Fatal("key lost by a NodeInfo without one")
	}
	if db.SetUser(5, &pb.User{LongName: "B"}) {
		t.Fatal("unchanged user reported changed")
	}
}

func TestNodeDBUpdateFromPacket(t *testing.T) {
	db := NewNodeDB()
	db.UpdateFromPacket(&pb.MeshPacket{From: 0}, t0)
	if len(db.Snapshot()) != 0 {
		t.Fatal("packet without a sender added a node")
	}
	db.UpdateFromPacket(&pb.MeshPacket{From: 7, HopStart: 3, HopLimit: 3, RxSnr: 6, RxRssi: proto.Int32(-70),
		Channel: 2, TransportMechanism: pb.MeshPacket_TRANSPORT_LORA,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}, t0)
	e, _ := db.Get(7)
	if e.HopsAway != 0 || e.SNR != 6 || e.RSSI != -70 || e.Channel != 2 || !e.LastHeard.Equal(t0) {
		t.Fatalf("entry = %+v", e)
	}
	db.UpdateFromPacket(&pb.MeshPacket{From: 7, HopStart: 3, HopLimit: 1, RxSnr: -3, ViaMqtt: true,
		PayloadVariant: &pb.MeshPacket_Encrypted{}}, t0)
	if e, _ := db.Get(7); e.HopsAway != 2 || e.SNR != 6 || !e.ViaMQTT {
		t.Fatalf("entry after relayed copy = %+v", e)
	}
}

func TestNodeDBSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", nodeDBFile)
	db := NewNodeDB()
	if err := db.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clean DB written")
	}
	db.SetUser(10, &pb.User{LongName: "Ten"})
	db.Update(10, func(e *NodeEntry) {
		e.LastHeard = t0
		e.Position = &pb.Position{LatitudeI: proto.Int32(5)}
		e.Metrics = &pb.DeviceMetrics{Voltage: proto.Float32(3.7)}
		e.HopsAway, e.NextHop, e.Ignored, e.Channel = 1, 0x22, true, 3
	})
	db.Update(11, func(e *NodeEntry) { e.Local = true })
	if err := db.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded := NewNodeDB()
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	e, ok := loaded.Get(10)
	if !ok || e.User.GetLongName() != "Ten" || e.Position.GetLatitudeI() != 5 || e.Metrics.GetVoltage() != 3.7 ||
		!e.LastHeard.Equal(t0) || e.HopsAway != 1 || e.NextHop != 0x22 || !e.Ignored || e.Channel != 3 {
		t.Fatalf("loaded entry = %+v", e)
	}
	if _, ok := loaded.Get(11); ok {
		t.Fatal("local entry saved")
	}
}

func TestNodeDBLoadErrors(t *testing.T) {
	dir := t.TempDir()
	db := NewNodeDB()
	if err := db.Load(filepath.Join(dir, "missing.json")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if err := db.Load(dir); err == nil {
		t.Fatal("loaded a directory")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Load(bad); err == nil {
		t.Fatal("loaded bad JSON")
	}
	// A path under a regular file can't be created.
	db.Update(1, func(*NodeEntry) {})
	if err := db.Save(filepath.Join(bad, "x.json")); err == nil {
		t.Fatal("saved under a file")
	}
	if err := writeJSONAtomic(filepath.Join(dir, "x.json"), func() {}); err == nil {
		t.Fatal("marshalled a func")
	}
	if err := writeJSONAtomic(dir, 1); err == nil {
		t.Fatal("renamed over a directory")
	}
	if marshalOpt((*pb.User)(nil)) != nil || marshalOpt(nil) != nil {
		t.Fatal("nil message marshalled")
	}
}

// ------------------------------------------------------------------------------- bus and log

func TestBus(t *testing.T) {
	b := NewBus()
	ch, stop := b.Subscribe(1)
	b.Publish(Event{Type: "a"})
	b.Publish(Event{Type: "b"}) // buffer full: dropped, not blocking
	if e := <-ch; e.Type != "a" {
		t.Fatalf("event = %+v", e)
	}
	stop()
	stop() // idempotent
	if _, open := <-ch; open {
		t.Fatal("channel open after unsubscribe")
	}
	b.Publish(Event{Type: "c"}) // no subscribers left
}

func TestPacketLog(t *testing.T) {
	l := NewPacketLog(3)
	for i := 1; i <= 5; i++ {
		r := l.Add(PacketRecord{ID: uint32(i), Time: int64(i * 100)})
		if r.Seq != uint64(i) {
			t.Fatalf("seq = %d", r.Seq)
		}
	}
	if r := l.Add(PacketRecord{ID: 6}); r.Time == 0 {
		t.Fatal("zero time not stamped")
	}
	ids := func(rs []PacketRecord) (out []uint32) {
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids(l.List(10, 0, nil)); len(got) != 3 || got[0] != 6 || got[2] != 4 {
		t.Fatalf("list = %v", got)
	}
	if got := ids(l.List(10, 500, nil)); len(got) != 1 || got[0] != 4 {
		t.Fatalf("before 500 = %v", got)
	}
	even := func(r *PacketRecord) bool { return r.ID%2 == 0 }
	if got := ids(l.List(1, 0, even)); len(got) != 1 || got[0] != 6 {
		t.Fatalf("filtered = %v", got)
	}
}

// ----------------------------------------------------------------------------- message store

func TestConversationKeys(t *testing.T) {
	for _, c := range []struct {
		m    Message
		want string
	}{
		{Message{To: "!ffffffff", Channel: 3}, "ch:3"},
		{Message{To: "!ffffffff", Channel: 0}, "ch:0"},
		{Message{To: "!ffffffff", Channel: -12}, "ch:-12"},
		{Message{From: "!me", To: "!you"}, "dm:!you"},
		{Message{From: "!you", To: "!me"}, "dm:!you"},
	} {
		if got := c.m.Conversation("!me"); got != c.want {
			t.Errorf("%+v: %q, want %q", c.m, got, c.want)
		}
	}
	if itoa(1234567) != "1234567" {
		t.Fatal("itoa")
	}
}

func TestMessageStoreStatus(t *testing.T) {
	s := NewMessageStore(2)
	for i := uint32(1); i <= 3; i++ {
		s.Add(1, &Message{ID: i, Direction: "out", Status: "queued", To: "!ffffffff", Time: int64(i)})
	}
	if l := s.List(1, "!me", "", 0, 10); len(l) != 2 || l[0].ID != 2 || l[1].ID != 3 {
		t.Fatalf("trimmed list = %+v", l)
	}
	if _, ok := s.SetStatus(1, 9, "sent", ""); ok {
		t.Fatal("status set on an unknown message")
	}
	if m, ok := s.SetStatus(1, 3, "acked", ""); !ok || m.Status != "acked" {
		t.Fatalf("ack = %+v, %v", m, ok)
	}
	if m, ok := s.SetStatus(1, 3, "failed", "late NAK"); ok || m.Status != "acked" {
		t.Fatalf("acked message downgraded: %+v", m)
	}
	if l := s.List(1, "!me", "", 3, 10); len(l) != 1 || l[0].ID != 2 {
		t.Fatalf("before 3 = %+v", l)
	}
	if w := s.Window(1, 3); len(w) != 1 || w[0].ID != 3 {
		t.Fatalf("window = %+v", w)
	}
}

func TestMessageStoreUnread(t *testing.T) {
	s := NewMessageStore(10)
	future := time.Now().Add(time.Hour).UnixMilli() // newer than any read mark set below
	s.Add(1, &Message{ID: 1, From: "!bob", To: "!me", Direction: "in", Text: "hi", Time: future})
	s.Add(1, &Message{ID: 2, From: "!bob", To: "!me", Direction: "in", Text: "there", Time: future})
	s.Add(1, &Message{ID: 3, From: "!me", To: "!ffffffff", Direction: "out", Text: "all"})
	s.Add(1, &Message{ID: 4, From: "!sue", To: "!ffffffff", Direction: "in", Text: "old", Time: 1})

	cs := s.Conversations(1, "!me")
	if len(cs) != 2 || cs[0].Key != "dm:!bob" || cs[0].Unread != 2 || cs[0].LastText != "there" || cs[1].Unread != 1 {
		t.Fatalf("conversations = %+v", cs)
	}
	if n := s.UnreadTotal(1, "!me"); n != 3 {
		t.Fatalf("unread = %d", n)
	}
	if l := s.List(1, "!me", "ch:0", 0, 10); len(l) != 2 {
		t.Fatalf("channel list = %+v", l)
	}
	if n := s.UnreadTotal(1, "!me"); n != 2 {
		t.Fatalf("unread after reading the channel = %d", n)
	}
	s.MarkRead(2, "dm:!bob") // another identity, no marks yet
	if n := s.UnreadTotal(1, "!me"); n != 2 {
		t.Fatalf("other identity's mark counted: %d", n)
	}
}

func TestMessageStoreTakeAndPut(t *testing.T) {
	s := NewMessageStore(2)
	s.Add(1, &Message{ID: 1})
	s.MarkRead(1, "ch:0")
	msgs, read := s.Take(1)
	if len(msgs) != 1 || read["ch:0"] == 0 || len(s.List(1, "", "", 0, 10)) != 0 {
		t.Fatal("take didn't move the messages out")
	}
	other := NewMessageStore(2)
	other.Add(5, &Message{ID: 9})
	other.Add(5, &Message{ID: 8})
	other.Put(5, msgs, read)
	if l := other.List(5, "", "", 0, 10); len(l) != 2 || l[1].ID != 1 {
		t.Fatalf("put list = %+v", l)
	}
	other.Put(6, nil, nil)
	if len(other.List(6, "", "", 0, 10)) != 0 {
		t.Fatal("empty put created messages")
	}
}

func TestMessageStoreSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), messagesFile)
	s := NewMessageStore(10)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clean store written")
	}
	s.Add(7, &Message{ID: 1, Direction: "out", Status: "queued"})
	s.Add(7, &Message{ID: 2, Direction: "out", Status: "acked"})
	s.Add(7, &Message{ID: 3, Direction: "in", Status: "received"})
	s.MarkRead(7, "ch:0")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded := NewMessageStore(10)
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	l := loaded.List(7, "", "", 0, 10)
	if len(l) != 3 || l[0].Status != "failed" || l[0].Error == "" || l[1].Status != "acked" || l[2].Status != "received" {
		t.Fatalf("loaded = %+v", l)
	}
	if loaded.read[7]["ch:0"] == 0 {
		t.Fatal("read marks not loaded")
	}
}

func TestMessageStoreLoadErrors(t *testing.T) {
	dir := t.TempDir()
	s := NewMessageStore(10)
	if err := s.Load(filepath.Join(dir, "missing.json")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if err := s.Load(dir); err == nil {
		t.Fatal("loaded a directory")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(bad); err == nil {
		t.Fatal("loaded bad JSON")
	}
	odd := filepath.Join(dir, "odd.json")
	body := `{"messages":{"x":[{"id":1}],"4":[{"id":2}]},"read":{"y":{"a":1},"4":{"ch:0":5}}}`
	if err := os.WriteFile(odd, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(odd); err != nil {
		t.Fatal(err)
	}
	if l := s.List(4, "", "", 0, 10); len(l) != 1 || l[0].ID != 2 || s.read[4]["ch:0"] != 5 || len(s.per) != 1 {
		t.Fatalf("odd keys not skipped: %+v", s.per)
	}
}
