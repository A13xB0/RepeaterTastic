package plugins

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// record is what state.json keeps for one plugin. It holds settings secrets: the file is 0600.
type record struct {
	Enabled     bool           `json:"enabled"`
	Granted     []string       `json:"granted,omitempty"`
	Settings    map[string]any `json:"settings,omitempty"`
	InstalledAt time.Time      `json:"installed_at"`
	Source      string         `json:"source,omitempty"` // upload, url, inbox, folder, cli, attach
	// Attached plugins run elsewhere and connect over TCP with a token.
	Attached  bool   `json:"attached,omitempty"`
	Name      string `json:"name,omitempty"`
	TokenHash string `json:"token_hash,omitempty"`
	// ManifestYAML is what an attached plugin last sent in Hello.
	ManifestYAML string `json:"manifest_yaml,omitempty"`
}

type stateFile struct {
	Plugins map[string]*record `json:"plugins"`
}

func loadState(path string) (stateFile, error) {
	st := stateFile{Plugins: map[string]*record{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s: %w", path, err)
	}
	if st.Plugins == nil {
		st.Plugins = map[string]*record{}
	}
	return st, nil
}

func saveState(path string, st stateFile) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func tokenMatches(tok, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1
}

// ------------------------------------------------------------------------------------ settings

// SecretMask stands in for a saved secret in the API. Sending it back keeps the saved value.
const SecretMask = "••••••••"

// resolveSettings: schema defaults, then saved values, with ${ENV} expanded in pinned ones.
func resolveSettings(schema []Setting, saved map[string]any, expandEnv bool) map[string]any {
	out := map[string]any{}
	for _, s := range schema {
		if s.Default != nil {
			out[s.Key] = s.Default
		}
	}
	for k, v := range saved {
		if str, ok := v.(string); ok && expandEnv {
			v = expandVars(str)
		}
		out[k] = v
	}
	return out
}

var envVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandVars(s string) string {
	return envVar.ReplaceAllStringFunc(s, func(m string) string { return os.Getenv(m[2 : len(m)-1]) })
}

// maskSettings hides secrets for the API and lists which secrets are set.
func maskSettings(schema []Setting, values map[string]any) (map[string]any, []string) {
	out := maps.Clone(values)
	if out == nil {
		out = map[string]any{}
	}
	var set []string
	for _, s := range schema {
		if s.Type != "secret" {
			continue
		}
		if v, ok := out[s.Key].(string); ok && v != "" {
			out[s.Key] = SecretMask
			set = append(set, s.Key)
		} else {
			delete(out, s.Key)
		}
	}
	return out, set
}

// mergeSettings checks new values against the schema and applies them over the saved ones.
// A secret sent as SecretMask (or left out) keeps its saved value.
func mergeSettings(schema []Setting, saved, in map[string]any) (map[string]any, error) {
	out := maps.Clone(saved)
	if out == nil {
		out = map[string]any{}
	}
	byKey := map[string]Setting{}
	for _, s := range schema {
		byKey[s.Key] = s
	}
	for k, v := range in {
		s, ok := byKey[k]
		if !ok {
			return nil, fmt.Errorf("the plugin has no setting %q", k)
		}
		if v == nil {
			delete(out, k)
			continue
		}
		if s.Type == "secret" && v == SecretMask {
			continue
		}
		cv, err := coerce(s, v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Label, err)
		}
		if str, ok := cv.(string); ok && str == "" {
			delete(out, k)
			continue
		}
		out[k] = cv
	}
	return out, nil
}

func coerce(s Setting, v any) (any, error) {
	switch s.Type {
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("must be true or false")
		}
		return b, nil
	case "int", "number":
		f, ok := v.(float64)
		if !ok {
			if i, isInt := v.(int); isInt {
				f, ok = float64(i), true
			}
		}
		if !ok {
			return nil, errors.New("must be a number")
		}
		if s.Type == "int" {
			if f != float64(int64(f)) {
				return nil, errors.New("must be a whole number")
			}
			return int64(f), nil
		}
		return f, nil
	}
	str, ok := v.(string)
	if !ok {
		return nil, errors.New("must be text")
	}
	str = strings.TrimSpace(str)
	switch s.Type {
	case "url":
		if str != "" {
			u, err := url.Parse(str)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
				return nil, errors.New("must be a URL like https://example.org")
			}
		}
	case "select":
		if str != "" && !slices.Contains(s.Options, str) {
			return nil, fmt.Errorf("must be one of %s", strings.Join(s.Options, ", "))
		}
	}
	if len(str) > 4096 {
		return nil, errors.New("is too long")
	}
	return str, nil
}

// missingSettings lists required settings without a value.
func missingSettings(schema []Setting, values map[string]any) []string {
	var out []string
	for _, s := range schema {
		if !s.Required {
			continue
		}
		v, ok := values[s.Key]
		if str, isStr := v.(string); !ok || v == nil || (isStr && str == "") {
			out = append(out, s.Label)
		}
	}
	return out
}
