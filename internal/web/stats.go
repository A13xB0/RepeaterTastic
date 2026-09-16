// The packet log and statistics.

package web

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

type rfPoint struct {
	Time          int64   `json:"time"`
	NoiseFloorDBm float64 `json:"noise_floor_dbm"`
	ChannelUtil   float64 `json:"channel_util_pct"`
	Rx            uint64  `json:"rx"`
	Tx            uint64  `json:"tx"`
}

// rfHistory samples noise floor and channel utilisation every minute for a week.
type rfHistory struct {
	mu     sync.Mutex
	points []rfPoint
}

const rfKeep = 7 * 24 * 60

func (s *Server) sampleRF(ctx context.Context, rc *radioCtx) { s.sampleRFEvery(ctx, rc, time.Minute) }

// sampleRFEvery is sampleRF with the sampling period as a parameter (tests sample faster).
func (s *Server) sampleRFEvery(ctx context.Context, rc *radioCtx, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var lastRx, lastTx uint64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			st := rc.stats(ctx)
			rx, tx := rc.host.Counters.Rx.Load(), rc.host.Counters.Tx.Load()
			p := rfPoint{Time: now.Truncate(time.Minute).UnixMilli(), NoiseFloorDBm: float64(st.NoiseFloorDBm),
				ChannelUtil: rc.host.Air.ChannelUtilPercent(now), Rx: rx - lastRx, Tx: tx - lastTx}
			lastRx, lastTx = rx, tx
			rc.rf.mu.Lock()
			rc.rf.points = append(rc.rf.points, p)
			if len(rc.rf.points) > rfKeep {
				rc.rf.points = rc.rf.points[len(rc.rf.points)-rfKeep:]
			}
			rc.rf.mu.Unlock()
		}
	}
}

func bucketFor(window time.Duration) time.Duration {
	switch {
	case window <= time.Hour:
		return time.Minute
	case window <= 24*time.Hour:
		return 10 * time.Minute
	default:
		return time.Hour
	}
}

func (s *Server) statsRF(w http.ResponseWriter, r *http.Request) {
	window := windowParam(r)
	bucket := bucketFor(window)
	cut := time.Now().Add(-window).UnixMilli()
	var src []rfPoint
	for _, rc := range s.radiosFor(r) {
		rc.rf.mu.Lock()
		src = append(src, rc.rf.points...)
		rc.rf.mu.Unlock()
	}
	sort.SliceStable(src, func(i, j int) bool { return src[i].Time < src[j].Time })
	type acc struct {
		p         rfPoint
		n, nNoise int
	}
	var order []int64
	agg := map[int64]*acc{}
	for _, p := range src {
		if p.Time < cut {
			continue
		}
		k := time.UnixMilli(p.Time).Truncate(bucket).UnixMilli()
		a, ok := agg[k]
		if !ok {
			a = &acc{p: rfPoint{Time: k}}
			agg[k] = a
			order = append(order, k)
		}
		a.n++
		a.p.ChannelUtil += p.ChannelUtil
		a.p.Rx += p.Rx
		a.p.Tx += p.Tx
		if p.NoiseFloorDBm != 0 {
			a.p.NoiseFloorDBm += p.NoiseFloorDBm
			a.nNoise++
		}
	}
	points := []rfPoint{}
	for _, k := range order {
		a := agg[k]
		a.p.ChannelUtil /= float64(a.n)
		if a.nNoise > 0 {
			a.p.NoiseFloorDBm /= float64(a.nNoise)
		}
		points = append(points, a.p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket_s": int(bucket.Seconds()), "points": points})
}

// identityStat is one identity's traffic over a window.
type identityStat struct {
	NodeID    string  `json:"node_id"`
	RadioID   string  `json:"radio_id"`
	Tx        int     `json:"tx"`
	Rx        int     `json:"rx"`
	AckOK     int     `json:"ack_ok"`
	AckFail   int     `json:"ack_fail"`
	AirtimeMs float64 `json:"airtime_ms"`
}

func (s *Server) statsIdentities(w http.ResponseWriter, r *http.Request) {
	window := windowParam(r)
	out := []*identityStat{}
	for _, rc := range s.radiosFor(r) {
		out = append(out, identityStats(rc, window)...)
	}
	writeJSON(w, http.StatusOK, out)
}

// identityStats counts each of a radio's identities' packets, acks and airtime.
func identityStats(rc *radioCtx, window time.Duration) []*identityStat {
	h := rc.host
	now := time.Now()
	cut := now.Add(-window).UnixMilli()
	airtime, relayMs := identityAirtime(h, now, window)
	stats := map[string]*identityStat{}
	var out []*identityStat
	for _, id := range h.Identities() {
		st := &identityStat{NodeID: id.NodeID(), RadioID: rc.id, AirtimeMs: airtime[id.NodeID()]}
		if id.IsRelay {
			st.AirtimeMs += relayMs
		}
		stats[id.NodeID()] = st
		out = append(out, st)
		countAcks(st, h.Messages.Window(id.NodeNum, cut))
	}
	relayID := ""
	if rel := h.Relay(); rel != nil {
		relayID = rel.NodeID()
	}
	for _, p := range h.Packets.List(5000, 0, func(p *mesh.PacketRecord) bool { return p.Time >= cut }) {
		countPacket(stats, relayID, p)
	}
	return out
}

// identityAirtime sums a window's airtime by identity, and the relaying airtime apart.
func identityAirtime(h *mesh.Host, now time.Time, window time.Duration) (map[string]float64, float64) {
	airtime := map[string]float64{}
	var relayMs float64
	for _, b := range h.Air.Buckets(now, window) {
		for k, v := range b.ByIdentity {
			airtime[wire.NodeID(k)] += v
		}
		relayMs += b.RelayMs
	}
	return airtime, relayMs
}

// countAcks counts an identity's acknowledged and failed messages.
func countAcks(st *identityStat, msgs []mesh.Message) {
	for _, m := range msgs {
		switch m.Status {
		case "acked":
			st.AckOK++
		case "failed":
			st.AckFail++
		}
	}
}

// countPacket adds one packet to the identity that sent or received it: relayed packets count
// for the relay persona, and delivered broadcasts for every identity.
func countPacket(stats map[string]*identityStat, relayID string, p mesh.PacketRecord) {
	switch {
	case p.Direction == "tx" && p.Kind == "ours":
		if st := stats[p.From]; st != nil {
			st.Tx++
		}
	case p.Direction == "tx" && p.Kind == "relayed":
		if st := stats[relayID]; st != nil {
			st.Tx++
		}
	case p.Direction == "rx" && p.Kind == "delivered":
		countDelivered(stats, p.To)
	}
}

// countDelivered counts a delivered packet for its identity, or for all of them if it was a broadcast.
func countDelivered(stats map[string]*identityStat, to string) {
	if st := stats[to]; st != nil {
		st.Rx++
		return
	}
	if to != "!ffffffff" {
		return
	}
	for _, st := range stats {
		st.Rx++
	}
}

func (s *Server) listPackets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	node, port, kind := q.Get("node"), q.Get("port"), q.Get("kind")
	dir, channel, text := q.Get("direction"), q.Get("channel"), strings.ToLower(q.Get("q"))
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	match := func(p *mesh.PacketRecord) bool {
		switch {
		case node != "" && p.From != node && p.To != node:
			return false
		case port != "" && p.Port != port:
			return false
		case kind != "" && p.Kind != kind:
			return false
		case dir != "" && p.Direction != dir:
			return false
		case channel != "" && p.Channel != channel:
			return false
		case since > 0 && p.Time < since:
			return false
		case text != "" && !strings.Contains(strings.ToLower(p.Summary), text):
			return false
		}
		return true
	}
	writeJSON(w, http.StatusOK, s.packets(r, limit, before, match))
}

// packets is the newest limit packet records of the request's radios, newest first.
func (s *Server) packets(r *http.Request, limit int, before int64, match func(*mesh.PacketRecord) bool) []mesh.PacketRecord {
	radios := s.radiosFor(r)
	if len(radios) == 1 {
		return radios[0].host.Packets.List(limit, before, match)
	}
	var out []mesh.PacketRecord
	for _, rc := range radios {
		out = append(out, rc.host.Packets.List(limit, before, match)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []mesh.PacketRecord{}
	}
	return out
}

func windowParam(r *http.Request) time.Duration {
	switch r.URL.Query().Get("window") {
	case "1h":
		return time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func (s *Server) statsAirtime(w http.ResponseWriter, r *http.Request) {
	type bucket struct {
		Time       int64              `json:"time"`
		TxMs       float64            `json:"tx_ms"`
		RxMs       float64            `json:"rx_ms"`
		RelayMs    float64            `json:"relay_ms"`
		ByIdentity map[string]float64 `json:"by_identity"`
	}
	byTime := map[int64]*bucket{}
	now := time.Now()
	for _, rc := range s.radiosFor(r) {
		for _, b := range rc.host.Air.Buckets(now, windowParam(r)) {
			t := b.Start.UnixMilli()
			o := byTime[t]
			if o == nil {
				o = &bucket{Time: t, ByIdentity: map[string]float64{}}
				byTime[t] = o
			}
			o.TxMs, o.RxMs, o.RelayMs = o.TxMs+b.TxMs, o.RxMs+b.RxMs, o.RelayMs+b.RelayMs
			for k, v := range b.ByIdentity {
				o.ByIdentity[wire.NodeID(k)] += v
			}
		}
	}
	buckets := make([]*bucket, 0, len(byTime))
	for _, b := range byTime {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Time < buckets[j].Time })
	writeJSON(w, http.StatusOK, map[string]any{"bucket_s": 600, "buckets": buckets})
}

func (s *Server) statsPorts(w http.ResponseWriter, r *http.Request) {
	cut := time.Now().Add(-windowParam(r)).UnixMilli()
	counts := map[string][2]int{}
	for _, p := range s.packets(r, 5000, 0, func(p *mesh.PacketRecord) bool { return p.Time >= cut && p.Port != "" }) {
		c := counts[p.Port]
		if p.Direction == "tx" {
			c[1]++
		} else {
			c[0]++
		}
		counts[p.Port] = c
	}
	out := []map[string]any{}
	for port, c := range counts {
		out = append(out, map[string]any{"port": port, "rx": c[0], "tx": c[1]})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["rx"].(int)+out[i]["tx"].(int) > out[j]["rx"].(int)+out[j]["tx"].(int)
	})
	writeJSON(w, http.StatusOK, out)
}
