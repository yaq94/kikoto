package httpapi

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

// liveTranslationConfig is the opt-in endpoint used by the real-model tests.
// Production serves the model on loopback HTTP, so a dedicated live run may
// explicitly allow that transport; any other plain HTTP endpoint stays denied.
type liveTranslationConfig struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

func loadLiveTranslationConfig(t *testing.T, raw string) liveTranslationConfig {
	t.Helper()
	var cfg liveTranslationConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil || cfg.BaseURL == "" || cfg.Model != "Index-Translate-35B-A3B" {
		t.Fatal("invalid dedicated live configuration")
	}
	if strings.HasPrefix(cfg.BaseURL, "https://") {
		return cfg
	}
	parsed, err := url.Parse(cfg.BaseURL)
	loopback := parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost" || parsed.Hostname() == "::1"
	if err != nil || parsed.Scheme != "http" || !loopback || os.Getenv("KIKOTO_TRANSLATION_LIVE_ALLOW_LOOPBACK") != "1" {
		t.Fatal("live endpoint requires HTTPS unless loopback HTTP is explicitly allowed")
	}
	return cfg
}
