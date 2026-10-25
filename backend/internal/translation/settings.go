// Package translation owns the server-only model configuration and bounded
// translation calls. API consumers receive a separate, redacted projection.
package translation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/yexca/kikoto/backend/internal/outbound"
)

const DefaultModel = "Index-Translate-35B-A3B"
const settingKey = "translation_service"

var ErrInvalidConfig = errors.New("invalid translation configuration")

// Config contains the secret and must never be serialized to an API response.
type Limits struct {
	Concurrency int `json:"concurrency"`
	QueueLimit  int `json:"queueLimit"`
	// MaxEntries bounds rebuildable model rows in the persistent translation
	// table. Zero means unlimited; authored native rows are not cache entries.
	MaxEntries int `json:"maxEntries"`
	// CacheBytes bounds the measured SQLite page footprint of the rebuildable
	// translation cache, including indexes and FTS shadow tables.
	CacheBytes   int64 `json:"cacheBytes"`
	MinFreeBytes int64 `json:"minFreeBytes"`
}

func DefaultLimits() Limits {
	// The measured model rate plateaus at eight concurrent calls; the queue
	// covers a full backfill pass without rejecting interactive work.
	return Limits{Concurrency: 8, QueueLimit: 2048, MaxEntries: 0, CacheBytes: 256 << 20, MinFreeBytes: 2 << 30}
}

type Config struct {
	Limits
	BaseURL         string `json:"baseUrl"`
	APIKey          string `json:"apiKey"`
	Model           string `json:"model"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	DisableThinking bool   `json:"disableThinking"`
}

type Settings struct {
	Limits
	BaseURL         string `json:"baseUrl"`
	Model           string `json:"model"`
	HasAPIKey       bool   `json:"hasApiKey"`
	APIKeyMask      string `json:"apiKeyMask"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	DisableThinking bool   `json:"disableThinking"`
}

func (c Config) Settings() Settings {
	mask := ""
	if c.APIKey != "" {
		mask = "********"
	}
	return Settings{Limits: c.Limits, BaseURL: c.BaseURL, Model: c.Model, HasAPIKey: c.APIKey != "", APIKeyMask: mask, TimeoutSeconds: c.TimeoutSeconds, DisableThinking: c.DisableThinking}
}

type Patch struct {
	Concurrency     *int    `json:"concurrency"`
	QueueLimit      *int    `json:"queueLimit"`
	MaxEntries      *int    `json:"maxEntries"`
	CacheBytes      *int64  `json:"cacheBytes"`
	MinFreeBytes    *int64  `json:"minFreeBytes"`
	BaseURL         *string `json:"baseUrl"`
	APIKey          *string `json:"apiKey"`
	Model           *string `json:"model"`
	TimeoutSeconds  *int    `json:"timeoutSeconds"`
	DisableThinking *bool   `json:"disableThinking"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ConfigKey identifies the output-shaping translation configuration. It
// deliberately excludes credentials and resource limits: changing those must
// not invalidate a corpus row or split the prefill cursor.
func ConfigKey(cfg Config) string {
	payload := strings.Join([]string{
		cfg.Model,
		cfg.BaseURL,
		TemplateVersion,
		strconv.FormatBool(cfg.DisableThinking),
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func load(ctx context.Context, db queryer) (Config, error) {
	cfg := Config{Limits: DefaultLimits(), Model: DefaultModel, TimeoutSeconds: 60, DisableThinking: true}
	var raw string
	err := db.QueryRowContext(ctx, "SELECT value_json FROM app_setting WHERE key = ?", settingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, ErrInvalidConfig
	}
	// Stored limits are returned exactly as saved. Upgrading a tuple that
	// happens to match an older default would silently rewrite an operator's
	// deliberate choice, and a save/reload would no longer round-trip.
	return cfg, nil
}

func (s *Store) Load(ctx context.Context) (Config, error) { return load(ctx, s.db) }

func (s *Store) Update(ctx context.Context, p Patch) (Settings, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cfg, err := load(ctx, tx)
	if err != nil {
		return Settings{}, err
	}
	if p.BaseURL != nil {
		cfg.BaseURL = strings.TrimRight(strings.TrimSpace(*p.BaseURL), "/")
	}
	if p.APIKey != nil {
		cfg.APIKey = *p.APIKey
	}
	if p.Model != nil {
		cfg.Model = *p.Model
	}
	if p.TimeoutSeconds != nil {
		cfg.TimeoutSeconds = *p.TimeoutSeconds
	}
	if p.DisableThinking != nil {
		cfg.DisableThinking = *p.DisableThinking
	}
	if p.Concurrency != nil {
		cfg.Concurrency = *p.Concurrency
	}
	if p.QueueLimit != nil {
		cfg.QueueLimit = *p.QueueLimit
	}
	if p.MaxEntries != nil {
		cfg.MaxEntries = *p.MaxEntries
	}
	if p.CacheBytes != nil {
		cfg.CacheBytes = *p.CacheBytes
	}
	if p.MinFreeBytes != nil {
		cfg.MinFreeBytes = *p.MinFreeBytes
	}
	if err := cfg.Validate(); err != nil {
		return Settings{}, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return Settings{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO app_setting (key, value_json) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json", settingKey, string(raw))
	if err != nil {
		return Settings{}, err
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, err
	}
	return cfg.Settings(), nil
}

func (c Config) Validate() error {
	if c.Concurrency < 1 || c.Concurrency > 16 || c.QueueLimit < 0 || c.QueueLimit > 4096 || c.MaxEntries < 0 || c.MaxEntries > 2_000_000 || c.CacheBytes < 4096 || c.CacheBytes > 4<<30 || c.MinFreeBytes < 0 || c.MinFreeBytes > 1<<40 {
		return ErrInvalidConfig
	}
	if len(c.BaseURL) > 2048 || len(c.APIKey) > 4096 || strings.ContainsAny(c.APIKey, "\r\n") || strings.TrimSpace(c.Model) != c.Model || c.Model == "" || len(c.Model) > 256 || strings.ContainsAny(c.Model, "\r\n") || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60 {
		return ErrInvalidConfig
	}
	if c.BaseURL != "" {
		u, err := outbound.ParseHTTPURL(c.BaseURL)
		if err != nil || u.RawQuery != "" || u.ForceQuery {
			return ErrInvalidConfig
		}
	}
	return nil
}
