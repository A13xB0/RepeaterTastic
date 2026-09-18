package plugins

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Bundle limits: a plugin is a program, a logo and maybe a small web panel.
const (
	MaxBundleBytes   = 100 << 20 // compressed upload or download
	maxUnpackedBytes = 250 << 20
	maxBundleFiles   = 2000
)

// unpack extracts a bundle into a new folder under parent and returns it with its manifest.
// The bundle's files may sit at the root or inside one top-level folder. Anything that could
// escape the folder (absolute paths, "..", links, devices) rejects the whole bundle.
func unpack(r io.ReaderAt, size int64, parent string) (string, *Manifest, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", nil, errors.New("not a zip file")
	}
	if len(zr.File) > maxBundleFiles {
		return "", nil, fmt.Errorf("the bundle has more than %d files", maxBundleFiles)
	}
	prefix, err := bundlePrefix(zr.File)
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(parent, ".staging-"+randomHex(6))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	fail := func(err error) (string, *Manifest, error) {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	var total int64
	for _, f := range zr.File {
		if err := unpackFile(f, prefix, dir, &total); err != nil {
			return fail(err)
		}
	}
	m, err := loadUnpacked(dir)
	if err != nil {
		return fail(err)
	}
	return dir, m, nil
}

// unpackFile extracts one bundle entry into dir, adding its size to total.
func unpackFile(f *zip.File, prefix, dir string, total *int64) error {
	name := strings.TrimPrefix(f.Name, prefix)
	if name == "" || strings.HasSuffix(f.Name, "/") {
		return nil
	}
	if !localPath(name) {
		return fmt.Errorf("unsafe path in bundle: %q", f.Name)
	}
	mode := f.Mode()
	if !mode.IsRegular() {
		return fmt.Errorf("%s is not a regular file (links and devices aren't allowed)", f.Name)
	}
	*total += int64(f.UncompressedSize64)
	if *total > maxUnpackedBytes || f.UncompressedSize64 > maxUnpackedBytes {
		return fmt.Errorf("the bundle unpacks to more than %d MB", maxUnpackedBytes>>20)
	}
	dst := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if mode.Perm()&0o111 != 0 {
		perm = 0o755
	}
	if err := extract(f, dst, perm); err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	return nil
}

// loadUnpacked reads and checks the manifest of an unpacked bundle.
func loadUnpacked(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("the bundle has no %s", ManifestFile)
	}
	m, err := ParseManifest(b)
	if err != nil {
		return nil, err
	}
	if err := m.checkFiles(dir); err != nil {
		return nil, err
	}
	if exe, err := m.ExecPath(); err == nil {
		// Zips made on Windows lose the executable bit.
		if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(exe)), 0o755); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// bundlePrefix is "folder/" when every entry sits in one top-level folder holding plugin.yaml.
// peek reads a bundle's manifest without unpacking it, for checks that should happen before
// anything is written to disk. It doesn't check the bundle's files, so the manifest it returns is
// only good for reading what the bundle says it is.
func peek(r io.ReaderAt, size int64) (*Manifest, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, errors.New("not a zip file")
	}
	prefix, err := bundlePrefix(zr.File)
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != prefix+ManifestFile {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		if err != nil {
			return nil, err
		}
		return ParseManifest(b)
	}
	return nil, fmt.Errorf("the bundle has no %s", ManifestFile)
}

func bundlePrefix(files []*zip.File) (string, error) {
	for _, f := range files {
		if f.Name == ManifestFile {
			return "", nil
		}
	}
	top := ""
	for _, f := range files {
		first, _, _ := strings.Cut(f.Name, "/")
		if top == "" {
			top = first
		} else if first != top {
			return "", fmt.Errorf("the bundle has no %s at its root", ManifestFile)
		}
	}
	for _, f := range files {
		if f.Name == top+"/"+ManifestFile {
			return top + "/", nil
		}
	}
	return "", fmt.Errorf("the bundle has no %s at its root", ManifestFile)
}

func extract(f *zip.File, dst string, perm os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	// The header's size can lie; never write more than it claimed.
	n, err := io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > int64(f.UncompressedSize64) {
		err = errors.New("file is larger than its zip header says")
	}
	return err
}

// Download fetches a bundle from an http(s) URL into a temporary file under dir.
func Download(ctx context.Context, url, dir string) (*os.File, error) {
	if !isHTTPURL(url) {
		return nil, errors.New("the URL must start with https:// or http://")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading: %s", resp.Status)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".download-*.zip")
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, MaxBundleBytes+1))
	if err == nil && n > MaxBundleBytes {
		err = fmt.Errorf("the bundle is larger than %d MB", MaxBundleBytes>>20)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// isHTTPURL reports whether s is an http(s) URL rather than a file path.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// bundleName is a readable name for a download or upload, for logs.
func bundleName(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return path.Base(s)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
