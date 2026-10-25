package library

import (
	"strings"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// DisplaySearchTarget is one corpus language and the query text that matches
// it. A display language can need more than one target: Traditional Chinese
// searches the Simplified corpus with a t2s-normalised query and the native
// Traditional rows with the text as typed.
type DisplaySearchTarget struct {
	Language string
	Query    string
}

// DisplaySearchTargets maps an API display language to the corpus languages
// that carry its text. ok is false for the original-language mode and for an
// unknown code, which keeps the caller on the existing search path.
func DisplaySearchTargets(display, query string) ([]DisplaySearchTarget, bool) {
	switch strings.ToLower(strings.TrimSpace(display)) {
	case "en", "en-us":
		return []DisplaySearchTarget{{Language: translation.SourceEnglish, Query: query}}, true
	case "zh-cn", "zh-hans":
		return []DisplaySearchTarget{{Language: translation.SourceHans, Query: query}}, true
	case "zh-tw", "zh-hant":
		// The model writes Simplified rows and the display converts them. A
		// query whose characters are shared by both scripts ("少女") changes no
		// glyph during conversion, but its translation still lives in the
		// Simplified corpus, so both targets are always searched.
		simplified := query
		if converted, err := translation.Simplified(query); err == nil && converted != "" {
			simplified = converted
		}
		return []DisplaySearchTarget{
			{Language: translation.SourceHans, Query: simplified},
			{Language: translation.SourceHant, Query: query},
		}, true
	case "ja", "ja-jp":
		return []DisplaySearchTarget{{Language: translation.SourceJapanese, Query: query}}, true
	case "ko", "ko-kr":
		return []DisplaySearchTarget{{Language: translation.SourceKorean, Query: query}}, true
	default:
		return nil, false
	}
}

// LikeContainsPattern turns a literal needle into an escaped SQLite LIKE
// pattern. SQLite's LIKE is case-insensitive for ASCII, matching the previous
// lower() comparison, and it avoids a function call per candidate row.
func LikeContainsPattern(needle string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(needle) + "%"
}
