package translation_test

import (
	"testing"

	"github.com/yexca/kikoto/backend/internal/translation"
)

func TestCacheIdentitySeparatesContentLanguageModelAndTemplate(t *testing.T) {
	cfg := translation.Config{Model: "Example Model", BaseURL: "https://translation.example.invalid", Limits: translation.DefaultLimits()}
	in := translation.Input{Text: "Example title", TargetLanguage: "zh-Hans"}
	base, err := translation.CacheKey(cfg, in, in.Text, "template-one")
	if err != nil {
		t.Fatal(err)
	}
	variants := []string{
		mustCacheKey(t, cfg, in, in.Text, "template-two"),
		mustCacheKey(t, cfg, in, "Changed title", "template-one"),
		mustCacheKey(t, cfg, translation.Input{Text: in.Text, TargetLanguage: "ja"}, in.Text, "template-one"),
	}
	cfg.Model = "Another Model"
	variants = append(variants, mustCacheKey(t, cfg, in, in.Text, "template-one"))
	for _, key := range variants {
		if key == base {
			t.Fatal("incompatible translation reused cache identity")
		}
	}
}

func mustCacheKey(t *testing.T, cfg translation.Config, in translation.Input, original, template string) string {
	t.Helper()
	key, err := translation.CacheKey(cfg, in, original, template)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
