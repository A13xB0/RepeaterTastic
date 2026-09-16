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

func (s *Server) sampleRF(ctx context.Context, rc *radioCtx) {
	t := time.NewTicker(time.Minute)
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
	rc := s.radioFor(r)
	rc.rf.mu.Lock()
	src := append([]rfPoint(nil), rc.rf.points...)
	rc.rf.mu.Unlock()
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

func (s *Server) statsIdentities(w http.ResponseWriter, r *http.Request) {
	window := windowParam(r)
	now := time.Now()
	cut := now.Add(-window).UnixMilli()
	airtime := map[string]float64{}
	var relayMs float64
	for _, b := range s.hostFor(r).Air.Buckets(now, window) {
		for k, v := range b.ByIdentity {
			airtime[wire.NodeID(k)] += v
		}
		relayMs += b.RelayMs
	}
	type stat struct {
		NodeID    string  `json:"node_id"`
		Tx        int     `json:"tx"`
		Rx        int     `json:"rx"`
		AckOK     int     `json:"ack_ok"`
		AckFail   int     `json:"ack_fail"`
		AirtimeMs float64 `json:"airtime_ms"`
	}
	stats := map[string]*stat{}
	var order []string
	for _, id := range s.hostFor(r).Identities() {
		st := &stat{NodeID: id.NodeID(), AirtimeMs: airtime[id.NodeID()]}
		if id.IsRelay {
			st.AirtimeMs += relayMs
		}
		stats[id.NodeID()] = st
		order = append(order, id.NodeID())
		for _, m := range s.hostFor(r).Messages.Window(id.NodeNum, cut) {
			switch m.Status {
			case "acked":
				st.AckOK++
			case "failed":
				st.AckFail++
			}
		}
	}
	relayID := ""
	if rel := s.hostFor(r).Relay(); rel != nil {
		relayID = rel.NodeID()
	}
	for _, p := range s.hostFor(r).Packets.List(5000, 0, func(p *mesh.PacketRecord) bool { return p.Time >= cut }) {
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
			if st := stats[p.To]; st != nil {
				st.Rx++
			} else if p.To == "!ffffffff" {
				for _, st := range stats {
					st.Rx++
				}
			}
		}
	}
	out := make([]*stat, 0, len(order))
	for _, k := range order {
		out = append(out, stats[k])
	}
	writeJSON(w, http.StatusOK, out)
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
	writeJSON(w, http.StatusOK, s.hostFor(r).Packets.List(limit, before, func(p *mesh.PacketRecord) bool {
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
	}))
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
	buckets := []map[string]any{}
	for _, b := range s.hostFor(r).Air.Buckets(time.Now(), windowParam(r)) {
		by := map[string]float64{}
		for k, v := range b.ByIdentity {
			by[wire.NodeID(k)] = v
		}
		buckets = append(buckets, map[string]any{"time": b.Start.UnixMilli(), "tx_ms": b.TxMs, "rx_ms": b.RxMs,
			"relay_ms": b.RelayMs, "by_identity": by})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket_s": 600, "buckets": buckets})
}

func (s *Server) statsPorts(w http.ResponseWriter, r *http.Request) {
	cut := time.Now().Add(-windowParam(r)).UnixMilli()
	counts := map[string][2]int{}
	for _, p := range s.hostFor(r).Packets.List(5000, 0, func(p *mesh.PacketRecord) bool { return p.Time >= cut && p.Port != "" }) {
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
