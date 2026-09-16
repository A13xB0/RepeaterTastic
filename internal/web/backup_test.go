package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
)

// getBackup downloads a backup from the server.
func getBackup(t *testing.T, env *testEnv, tok string) backupFile {
	t.Helper()
	code, body := env.do(t, "GET", "/api/v1/backup", tok, "", "")
	var b backupFile
	if code != 200 || json.Unmarshal([]byte(body), &b) != nil {
		t.Fatalf("backup: %d %s", code, body)
	}
	return b
}

// restore posts a backup and returns the status and body.
func restore(t *testing.T, env *testEnv, tok string, b backupFile) (int, string) {
	t.Helper()
	return env.do(t, "POST", "/api/v1/restore", tok, "application/json", jsonOf(b))
}

func TestRestoreStagesBackup(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	b := getBackup(t, env, tok)
	if b.Format != "repeatertastic-backup-1" || len(b.Identities) != 1 || b.RadioIdentities != nil || !strings.Contains(b.Config, "state_dir") {
		t.Fatalf("backup = %+v", b)
	}
	extra, _ := mesh.NewIdentity(nil, "Other", "OTH")
	b.RadioIdentities = map[string][]mesh.IdentityRecord{"ls": {extra.Record()}}
	code, body := restore(t, env, tok, b)
	if code != 200 || !strings.Contains(body, `"restart_required":true`) {
		t.Fatalf("restore: %d %s", code, body)
	}
	for _, p := range []string{env.cfg.Path() + ".restore", filepath.Join(env.cfg.StateDir, "identities.json.restore"),
		filepath.Join(env.cfg.StateDir, "radios", "ls", "identities.json.restore")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not staged: %v", err)
		}
	}
	if _, st, _ := call(t, env.srv, "GET", "/api/v1/status", tok, nil); !strings.Contains(jsonOf(st["restart_reasons"]), "restored backup") {
		t.Fatalf("restart reasons = %v", st["restart_reasons"])
	}
}

func TestRestoreRefusesBadBackups(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	good := getBackup(t, env, tok)
	edits := map[string]func(b *backupFile){
		"wrong format":    func(b *backupFile) { b.Format = "other" },
		"no identities":   func(b *backupFile) { b.Identities = nil },
		"main as extra":   func(b *backupFile) { b.RadioIdentities = map[string][]mesh.IdentityRecord{"main": nil} },
		"bad radio id":    func(b *backupFile) { b.RadioIdentities = map[string][]mesh.IdentityRecord{"../x": nil} },
		"bad identity":    func(b *backupFile) { b.Identities = []mesh.IdentityRecord{{LongName: "Broken", PrivateKey: "AQ=="}} },
		"unreadable yaml": func(b *backupFile) { b.Config = "radio: [" },
		"invalid config":  func(b *backupFile) { b.Config = "log_level: loud\n" },
	}
	for name, edit := range edits {
		b := good
		edit(&b)
		if code, body := restore(t, env, tok, b); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, _ := env.do(t, "POST", "/api/v1/restore", tok, "application/json", "nope"); code != http.StatusBadRequest {
		t.Errorf("not JSON: %d", code)
	}
	// A backup without a configuration only stages identities.
	b := good
	b.Config = ""
	if code, _ := restore(t, env, tok, b); code != 200 {
		t.Fatalf("identities only: %d", code)
	}
	if _, err := os.Stat(env.cfg.Path() + ".restore"); !os.IsNotExist(err) {
		t.Fatalf("config staged without one in the backup: %v", err)
	}
}

func TestRestoreFailsWhenItCantWrite(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	b := getBackup(t, env, tok)
	// The config can't be staged: its .restore name is a folder.
	if err := os.MkdirAll(env.cfg.Path()+".restore/x", 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _ := restore(t, env, tok, b); code != http.StatusInternalServerError {
		t.Fatalf("unwritable config: %d", code)
	}
	// Identities can't be staged: a file is where a radio's folder goes.
	if err := os.WriteFile(filepath.Join(env.cfg.StateDir, "radios"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	extra, _ := mesh.NewIdentity(nil, "Other", "OTH")
	b.RadioIdentities = map[string][]mesh.IdentityRecord{"ls": {extra.Record()}}
	if code, _ := restore(t, env, tok, b); code != http.StatusInternalServerError {
		t.Fatalf("unwritable identities: %d", code)
	}
	if err := stageIdentities(filepath.Join(env.cfg.StateDir, "identities.json.restore"), map[string][]mesh.IdentityRecord{"main": nil}); err == nil {
		t.Fatal("staged into a file")
	}
}
