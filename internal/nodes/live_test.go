package nodes

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/null"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// TestLiveSeed gives a real meshtasticd (Lora: Module: sim) a saved identity:
//
//	RT_TEST_MESHTASTICD_SEED=127.0.0.1:4411 go test ./internal/nodes -run LiveSeed -v
func TestLiveSeed(t *testing.T) {
	addr := os.Getenv("RT_TEST_MESHTASTICD_SEED")
	if addr == "" {
		t.Skip("RT_TEST_MESHTASTICD_SEED not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST, RelayRole: mesh.RoleClientMute, HopLimit: 3,
		TelemetryInterval: 3 * time.Hour, StateDir: t.TempDir()}, null.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := mtclient.New(mtclient.Options{Address: addr, Logf: func(string, ...any) {}, ReconnectInterval: 500 * time.Millisecond})
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	n := newNode(addr, t.TempDir(), c, t.Logf)
	want, _ := mesh.NewIdentity(nil, "Seed test", "SEED")
	want.IsRelay = os.Getenv("RT_TEST_SEED_RELAY") != ""
	n.SetSeed(want.Record())
	id, err := n.Identity(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	h.AddConfigApplier(n)
	n.Bind(h, id)
	go n.Run(ctx)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		s := c.Snapshot()
		if s.Connected && s.NodeNum() == want.NodeNum {
			t.Logf("seeded: %s, reconnects %d", id.NodeID(), s.Reconnects)
			return
		}
		time.Sleep(time.Second)
	}
	s := c.Snapshot()
	t.Fatalf("node is !%08x, want %s (reconnects %d)", s.NodeNum(), want.NodeID(), s.Reconnects)
}
