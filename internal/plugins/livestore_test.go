package plugins

import (
	"context"
	"os"
	"testing"
)

// TestLiveScotMeshStore reads the real published index, so a change to the store repo this code
// can't read shows up here rather than on somebody's node. It needs the internet, so it only runs
// when RT_LIVE_STORE is set.
func TestLiveScotMeshStore(t *testing.T) {
	if os.Getenv("RT_LIVE_STORE") == "" {
		t.Skip("set RT_LIVE_STORE=1 to read the published store")
	}
	s := NewStore("", t.TempDir(), "0.3.3")
	idx, err := s.Index(context.Background(), true)
	if err != nil {
		t.Fatalf("reading %s: %v", s.URL(), err)
	}
	if len(idx.Plugins) == 0 {
		t.Fatal("the published store lists nothing")
	}
	for _, p := range idx.Plugins {
		t.Logf("%s v%s %v", p.ID, p.Latest.Version, p.Permissions)
		if why := p.Unusable("0.3.3"); why != "" {
			t.Errorf("%s should be installable on a current node: %s", p.ID, why)
		}
		if p.Latest.MinHost != "" {
			if why := p.Unusable("0.0.1"); why == "" {
				t.Errorf("%s has min_host %s but an ancient node was told it could install it", p.ID, p.Latest.MinHost)
			}
		}
		if b, err := s.Logo(context.Background(), p.ID); err != nil || len(b) < 100 {
			t.Errorf("%s logo: %d bytes, %v", p.ID, len(b), err)
		}
	}
}
