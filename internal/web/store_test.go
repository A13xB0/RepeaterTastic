package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

const storeManifest = `id: gadget
name: Gadget
version: 2.0.0
api: 1
permissions: [nodes.read]
run:
  managed:
    exec: run.sh
`

// fakeStoreServer serves an index, a logo and a bundle, the way the ScotMesh store repo does.
func fakeStoreServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	bundle := storeBundle(t)
	sum := sha256.Sum256(bundle)
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		index := map[string]any{
			"version": 1,
			"plugins": []map[string]any{{
				"id": "gadget", "name": "Gadget", "summary": "Does a thing",
				"author": "ScotMesh", "homepage": "https://example.invalid",
				"license": "GPL-3.0-or-later", "logo": "logos/gadget.png",
				"permissions": []string{"nodes.read"},
				"latest": map[string]any{
					"version": version, "api": 1, "url": base + "/bundle.zip",
					"sha256": hex.EncodeToString(sum[:]), "size": len(bundle),
					"arches": []string{runtime.GOOS + "/" + runtime.GOARCH},
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(index)
	})
	mux.HandleFunc("/logos/gadget.png", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("\x89PNG store logo"))
	})
	mux.HandleFunc("/bundle.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bundle)
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func storeBundle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string, mode uint32) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o644)
		if mode != 0 {
			h.SetMode(0o755)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, body)
	}
	add("plugin.yaml", storeManifest, 0)
	add("run.sh", "#!/bin/sh\nsleep 60\n", 1)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStoreAPI(t *testing.T) {
	store := fakeStoreServer(t, "2.0.0")
	srv, tok := testPluginServerWith(t, func(c *config.Config) {
		c.Plugins.StoreURL = store.URL + "/index.json"
	})

	if code, _, _ := call(t, srv, "GET", "/api/v1/plugins/store", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("the store should need a token, got %d", code)
	}

	code, obj, _ := call(t, srv, "GET", "/api/v1/plugins/store", tok, nil)
	if code != http.StatusOK || obj["enabled"] != true {
		t.Fatalf("store list: %d %v", code, obj)
	}
	if obj["error"] != nil {
		t.Fatalf("the store should have been readable: %v", obj["error"])
	}
	list, _ := obj["plugins"].([]any)
	if len(list) != 1 {
		t.Fatalf("wanted one plugin, got %v", obj["plugins"])
	}
	entry, _ := list[0].(map[string]any)
	if entry["id"] != "gadget" || entry["installed"] != nil || entry["update_available"] != false {
		t.Fatalf("nothing is installed yet: %v", entry)
	}
	if entry["unusable"] != nil {
		t.Errorf("it should be installable here: %v", entry["unusable"])
	}

	// The logo comes through the daemon, so a browser that can't reach the store still sees it.
	logoURL, _ := entry["logo_url"].(string)
	if !strings.HasPrefix(logoURL, "/plugin-store-logo/") {
		t.Fatalf("no logo URL: %v", entry)
	}
	resp, err := http.Get(srv.URL + logoURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Fatalf("logo: %d %q", resp.StatusCode, body)
	}
	// A made-up key gets nothing.
	bad, err := http.Get(srv.URL + "/plugin-store-logo/deadbeef/gadget")
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Errorf("a wrong key should 404, got %d", bad.StatusCode)
	}

	// Install it.
	code, obj, _ = call(t, srv, "POST", "/api/v1/plugins/store/gadget/install", tok, nil)
	if code != http.StatusCreated || obj["id"] != "gadget" || obj["version"] != "2.0.0" {
		t.Fatalf("install from store: %d %v", code, obj)
	}
	if obj["state"] != "disabled" {
		t.Errorf("it should arrive switched off pending review, got %v", obj["state"])
	}

	// The list now knows it is installed, and the Plugins tab shows no updates.
	_, obj, _ = call(t, srv, "GET", "/api/v1/plugins/store?refresh=1", tok, nil)
	entry = obj["plugins"].([]any)[0].(map[string]any)
	if entry["installed"] != "2.0.0" || entry["update_available"] != false {
		t.Fatalf("wanted 2.0.0 installed and no update, got %v", entry)
	}
	_, obj, _ = call(t, srv, "GET", "/api/v1/plugins", tok, nil)
	st, _ := obj["store"].(map[string]any)
	if st == nil || st["enabled"] != true || st["updates"].(float64) != 0 {
		t.Fatalf("plugins list store block: %v", obj["store"])
	}

	// Something that isn't in the store.
	if code, _, _ := call(t, srv, "POST", "/api/v1/plugins/store/nosuch/install", tok, nil); code != http.StatusNotFound {
		t.Errorf("installing a plugin the store doesn't list: %d", code)
	}
}

func TestStoreCanBeTurnedOff(t *testing.T) {
	srv, tok := testPluginServerWith(t, func(c *config.Config) { c.Plugins.StoreURL = "off" })

	code, obj, _ := call(t, srv, "GET", "/api/v1/plugins/store", tok, nil)
	if code != http.StatusOK || obj["enabled"] != false {
		t.Fatalf("wanted a polite empty store, got %d %v", code, obj)
	}
	if code, _, _ := call(t, srv, "POST", "/api/v1/plugins/store/gadget/install", tok, nil); code != http.StatusForbidden {
		t.Errorf("installing with the store off: %d", code)
	}
	_, obj, _ = call(t, srv, "GET", "/api/v1/plugins", tok, nil)
	if st, _ := obj["store"].(map[string]any); st["enabled"] != false {
		t.Errorf("the Plugins tab should know the store is off, got %v", obj["store"])
	}
}

func TestStoreListsTheCachedCopyWhenItCannotBeReached(t *testing.T) {
	store := fakeStoreServer(t, "2.0.0")
	srv, tok := testPluginServerWith(t, func(c *config.Config) {
		c.Plugins.StoreURL = store.URL + "/index.json"
	})
	if _, obj, _ := call(t, srv, "GET", "/api/v1/plugins/store", tok, nil); len(obj["plugins"].([]any)) != 1 {
		t.Fatal("the first read should have filled the cache")
	}
	store.Close()

	code, obj, _ := call(t, srv, "GET", "/api/v1/plugins/store?refresh=1", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("an unreachable store should still answer, got %d", code)
	}
	if len(obj["plugins"].([]any)) != 1 {
		t.Errorf("wanted the cached list, got %v", obj["plugins"])
	}
	if obj["error"] == nil {
		t.Error("it should say why the list might be out of date")
	}
}
