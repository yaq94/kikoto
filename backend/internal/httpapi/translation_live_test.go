package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Opt in only with a dedicated non-production configuration passed in the
// process environment. No live credentials or endpoints belong in fixtures.
func TestTranslationLiveConfiguredService(t *testing.T) {
	raw := os.Getenv("KIKOTO_TRANSLATION_LIVE_CONFIG")
	if raw == "" {
		t.Skip("dedicated live configuration is not set")
	}
	var cfg struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil || cfg.BaseURL == "" || cfg.Model != "Index-Translate-35B-A3B" {
		t.Fatal("invalid dedicated live configuration")
	}
	if !strings.HasPrefix(cfg.BaseURL, "https://") {
		t.Fatal("live endpoint requires HTTPS")
	}
	_, host, admin, _ := translationHTTPFixture(t)
	request := func(method, path, body string) map[string]any {
		req, err := http.NewRequest(method, host.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("create application request failed")
		}
		req.AddCookie(admin)
		req.Header.Set("Content-Type", "application/json")
		res, err := host.Client().Do(req)
		if err != nil {
			t.Fatal("application request failed")
		}
		defer func() { _ = res.Body.Close() }()
		data, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
		if err != nil {
			t.Fatal("read application response failed")
		}
		var result map[string]any
		if json.Unmarshal(data, &result) != nil {
			t.Fatal("invalid application response")
		}
		if res.StatusCode != 200 {
			t.Fatalf("application status=%d code=%v", res.StatusCode, result["code"])
		}
		return result
	}
	request("PATCH", "/api/settings/translation", raw)
	result := request("POST", "/api/settings/translation/test", `{"text":"今日はいい天気です。","targetLanguage":"zh-Hans"}`)
	text, ok := result["text"].(string)
	if !ok || strings.TrimSpace(text) == "" || result["model"] != cfg.Model {
		t.Fatal("live translation was incomplete")
	}
	if (cfg.APIKey != "" && strings.Contains(text, cfg.APIKey)) || strings.Contains(text, cfg.BaseURL) {
		t.Fatal("live response contained configuration details")
	}
	t.Logf("LIVE model=%s template=%v elapsedMs=%v text=%q", cfg.Model, result["templateVersion"], result["elapsedMs"], text)
}
