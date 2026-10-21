package translation

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Source languages a title can be authored in. zh-Hant is distinct from
// zh-Hans because a Traditional title is a native row, not a conversion.
const (
	SourceJapanese = "ja"
	SourceKorean   = "ko"
	SourceHans     = "zh-Hans"
	SourceHant     = "zh-Hant"
	SourceEnglish  = "en"
	SourceUnknown  = "unknown"
)

// DetectSourceLanguage names the language a piece of content was written in.
// A wrong guess only costs a model call or a missing native row; it never
// changes the stored text, so the detector stays conservative: kana and Hangul
// are decisive, Han characters are split with the embedded OpenCC dictionary,
// and anything mixed or unreadable is unknown so the caller translates it.
func DetectSourceLanguage(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) {
		return SourceUnknown
	}
	var kana, hangul, han, latin, other int
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			kana++
		case unicode.In(r, unicode.Hangul):
			hangul++
		case unicode.In(r, unicode.Han):
			han++
		case unicode.IsLetter(r) && r < utf8.RuneSelf:
			latin++
		case unicode.IsLetter(r):
			other++
		}
	}
	switch {
	case kana > 0:
		return SourceJapanese
	case hangul > 0:
		return SourceKorean
	case han > 0 && latin > han*2:
		// A mostly Latin title that carries a Chinese circle or cast name is not
		// Chinese prose. Treating it as native would leave the reader with the
		// untranslated source, so the caller translates it instead.
		return SourceUnknown
	case han > 0:
		return hanLanguage(text, han)
	case latin > 0 && other == 0:
		return SourceEnglish
	default:
		return SourceUnknown
	}
}

// hanLanguage compares the text with its Simplified form. A large share of
// changed characters means the source carried Traditional glyphs; a title with
// only a couple of variant characters stays Simplified because OpenCC also
// rewrites a few orthographic variants.
func hanLanguage(text string, han int) string {
	simplified, err := Simplified(text)
	if err != nil || simplified == text {
		return SourceHans
	}
	left, right := []rune(text), []rune(simplified)
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	changed := 0
	for i := 0; i < limit; i++ {
		if left[i] != right[i] && (unicode.In(left[i], unicode.Han) || unicode.In(right[i], unicode.Han)) {
			changed++
		}
	}
	// A conversion that changes the rune count rewrites structure, not just
	// glyph variants, so it cannot be evidence of a Traditional source.
	if len(left) != len(right) {
		return SourceHans
	}
	if han >= 4 && changed*100 >= han*20 {
		return SourceHant
	}
	return SourceHans
}
