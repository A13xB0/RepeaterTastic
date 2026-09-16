package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const minimalManifest = "id: demo\napi: 1\n"

func TestParseManifestErrors(t *testing.T) {
	cases := map[string]string{
		"yaml":              "id: [",
		"api":               "id: demo\napi: 2\n",
		"duplicate key":     minimalManifest + "settings:\n  - key: a\n  - key: a\n",
		"bad key":           minimalManifest + "settings:\n  - key: A\n",
		"bad type":          minimalManifest + "settings:\n  - key: a\n    type: colour\n",
		"select no options": minimalManifest + "settings:\n  - key: a\n    type: select\n",
		"logo outside":      minimalManifest + "logo: ../logo.png\n",
		"logo type":         minimalManifest + "logo: logo.gif\n",
		"panel outside":     minimalManifest + "ui:\n  panel: /panel.html\n",
		"exec outside":      minimalManifest + "run:\n  managed:\n    exec: ../bin\n",
		"exec empty":        minimalManifest + "run:\n  managed: {}\n",
	}
	for name, y := range cases {
		if _, err := ParseManifest([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseManifestDefaults(t *testing.T) {
	m, err := ParseManifest([]byte(minimalManifest + "settings:\n  - key: a\n  - key: b\n    type: multiselect\n    options: [x]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "demo" || m.Settings[0].Type != "string" || m.Settings[0].Label != "a" {
		t.Fatalf("defaults not filled in: %+v", m)
	}
	if _, err := m.ExecPath(); !errors.Is(err, errNoManagedRun) {
		t.Fatalf("ExecPath: %v", err)
	}
}

func TestExecPathPlaceholders(t *testing.T) {
	m := &Manifest{Run: Run{Managed: &Managed{Exec: "bin/p-{os}-{arch}"}}}
	got, err := m.ExecPath()
	if err != nil || got != "bin/p-"+runtime.GOOS+"-"+runtime.GOARCH {
		t.Fatalf("%q %v", got, err)
	}
}

func TestCheckFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "logo.png"), "png")
	writeFile(t, filepath.Join(dir, "ui", "index.html"), "<p>")
	if err := os.Mkdir(filepath.Join(dir, "bindir"), 0o755); err != nil {
		t.Fatal(err)
	}
	full := &Manifest{Logo: "logo.png", UI: UI{Panel: "ui/index.html"}}
	if err := full.checkFiles(dir); err != nil {
		t.Fatalf("all present: %v", err)
	}
	cases := map[string]struct {
		m    Manifest
		want string
	}{
		"logo":     {Manifest{Logo: "missing.png"}, "logo"},
		"panel":    {Manifest{UI: UI{Panel: "nope.html"}}, "ui.panel"},
		"exec":     {Manifest{Run: Run{Managed: &Managed{Exec: "missing"}}}, "no program"},
		"exec dir": {Manifest{Run: Run{Managed: &Managed{Exec: "bindir"}}}, "no program"},
	}
	for name, c := range cases {
		if err := c.m.checkFiles(dir); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"a/b": true, "": false, "a\\b": false, "../a": false, "/a": false, "a/./b": false, "a//b": false,
	} {
		if localPath(p) != want {
			t.Errorf("localPath(%q) != %v", p, want)
		}
	}
}
