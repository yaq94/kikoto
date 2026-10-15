package translation_test

import (
	"github.com/yexca/kikoto/backend/internal/translation"
	"testing"
)

func TestCacheIdentitySeparatesContentLanguageModelAndTemplate(t *testing.T) {
	cfg := translation.Config{Model: "Example Model", BaseURL: "https://translation.example.invalid", Limits: translation.DefaultLimits()}
	in := translation.Input{Text: "Example title", TargetLanguage: "zh-Hans"}
	base := translation.CacheKey(cfg, in, in.Text, "template-one")
	variants := []string{
		translation.CacheKey(cfg, in, in.Text, "template-two"),
		translation.CacheKey(cfg, in, "Changed title", "template-one"),
		translation.CacheKey(cfg, translation.Input{Text: in.Text, TargetLanguage: "ja"}, in.Text, "template-one"),
	}
	cfg.Model = "Another Model"
	variants = append(variants, translation.CacheKey(cfg, in, in.Text, "template-one"))
	for _, key := range variants {
		if key == base {
			t.Fatal("incompatible translation reused cache identity")
		}
	}
}
