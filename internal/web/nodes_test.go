package web

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
	"google.golang.org/protobuf/proto"
)

func TestNodeJSONPositionAndTelemetry(t *testing.T) {
	e := mesh.NodeEntry{Num: 5, HopsAway: 1, NextHop: 0x22, LastHeard: time.UnixMilli(1000),
		Position: &pb.Position{LatitudeI: proto.Int32(559000000), LongitudeI: proto.Int32(-31000000), Altitude: proto.Int32(12), Time: 7},
		Metrics:  &pb.DeviceMetrics{BatteryLevel: proto.Uint32(80), Voltage: proto.Float32(4.1)}}
	n := nodeJSON(e, []string{"!00000001"})
	pos := n["position"].(map[string]any)
	if pos["lat"] != 55.9 || pos["lon"] != -3.1 || pos["alt"] != int32(12) || pos["time"] != int64(7000) {
		t.Errorf("position = %v", pos)
	}
	tel := n["telemetry"].(map[string]any)
	if tel["battery"] != uint32(80) || tel["voltage"] != float32(4.1) {
		t.Errorf("telemetry = %v", tel)
	}
	if jsonOf(n["hops_away"]) != "1" || jsonOf(n["next_hop"]) != "34" || jsonOf(n["last_heard"]) != "1000" {
		t.Errorf("node = %v", n)
	}
	// 0,0 is no position; a local node is known by nobody.
	e.Position = &pb.Position{}
	e.Local = true
	n = nodeJSON(e, []string{"!00000001"})
	if n["position"] != nil || len(n["known_by"].([]string)) != 0 {
		t.Errorf("local node = %v", n)
	}
	if nodeTelemetryJSON(nil) != nil {
		t.Error("no metrics should be nil")
	}
}

func TestTracerouteAndNodeInfoRequests(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	target := "!0000abcd"
	for body, want := range map[string]int{`{"from":"!12345678"}`: 400, `{"from":"junk"}`: 400, `nope`: 400} {
		if code, _ := env.do(t, "POST", "/api/v1/nodes/"+target+"/traceroute", tok, "application/json", body); code != want {
			t.Errorf("traceroute with %s: %d, want %d", body, code, want)
		}
	}
	if code, _ := env.do(t, "POST", "/api/v1/nodes/zzz/traceroute", tok, "application/json", `{}`); code != 400 {
		t.Errorf("traceroute to a bad id: %d", code)
	}
	_, desk, _ := call(t, env.srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk"})
	from := desk["node_id"].(string)
	code, obj, _ := call(t, env.srv, "POST", "/api/v1/nodes/"+target+"/traceroute", tok, map[string]any{"from": from})
	if code != http.StatusAccepted || obj["status"] != "sent" {
		t.Fatalf("traceroute: %d %v", code, obj)
	}
	if !tracePending(env.s, from+"|"+target) {
		t.Fatal("traceroute not awaited")
	}
	// The firmware allows one traceroute per 30 s.
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/nodes/"+target+"/traceroute", tok, map[string]any{"from": from}); code != http.StatusTooManyRequests {
		t.Fatalf("second traceroute: %d", code)
	}
	// Without from it comes from the relay persona, which here has no node to send it.
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/nodes/"+target+"/traceroute", tok, map[string]any{}); code != http.StatusTooManyRequests {
		t.Fatalf("traceroute from the relay: %d", code)
	}
	if code, obj, _ := call(t, env.srv, "POST", "/api/v1/nodes/"+target+"/request-nodeinfo", tok, map[string]any{}); code != http.StatusAccepted || obj["status"] != "sent" {
		t.Fatalf("request nodeinfo: %d %v", code, obj)
	}
	if code, _ := env.do(t, "POST", "/api/v1/nodes/"+target+"/request-nodeinfo", tok, "application/json", `{"from":"!12345678"}`); code != 400 {
		t.Fatalf("nodeinfo from a stranger: %d", code)
	}
}

// tracePending reports whether a traceroute is awaited.
func tracePending(s *Server, key string) bool {
	s.traces.mu.Lock()
	defer s.traces.mu.Unlock()
	_, ok := s.traces.pending[key]
	return ok
}

func TestTracerouteAnswersAndTimeouts(t *testing.T) {
	env := newTestEnv(t, nil)
	s, rc := env.s, env.s.radios[0]
	relay := env.host.Relay().NodeID()
	events, unsub := env.host.Bus.Subscribe(16)
	defer unsub()

	// Timed out on this radio: reported as failed. Another radio's, or a bad key: left alone.
	s.traces.mu.Lock()
	s.traces.pending[relay+"|!0000aaaa"] = time.Now().Add(-time.Second)
	s.traces.pending["!12345678|!0000aaaa"] = time.Now().Add(-time.Second)
	s.traces.pending["junk|!0000aaaa"] = time.Now().Add(-time.Second)
	s.traces.pending[relay+"|!0000bbbb"] = time.Now().Add(time.Minute)
	s.traces.mu.Unlock()
	s.expireTraceroutes(rc, time.Now())
	if tracePending(s, relay+"|!0000aaaa") || !tracePending(s, "!12345678|!0000aaaa") || !tracePending(s, "junk|!0000aaaa") || !tracePending(s, relay+"|!0000bbbb") {
		t.Fatalf("pending after expiry = %v", s.traces.pending)
	}
	failed := nextEvent(t, events, "traceroute")
	if m := failed.Data.(map[string]any); m["identity"] != relay || m["target"] != "!0000aaaa" || m["error"] == "" {
		t.Fatalf("timeout event = %v", m)
	}

	// The watcher clears answered traceroutes, and expires the rest itself.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.watchTraceroutes(ctx, rc); close(done) }()
	defer func() { cancel(); <-done }()
	s.expectTraceroute(relay, "!0000cccc")
	s.traces.mu.Lock()
	s.traces.pending[relay+"|!0000cccc"] = time.Now().Add(-time.Second)
	s.traces.mu.Unlock()
	waitUntil(t, "traceroutes settled", func() bool {
		// Published until the watcher, which subscribes when it starts, has seen it.
		env.host.Bus.Publish(mesh.Event{Type: "traceroute", Data: mesh.TracerouteResult{Identity: relay, Target: "!0000bbbb"}})
		return !tracePending(s, relay+"|!0000bbbb") && !tracePending(s, relay+"|!0000cccc")
	})
}

// nextEvent waits for the next bus event of a type.
func nextEvent(t *testing.T, events <-chan mesh.Event, typ string) mesh.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == typ {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event", typ)
		}
	}
}

// waitUntil polls cond for up to five seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDeleteNodeAndSightings(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	num := uint32(0x0000abcd)
	env.host.DB.Update(num, func(e *mesh.NodeEntry) { e.LastHeard, e.SNR, e.RSSI = time.Now(), 5, -90 })
	id := wire.NodeID(num)

	if code, _, _ := call(t, env.srv, "GET", "/api/v1/nodes/zzz/sightings", tok, nil); code != 400 {
		t.Fatalf("sightings of a bad id: %d", code)
	}
	code, _, list := call(t, env.srv, "GET", "/api/v1/nodes/"+id+"/sightings", tok, nil)
	if code != 200 || len(list) != 1 {
		t.Fatalf("sightings: %d %v", code, list)
	}
	sg := list[0].(map[string]any)
	if sg["radio_id"] != "main" || sg["snr"] != float64(5) || sg["rssi"] != float64(-90) || sg["radio_name"] == "" {
		t.Fatalf("sighting = %v", sg)
	}

	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/nodes/zzz", tok, nil); code != 400 {
		t.Fatalf("delete a bad id: %d", code)
	}
	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/nodes/"+env.host.Relay().NodeID(), tok, nil); code != http.StatusConflict {
		t.Fatalf("delete an identity's node: %d", code)
	}
	if code, _, _ := call(t, env.srv, "DELETE", "/api/v1/nodes/"+id, tok, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if _, ok := env.host.DB.Get(num); ok {
		t.Fatal("node still in the DB")
	}
	if _, _, list := call(t, env.srv, "GET", "/api/v1/nodes/"+id+"/sightings", tok, nil); len(list) != 0 {
		t.Fatalf("sightings of a deleted node = %v", list)
	}
}
