package web

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

func TestBucketForAndWindowParam(t *testing.T) {
	for _, c := range []struct {
		window time.Duration
		want   time.Duration
	}{{30 * time.Minute, time.Minute}, {time.Hour, time.Minute}, {24 * time.Hour, 10 * time.Minute}, {7 * 24 * time.Hour, time.Hour}} {
		if got := bucketFor(c.window); got != c.want {
			t.Errorf("bucketFor(%v) = %v, want %v", c.window, got, c.want)
		}
	}
	for q, want := range map[string]time.Duration{"1h": time.Hour, "7d": 7 * 24 * time.Hour, "24h": 24 * time.Hour, "": 24 * time.Hour, "junk": 24 * time.Hour} {
		if got := windowParam(httptest.NewRequest("GET", "/?window="+q, nil)); got != want {
			t.Errorf("window %q = %v, want %v", q, got, want)
		}
	}
}

func TestCountPacketsAndAcks(t *testing.T) {
	a, b, relay := &identityStat{NodeID: "!00000001"}, &identityStat{NodeID: "!00000002"}, &identityStat{NodeID: "!000000aa"}
	stats := map[string]*identityStat{a.NodeID: a, b.NodeID: b, relay.NodeID: relay}
	for _, p := range []mesh.PacketRecord{
		{Direction: "tx", Kind: "ours", From: a.NodeID},
		{Direction: "tx", Kind: "ours", From: "!0000dead"}, // not ours: ignored
		{Direction: "tx", Kind: "relayed", From: "!0000beef"},
		{Direction: "rx", Kind: "delivered", To: b.NodeID},
		{Direction: "rx", Kind: "delivered", To: "!ffffffff"},
		{Direction: "rx", Kind: "delivered", To: "!0000dead"}, // someone else's DM
		{Direction: "rx", Kind: "dupe", To: a.NodeID},
	} {
		countPacket(stats, relay.NodeID, p)
	}
	if a.Tx != 1 || relay.Tx != 1 || b.Tx != 0 {
		t.Errorf("tx a=%d relay=%d b=%d", a.Tx, relay.Tx, b.Tx)
	}
	if a.Rx != 1 || b.Rx != 2 || relay.Rx != 1 {
		t.Errorf("rx a=%d b=%d relay=%d", a.Rx, b.Rx, relay.Rx)
	}
	countAcks(a, []mesh.Message{{Status: "acked"}, {Status: "acked"}, {Status: "failed"}, {Status: "queued"}})
	if a.AckOK != 2 || a.AckFail != 1 {
		t.Errorf("acks ok=%d fail=%d", a.AckOK, a.AckFail)
	}
	// With no relay, relayed packets count for nobody.
	countPacket(stats, "", mesh.PacketRecord{Direction: "tx", Kind: "relayed"})
	if relay.Tx != 1 {
		t.Errorf("relay tx without a relay id = %d", relay.Tx)
	}
}

func TestStatsIdentitiesAirtimeAndPorts(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	_, desk, _ := call(t, env.srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "short_name": "DESK"})
	deskID := desk["node_id"].(string)
	deskNum, _ := wire.ParseNodeID(deskID)
	relayID := env.host.Relay().NodeID()
	now := time.Now()
	env.host.Air.AddTx(now, 120, deskNum)
	env.host.Air.AddTx(now, 30, 0)
	env.host.Air.AddRx(now, 50)
	env.host.Messages.Add(deskNum, &mesh.Message{ID: 9, From: deskID, To: "!12345678", Direction: "out", Status: "acked", Time: now.UnixMilli()})
	for _, p := range []mesh.PacketRecord{
		{Direction: "tx", Kind: "ours", From: deskID, To: "!ffffffff", Port: "TEXT_MESSAGE_APP"},
		{Direction: "tx", Kind: "relayed", From: "!12345678", To: "!ffffffff", Port: "TEXT_MESSAGE_APP"},
		{Direction: "rx", Kind: "delivered", From: "!12345678", To: "!ffffffff", Port: "POSITION_APP"},
		{Direction: "rx", Kind: "delivered", From: "!12345678", To: "!ffffffff", Port: "POSITION_APP"},
		{Direction: "rx", Kind: "delivered", From: "!12345678", To: "!ffffffff", Port: "POSITION_APP"},
		{Direction: "rx", Kind: "old", From: "!12345678", To: deskID, Port: "TEXT_MESSAGE_APP", Time: now.Add(-48 * time.Hour).UnixMilli()},
	} {
		env.host.Packets.Add(p)
	}

	byID := map[string]map[string]any{}
	_, _, list := call(t, env.srv, "GET", "/api/v1/stats/identities?window=1h", tok, nil)
	for _, x := range list {
		m := x.(map[string]any)
		byID[m["node_id"].(string)] = m
	}
	d, r := byID[deskID], byID[relayID]
	if d == nil || r == nil {
		t.Fatalf("identity stats = %v", list)
	}
	if d["tx"] != float64(1) || d["rx"] != float64(3) || d["ack_ok"] != float64(1) || d["airtime_ms"] != float64(120) || d["radio_id"] != "main" {
		t.Errorf("desk stats = %v", d)
	}
	if r["tx"] != float64(1) || r["airtime_ms"] != float64(30) {
		t.Errorf("relay stats = %v", r)
	}
	checkAirtimeStats(t, env, tok, deskID)
	checkPortStats(t, env, tok)
}

// checkAirtimeStats checks the airtime buckets add up TX, RX, relay and per-identity time.
func checkAirtimeStats(t *testing.T, env *testEnv, tok, deskID string) {
	t.Helper()
	// An hour's worth is answered from the per-minute ring: bucketFor has always
	// said a minute for this window, and now the data behind it is per-minute too
	// rather than ten-minute buckets labelled 600.
	code, obj, _ := call(t, env.srv, "GET", "/api/v1/stats/airtime?window=1h", tok, nil)
	buckets, _ := obj["buckets"].([]any)
	if code != 200 || obj["bucket_s"] != float64(60) || len(buckets) == 0 {
		t.Fatalf("airtime: %d %v", code, obj)
	}
	var tx, rx, relay, desk float64
	for _, b := range buckets {
		m := b.(map[string]any)
		tx += m["tx_ms"].(float64)
		rx += m["rx_ms"].(float64)
		relay += m["relay_ms"].(float64)
		desk += m["by_identity"].(map[string]any)[deskID].(float64)
	}
	if tx != 150 || rx != 50 || relay != 30 || desk != 120 {
		t.Fatalf("airtime totals tx=%v rx=%v relay=%v desk=%v", tx, rx, relay, desk)
	}
}

// checkPortStats checks packets are counted per port, busiest first, within the window.
func checkPortStats(t *testing.T, env *testEnv, tok string) {
	t.Helper()
	_, _, ports := call(t, env.srv, "GET", "/api/v1/stats/ports", tok, nil)
	if len(ports) != 2 {
		t.Fatalf("ports = %v", ports)
	}
	first, second := ports[0].(map[string]any), ports[1].(map[string]any)
	if first["port"] != "POSITION_APP" || first["rx"] != float64(3) || first["tx"] != float64(0) {
		t.Errorf("busiest port = %v", first)
	}
	if second["port"] != "TEXT_MESSAGE_APP" || second["tx"] != float64(2) || second["rx"] != float64(0) {
		t.Errorf("second port = %v", second)
	}
}

func TestListPacketsFilters(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	now := time.Now().UnixMilli()
	for _, p := range []mesh.PacketRecord{
		{Direction: "rx", Kind: "delivered", From: "!00000001", To: "!ffffffff", Port: "TEXT_MESSAGE_APP", Channel: "LongFast", Summary: "Hello World", Time: now - 5000},
		{Direction: "tx", Kind: "relayed", From: "!00000002", To: "!00000003", Port: "POSITION_APP", Channel: "Ops", Time: now - 4000},
		{Direction: "rx", Kind: "dupe", From: "!00000004", To: "!ffffffff", Port: "TELEMETRY_APP", Time: now - 3000},
	} {
		env.host.Packets.Add(p)
	}
	cases := map[string]int{
		"":                            3,
		"?node=!00000003":             1,
		"?port=POSITION_APP":          1,
		"?kind=dupe":                  1,
		"?direction=rx":               2,
		"?channel=Ops":                1,
		"?q=hello":                    1,
		"?since=" + itoa64(now-3500):  1,
		"?limit=1":                    1,
		"?limit=99999":                3,
		"?before=" + itoa64(now-3500): 2,
	}
	for q, want := range cases {
		code, _, list := call(t, env.srv, "GET", "/api/v1/packets"+q, tok, nil)
		if code != 200 || len(list) != want {
			t.Errorf("packets%s: %d, %d records, want %d", q, code, len(list), want)
		}
	}
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

func TestPacketsAcrossRadiosWithNoneIsEmpty(t *testing.T) {
	srv := testWebTwoRadios(t)
	tok := setupAndSignIn(t, srv)
	code, obj, list := call(t, srv, "GET", "/api/v1/packets?radio=all&kind=nothing", tok, nil)
	if code != 200 || obj != nil || list == nil || len(list) != 0 {
		t.Fatalf("no packets: %d %v %v", code, obj, list)
	}
}

func TestStatsRFAggregatesSamples(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	rc := env.s.radios[0]
	base := time.Now().Truncate(time.Minute)
	rc.rf.mu.Lock()
	rc.rf.points = []rfPoint{
		{Time: base.Add(-3 * time.Hour).UnixMilli(), ChannelUtil: 99, Rx: 99}, // outside the hour
		{Time: base.Add(-2 * time.Minute).UnixMilli(), NoiseFloorDBm: -120, ChannelUtil: 10, Rx: 1, Tx: 2},
		{Time: base.Add(-time.Minute).UnixMilli(), NoiseFloorDBm: 0, ChannelUtil: 20, Rx: 3, Tx: 4},
	}
	rc.rf.mu.Unlock()
	_, obj, _ := call(t, env.srv, "GET", "/api/v1/stats/rf?window=1h", tok, nil)
	points, _ := obj["points"].([]any)
	if obj["bucket_s"] != float64(60) || len(points) != 2 {
		t.Fatalf("rf 1h = %v", obj)
	}
	// A day groups them into one ten-minute bucket; a sample without a noise reading doesn't count.
	_, obj, _ = call(t, env.srv, "GET", "/api/v1/stats/rf", tok, nil)
	points, _ = obj["points"].([]any)
	if obj["bucket_s"] != float64(600) || len(points) == 0 {
		t.Fatalf("rf 24h = %v", obj)
	}
	var rx, tx float64
	for _, p := range points {
		m := p.(map[string]any)
		rx += m["rx"].(float64)
		tx += m["tx"].(float64)
		if m["noise_floor_dbm"].(float64) != 0 && m["noise_floor_dbm"].(float64) != -120 {
			t.Errorf("noise floor averaged over a missing reading: %v", m)
		}
	}
	if rx != 103 || tx != 6 {
		t.Fatalf("rf 24h totals rx=%v tx=%v", rx, tx)
	}
}

func TestSampleRFRecordsAndTrims(t *testing.T) {
	env := newTestEnv(t, nil)
	rc := env.s.radios[0]
	rc.rf.mu.Lock()
	rc.rf.points = make([]rfPoint, rfKeep)
	rc.rf.mu.Unlock()
	env.host.Counters.Rx.Add(5)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { env.s.sampleRFEvery(ctx, rc, 5*time.Millisecond); close(done) }()
	waitForSamples(t, rc, 2)
	cancel()
	<-done
	rc.rf.mu.Lock()
	defer rc.rf.mu.Unlock()
	if len(rc.rf.points) != rfKeep {
		t.Errorf("history grew to %d", len(rc.rf.points))
	}
	var rx uint64
	for _, p := range rc.rf.points {
		rx += p.Rx
	}
	// The counter is sampled as a difference: the 5 packets are counted once.
	if rx != 5 {
		t.Fatalf("samples counted %d packets, want 5", rx)
	}
}

// waitForSamples waits until the radio's RF history ends with n real samples.
func waitForSamples(t *testing.T, rc *radioCtx, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rc.rf.mu.Lock()
		got := 0
		for i := len(rc.rf.points) - 1; i >= 0 && rc.rf.points[i].Time != 0; i-- {
			got++
		}
		rc.rf.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d samples recorded, want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A caller may ask for a finer chart than the daemon recorded. The answer is to
// give them the finest that is real, not to invent detail or refuse the request.
func TestBucketParamClampsToWhatWasRecorded(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		name   string
		query  string
		window time.Duration
		floor  time.Duration
		want   time.Duration
	}{
		{"rf may go down to its sample interval", "bucket=1m", day, rfSampleInterval, time.Minute},
		{"rf finer than sampling is raised to it", "bucket=5s", day, rfSampleInterval, time.Minute},
		{"airtime is held at its storage bucket", "bucket=1m", day, mesh.StatBucket, mesh.StatBucket},
		{"airtime may still be coarsened", "bucket=1h", day, mesh.StatBucket, time.Hour},
		{"no bucket asked for keeps the old default", "", day, rfSampleInterval, 10 * time.Minute},
		{"rubbish falls back to the default", "bucket=banana", day, rfSampleInterval, 10 * time.Minute},
		{"zero falls back to the default", "bucket=0s", day, rfSampleInterval, 10 * time.Minute},
		{"nothing coarser than the window itself", "bucket=48h", day, rfSampleInterval, day},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/stats/rf?"+c.query, nil)
			if got := bucketParam(r, c.window, c.floor); got != c.want {
				t.Errorf("bucketParam(%q) = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

// Asking for a coarser bucket than the store holds groups the stored ones; the
// totals must survive the grouping either way.
func TestAirtimeCoarseBucketKeepsTheTotals(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	now := time.Now()
	env.host.Air.AddTx(now.Add(-2*time.Minute), 150, 0x1234)
	env.host.Air.AddRx(now.Add(-2*time.Minute), 50)

	fine := airtimeTotal(t, env, tok, "window=1h&bucket=1m")
	coarse := airtimeTotal(t, env, tok, "window=1h&bucket=1h")
	if fine != coarse {
		t.Errorf("totals differ by resolution: 1m gave %v, 1h gave %v", fine, coarse)
	}
	if fine != 200 {
		t.Errorf("total tx+rx = %v, want 200", fine)
	}
}

func airtimeTotal(t *testing.T, env *testEnv, tok, query string) float64 {
	t.Helper()
	code, obj, _ := call(t, env.srv, "GET", "/api/v1/stats/airtime?"+query, tok, nil)
	if code != 200 {
		t.Fatalf("airtime %s: %d", query, code)
	}
	buckets, _ := obj["buckets"].([]any)
	var total float64
	for _, b := range buckets {
		m := b.(map[string]any)
		total += m["tx_ms"].(float64) + m["rx_ms"].(float64)
	}
	return total
}
