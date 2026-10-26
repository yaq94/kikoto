package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// The trial endpoint distinguishes why an input was refused. A single generic
// code told the operator nothing about whether the text was empty, too long or
// aimed at an unsupported language.
func TestTranslationTrialReportsEachInvalidInputReason(t *testing.T) {
	_, host, admin, _ := translationHTTPFixture(t)
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{name: "empty", body: `{"text":"   ","targetLanguage":"ja"}`, code: "translation_empty_text"},
		{name: "too long", body: `{"text":"` + strings.Repeat("日", translation.MaxInputBytes) + `","targetLanguage":"ja"}`, code: "translation_text_too_long"},
		{name: "unsupported language", body: `{"text":"Hello.","targetLanguage":"fr"}`, code: "translation_unsupported_language"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := translationRequest(t, host, admin, "POST", "/api/settings/translation/test", tc.body, http.StatusBadRequest)
			if result["code"] != tc.code {
				t.Fatalf("code = %v, want %s", result["code"], tc.code)
			}
		})
	}
}
