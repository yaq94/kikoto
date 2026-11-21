package httpapi

import (
	"regexp"
	"slices"
)

// Only a labelled credit is evidence of a name. Brackets and line/section
// separators end the credit without consuming the next label's boundary.
// Reuse the sidecar name-list cleanup without importing its title inference or
// turning this local protection list into persisted work metadata.
var translationCreditPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)(?:^|[\s　【「『〔［\[(（;；|｜])(?:CV|ＣＶ|声優|聲優|声优|配音|キャスト|サークル|社团|社團|Circle|ブランド)[\t 　]*[:：][\t 　]*[【「『〔［\[(（]?([^\r\n【】「」『』〔〕［］\[\]()（）;；|｜]+)`),
	// A closed parenthesis is an explicit CV credit even without a colon.
	// Keep that exception bracket-bound: bare CV text must not infer a name.
	regexp.MustCompile(`(?i)[(（][\t 　]*(?:CV|ＣＶ)[\t 　]*([^:：\r\n【】「」『』〔〕［］\[\]()（）;；|｜]+)[)）]`),
}

func translationProtectedNames(names []string, texts ...string) []string {
	// Prefill workers can share the entity's names slice across languages.
	result := slices.Clone(names)
	for _, text := range texts {
		result = append(result, catalogSidecarNames(text, translationCreditPatterns)...)
	}
	return result
}
