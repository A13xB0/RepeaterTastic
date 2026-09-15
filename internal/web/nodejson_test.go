package web

import (
	"net/http/httptest"
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

func TestNodeJSONSignalOnlyForDirectNodes(t *testing.T) {
	relayed := nodeJSON(mesh.NodeEntry{Num: 1, HopsAway: 3, SNR: 0, RSSI: 0}, nil)
	if relayed["snr"] != nil || relayed["rssi"] != nil {
		t.Errorf("relayed node signal = %v/%v, want unknown", relayed["snr"], relayed["rssi"])
	}
	direct := nodeJSON(mesh.NodeEntry{Num: 2, HopsAway: 0, SNR: -7.5, RSSI: -118}, nil)
	if direct["snr"] != float32(-7.5) || direct["rssi"] != int32(-118) {
		t.Errorf("direct node signal = %v/%v, want -7.5/-118", direct["snr"], direct["rssi"])
	}
}

func TestWithMapKey(t *testing.T) {
	base := "https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png"
	if got := withMapKey(base+"?key={api_key}", "k 1"); got != base+"?key=k+1" {
		t.Errorf("with key = %s", got)
	}
	if got := withMapKey(base+"?key={api_key}", ""); got != base {
		t.Errorf("without key = %s", got)
	}
	if got := withMapKey(base+"?api_key={api_key}&lang=en", ""); got != base+"?lang=en" {
		t.Errorf("without key, more params = %s", got)
	}
	if got := withMapKey(base+"?lang=en&api_key={api_key}", ""); got != base+"?lang=en" {
		t.Errorf("without key, trailing = %s", got)
	}
	if got := withMapKey("https://tiles.example/{z}/{x}/{y}.png", "k"); got != "https://tiles.example/{z}/{x}/{y}.png" {
		t.Errorf("url without placeholder changed: %s", got)
	}
}

func TestMissingAssetIs404(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.spa().ServeHTTP(rec, httptest.NewRequest("GET", "/assets/Links-gone0000.js", nil))
	if rec.Code != 404 {
		t.Fatalf("missing asset = %d %s, want 404", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	s.spa().ServeHTTP(rec, httptest.NewRequest("GET", "/config/mqtt", nil))
	if rec.Code != 200 {
		t.Fatalf("history route = %d, want the app page", rec.Code)
	}
}
