package web

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const tokenLifetime = 7 * 24 * time.Hour

// pbkdf2Iterations is the password hash's work factor for new passwords (a variable so tests can
// hash faster; saved passwords keep the count they were hashed with).
var pbkdf2Iterations = 210_000

type apiToken struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hash     string `json:"hash"`
	Created  int64  `json:"created"`
	LastUsed int64  `json:"last_used,omitempty"`
}

type authFile struct {
	PasswordHash string     `json:"password_hash,omitempty"`
	Salt         string     `json:"salt,omitempty"`
	Iterations   int        `json:"iterations,omitempty"`
	JWTSecret    string     `json:"jwt_secret"`
	Tokens       []apiToken `json:"tokens"`
}

// Auth stores the admin password, the JWT signing key and API tokens in the state dir.
type Auth struct {
	mu   sync.Mutex
	path string
	f    authFile
	ttl  func() time.Duration
}

func loadAuth(stateDir string) (*Auth, error) {
	a := &Auth{path: filepath.Join(stateDir, "auth.json")}
	b, err := os.ReadFile(a.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		a.f.JWTSecret = hex.EncodeToString(randBytes(32))
		return a, a.save()
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &a.f); err != nil {
		return nil, err
	}
	if a.f.JWTSecret == "" {
		a.f.JWTSecret = hex.EncodeToString(randBytes(32))
	}
	return a, nil
}

func (a *Auth) save() error {
	b, _ := json.MarshalIndent(a.f, "", "  ")
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func (a *Auth) SetupNeeded() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f.PasswordHash == ""
}

func (a *Auth) SetPassword(pw string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	salt := randBytes(16)
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iterations, 32)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.f.PasswordHash = hex.EncodeToString(dk)
	a.f.Salt = hex.EncodeToString(salt)
	a.f.Iterations = pbkdf2Iterations
	a.f.JWTSecret = hex.EncodeToString(randBytes(32)) // invalidate old sessions
	return a.save()
}

func (a *Auth) CheckPassword(pw string) bool {
	a.mu.Lock()
	hash, saltHex, iter := a.f.PasswordHash, a.f.Salt, a.f.Iterations
	a.mu.Unlock()
	if hash == "" {
		return false
	}
	salt, _ := hex.DecodeString(saltHex)
	want, _ := hex.DecodeString(hash)
	dk, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(dk, want) == 1
}

// RevokeSessions signs every browser out by rotating the session signing secret. API tokens
// are unaffected.
func (a *Auth) RevokeSessions() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.f.JWTSecret = hex.EncodeToString(randBytes(32))
	return a.save()
}

// ---------------------------------------------------------------------------------------- JWT

var b64 = base64.RawURLEncoding

func (a *Auth) IssueJWT() (string, time.Time) {
	life := tokenLifetime
	if a.ttl != nil && a.ttl() > 0 {
		life = a.ttl()
	}
	exp := time.Now().Add(life)
	header := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"sub": "admin", "exp": exp.Unix(), "iat": time.Now().Unix()})
	payload := header + "." + b64.EncodeToString(claims)
	return payload + "." + b64.EncodeToString(a.sign(payload)), exp
}

func (a *Auth) sign(s string) []byte {
	a.mu.Lock()
	key, _ := hex.DecodeString(a.f.JWTSecret)
	a.mu.Unlock()
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func (a *Auth) validJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, a.sign(parts[0]+"."+parts[1])) {
		return false
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	return json.Unmarshal(body, &c) == nil && time.Now().Unix() < c.Exp
}

// ---------------------------------------------------------------------------------- API tokens

const tokenPrefix = "rpt_"

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

func (a *Auth) CreateToken(name string) (apiToken, string, error) {
	secret := tokenPrefix + b64.EncodeToString(randBytes(24))
	t := apiToken{ID: hex.EncodeToString(randBytes(6)), Name: name, Hash: hashToken(secret), Created: time.Now().UnixMilli()}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.f.Tokens = append(a.f.Tokens, t)
	return t, secret, a.save()
}

func (a *Auth) Tokens() []apiToken {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]apiToken, len(a.f.Tokens))
	copy(out, a.f.Tokens)
	return out
}

func (a *Auth) DeleteToken(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, t := range a.f.Tokens {
		if t.ID == id {
			a.f.Tokens = append(a.f.Tokens[:i], a.f.Tokens[i+1:]...)
			_ = a.save()
			return true
		}
	}
	return false
}

// Valid accepts a JWT or an API token.
func (a *Auth) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	if strings.HasPrefix(tok, tokenPrefix) {
		h := hashToken(tok)
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, t := range a.f.Tokens {
			if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) == 1 {
				now := time.Now().UnixMilli()
				if now-t.LastUsed > int64(time.Hour/time.Millisecond) {
					a.f.Tokens[i].LastUsed = now
					_ = a.save()
				}
				return true
			}
		}
		return false
	}
	return a.validJWT(tok)
}
