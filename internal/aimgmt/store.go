package aimgmt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
	"tradebot/internal/ai"
	"tradebot/internal/profile"
)

type Version struct {
	ID              int     `json:"id"`
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	MaxTokens       int     `json:"max_tokens"`
	Temperature     float64 `json:"temperature"`
	ReasoningBudget int     `json:"reasoning_budget"`
	ReasoningEffort string  `json:"reasoning_effort"`
	BaseURL         string  `json:"base_url"`
	TimeoutSeconds  int     `json:"timeout_seconds"`
	Enabled         bool    `json:"enabled"`
	Active          bool    `json:"active"`
	HasKey          bool    `json:"has_key"`
	CreatedAt       string  `json:"created_at"`
}
type Store struct {
	DB  *sql.DB
	Key []byte
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,120}$`)

func New(db *sql.DB, hexKey string) (*Store, error) {
	key, e := hex.DecodeString(hexKey)
	if e != nil || len(key) != 32 {
		return nil, errors.New("AI_CONFIG_MASTER_KEY must be 32-byte hex")
	}
	s := &Store{DB: db, Key: key}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS ai_versions(id INTEGER PRIMARY KEY AUTOINCREMENT,provider TEXT NOT NULL,model TEXT NOT NULL,max_tokens INTEGER NOT NULL,temperature REAL NOT NULL,reasoning_budget INTEGER NOT NULL,reasoning_effort TEXT NOT NULL,key_cipher BLOB NOT NULL,active INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL)`)
	for _, alter := range []string{`ALTER TABLE ai_versions ADD COLUMN base_url TEXT NOT NULL DEFAULT ''`, `ALTER TABLE ai_versions ADD COLUMN timeout_seconds INTEGER NOT NULL DEFAULT 150`, `ALTER TABLE ai_versions ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`} {
		if _, alterErr := db.Exec(alter); alterErr != nil && !regexp.MustCompile(`(?i)duplicate column`).MatchString(alterErr.Error()) {
			return nil, alterErr
		}
	}
	_, e = db.Exec(`UPDATE ai_versions SET base_url=CASE provider WHEN 'nvidia' THEN 'https://integrate.api.nvidia.com/v1' WHEN 'openai' THEN 'https://api.openai.com/v1' WHEN 'google' THEN 'https://generativelanguage.googleapis.com/v1beta' ELSE base_url END WHERE base_url=''`)
	if e != nil {
		return nil, e
	}
	_, e = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS one_active_ai ON ai_versions(active) WHERE active=1`)
	return s, e
}
func validate(v Version) error {
	if v.BaseURL == "" {
		v.BaseURL = defaultBaseURL(v.Provider)
	}
	if v.TimeoutSeconds == 0 {
		v.TimeoutSeconds = 150
	}
	u, urlErr := url.Parse(v.BaseURL)
	if v.Provider != "nvidia" && v.Provider != "openai" && v.Provider != "google" || !modelPattern.MatchString(v.Model) || v.MaxTokens < 128 || v.MaxTokens > 8192 || v.Temperature < 0 || v.Temperature > 2 || v.ReasoningBudget < 0 || v.ReasoningBudget > 4096 {
		return errors.New("invalid AI settings")
	}
	if urlErr != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || v.TimeoutSeconds < 10 || v.TimeoutSeconds > 300 {
		return errors.New("invalid AI endpoint")
	}
	if v.ReasoningEffort != "none" && v.ReasoningEffort != "low" && v.ReasoningEffort != "medium" && v.ReasoningEffort != "high" {
		return errors.New("invalid reasoning effort")
	}
	return nil
}
func defaultBaseURL(provider string) string {
	return map[string]string{"nvidia": "https://integrate.api.nvidia.com/v1", "openai": "https://api.openai.com/v1", "google": "https://generativelanguage.googleapis.com/v1beta"}[provider]
}
func (s *Store) seal(clear string) ([]byte, error) {
	block, e := aes.NewCipher(s.Key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return a.Seal(nonce, nonce, []byte(clear), nil), nil
}
func (s *Store) open(ciphertext []byte) (string, error) {
	block, e := aes.NewCipher(s.Key)
	if e != nil {
		return "", e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	if len(ciphertext) < a.NonceSize() {
		return "", errors.New("invalid encrypted key")
	}
	raw, e := a.Open(nil, ciphertext[:a.NonceSize()], ciphertext[a.NonceSize():], nil)
	return string(raw), e
}

// SealSecret and OpenSecret allow other server-side configuration stores to use
// the same AES-GCM master key without exposing plaintext to browser APIs.
func (s *Store) SealSecret(clear string) ([]byte, error)      { return s.seal(clear) }
func (s *Store) OpenSecret(ciphertext []byte) (string, error) { return s.open(ciphertext) }
func (s *Store) Draft(admin int64, v Version, newKey string) (Version, error) {
	if admin != profile.AdminID {
		return Version{}, errors.New("forbidden")
	}
	if e := validate(v); e != nil {
		return Version{}, e
	}
	if v.BaseURL == "" {
		v.BaseURL = defaultBaseURL(v.Provider)
	}
	if v.TimeoutSeconds == 0 {
		v.TimeoutSeconds = 150
	}
	v.Enabled = true
	var sealed []byte
	var e error
	if newKey == "" {
		e = s.DB.QueryRow(`SELECT key_cipher FROM ai_versions WHERE active=1 AND provider=?`, v.Provider).Scan(&sealed)
		if e != nil {
			return Version{}, errors.New("replacement API key required for this provider")
		}
	} else {
		sealed, e = s.seal(newKey)
		if e != nil {
			return Version{}, e
		}
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return Version{}, e
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	r, e := tx.Exec(`INSERT INTO ai_versions(provider,model,max_tokens,temperature,reasoning_budget,reasoning_effort,key_cipher,base_url,timeout_seconds,enabled,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.Provider, v.Model, v.MaxTokens, v.Temperature, v.ReasoningBudget, v.ReasoningEffort, sealed, v.BaseURL, v.TimeoutSeconds, boolInt(v.Enabled), now)
	if e != nil {
		return Version{}, e
	}
	id, _ := r.LastInsertId()
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'ai_draft',?,?)`, admin, fmt.Sprintf("%d", id), now); e != nil {
		return Version{}, e
	}
	if e = tx.Commit(); e != nil {
		return Version{}, e
	}
	v.ID = int(id)
	v.HasKey = true
	v.CreatedAt = now
	return v, nil
}
func (s *Store) SetEnabled(admin int64, id int, enabled bool) error {
	if admin != profile.AdminID {
		return errors.New("forbidden")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var active int
	if e = tx.QueryRow(`SELECT active FROM ai_versions WHERE id=?`, id).Scan(&active); e != nil {
		return e
	}
	if !enabled && active == 1 {
		return errors.New("active provider cannot be disabled")
	}
	if _, e = tx.Exec(`UPDATE ai_versions SET enabled=? WHERE id=?`, boolInt(enabled), id); e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'ai_enabled',?,?)`, admin, fmt.Sprintf("version=%d enabled=%t", id, enabled), time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) List(admin int64) ([]Version, error) {
	if admin != profile.AdminID {
		return nil, errors.New("forbidden")
	}
	rows, e := s.DB.Query(`SELECT id,provider,model,max_tokens,temperature,reasoning_budget,reasoning_effort,base_url,timeout_seconds,enabled,active,created_at FROM ai_versions ORDER BY id DESC LIMIT 50`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var active, enabled int
		if e = rows.Scan(&v.ID, &v.Provider, &v.Model, &v.MaxTokens, &v.Temperature, &v.ReasoningBudget, &v.ReasoningEffort, &v.BaseURL, &v.TimeoutSeconds, &enabled, &active, &v.CreatedAt); e != nil {
			return nil, e
		}
		v.Active = active == 1
		v.Enabled = enabled == 1
		v.HasKey = true
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Publish(admin int64, id int) error {
	if admin != profile.AdminID {
		return errors.New("forbidden")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow(`SELECT count(*) FROM ai_versions WHERE id=? AND enabled=1`, id).Scan(&n); e != nil || n != 1 {
		return errors.New("AI version unavailable")
	}
	if _, e = tx.Exec(`UPDATE ai_versions SET active=0 WHERE active=1`); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE ai_versions SET active=1 WHERE id=?`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'ai_publish',?,?)`, admin, fmt.Sprint(id), time.Now().UTC().Format(time.RFC3339)); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Client(id int) (ai.Client, error) {
	var v Version
	var cipherKey []byte
	query := `SELECT provider,model,max_tokens,temperature,reasoning_budget,reasoning_effort,key_cipher,base_url,timeout_seconds,enabled FROM ai_versions WHERE active=1 AND enabled=1`
	if id > 0 {
		query = `SELECT provider,model,max_tokens,temperature,reasoning_budget,reasoning_effort,key_cipher,base_url,timeout_seconds,enabled FROM ai_versions WHERE id=?`
	}
	var e error
	if id > 0 {
		var enabled int
		e = s.DB.QueryRow(query, id).Scan(&v.Provider, &v.Model, &v.MaxTokens, &v.Temperature, &v.ReasoningBudget, &v.ReasoningEffort, &cipherKey, &v.BaseURL, &v.TimeoutSeconds, &enabled)
		v.Enabled = enabled == 1
	} else {
		var enabled int
		e = s.DB.QueryRow(query).Scan(&v.Provider, &v.Model, &v.MaxTokens, &v.Temperature, &v.ReasoningBudget, &v.ReasoningEffort, &cipherKey, &v.BaseURL, &v.TimeoutSeconds, &enabled)
		v.Enabled = enabled == 1
	}
	if e != nil {
		return ai.Client{}, e
	}
	key, e := s.open(cipherKey)
	if e != nil {
		return ai.Client{}, e
	}
	endpoint := v.BaseURL
	if _, e = url.Parse(endpoint); e != nil {
		return ai.Client{}, e
	}
	return ai.Client{Provider: v.Provider, Key: key, BaseURL: endpoint, Model: v.Model, MaxTokens: v.MaxTokens, Temperature: v.Temperature, ReasoningBudget: v.ReasoningBudget, ReasoningEffort: v.ReasoningEffort, HTTP: &http.Client{Timeout: time.Duration(v.TimeoutSeconds) * time.Second}}, nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Store) Seed(v Version, key string) error {
	var n int
	if e := s.DB.QueryRow(`SELECT count(*) FROM ai_versions`).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	draft, e := s.Draft(profile.AdminID, v, key)
	if e != nil {
		return e
	}
	return s.Publish(profile.AdminID, draft.ID)
}
