// Backup and restore.

package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"gopkg.in/yaml.v3"
)

type backupFile struct {
	Format     string                `json:"format"`
	Created    int64                 `json:"created"`
	Version    string                `json:"version"`
	Config     string                `json:"config_yaml"`
	Identities []mesh.IdentityRecord `json:"identities"` // the main radio's
	// RadioIdentities holds every other radio's identities by radio id.
	RadioIdentities map[string][]mesh.IdentityRecord `json:"radio_identities,omitempty"`
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.Lock()
	y, _ := yaml.Marshal(s.cfg)
	s.cfgMu.Unlock()
	b := backupFile{Format: "repeatertastic-backup-1", Created: time.Now().UnixMilli(), Version: s.opt.Version, Config: string(y)}
	for _, rc := range s.radios {
		var recs []mesh.IdentityRecord
		for _, id := range rc.host.Identities() {
			recs = append(recs, id.Record())
		}
		if rc == s.radios[0] {
			b.Identities = recs
			continue
		}
		if b.RadioIdentities == nil {
			b.RadioIdentities = map[string][]mesh.IdentityRecord{}
		}
		b.RadioIdentities[rc.id] = recs
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="repeatertastic-backup-%s.json"`, time.Now().Format("2006-01-02")))
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var b backupFile
	// A backup with several radios' identities is bigger than readJSON's usual 1 MB.
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid backup file: "+err.Error())
		return
	}
	sets, err := backupIdentitySets(b)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Nothing is replaced under the running daemon (it would save its own state back over it):
	// the backup is staged next to each file and moved into place when the daemon next starts.
	cfgYAML, err := backupConfigYAML(b)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfgMu.Lock()
	stateDir, cfgPath := s.cfg.StateDir, s.cfg.Path()
	s.cfgMu.Unlock()
	if err := stageIdentities(stateDir, sets); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfgYAML != nil && cfgPath != "" {
		if err := os.WriteFile(cfgPath+".restore", cfgYAML, 0o600); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.restorePending.Store(true)
	s.log.Warn("backup staged; it replaces the configuration and identities when the daemon restarts")
	writeJSON(w, http.StatusOK, map[string]any{"restart_required": true})
}

// backupIdentitySets checks a backup's identities and returns them by radio id.
func backupIdentitySets(b backupFile) (map[string][]mesh.IdentityRecord, error) {
	if b.Format != "repeatertastic-backup-1" || len(b.Identities) == 0 {
		return nil, errors.New("not a RepeaterTastic backup file")
	}
	sets := map[string][]mesh.IdentityRecord{config.MainRadioID: b.Identities}
	for id, recs := range b.RadioIdentities {
		if id == config.MainRadioID || !radioIDOK(id) {
			return nil, errors.New("backup names an invalid radio " + id)
		}
		sets[id] = recs
	}
	for _, recs := range sets {
		for _, rec := range recs {
			if _, err := mesh.IdentityFromRecord(rec); err != nil {
				return nil, errors.New("backup contains an invalid identity: " + err.Error())
			}
		}
	}
	return sets, nil
}

// backupConfigYAML returns the backup's configuration once it parses and validates, or nil if it has none.
func backupConfigYAML(b backupFile) ([]byte, error) {
	if b.Config == "" {
		return nil, nil
	}
	c := config.Default()
	if err := yaml.Unmarshal([]byte(b.Config), c); err != nil {
		return nil, errors.New("the backup's configuration can't be read: " + err.Error())
	}
	if err := c.Validate(); err != nil {
		return nil, errors.New("the backup's configuration isn't valid: " + err.Error())
	}
	return []byte(b.Config), nil
}

// stageIdentities writes each radio's identities next to its state file, to be moved into place at start-up.
func stageIdentities(stateDir string, sets map[string][]mesh.IdentityRecord) error {
	for id, recs := range sets {
		dir := stateDir
		if id != config.MainRadioID {
			dir = filepath.Join(stateDir, "radios", id) // where RadioConfigs puts that radio's state
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(recs, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "identities.json.restore"), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
