package plugins

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadState(t *testing.T) {
	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "missing.json"))
	if err != nil || st.Plugins == nil {
		t.Fatalf("missing file: %v %v", st, err)
	}
	if _, err := loadState(dir); err == nil {
		t.Fatal("reading a directory succeeded")
	}
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, "{nope")
	if _, err := loadState(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("bad JSON: %v", err)
	}
	empty := filepath.Join(dir, "empty.json")
	writeFile(t, empty, `{"plugins": null}`)
	if st, err := loadState(empty); err != nil || st.Plugins == nil {
		t.Fatalf("null plugins: %v %v", st, err)
	}
}

func TestSaveState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st := stateFile{Plugins: map[string]*record{"a": {Enabled: true, Settings: map[string]any{"k": "v"}}}}
	if err := saveState(path, st); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	back, err := loadState(path)
	if err != nil || !back.Plugins["a"].Enabled || back.Plugins["a"].Settings["k"] != "v" {
		t.Fatalf("round trip: %+v %v", back.Plugins["a"], err)
	}
	// Unchanged state leaves the file alone, even when it can't be written.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := saveState(path, st); err != nil {
		t.Fatalf("unchanged save: %v", err)
	}
	if os.Geteuid() != 0 {
		st.Plugins["b"] = &record{}
		if err := saveState(path, st); err == nil {
			t.Fatal("wrote into a read-only folder")
		}
	}
	if err := saveState(path, stateFile{Plugins: map[string]*record{"x": {Settings: map[string]any{"f": func() {}}}}}); err == nil {
		t.Fatal("encoded a func")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTokens(t *testing.T) {
	h := hashToken("abc")
	if len(h) != 64 || h != hashToken("abc") || h == hashToken("abd") {
		t.Fatalf("hash %q", h)
	}
	if !tokenMatches("x", "x") || tokenMatches("x", "y") || tokenMatches("", "") {
		t.Fatal("tokenMatches")
	}
}

type coerceCase struct {
	name    string
	s       Setting
	in      any
	want    any
	wantErr string
}

func TestCoerce(t *testing.T) {
	sel := Setting{Type: "select", Options: []string{"a", "b"}}
	ids := Setting{Type: "identities"}
	cases := []coerceCase{
		{"bool", Setting{Type: "bool"}, true, true, ""},
		{"bool bad", Setting{Type: "bool"}, "yes", nil, "true or false"},
		{"int from float", Setting{Type: "int"}, 3.0, int64(3), ""},
		{"int from int", Setting{Type: "int"}, 4, int64(4), ""},
		{"int fraction", Setting{Type: "int"}, 3.5, nil, "whole"},
		{"number", Setting{Type: "number"}, 2.5, 2.5, ""},
		{"number bad", Setting{Type: "number"}, "2", nil, "must be a number"},
		{"text trimmed", Setting{Type: "string"}, "  hi ", "hi", ""},
		{"text bad", Setting{Type: "string"}, 1, nil, "must be text"},
		{"text long", Setting{Type: "secret"}, strings.Repeat("x", 4097), nil, "too long"},
		{"url ok", Setting{Type: "url"}, "wss://h.example/x", "wss://h.example/x", ""},
		{"url empty", Setting{Type: "url"}, "", "", ""},
		{"url no host", Setting{Type: "url"}, "https://", nil, "must be a URL"},
		{"url scheme", Setting{Type: "url"}, "ftp://h.example", nil, "must be a URL"},
		{"url unparsable", Setting{Type: "url"}, "http://[::1", nil, "must be a URL"},
		{"select ok", sel, "b", "b", ""},
		{"select bad", sel, "c", nil, "one of a, b"},
		{"list from strings", ids, []string{"!1", "!1", "!2"}, []string{"!1", "!2"}, ""},
		{"list unchecked", ids, []any{"!9"}, []string{"!9"}, ""},
		{"list not list", ids, "!1", nil, "must be a list"},
		{"list not names", ids, []any{1}, nil, "list of names"},
	}
	for _, c := range cases {
		checkCoerce(t, c, siteChoices{})
	}
	checkCoerce(t, coerceCase{"unknown identity", ids, []any{"!9"}, nil, "no identity !9"}, siteChoices{identities: []string{"!1"}})
}

func checkCoerce(t *testing.T, c coerceCase, choices siteChoices) {
	t.Helper()
	got, err := coerce(c.s, c.in, choices)
	if c.wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
		}
		return
	}
	if err != nil || !reflect.DeepEqual(got, c.want) {
		t.Errorf("%s: got %#v %v, want %#v", c.name, got, err, c.want)
	}
}

func TestMergeSettingsClears(t *testing.T) {
	schema := []Setting{{Key: "a", Label: "A", Type: "string"}, {Key: "n", Label: "N", Type: "int"}}
	got, err := mergeSettings(schema, map[string]any{"a": "x", "n": int64(1)}, map[string]any{"a": "", "n": nil}, siteChoices{})
	if err != nil || len(got) != 0 {
		t.Fatalf("cleared: %v %v", got, err)
	}
	if _, err := mergeSettings(schema, nil, map[string]any{"n": "one"}, siteChoices{}); err == nil || !strings.HasPrefix(err.Error(), "N: ") {
		t.Fatalf("bad value: %v", err)
	}
}

func TestMaskSettingsEmpty(t *testing.T) {
	schema := []Setting{{Key: "k", Type: "secret"}}
	out, set := maskSettings(schema, nil)
	if len(out) != 0 || len(set) != 0 {
		t.Fatalf("%v %v", out, set)
	}
	out, _ = maskSettings(schema, map[string]any{"k": ""})
	if _, ok := out["k"]; ok {
		t.Fatal("an empty secret was shown")
	}
}

func TestResolveSettingsDefaults(t *testing.T) {
	schema := []Setting{{Key: "a", Default: "d"}, {Key: "b"}}
	got := resolveSettings(schema, map[string]any{"b": "${NOT_SET_ANYWHERE_RT}x"}, false)
	if got["a"] != "d" || got["b"] != "${NOT_SET_ANYWHERE_RT}x" {
		t.Fatalf("%v", got)
	}
}

func TestMissingSettings(t *testing.T) {
	schema := []Setting{
		{Key: "a", Label: "A", Required: true},
		{Key: "b", Label: "B", Required: true},
		{Key: "c", Label: "C", Required: true},
		{Key: "d", Label: "D", Required: true},
		{Key: "e", Label: "E"},
	}
	got := missingSettings(schema, map[string]any{"a": "", "b": []any{}, "c": nil, "d": 0})
	if strings.Join(got, ",") != "A,B,C" {
		t.Fatalf("missing %v", got)
	}
}

func TestEmptyValue(t *testing.T) {
	for v, want := range map[any]bool{"": true, "x": false, 0: false, false: false} {
		if emptyValue(v) != want {
			t.Errorf("emptyValue(%#v) != %v", v, want)
		}
	}
	if !emptyValue([]string{}) || emptyValue([]string{"a"}) {
		t.Error("emptyValue on lists")
	}
}

func TestSettingNames(t *testing.T) {
	if got := settingNames([]string{"a"}); len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if got := settingNames([]any{"a", 1, "b"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("%v", got)
	}
	if got := settingNames(3); got != nil {
		t.Fatalf("%v", got)
	}
}
