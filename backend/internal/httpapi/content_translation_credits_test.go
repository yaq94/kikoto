package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestContentTranslationCatalogTitleProtectsExplicitCreditsWithoutSidecar(t *testing.T) {
	// Synthetic names intentionally contain characters changed by s2t. Their
	// published spelling, including Simplified characters, must stay verbatim.
	const name = "示例声优甲"
	const title = "おやすみの音声】(CV" + name + ")"
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if len(input.Messages) != 1 {
			t.Error("unexpected model message count")
			return
		}
		prompt := input.Messages[0].Content
		if strings.Contains(prompt, name) {
			t.Error("explicit catalog credit was sent to the model")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"听见音乐】(CV⟪KIKOTO_NAME_0⟫)"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, title)
	metadataReviewExec(t, s.db, `INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,'https://source.example.invalid','https://source.example.invalid')`)
	configurePrefillModel(t, host, admin, model.URL)
	for _, test := range []struct{ language, text, cache string }{
		{"zh-cn", "听见音乐】(CV" + name + ")", "stored"},
		{"zh-tw", "聽見音樂】(CV" + name + ")", "hit"},
	} {
		body, _ := json.Marshal(map[string]any{"catalogEntryId": 1, "field": "title", "text": title, "language": test.language})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		if result["text"] != test.text || result["cacheStatus"] != test.cache || result["original"] != title {
			t.Errorf("%s title lost its published credit: %#v", test.language, result)
		}
	}
}

func TestContentTranslationCreditProtocolRebuildsLegacyModelCorpus(t *testing.T) {
	const title = "おやすみ】(CV示例声优甲)"
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) != 1 {
			t.Error("invalid model request")
			return
		}
		text := strings.SplitN(input.Messages[0].Content, "\n\n", 2)[1]
		if strings.Contains(text, "示例声优甲") {
			t.Error("prefill sent the explicit credit")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": strings.ReplaceAll(text, "おやすみ", "听见音乐")}, "finish_reason": "stop",
		}}})
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, title)
	configurePrefillModel(t, host, admin, model.URL)
	cfg := mustTranslationConfig(t, s)
	const legacyTemplate = "index-user-v1"
	legacyConfig := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{cfg.Model, cfg.BaseURL, legacyTemplate, strconv.FormatBool(cfg.DisableThinking)}, "|"))))
	key := contentTranslationCacheKey("entry", 1, "", "title", "zh-Hans", title, "model", cfg.Model, cfg.BaseURL, legacyTemplate, cfg.DisableThinking)
	_, err := s.saveContentTranslations(context.Background(), []contentTranslationRow{
		{CacheKey: key, EntityKind: "entry", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: title, TranslatedText: "错误译文", Origin: "model", ConfigKey: legacyConfig, Model: cfg.Model, TemplateVersion: legacyTemplate},
		{CacheKey: "native-credit-fixture", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: title, TranslatedText: title, Origin: "native"},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, s.db, `UPDATE translation_prefill_state SET config_key=?,status='complete',cursor_id=1`, legacyConfig)
	version := translationRequest(t, host, listener, "GET", "/api/content-translations/version", "", 200)
	if strings.HasSuffix(version["version"].(string), "|"+legacyTemplate) {
		t.Error("browser still accepts the legacy display cache")
	}
	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	langs := prefillLanguageRows(t, view)
	if langs["ja"]["native"] != float64(1) || langs["zh-Hans"]["stale"] != float64(1) || langs["zh-Hans"]["model"] != float64(0) {
		t.Errorf("legacy model/native coverage: %#v", langs)
	}
	search := translationRequest(t, host, listener, "GET", "/api/remote-sources/1/works?lang=zh-cn&q="+url.QueryEscape("错误译文"), "", 200)
	if search["total"] != float64(0) {
		t.Error("legacy unprotected translation remains searchable")
	}
	for _, language := range []string{"origin", "ja-jp"} {
		body := mustCreditJSON(t, map[string]any{"catalogEntryId": 1, "field": "title", "text": title, "language": language})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		if result["text"] != title {
			t.Errorf("original/native text unavailable: %#v", result)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("native text generated a model request")
	}
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"zh-cn", "zh-tw"} {
		body := mustCreditJSON(t, map[string]any{"catalogEntryId": 1, "field": "title", "text": title, "language": language})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		want := "听见音乐】(CV示例声优甲)"
		if language == "zh-tw" {
			want = "聽見音樂】(CV示例声优甲)"
		}
		if result["text"] != want || result["cacheStatus"] != "hit" {
			t.Errorf("prefill result: %#v", result)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("model rebuild calls=%d; want three non-native languages", calls.Load())
	}
	view = translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	langs = prefillLanguageRows(t, view)
	if langs["zh-Hans"]["stale"] != float64(0) || langs["zh-Hans"]["model"] != float64(1) {
		t.Errorf("rebuilt coverage: %#v", langs)
	}
}

func TestContentTranslationExplicitCreditsProtectRelatedFields(t *testing.T) {
	const name = "示例声优甲"
	const circle = "示例社团乙"
	const title = "【CV：" + name + "】【サークル：" + circle + "】おやすみの音声"
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) != 1 {
			t.Error("invalid model request")
			return
		}
		text := strings.SplitN(input.Messages[0].Content, "\n\n", 2)[1]
		if strings.Contains(text, name) || strings.Contains(text, circle) {
			t.Error("published explicit credit reached the model")
		}
		if !strings.Contains(text, "おやすみ") {
			t.Error("credit parser swallowed ordinary prose")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": strings.ReplaceAll(text, "おやすみ", "听见音乐")}, "finish_reason": "stop",
		}}})
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, title)
	metadataReviewExec(t, s.db, `INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO catalog_series(id,title) VALUES(1,'Example series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual');`)
	metadataReviewExec(t, s.db, `UPDATE source_catalog_file SET title=?,relative_path='Original/01.wav' WHERE catalog_entry_id=1`, name+"と"+circle+"のおやすみ.wav")
	metadataReviewExec(t, s.db, `UPDATE catalog_series SET title=? WHERE id=1`, name+"と"+circle+"のおやすみ")
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title,description) VALUES(1,'RJ00000000',?,?)`, title, "おやすみ\nCV："+name+"\nサークル："+circle)
	configurePrefillModel(t, host, admin, model.URL)
	for _, selector := range []map[string]any{
		{"catalogEntryId": 1, "field": "title", "text": title},
		{"catalogEntryId": 1, "field": "directory", "path": "Original/01.wav", "text": name + "と" + circle + "のおやすみ.wav"},
		{"seriesId": 1, "field": "title", "text": name + "と" + circle + "のおやすみ"},
		{"workId": 1, "field": "description", "text": "おやすみ\nCV：" + name + "\nサークル：" + circle},
	} {
		for _, language := range []string{"zh-cn", "zh-tw"} {
			selector["language"] = language
			body, _ := json.Marshal(selector)
			result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
			phrase := "听见音乐"
			if language == "zh-tw" {
				phrase = "聽見音樂"
			}
			text, _ := result["text"].(string)
			if result["status"] != "translated" || !strings.Contains(text, name) || !strings.Contains(text, circle) || !strings.Contains(text, phrase) {
				t.Errorf("protected field %s: %#v", body, result)
			}
		}
	}
	tracks := translationRequest(t, host, listener, "GET", "/api/source-catalog/1/tracks", "", 200)
	if !strings.Contains(string(mustCreditJSON(t, tracks)), "Original/01.wav") {
		t.Fatal("translation changed the media path")
	}
}

func mustCreditJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestContentTranslationRemoteExplicitCreditsIgnoreLegacyDisplayCache(t *testing.T) {
	const title = "おやすみ【CV：示例声优甲】"
	const track = "示例声优甲のおやすみ.wav"
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) != 1 {
			t.Error("invalid model request")
			return
		}
		text := strings.SplitN(input.Messages[0].Content, "\n\n", 2)[1]
		if strings.Contains(text, "示例声优甲") {
			t.Error("remote explicit credit reached the model")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": strings.ReplaceAll(text, "おやすみ", "听见音乐")}, "finish_reason": "stop",
		}}})
	}))
	defer model.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workInfo/RJ00000001":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "title": title, "source_id": "RJ00000001", "vas": []any{}, "tags": []any{}})
		case "/api/tracks/1":
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"type": "audio", "title": track, "mediaStreamUrl": "https://media.example.invalid/01.wav"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_remote','Example Remote','kikoeru_compatible');
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,?,?)`, source.URL, source.URL)
	configurePrefillModel(t, host, admin, model.URL)
	cfg := mustTranslationConfig(t, s)
	key := sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "zh-Hans", title, cfg.Model, cfg.BaseURL, "index-user-v1", cfg.DisableThinking)
	if _, err := s.saveSourceFieldTranslations(context.Background(), []sourceFieldTranslationRow{{CacheKey: key, SourceID: 1, WorkCode: "RJ00000001", Field: "title", Language: "zh-Hans", SourceText: title, TranslatedText: "错误译文", Model: cfg.Model, TemplateVersion: "index-user-v1"}}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct{ field, text, path string }{{"title", title, ""}, {"directory", track, track}} {
		for _, language := range []string{"zh-cn", "zh-tw"} {
			result := sourceFieldRequest(t, host, listener, field.field, field.text, field.path, language)
			phrase := "听见音乐"
			if language == "zh-tw" {
				phrase = "聽見音樂"
			}
			text, _ := result["text"].(string)
			if result["status"] != "translated" || !strings.Contains(text, "示例声优甲") || !strings.Contains(text, phrase) {
				t.Errorf("remote credit: %#v", result)
			}
		}
	}
	if calls.Load() != 2 {
		t.Errorf("remote title/track generation=%d; want 2 with shared script caches", calls.Load())
	}
}

func TestContentTranslationExplicitCreditMarkersDoNotInferUnlabelledNames(t *testing.T) {
	const name = "示例声优甲"
	for _, test := range []struct {
		title     string
		protected bool
	}{
		{"おやすみ（CV：" + name + "、示例声优乙）", true},
		{"おやすみ [cv: Example Voice]", true},
		{"おやすみ\n声優：" + name, true},
		{"おやすみ CV:" + name, true},
		{"おやすみ【CV:" + name + ";サークル:示例社团乙】", true},
		{"おやすみ CV:" + name + "|CV:示例声优乙", true},
		{"おやすみ CV:" + name + "【Circle:示例社团乙】", true},
		{"おやすみ】(CV" + name + ")", true},
		{"おやすみ（ＣＶ" + name + "）", true},
		{"おやすみ " + name, false},
		{"おやすみ SCV:" + name, false},
		{"おやすみ(SCV" + name + ")", false},
		{"おやすみ CV " + name, false},
	} {
		t.Run(test.title, func(t *testing.T) {
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) != 1 {
					t.Error("invalid model request")
					return
				}
				text := strings.SplitN(input.Messages[0].Content, "\n\n", 2)[1]
				if strings.Contains(text, "⟪KIKOTO_NAME_") != test.protected {
					t.Errorf("incorrect explicit-credit boundary: %q", text)
				}
				if test.protected && (strings.Contains(text, name) || strings.Contains(text, "示例声优乙") || strings.Contains(text, "示例社团乙") || strings.Contains(text, "Example Voice")) {
					t.Error("credit was not masked")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}})
			}))
			defer model.Close()
			s, host, admin, listener := translationHTTPFixture(t)
			seedPrefillCard(t, s, 1, test.title)
			configurePrefillModel(t, host, admin, model.URL)
			body := mustCreditJSON(t, map[string]any{"catalogEntryId": 1, "field": "title", "text": test.title, "language": "zh-cn"})
			result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
			if result["text"] != test.title || result["status"] != "translated" {
				t.Errorf("credit did not round-trip: %#v", result)
			}
		})
	}
}
