package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in test sends only the two neutral, authored samples below through
// the real content route. Credentials stay in process memory and the test DB.
func TestContentTranslationTraditionalLiveConfiguredService(t *testing.T) {
	raw := os.Getenv("KIKOTO_TRANSLATION_LIVE_CONFIG")
	if raw == "" {
		t.Skip("dedicated live configuration is not set")
	}
	cfg := loadLiveTranslationConfig(t, raw)
	s, host, admin, listener := translationHTTPFixture(t)
	request := func(cookie *http.Cookie, method, path string, body any) map[string]any {
		data, _ := json.Marshal(body)
		req, err := http.NewRequest(method, host.URL+path, strings.NewReader(string(data)))
		if err != nil {
			t.Fatal("create application request failed")
		}
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		res, err := host.Client().Do(req)
		if err != nil {
			t.Fatal("application request failed")
		}
		defer func() { _ = res.Body.Close() }()
		data, err = io.ReadAll(io.LimitReader(res.Body, 256<<10))
		if err != nil {
			t.Fatal("read application response failed")
		}
		var result map[string]any
		if json.Unmarshal(data, &result) != nil || res.StatusCode != 200 {
			t.Fatalf("application status=%d code=%v", res.StatusCode, result["code"])
		}
		return result
	}
	request(admin, "PATCH", "/api/settings/translation", map[string]any{"baseUrl": cfg.BaseURL, "apiKey": cfg.APIKey, "model": cfg.Model, "minFreeBytes": 0})
	metadataReviewExec(t, s.db, `INSERT INTO party(id,display_name) VALUES(1,'示例社团'); INSERT INTO person(id,display_name) VALUES(1,'示例声优');`)
	// Sample 0 is Japanese, so both scripts go through the model once. Sample 1
	// is already Simplified Chinese: the model language of a zh-tw request is
	// zh-Hans, so the card is native for both scripts and only the display text
	// is converted. Both shapes are part of the contract.
	samples := []struct {
		text       string
		wantStatus string
	}{
		{"今日はいい天気です。示例声优と示例社团の音楽を聞きます。", "translated"},
		{"风吹过森林，示例社团和示例声优一起听音乐。", "native"},
	}
	for i, sample := range samples {
		original := sample.text
		id := i + 1
		metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(?,?,?)`, id, fmt.Sprintf("RJ0000000%d", i), original)
		metadataReviewExec(t, s.db, `INSERT INTO work_party(work_id,party_id) VALUES(?,1)`, id)
		metadataReviewExec(t, s.db, `INSERT INTO work_credit(work_id,person_id,role) VALUES(?,1,'voice_actor')`, id)
		start := time.Now()
		traditional := request(listener, "POST", "/api/content-translations", map[string]any{"workId": id, "field": "title", "text": original, "language": "zh-tw"})
		generationElapsed := time.Since(start).Milliseconds()
		start = time.Now()
		simplified := request(listener, "POST", "/api/content-translations", map[string]any{"workId": id, "field": "title", "text": original, "language": "zh-cn"})
		cached := request(listener, "POST", "/api/content-translations", map[string]any{"workId": id, "field": "title", "text": original, "language": "zh-tw"})
		for _, result := range []map[string]any{traditional, simplified, cached} {
			text, ok := result["text"].(string)
			if !ok || result["status"] != sample.wantStatus || result["original"] != original || !strings.Contains(text, "示例声优") || !strings.Contains(text, "示例社团") {
				t.Fatalf("live conversion incomplete: status=%v code=%v", result["status"], result["code"])
			}
			if (cfg.APIKey != "" && strings.Contains(text, cfg.APIKey)) || strings.Contains(text, cfg.BaseURL) {
				t.Fatal("live response contained configuration details")
			}
		}
		if traditional["text"] == simplified["text"] {
			t.Fatal("Traditional and Simplified display text were identical")
		}
		if sample.wantStatus == "translated" {
			if traditional["cacheStatus"] != "stored" || simplified["cacheStatus"] != "hit" || cached["cacheStatus"] != "hit" {
				t.Fatal("live script switching did not reuse one model result")
			}
		} else if cached["text"] != traditional["text"] || simplified["cacheStatus"] != "stored" {
			t.Fatal("native Simplified text did not convert consistently")
		}
		t.Logf("LIVE sample=%d model=%s status=%s original=%q simplified=%q traditional=%q generationAndConversionMs=%d cachedSwitchesMs=%d", id, cfg.Model, sample.wantStatus, original, simplified["text"], traditional["text"], generationElapsed, time.Since(start).Milliseconds())
	}
	stats := request(admin, "GET", "/api/settings/translation/runtime", nil)
	queue, _ := stats["queue"].(map[string]any)
	languages, _ := stats["languages"].([]any)
	if queue["readers"] != float64(0) || len(languages) != 1 {
		t.Fatalf("live requests did not settle into one Simplified corpus: %#v", stats)
	}
}
