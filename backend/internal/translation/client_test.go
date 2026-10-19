package translation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yexca/kikoto/backend/internal/translation"
)

func TestOutputBudgetScalesWithInputAndKeepsFailureTaxonomy(t *testing.T) {
	const ceiling = 4096
	for _, tc := range []struct {
		name       string
		text       string
		finish     string
		content    string
		wantTokens int
		wantCode   string
		wantText   string
	}{
		{
			name:       "short input keeps the floor",
			text:       "Short title",
			finish:     "stop",
			content:    "短标题",
			wantTokens: 256,
			wantText:   "短标题",
		},
		{
			name:       "long input scales with its rune count",
			text:       strings.Repeat("a", 1500),
			finish:     "stop",
			content:    "translated synopsis",
			wantTokens: 3000,
			wantText:   "translated synopsis",
		},
		{
			name:       "maximum input clamps to the ceiling",
			text:       strings.Repeat("a", 8000),
			finish:     "stop",
			content:    "translated synopsis",
			wantTokens: ceiling,
			wantText:   "translated synopsis",
		},
		{
			name:       "length finish reason stays a failure",
			text:       "A synopsis",
			finish:     "length",
			content:    "partial text",
			wantTokens: 256,
			wantCode:   "translation_truncated",
		},
		{
			name:       "normal stop response is unchanged",
			text:       "Example title",
			finish:     "stop",
			content:    "示例标题",
			wantTokens: 256,
			wantText:   "示例标题",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var sentTokens int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var payload struct {
					MaxTokens int `json:"max_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
				}
				sentTokens = payload.MaxTokens
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{
					map[string]any{"message": map[string]string{"content": tc.content}, "finish_reason": tc.finish},
				}})
			}))
			defer upstream.Close()
			cfg := translation.Config{
				Limits:         translation.DefaultLimits(),
				BaseURL:        upstream.URL,
				Model:          "Example-Model",
				TimeoutSeconds: 5,
			}
			result, err := translation.Translate(context.Background(), upstream.Client(), cfg, translation.Input{Text: tc.text, TargetLanguage: "zh-Hans"})
			if calls.Load() != 1 {
				t.Fatalf("upstream received %d requests, want one", calls.Load())
			}
			if sentTokens != tc.wantTokens {
				t.Fatalf("max_tokens = %d, want %d", sentTokens, tc.wantTokens)
			}
			if sentTokens < 1 || sentTokens > ceiling {
				t.Fatalf("max_tokens = %d, want 1..%d", sentTokens, ceiling)
			}
			if tc.wantCode != "" {
				var failure *translation.Error
				if !errors.As(err, &failure) || failure.Code != tc.wantCode {
					t.Fatalf("error = %v, want code %s", err, tc.wantCode)
				}
				if result.Text != "" {
					t.Fatalf("truncated result exposed text: %q", result.Text)
				}
				return
			}
			if err != nil {
				t.Fatalf("translate: %v", err)
			}
			if result.Text != tc.wantText || result.Model != cfg.Model || result.TemplateVersion != translation.TemplateVersion {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}
