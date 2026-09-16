package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var goodBundle = map[string]string{"plugin.yaml": echoManifest, "run.sh": "#!/bin/sh\n"}

// rawBundle is goodBundle plus one stored entry whose header claims size bytes but holds body.
func rawBundle(t *testing.T, name string, size uint64, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range goodBundle {
		w, _ := zw.Create(n)
		_, _ = io.WriteString(w, b)
	}
	h := &zip.FileHeader{Name: name, Method: zip.Store, UncompressedSize64: size,
		CompressedSize64: uint64(len(body)), CRC32: crc32.ChecksumIEEE([]byte(body))}
	h.SetMode(0o644)
	w, err := zw.CreateRaw(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, body)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func unpackErr(t *testing.T, b []byte) error {
	t.Helper()
	_, _, err := unpack(bytes.NewReader(b), int64(len(b)), t.TempDir())
	return err
}

func TestUnpackMoreRejections(t *testing.T) {
	many := map[string]string{"plugin.yaml": echoManifest}
	for i := 0; i <= maxBundleFiles; i++ {
		many[fmt.Sprintf("f%d", i)] = ""
	}
	twoTops := map[string]string{"a/plugin.yaml": echoManifest, "b/run.sh": ""}
	noYaml := map[string]string{"a/run.sh": "", "a/other": ""}
	cases := map[string]struct {
		b    []byte
		want string
	}{
		"not zip":       {[]byte("hello"), "not a zip"},
		"too many":      {zipBundle(t, many, nil), "more than"},
		"two folders":   {zipBundle(t, twoTops, nil), "at its root"},
		"folder no yml": {zipBundle(t, noYaml, nil), "at its root"},
		"lying header":  {rawBundle(t, "data.bin", 2, "12345"), "data.bin"}, // archive/zip refuses the extra bytes
		"huge":          {rawBundle(t, "data.bin", maxUnpackedBytes+1, "x"), "unpacks to more than"},
		"bad manifest":  {zipBundle(t, map[string]string{"plugin.yaml": "id: ["}, nil), "plugin.yaml"},
	}
	for name, c := range cases {
		if err := unpackErr(t, c.b); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
	}
}

func TestUnpackSkipsFolderEntriesAndChecksumErrors(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	_, _ = zw.Create("docs/")
	for n, b := range goodBundle {
		w, _ := zw.Create(n)
		_, _ = io.WriteString(w, b)
	}
	w, _ := zw.Create("docs/readme.txt")
	_, _ = io.WriteString(w, "hi")
	_ = zw.Close()
	dir, _, err := unpack(bytes.NewReader(buf.Bytes()), int64(buf.Len()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "docs", "readme.txt")); string(b) != "hi" {
		t.Fatalf("readme %q", b)
	}
	// A stored entry whose CRC doesn't match fails to extract.
	bad := rawBundle(t, "data.bin", 3, "abc")
	i := bytes.LastIndex(bad, []byte("abc"))
	bad[i] = 'x'
	if err := unpackErr(t, bad); err == nil || !strings.Contains(err.Error(), "data.bin") {
		t.Fatalf("checksum: %v", err)
	}
}

func TestUnpackParentUnwritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	b := zipBundle(t, goodBundle, nil)
	if _, _, err := unpack(bytes.NewReader(b), int64(len(b)), file); err == nil {
		t.Fatal("unpacked under a file")
	}
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/b.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "zipdata")
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "dl")
	f, err := Download(context.Background(), srv.URL+"/b.zip", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if b, _ := os.ReadFile(f.Name()); string(b) != "zipdata" || filepath.Dir(f.Name()) != dir {
		t.Fatalf("downloaded %q to %s", b, f.Name())
	}

	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	cases := map[string]struct{ url, dir, want string }{
		"not http":  {"ftp://x/b.zip", dir, "must start with"},
		"bad url":   {"http://[::1/b.zip", dir, ""},
		"not found": {srv.URL + "/nope", dir, "404"},
		"bad dir":   {srv.URL + "/b.zip", filepath.Join(file, "sub"), ""},
	}
	for name, c := range cases {
		if _, err := Download(context.Background(), c.url, c.dir); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	srv.Close()
	if _, err := Download(context.Background(), srv.URL+"/b.zip", dir); err == nil || !strings.Contains(err.Error(), "downloading") {
		t.Fatalf("closed server: %v", err)
	}
}

func TestBundleNameAndURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://h/x/p-1.zip?sig=1#frag": "p-1.zip",
		"/tmp/a/b.zip":                   "b.zip",
	} {
		if got := bundleName(in); got != want {
			t.Errorf("bundleName(%q) = %q", in, got)
		}
	}
	if !isHTTPURL("http://x") || !isHTTPURL("https://x") || isHTTPURL("/x") {
		t.Fatal("isHTTPURL")
	}
}

func TestInstallRejects(t *testing.T) {
	m := newTestManager(t, nil)
	if _, err := m.Install(bytes.NewReader(nil), MaxBundleBytes+1, "test"); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := m.Install(bytes.NewReader([]byte("x")), 1, "test"); err == nil {
		t.Fatal("installed garbage")
	}
	if _, err := m.Attach("echo", "", nil); err != nil {
		t.Fatal(err)
	}
	b := zipBundle(t, goodBundle, nil)
	if _, err := m.Install(bytes.NewReader(b), int64(len(b)), "test"); !errors.Is(err, ErrConflict) {
		t.Fatalf("over an attached plugin: %v", err)
	}
}
