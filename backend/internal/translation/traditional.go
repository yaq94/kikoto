package translation

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/longbridgeapp/opencc"
)

var traditionalConverter = sync.OnceValues(func() (*opencc.OpenCC, error) {
	return opencc.New("s2t")
})

var simplifiedConverter = sync.OnceValues(func() (*opencc.OpenCC, error) {
	return opencc.New("t2s")
})

// Traditional converts masked Simplified text. Callers restore protected names
// only after this step, and never store this result under a Simplified cache key.
func Traditional(text string) (string, error) {
	return convertText(traditionalConverter, text)
}

// Simplified normalises a Traditional query or title to Simplified so a search
// and a language check can reuse the same stored zh-Hans rows.
func Simplified(text string) (string, error) {
	return convertText(simplifiedConverter, text)
}

func convertText(converter func() (*opencc.OpenCC, error), text string) (string, error) {
	if len(text) > MaxResponseBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return "", failure("translation_conversion_failed", "Chinese conversion failed.")
	}
	value, err := converter()
	if err != nil {
		return "", failure("translation_conversion_failed", "Chinese conversion failed.")
	}
	result, err := value.Convert(text)
	if err != nil || strings.TrimSpace(result) == "" {
		return "", failure("translation_conversion_failed", "Chinese conversion failed.")
	}
	return result, nil
}
