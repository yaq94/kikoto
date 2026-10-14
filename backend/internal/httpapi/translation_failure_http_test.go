package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTranslationTrialFailuresAreClassifiedWithoutLeakingOrRetrying(t *testing.T) {
	const secret = "synthetic-upstream-secret"
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
		timeout    bool
	}{
		{"empty", 200, `{"choices":[{"message":{"content":"  "},"finish_reason":"stop"}]}`, "translation_empty_output", false},
		{"truncated", 200, `{"choices":[{"message":{"content":"partial text"},"finish_reason":"length"}]}`, "translation_truncated", false},
		{"unfinished", 200, `{"choices":[{"message":{"content":"partial text"}}]}`, "translation_invalid_response", false},
		{"invalid-json", 200, `{broken`, "translation_invalid_response", false},
		{"invalid-utf8", 200, "{\"choices\":[{\"message\":{\"content\":\"\xff\"},\"finish_reason\":\"stop\"}]}", "translation_invalid_response", false},
		{"reasoning", 200, `{"choices":[{"message":{"content":"<think>internal</think>text"},"finish_reason":"stop"}]}`, "translation_invalid_response", false},
		{"auth", 401, secret, "translation_auth_failed", false},
		{"forbidden", 403, secret, "translation_auth_failed", false},
		{"rate-limit", 429, secret, "translation_rate_limited", false},
		{"bad-parameter", 400, secret, "translation_request_rejected", false},
		{"server-error", 503, secret, "translation_upstream_error", false},
		{"redirect", 307, secret, "translation_redirect_blocked", false},
		{"oversized", 200, strings.Repeat("x", (256<<10)+1), "translation_response_too_large", false},
		{"timeout", 200, "", "translation_timeout", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if tc.timeout {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status == 307 {
					w.Header().Set("Location", "http://unconfigured.example/private")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			_, host, admin, _ := translationHTTPFixture(t)
			settings, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL + "/v1", "apiKey": secret, "timeoutSeconds": 1})
			translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(settings), 200)
			result := translationRequest(t, host, admin, "POST", "/api/settings/translation/test", `{"text":"Hello.","targetLanguage":"ja"}`, 422)
			if result["code"] != tc.code {
				t.Fatalf("code = %v, want %s", result["code"], tc.code)
			}
			if text, present := result["text"]; present {
				t.Fatalf("failure returned a translation: %v", text)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), upstream.URL) || strings.Contains(string(encoded), "unconfigured.example") {
				t.Fatal("error leaked upstream details")
			}
			if count.Load() != 1 {
				t.Fatalf("upstream received %d requests, want one", count.Load())
			}
		})
	}
}
