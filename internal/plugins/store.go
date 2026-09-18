package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A store is a JSON index of plugins someone publishes, usually a git repo served over HTTPS.
// It holds names, logos and download addresses; nothing in it runs here. The daemon fetches the
// index, offers what fits this node, downloads the bundle from wherever the index points and
// checks the sha256 before unpacking it, so a swapped download is caught even though the store
// itself never sees the bytes.
const (
	// DefaultStoreURL is the ScotMesh store, used unless plugins.store_url says otherwise.
	DefaultStoreURL = "https://raw.githubusercontent.com/ScotMesh/repeatertastic-plugins/main/index.json"

	storeMaxBytes  = 2 << 20 // an index is a few kB; this is a wide margin
	logoMaxBytes   = 1 << 20
	storeFresh     = 15 * time.Minute // how long a fetched index is reused without asking again
	storeTimeout   = 30 * time.Second
	storeIndexAPI  = 1 // the index format this understands
	storePluginAPI = 1 // the Plugin API version this host speaks
)

// Index is a store's index.json.
type Index struct {
	Version int           `json:"version"`
	Updated time.Time     `json:"updated"`
	Name    string        `json:"name,omitempty"`
	Plugins []StorePlugin `json:"plugins"`
}

// StorePlugin is one plugin as the store describes it. Everything the GUI shows before an install
// comes from here, including the permissions, so nobody is asked to grant them blind.
type StorePlugin struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Summary     string   `json:"summary"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author"`
	Homepage    string   `json:"homepage"`
	License     string   `json:"license"`
	Logo        string   `json:"logo,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Permissions []string `json:"permissions"`
	Network     []string `json:"network,omitempty"`
	// Image is set for plugins that run in their own container. Those are attached, not
	// installed: the daemon can't install into a container it doesn't own.
	Image  string  `json:"image,omitempty"`
	Latest Release `json:"latest"`
}

// Release is one downloadable version.
type Release struct {
	Version  string    `json:"version"`
	API      int       `json:"api"`
	MinHost  string    `json:"min_host,omitempty"`
	Released time.Time `json:"released,omitempty"`
	URL      string    `json:"url"`
	SHA256   string    `json:"sha256"`
	Size     int64     `json:"size"`
	Arches   []string  `json:"arches"`
	Notes    string    `json:"notes,omitempty"`
}

// Store reads one store index and caches it on disk, so the Browse tab still lists something when
// the node is off the internet.
type Store struct {
	url     string
	dir     string
	host    string // this daemon's version, for min_host
	client  *http.Client
	loaded  bool // the disk cache has been read
	mu      sync.Mutex
	index   *Index
	etag    string
	fetched time.Time
}

// NewStore reads the store at u, caching under dir. host is the daemon's version.
func NewStore(u, dir, host string) *Store {
	if strings.TrimSpace(u) == "" {
		u = DefaultStoreURL
	}
	return &Store{
		url:    strings.TrimSpace(u),
		dir:    dir,
		host:   host,
		client: &http.Client{Timeout: storeTimeout},
	}
}

// URL is the index address this store reads.
func (s *Store) URL() string { return s.url }

func (s *Store) indexPath() string { return filepath.Join(s.dir, "index.json") }
func (s *Store) etagPath() string  { return filepath.Join(s.dir, "index.etag") }
func (s *Store) logoPath(id string) string {
	return filepath.Join(s.dir, "logos", filepath.Base(id)+".png")
}

// Index returns the store's plugins, fetching if what we have is stale. force asks the server even
// when the cached copy is fresh, for the GUI's Refresh button. When the fetch fails but a cached
// index exists, it returns the cache and the error, so the GUI can list plugins and still say the
// list is old.
func (s *Store) Index(ctx context.Context, force bool) (*Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		s.loadCacheLocked()
		s.loaded = true
	}
	if !force && s.index != nil && time.Since(s.fetched) < storeFresh {
		return s.index, nil
	}
	idx, err := s.fetchLocked(ctx)
	if err != nil {
		return s.index, err // cached copy, if there is one, plus the reason it is old
	}
	return idx, nil
}

// Cached returns the last index without touching the network, for callers that only want to say
// whether an update exists.
func (s *Store) Cached() *Index {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		s.loadCacheLocked()
		s.loaded = true
	}
	return s.index
}

// Fetched is when the index last came off the wire.
func (s *Store) Fetched() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetched
}

// Plugin finds one entry by id.
func (s *Store) Plugin(ctx context.Context, id string) (StorePlugin, error) {
	idx, err := s.Index(ctx, false)
	if idx == nil {
		if err == nil {
			err = errors.New("the store is empty")
		}
		return StorePlugin{}, err
	}
	for _, p := range idx.Plugins {
		if p.ID == id {
			return p, nil
		}
	}
	return StorePlugin{}, fmt.Errorf("%w in the store: %s", ErrNotFound, id)
}

// fetchLocked gets index.json, using the stored ETag so an unchanged index costs a 304.
func (s *Store) fetchLocked(ctx context.Context) (*Index, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.etag != "" && s.index != nil {
		req.Header.Set("If-None-Match", s.etag)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading the store: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && s.index != nil {
		s.fetched = time.Now()
		return s.index, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reading the store: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, storeMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the store: %w", err)
	}
	if int64(len(body)) > storeMaxBytes {
		return nil, fmt.Errorf("the store index is larger than %d kB", storeMaxBytes>>10)
	}
	idx, err := parseIndex(body)
	if err != nil {
		return nil, err
	}
	s.index, s.etag, s.fetched = idx, resp.Header.Get("ETag"), time.Now()
	s.saveCacheLocked(body)
	return idx, nil
}

// parseIndex reads and sanity-checks an index.
func parseIndex(body []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("the store index isn't valid JSON: %w", err)
	}
	if idx.Version != storeIndexAPI {
		return nil, fmt.Errorf("this store is version %d; this RepeaterTastic reads version %d", idx.Version, storeIndexAPI)
	}
	idx.Plugins = slices.DeleteFunc(idx.Plugins, func(p StorePlugin) bool {
		return !validID(p.ID) || p.Latest.Version == ""
	})
	return &idx, nil
}

func (s *Store) loadCacheLocked() {
	body, err := os.ReadFile(s.indexPath())
	if err != nil {
		return
	}
	idx, err := parseIndex(body)
	if err != nil {
		return
	}
	s.index = idx
	if tag, err := os.ReadFile(s.etagPath()); err == nil {
		s.etag = strings.TrimSpace(string(tag))
	}
	// Left at zero: a cache off the disk is old by definition, so the next Index fetches.
}

func (s *Store) saveCacheLocked(body []byte) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(s.indexPath(), body, 0o644)
	if s.etag != "" {
		_ = os.WriteFile(s.etagPath(), []byte(s.etag), 0o644)
	}
}

// Logo returns a plugin's logo, from the disk cache when it is there. Logos are small and change
// about never, so one fetch per plugin per install of the daemon is plenty.
func (s *Store) Logo(ctx context.Context, id string) ([]byte, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	if b, err := os.ReadFile(s.logoPath(id)); err == nil && len(b) > 0 {
		return b, nil
	}
	p, err := s.Plugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Logo == "" {
		return nil, ErrNotFound
	}
	u, err := s.resolve(p.Logo)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the logo: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, logoMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > logoMaxBytes {
		return nil, errors.New("the logo is larger than 1 MB")
	}
	if err := os.MkdirAll(filepath.Dir(s.logoPath(id)), 0o755); err == nil {
		_ = os.WriteFile(s.logoPath(id), b, 0o644)
	}
	return b, nil
}

// resolve turns a path in the index ("logos/x.png") into an absolute URL next to index.json.
// An absolute URL in the index is used as it stands, as long as it is http(s).
func (s *Store) resolve(ref string) (string, error) {
	if isHTTPURL(ref) {
		return ref, nil
	}
	base, err := url.Parse(s.url)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	if rel.IsAbs() {
		return "", errors.New("the store points somewhere that isn't http(s)")
	}
	return base.ResolveReference(rel).String(), nil
}

// Download fetches a release into dir and verifies its size and sha256 before handing it back.
// A bundle that doesn't hash to what the index promised is deleted, not installed.
func (s *Store) Download(ctx context.Context, rel Release) (*os.File, error) {
	want, err := hex.DecodeString(rel.SHA256)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("the store lists no usable checksum for this version")
	}
	if !isHTTPURL(rel.URL) {
		return nil, errors.New("the store's download address isn't an http(s) URL")
	}
	dir := filepath.Join(s.dir, "downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading: %s", resp.Status)
	}
	f, err := os.CreateTemp(dir, ".store-*.zip")
	if err != nil {
		return nil, err
	}
	discard := func(err error) (*os.File, error) {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, MaxBundleBytes+1))
	if err != nil {
		return discard(err)
	}
	if n > MaxBundleBytes {
		return discard(fmt.Errorf("the bundle is larger than %d MB", MaxBundleBytes>>20))
	}
	if rel.Size > 0 && n != rel.Size {
		return discard(fmt.Errorf("the download is %d bytes, the store says %d", n, rel.Size))
	}
	if got := sum.Sum(nil); !slices.Equal(got, want) {
		return discard(fmt.Errorf("the download doesn't match the checksum in the store (got %s); it was not installed", hex.EncodeToString(got)))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return discard(err)
	}
	return f, nil
}

// Unusable says why this node can't install a release, or "" when it can. The GUI shows the reason
// instead of an Install button, which is friendlier than letting the install fail halfway.
func (p StorePlugin) Unusable(hostVersion string) string {
	if p.Image != "" {
		return "this plugin runs in its own container, so attach it rather than installing it"
	}
	r := p.Latest
	if r.API > storePluginAPI {
		return fmt.Sprintf("it needs plugin API %d and this RepeaterTastic speaks %d", r.API, storePluginAPI)
	}
	if len(r.Arches) > 0 && !slices.Contains(r.Arches, thisArch()) {
		return fmt.Sprintf("there is no build for %s", thisArch())
	}
	if r.MinHost != "" && compareVersions(hostVersion, r.MinHost) < 0 {
		return fmt.Sprintf("it needs RepeaterTastic %s or newer", r.MinHost)
	}
	for _, perm := range p.Permissions {
		if _, ok := Permissions[perm]; !ok {
			return fmt.Sprintf("it asks for a permission this version doesn't have (%s)", perm)
		}
	}
	return ""
}

// thisArch is the platform tag the store uses for the build running here.
func thisArch() string { return runtime.GOOS + "/" + runtime.GOARCH }

// compareVersions orders two dotted versions numerically, so 0.10.0 sorts above 0.9.0. A leading
// "v" and anything after a "-" or "+" are ignored, and missing parts count as zero.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(s string) []int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	var out []int
	for _, f := range strings.Split(s, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// UpdateFor reports the release that would upgrade an installed version, or false when the
// installed one is already current (or newer, which happens to anyone who installs by hand).
func (p StorePlugin) UpdateFor(installed string) (Release, bool) {
	if installed == "" || compareVersions(p.Latest.Version, installed) <= 0 {
		return Release{}, false
	}
	return p.Latest, true
}

// validID holds the store to the same ids a manifest allows, which also keeps an id from meaning
// anything in a file path before one is built from it.
func validID(id string) bool { return idPattern.MatchString(id) }

// ErrNoStore says the node has no store to read (plugins.store_url is "off").
var ErrNoStore = errors.New("the plugin store is turned off (plugins.store_url)")

// Store is the store this node reads, or nil when it is turned off.
func (m *Manager) Store() *Store { return m.store }

// StoreEntry is one store plugin next to what this node already knows about it.
type StoreEntry struct {
	StorePlugin
	// Installed is the version running here, empty when it isn't installed.
	Installed string `json:"installed,omitempty"`
	// Update is set when the store has a newer version than the one installed.
	Update bool `json:"update_available"`
	// Unusable says why this node can't install it, empty when it can.
	Unusable string `json:"unusable,omitempty"`
}

// StoreList returns the store's plugins against what's installed here. It reports the error from a
// failed fetch alongside whatever the disk cache holds, so the GUI can list the old copy and say
// it's old rather than showing nothing.
func (m *Manager) StoreList(ctx context.Context, force bool) ([]StoreEntry, time.Time, error) {
	if m.store == nil {
		return nil, time.Time{}, ErrNoStore
	}
	idx, err := m.store.Index(ctx, force)
	if idx == nil {
		return nil, time.Time{}, err
	}
	installed := map[string]string{}
	m.mu.Lock()
	for id, p := range m.plugins {
		if p.manifest != nil {
			installed[id] = p.manifest.Version
		}
	}
	m.mu.Unlock()

	out := make([]StoreEntry, 0, len(idx.Plugins))
	for _, p := range idx.Plugins {
		e := StoreEntry{StorePlugin: p, Installed: installed[p.ID], Unusable: p.Unusable(m.opt.Version)}
		_, e.Update = p.UpdateFor(e.Installed)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, m.store.Fetched(), err
}

// StoreUpdates is the installed plugins the store has a newer version of. It reads the cached
// index only, so the GUI can badge the Plugins tab without waiting on the network.
func (m *Manager) StoreUpdates() []StoreEntry {
	if m.store == nil {
		return nil
	}
	idx := m.store.Cached()
	if idx == nil {
		return nil
	}
	m.mu.Lock()
	installed := map[string]string{}
	for id, p := range m.plugins {
		if p.manifest != nil {
			installed[id] = p.manifest.Version
		}
	}
	m.mu.Unlock()

	var out []StoreEntry
	for _, p := range idx.Plugins {
		v := installed[p.ID]
		if _, ok := p.UpdateFor(v); !ok {
			continue
		}
		if why := p.Unusable(m.opt.Version); why != "" {
			continue // an update this node couldn't run isn't an update worth offering
		}
		out = append(out, StoreEntry{StorePlugin: p, Installed: v, Update: true})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// InstallFromStore downloads a plugin named in the store and installs it, the same way an upload
// would. The download is checked against the store's checksum first, so a bundle that isn't what
// the store described never reaches the unpacker. An upgrade keeps the plugin's settings and
// grants; if the new version asks for more permissions it waits for review before running.
func (m *Manager) InstallFromStore(ctx context.Context, id string) (*Manifest, error) {
	if m.store == nil {
		return nil, ErrNoStore
	}
	p, err := m.store.Plugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if why := p.Unusable(m.opt.Version); why != "" {
		return nil, fmt.Errorf("%s can't be installed here: %s", p.Name, why)
	}
	f, err := m.store.Download(ctx, p.Latest)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// Check the bundle calls itself what the store said before installing it, so what lands is
	// the plugin whose permissions were just shown to whoever pressed Install.
	if man, err := peek(f, st.Size()); err != nil {
		return nil, err
	} else if man.ID != id {
		return nil, fmt.Errorf("the store lists this as %q but the bundle calls itself %q; it was not installed", id, man.ID)
	}
	return m.Install(f, st.Size(), "the plugin store")
}
