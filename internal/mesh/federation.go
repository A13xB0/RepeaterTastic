package mesh

import (
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Experimental: identities on several radios.
//
// A Federation joins a site's hosts. An identity lives on its home host (key, app port, chats).
// Each of its channel slots is on exactly one radio, and it has a default radio: slot 0 is that
// radio's primary channel and new slots start on it. Other radios an identity's slots use treat it
// as a guest: they decode those slots and DMs addressed to it, deliver into the home host's message
// store, and transmit for it. Routing is set in the web GUI; the Meshtastic app never changes it.
// With the federation disabled, guests vanish and every radio behaves as before.

// DM routing values (MultiRadio.DM). Anything else names a radio.
const (
	DMAuto    = "auto"    // the radio where the destination was last heard best, else the default radio
	DMDefault = "default" // always the default radio
	dmHome    = "home"    // phase 3 name for the default radio
)

const (
	sightingMaxAge   = 24 * time.Hour
	fedDeliveryTTL   = 10 * time.Minute
	fedFallbackTTL   = 30 * time.Minute
	guestFirstAnnDly = 90 * time.Second
)

// MultiRadio is an identity's routing across radios.
type MultiRadio struct {
	// DefaultRadio is where slot 0 lives, new slots start, and DMs fall back ("" = home).
	DefaultRadio string `json:"default_radio,omitempty"`
	// Channels maps slot index (1-7) to the one radio that slot is on; missing = the default radio.
	Channels map[int]string `json:"channels,omitempty"`
	DM       string         `json:"dm,omitempty"`
	Fallback bool           `json:"fallback,omitempty"`

	// Phase 3 routing, converted by convertLegacy and never written back.
	Radios []string         `json:"radios,omitempty"`
	Listen map[int][]string `json:"listen,omitempty"`
	Send   map[int]string   `json:"send,omitempty"`
}

func (m *MultiRadio) clone() *MultiRadio {
	if m == nil {
		return nil
	}
	c := &MultiRadio{DefaultRadio: m.DefaultRadio, DM: m.DM, Fallback: m.Fallback, Radios: slices.Clone(m.Radios)}
	if m.Channels != nil {
		c.Channels = map[int]string{}
		for k, v := range m.Channels {
			c.Channels[k] = v
		}
	}
	if m.Listen != nil {
		c.Listen = map[int][]string{}
		for k, v := range m.Listen {
			c.Listen[k] = slices.Clone(v)
		}
	}
	if m.Send != nil {
		c.Send = map[int]string{}
		for k, v := range m.Send {
			c.Send[k] = v
		}
	}
	return c
}

// Default is the default radio's id.
func (m *MultiRadio) Default(home string) string {
	if m.DefaultRadio != "" {
		return m.DefaultRadio
	}
	return home
}

// SlotRadio is the radio a slot is configured on (slot 0 follows the default radio).
func (m *MultiRadio) SlotRadio(index int, home string) string {
	if index > 0 {
		if r := m.Channels[index]; r != "" {
			return r
		}
	}
	return m.Default(home)
}

// convertLegacy turns phase 3 listen/send routing into one radio per slot: a slot keeps the radio it
// sent on (or the first it listened on), and each further listening radio gets a copy of the
// channel in a free slot when there is one. It reports how many listening radios had no room.
func (id *Identity) convertLegacy() int {
	mr := id.multiRadio
	if mr == nil || (mr.Radios == nil && mr.Listen == nil && mr.Send == nil) {
		return 0
	}
	if mr.Channels == nil {
		mr.Channels = map[int]string{}
	}
	lost := 0
	for idx := 1; idx < MaxChannels; idx++ {
		ch := id.Channels[idx]
		if ch == nil || ch.Role == pb.Channel_DISABLED {
			continue
		}
		send := mr.Send[idx]
		listen := mr.Listen[idx]
		chosen := ""
		switch {
		case send != "" && send != "all":
			chosen = send
		case len(listen) > 0:
			chosen = listen[0]
		}
		if chosen != "" {
			mr.Channels[idx] = chosen
		}
		for _, r := range listen {
			if r == chosen || chosen == "" {
				continue
			}
			free := -1
			for k := 1; k < MaxChannels; k++ {
				if c := id.Channels[k]; c == nil || c.Role == pb.Channel_DISABLED {
					free = k
					break
				}
			}
			if free < 0 {
				lost++
				continue
			}
			cp := proto.Clone(ch).(*pb.Channel)
			cp.Index = int32(free)
			id.Channels[free] = cp
			mr.Channels[free] = r
		}
	}
	if mr.DM == dmHome {
		mr.DM = DMDefault
	}
	mr.Radios, mr.Listen, mr.Send = nil, nil, nil
	return lost
}

// MultiRadio returns a copy of the identity's multi-radio routing, or nil.
func (id *Identity) MultiRadio() *MultiRadio {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.multiRadio.clone()
}

// SetMultiRadio replaces the identity's routing (nil = everything on the home radio). Call
// Federation.Changed afterwards so every radio rebuilds its channel tables.
func (id *Identity) SetMultiRadio(m *MultiRadio) {
	id.mu.Lock()
	id.multiRadio = m.clone()
	id.mu.Unlock()
}

// SetSlotRadio puts a slot on a radio ("" = the default radio).
func (id *Identity) SetSlotRadio(index int, radio string) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if id.multiRadio == nil {
		id.multiRadio = &MultiRadio{}
	}
	mr := id.multiRadio
	if radio == "" {
		delete(mr.Channels, index)
		return
	}
	if mr.Channels == nil {
		mr.Channels = map[int]string{}
	}
	mr.Channels[index] = radio
}

type fedKey struct{ identity, from, id uint32 }

// Federation is a site's hosts, joined for multi-radio identities.
type Federation struct {
	enabled atomic.Bool
	hosts   []*Host
	gen     atomic.Uint64 // bumped whenever identities, channels or routing change (guest caches)

	mu        sync.Mutex
	delivered map[fedKey]time.Time
	fallbacks map[pktKey]time.Time
}

// NewFederation joins hosts. It starts disabled.
func NewFederation(hosts ...*Host) *Federation {
	f := &Federation{hosts: hosts, delivered: map[fedKey]time.Time{}, fallbacks: map[pktKey]time.Time{}}
	for _, h := range hosts {
		h.fed = f
	}
	return f
}

func (f *Federation) Enabled() bool { return f != nil && f.enabled.Load() }

// SetEnabled turns multi-radio identities on or off at once on every radio.
func (f *Federation) SetEnabled(on bool) {
	if f == nil {
		return
	}
	f.enabled.Store(on)
	f.Changed()
}

// Changed must be called after any identity's MultiRadio changes.
func (f *Federation) Changed() {
	if f == nil {
		return
	}
	f.gen.Add(1)
	for _, h := range f.hosts {
		h.ChannelsChanged()
	}
}

// Host returns the host for a radio id, or nil.
func (f *Federation) Host(radio string) *Host {
	if f == nil {
		return nil
	}
	for _, h := range f.hosts {
		if h.RadioID() == radio {
			return h
		}
	}
	return nil
}

func (f *Federation) homeOf(num uint32) *Host {
	for _, h := range f.hosts {
		if h.Identity(num) != nil {
			return h
		}
	}
	return nil
}

// ------------------------------------------------------------------------------ host side

func (h *Host) fedOn() bool { return h.fed.Enabled() }

// multiRadioOf is the identity's routing when the federation is on, else nil.
func (h *Host) multiRadioOf(id *Identity) *MultiRadio {
	if !h.fedOn() || id == nil || id.IsRelay {
		return nil
	}
	return id.MultiRadio()
}

// guests are identities from other radios that have their default radio or a slot on this one.
func (h *Host) guests() []*Identity {
	if !h.fedOn() {
		return nil
	}
	// Cached: this runs several times per received packet, and walking every identity's slots on
	// every other radio each time is wasteful. Any change bumps fed.gen.
	gen := h.fed.gen.Load()
	h.guestMu.Lock()
	if h.guestOK && h.guestGen == gen {
		out := h.guestList
		h.guestMu.Unlock()
		return out
	}
	h.guestMu.Unlock()
	out := h.computeGuests()
	h.guestMu.Lock()
	h.guestList, h.guestGen, h.guestOK = out, gen, true
	h.guestMu.Unlock()
	return out
}

func (h *Host) computeGuests() []*Identity {
	var out []*Identity
	for _, o := range h.fed.hosts {
		if o == h {
			continue
		}
		for _, id := range o.Identities() {
			if h.multiRadioOf(id) == nil {
				continue
			}
			for _, r := range o.radiosOf(id) {
				if r == h {
					out = append(out, id)
					break
				}
			}
		}
	}
	return out
}

// hostOr is the host for a radio id, or fallback when that radio isn't on the site.
func (h *Host) hostOr(radio string, fallback *Host) *Host {
	if o := h.fed.Host(radio); o != nil {
		return o
	}
	return fallback
}

// defaultHost is an identity's default radio (home if unset or gone).
func (h *Host) defaultHost(id *Identity, mr *MultiRadio) *Host {
	home := h.homeHost(id)
	return h.hostOr(mr.Default(home.RadioID()), home)
}

// slotHost is the radio a slot actually runs on: its radio, else the default radio, else home.
func (h *Host) slotHost(id *Identity, mr *MultiRadio, index int) *Host {
	def := h.defaultHost(id, mr)
	if index == 0 {
		return def
	}
	return h.hostOr(mr.SlotRadio(index, def.RadioID()), def)
}

// radiosOf lists the radios an identity is on: home, default, then each radio an enabled slot uses.
func (h *Host) radiosOf(id *Identity) []*Host {
	home := h.homeHost(id)
	mr := h.multiRadioOf(id)
	if mr == nil {
		return []*Host{home}
	}
	out := []*Host{home}
	add := func(o *Host) {
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	add(h.defaultHost(id, mr))
	for idx := 1; idx < MaxChannels; idx++ {
		if ch := id.ChannelCopy(idx); ch != nil && ch.Role != pb.Channel_DISABLED {
			add(h.slotHost(id, mr, idx))
		}
	}
	return out
}

// RadiosOf is radiosOf as radio ids, for the API.
func (h *Host) RadiosOf(id *Identity) []string {
	var out []string
	for _, o := range h.radiosOf(id) {
		out = append(out, o.RadioID())
	}
	return out
}

// SlotRadio is the radio id a slot actually runs on ("" when the identity has no routing or the
// federation is off, meaning its home radio).
func (h *Host) SlotRadio(id *Identity, index int) string {
	mr := h.multiRadioOf(id)
	if mr == nil {
		return ""
	}
	return h.slotHost(id, mr, index).RadioID()
}

// slotOnThisRadio reports whether a slot of a (local or guest) identity is decoded on this host.
func (h *Host) slotOnThisRadio(id *Identity, index int) bool {
	mr := h.multiRadioOf(id)
	if mr == nil {
		return h.Identity(id.NodeNum) == id
	}
	return h.slotHost(id, mr, index) == h
}

// identityAny finds one of this radio's identities or a guest.
func (h *Host) identityAny(num uint32) *Identity {
	if id := h.Identity(num); id != nil {
		return id
	}
	for _, g := range h.guests() {
		if g.NodeNum == num {
			return g
		}
	}
	return nil
}

// isSiteIdentity reports whether num is an identity on any radio of the site (with the federation
// on), so no relay persona rebroadcasts our own identities' packets heard on another radio.
func (h *Host) isSiteIdentity(num uint32) bool {
	if h.Identity(num) != nil {
		return true
	}
	return h.fedOn() && h.fed.homeOf(num) != nil
}

// SiteIdentity reports whether num is an identity on any radio of this site, whether or not
// multi-radio identities are switched on (e.g. to ignore our own packets coming back from MQTT).
func (h *Host) SiteIdentity(num uint32) bool {
	if h.Identity(num) != nil {
		return true
	}
	return h.fed != nil && h.fed.homeOf(num) != nil
}

// homeHost is the host an identity lives on.
func (h *Host) homeHost(id *Identity) *Host {
	if h.Identity(id.NodeNum) == id || h.fed == nil {
		return h
	}
	if o := h.fed.homeOf(id.NodeNum); o != nil {
		return o
	}
	return h
}

// storeFor is the message store holding an identity's chats (its home host's).
func (h *Host) storeFor(id *Identity) *MessageStore { return h.homeHost(id).Messages }

// publishMessage announces a new or updated message on the identity's home bus, where its
// chat views listen.
func (h *Host) publishMessage(id *Identity, m Message) {
	h.homeHost(id).Bus.Publish(Event{Type: "message", Data: MessageEvent{Identity: id.NodeID(), Message: m}})
}

// firstDelivery reports whether a packet should be delivered to a multi-radio identity: the same
// packet heard on two of its radios reaches it once.
func (h *Host) firstDelivery(id *Identity, p *pb.MeshPacket) bool {
	if h.multiRadioOf(id) == nil {
		return true
	}
	f := h.fed
	now := time.Now()
	k := fedKey{id.NodeNum, p.From, p.Id}
	f.mu.Lock()
	defer f.mu.Unlock()
	if at, ok := f.delivered[k]; ok && now.Sub(at) < fedDeliveryTTL {
		return false
	}
	f.delivered[k] = now
	if len(f.delivered) > 4096 {
		for key, at := range f.delivered {
			if now.Sub(at) > fedDeliveryTTL {
				delete(f.delivered, key)
			}
		}
	}
	return true
}

// Sighting is what one radio knows about a node.
type Sighting struct {
	Radio     string
	LastHeard time.Time
	SNR       float32
	RSSI      int32
	HopsAway  int
	ViaMQTT   bool
}

// bestSighting picks the host whose sighting of dest is freshest over RF within a day, preferring
// fewer hops, then more recent, then better SNR.
func bestSighting(hosts []*Host, dest uint32, now time.Time) (*Host, *NodeEntry) {
	type cand struct {
		h *Host
		e NodeEntry
	}
	var cs []cand
	for _, o := range hosts {
		e, ok := o.DB.Get(dest)
		if !ok || e.Local || e.ViaMQTT || e.LastHeard.IsZero() || now.Sub(e.LastHeard) > sightingMaxAge {
			continue
		}
		cs = append(cs, cand{o, e})
	}
	if len(cs) == 0 {
		return nil, nil
	}
	hops := func(e NodeEntry) int {
		if e.HopsAway < 0 {
			return 8
		}
		return e.HopsAway
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i].e, cs[j].e
		if hops(a) != hops(b) {
			return hops(a) < hops(b)
		}
		if !a.LastHeard.Equal(b.LastHeard) {
			return a.LastHeard.After(b.LastHeard)
		}
		return a.SNR > b.SNR
	})
	return cs[0].h, &cs[0].e
}

// sendHosts decides which radio a packet an identity originates goes out on (nil = this host)
// and why.
func (h *Host) sendHosts(from *Identity, to uint32, channel int) ([]*Host, string) {
	mr := h.multiRadioOf(from)
	if mr == nil {
		return nil, ""
	}
	def := h.defaultHost(from, mr)
	if to == wire.Broadcast || to == wire.BroadcastNoLoRa {
		if channel < 0 || channel >= MaxChannels {
			channel = 0
		}
		o := h.slotHost(from, mr, channel)
		return []*Host{o}, "channel is on " + o.RadioID()
	}
	switch mr.DM {
	case DMDefault, dmHome:
		return []*Host{def}, "DMs always use the default radio"
	case "", DMAuto:
		now := time.Now()
		if best, e := bestSighting(h.radiosOf(from), to, now); best != nil {
			return []*Host{best}, fmt.Sprintf("heard %s ago on %s, %s, SNR %.1f", roundAgo(now.Sub(e.LastHeard)),
				best.RadioID(), hopsText(e.HopsAway), e.SNR)
		}
		return []*Host{def}, "not heard on any of its radios in the last 24 h: default radio"
	default:
		if o := h.fed.Host(mr.DM); o != nil {
			return []*Host{o}, "DMs always use " + mr.DM
		}
		return []*Host{def}, "DM radio " + mr.DM + " isn't on the site: default radio"
	}
}

// RoutePreview says which radios a message from an identity would use and why.
func (h *Host) RoutePreview(from *Identity, to uint32, channel int) ([]string, string) {
	hosts, reason := h.sendHosts(from, to, channel)
	if hosts == nil {
		return []string{h.homeHost(from).RadioID()}, "single radio"
	}
	ids := make([]string, 0, len(hosts))
	for _, o := range hosts {
		ids = append(ids, o.RadioID())
	}
	return ids, reason
}

// Sightings lists what every radio of the site knows about a node, freshest first.
func (h *Host) Sightings(num uint32) []Sighting {
	hosts := []*Host{h}
	if h.fed != nil {
		hosts = h.fed.hosts
	}
	var out []Sighting
	for _, o := range hosts {
		if e, ok := o.DB.Get(num); ok && !e.LastHeard.IsZero() {
			out = append(out, Sighting{Radio: o.RadioID(), LastHeard: e.LastHeard, SNR: e.SNR, RSSI: e.RSSI, HopsAway: e.HopsAway, ViaMQTT: e.ViaMQTT})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastHeard.After(out[j].LastHeard) })
	return out
}

// NodesFor is the node list an identity's app sees: with multi-radio routing, every attached
// radio's nodes merged (the most recent sighting wins).
func (h *Host) NodesFor(id *Identity) []NodeEntry {
	mr := h.multiRadioOf(id)
	if mr == nil {
		return h.DB.Snapshot()
	}
	best := map[uint32]NodeEntry{}
	for _, o := range h.radiosOf(id) {
		for _, e := range o.DB.Snapshot() {
			if cur, ok := best[e.Num]; !ok || e.LastHeard.After(cur.LastHeard) {
				best[e.Num] = e
			}
		}
	}
	out := make([]NodeEntry, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastHeard.After(out[j].LastHeard) })
	return out
}

// stopPendingEverywhere clears a pending retransmission on whichever radio sent it (an ACK can
// arrive on another radio of the site).
func (h *Host) stopPendingEverywhere(k pktKey) {
	h.stopPending(k)
	if !h.fedOn() {
		return
	}
	for _, o := range h.fed.hosts {
		if o != h {
			o.stopPending(k)
		}
	}
}

// tryFallback gives a DM that exhausted its retries one more attempt on another radio where the
// destination was heard recently. It returns true when the message went to another radio.
func (h *Host) tryFallback(pd *pendingTx, k pktKey) bool {
	from := pd.origin
	mr := h.multiRadioOf(from)
	if mr == nil || !mr.Fallback || pd.broadcast || !pd.text {
		return false
	}
	now := time.Now()
	var alts []*Host
	for _, o := range h.radiosOf(from) {
		if o == h || !o.radioOK.Load() {
			continue
		}
		if limit := o.dutyLimit(); limit < 100 && o.Air.TxPercent(now) >= limit {
			continue
		}
		alts = append(alts, o)
	}
	alt, _ := bestSighting(alts, pd.pkt.To, now)
	if alt == nil {
		return false
	}
	f := h.fed
	f.mu.Lock()
	if at, done := f.fallbacks[k]; done && now.Sub(at) < fedFallbackTTL {
		f.mu.Unlock()
		return false
	}
	f.fallbacks[k] = now
	for key, at := range f.fallbacks {
		if now.Sub(at) > fedFallbackTTL {
			delete(f.fallbacks, key)
		}
	}
	f.mu.Unlock()
	p := &pb.MeshPacket{From: from.NodeNum, To: pd.pkt.To, Id: k.ID, Channel: uint32(pd.index), WantAck: true,
		HopLimit: alt.Config().HopLimit, PkiEncrypted: pd.pkt.PkiEncrypted,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: proto.Clone(pd.plain).(*pb.Data)}}
	if err := alt.transmit(from, p, true); err != nil {
		return false
	}
	h.log.Info("DM retried on another radio", "identity", from.NodeID(), "to", wire.NodeID(p.To), "radio", alt.RadioID())
	return true
}

// periodicGuests announces guests on this radio: NodeInfo and position on their normal
// intervals, with timers of this radio's own so the home radio's schedule isn't disturbed.
func (h *Host) periodicGuests(now time.Time) {
	guests := h.guests()
	if len(guests) == 0 {
		h.guestTimers = nil
		return
	}
	if h.guestTimers == nil {
		h.guestTimers = map[uint32]*guestTimer{}
	}
	cfg := h.Config()
	live := map[uint32]bool{}
	for _, g := range guests {
		live[g.NodeNum] = true
		t := h.guestTimers[g.NodeNum]
		if t == nil {
			stagger := time.Duration(g.NodeNum%120) * time.Second
			t = &guestTimer{nodeInfo: now.Add(guestFirstAnnDly + stagger), position: now.Add(2*guestFirstAnnDly + stagger)}
			h.guestTimers[g.NodeNum] = t
		}
		if !g.Enabled || !h.radioOK.Load() || h.Air.ChannelUtilPercent(now) > 40 {
			continue
		}
		if limit := h.dutyLimit(); limit < 100 && h.Air.TxPercent(now) > limit/2 {
			continue
		}
		if !now.Before(t.nodeInfo) {
			t.nodeInfo = now.Add(cfg.NodeInfoInterval)
			h.broadcastNodeInfo(g)
		}
		if pos, ok := h.positionFor(g); ok && !now.Before(t.position) {
			t.position = now.Add(pos.interval())
			h.sendPosition(g, pos, wire.Broadcast, 0, 0)
		}
	}
	for num := range h.guestTimers {
		if !live[num] {
			delete(h.guestTimers, num)
		}
	}
}

type guestTimer struct{ nodeInfo, position time.Time }

// broadcastNodeInfo sends an identity's NodeInfo on this radio without the per-identity rate
// limit, which the home radio's own broadcasts use.
func (h *Host) broadcastNodeInfo(id *Identity) {
	u := id.UserCopy()
	u.HwModel = h.Hardware()
	payload, _ := proto.Marshal(u)
	p := &pb.MeshPacket{To: wire.Broadcast, Priority: pb.MeshPacket_BACKGROUND,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: payload}}}
	p.From = id.NodeNum
	p.Id = wire.RandomPacketID()
	p.HopLimit = h.Config().HopLimit
	if err := h.transmit(id, p, false); err != nil {
		h.log.Debug("guest nodeinfo not sent", "identity", id.NodeID(), "err", err)
	}
}

func roundAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1f h", d.Hours())
}

func hopsText(h int) string {
	switch {
	case h < 0:
		return "hops unknown"
	case h == 0:
		return "direct"
	case h == 1:
		return "1 hop"
	}
	return fmt.Sprintf("%d hops", h)
}
