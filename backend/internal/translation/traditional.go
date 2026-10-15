package translation

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/longbridgeapp/opencc"
)

// OpenCC embeds its dictionaries. One immutable converter is shared by readers;
// it does not load files or make requests while converting.
var traditionalConverter = sync.OnceValues(func() (*opencc.OpenCC, error) {
	return opencc.New("s2t")
})

// Traditional converts masked Simplified text. Callers restore protected names
// only after this step, and never store this result under a Simplified cache key.
func Traditional(text string) (string, error) {
	if len(text) > MaxResponseBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return "", failure("translation_conversion_failed", "Traditional Chinese conversion failed.")
	}
	converter, err := traditionalConverter()
	if err != nil {
		return "", failure("translation_conversion_failed", "Traditional Chinese conversion failed.")
	}
	result, err := converter.Convert(text)
	if err != nil || strings.TrimSpace(result) == "" {
		return "", failure("translation_conversion_failed", "Traditional Chinese conversion failed.")
	}
	return result, nil
}
