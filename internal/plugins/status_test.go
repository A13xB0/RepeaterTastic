package plugins

import (
	"testing"

	"google.golang.org/grpc/codes"

	pluginv1 "github.com/ScotMesh/RepeaterTastic/api/plugin/v1"
)

// Radio said what a radio is; a plugin had no way to ask what it was doing.
func TestGetStatusNeedsThePermission(t *testing.T) {
	e := newAPIEnv(t)
	ctx := e.attach(t, "remote", "nodes.read")
	e.open(t, ctx, hello("remote", ""))
	_, err := e.client.GetStatus(ctx, &pluginv1.GetStatusRequest{})
	wantCode(t, "without status.read", err, codes.PermissionDenied)
}

func TestGetStatusReportsTheRadio(t *testing.T) {
	e := newAPIEnv(t)
	ctx := e.attach(t, "remote", "status.read")
	e.open(t, ctx, hello("remote", ""))

	res, err := e.client.GetStatus(ctx, &pluginv1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(res.Radios) != 1 {
		t.Fatalf("radios = %d, want 1", len(res.Radios))
	}
	s := res.Radios[0]
	if s.RadioId != "main" {
		t.Errorf("radio_id = %q, want main", s.RadioId)
	}
	// The duty limit is the point of reporting airtime: without it the figure
	// means nothing to whoever reads it.
	if s.DutyLimitPct <= 0 {
		t.Errorf("duty_limit_pct = %v, want the region's limit", s.DutyLimitPct)
	}
	if s.UptimeS < 0 {
		t.Errorf("uptime = %v", s.UptimeS)
	}
}

func TestGetStatusFiltersByRadio(t *testing.T) {
	e := newAPIEnv(t)
	ctx := e.attach(t, "remote", "status.read")
	e.open(t, ctx, hello("remote", ""))

	res, err := e.client.GetStatus(ctx, &pluginv1.GetStatusRequest{RadioId: "nope"})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(res.Radios) != 0 {
		t.Errorf("an unknown radio returned %d rows, want none", len(res.Radios))
	}
}

// A plugin holding the permission is told without having to ask.
func TestStatusEventArrivesOnTheStream(t *testing.T) {
	e := newAPIEnv(t)
	ctx := e.attach(t, "remote", "status.read")
	stream := e.open(t, ctx, hello("remote", ""))
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if ev := msg.GetStatusEvent(); ev != nil {
			if len(ev.Radios) == 0 {
				t.Fatal("StatusEvent carried no radios")
			}
			return
		}
	}
}
