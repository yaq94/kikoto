package translation

import "testing"

// The detector decides whether a row can be served natively or must reach the
// model. Kana and Hangul are decisive; Han density decides the Chinese script;
// anything mixed beyond those rules stays unknown so the caller translates it.
func TestDetectSourceLanguage(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"hiragana", "深海少女の告白", SourceJapanese},
		{"katakana", "サンプルボイス", SourceJapanese},
		{"korean", "심해 소녀의 고백", SourceKorean},
		{"simplified", "深海少女的告白", SourceHans},
		{"traditional", "這裡有一隻貓", SourceHant},
		{"english", "Deep sea girl", SourceEnglish},
		{"cjk with latin", "深海少女 Deep sea", SourceHans},
		{"kana wins over han", "深海少女の告白 Deep sea", SourceJapanese},
		{"empty", "", SourceUnknown},
		{"spaces", "   ", SourceUnknown},
		{"punctuation", "!!! ...", SourceUnknown},
		{"digits", "1234567890", SourceUnknown},
		{"latin with cyrillic", "Hello Привет", SourceUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectSourceLanguage(tc.text); got != tc.want {
				t.Fatalf("DetectSourceLanguage(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// A short title with one variant character must not flip to Traditional:
// OpenCC also rewrites a few orthographic variants.
func TestDetectSourceLanguageKeepsShortTitlesSimplified(t *testing.T) {
	if got := DetectSourceLanguage("告白"); got != SourceHans {
		t.Fatalf("short title detected as %q", got)
	}
}

func TestScriptConversionRoundTrip(t *testing.T) {
	traditional, err := Traditional("这里有一只猫")
	if err != nil {
		t.Fatal(err)
	}
	if traditional != "這裏有一隻貓" {
		t.Fatalf("Traditional = %q", traditional)
	}
	simplified, err := Simplified(traditional)
	if err != nil {
		t.Fatal(err)
	}
	if simplified != "这里有一只猫" {
		t.Fatalf("Simplified = %q", simplified)
	}
}
