package web

import (
	"testing"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
)

// A node that has been heard but hasn't sent its NodeInfo has no User. The web
// GUI sorts and filters on long_name, so a missing name used to throw and blank
// the whole Nodes & map page; the API now fills in the firmware's own defaults.
func TestNodeJSONWithoutUserHasFirmwareDefaults(t *testing.T) {
	n := nodeJSON(mesh.NodeEntry{Num: 0x2b6a77f6, HopsAway: -1}, nil)
	want := map[string]any{"long_name": "Meshtastic 77f6", "short_name": "77f6", "hw_model": "UNSET", "role": "CLIENT", "has_user": false}
	for k, v := range want {
		if n[k] != v {
			t.Errorf("%s = %v, want %v", k, n[k], v)
		}
	}
}
