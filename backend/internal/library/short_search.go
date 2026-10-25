package library

import (
	"strings"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// ShortSearchArm is one entity kind that a 1-2 character translation search
// covers. Scope is an extra predicate over the content_translation alias `ct`
// that ties a row to a still-effective entity: current source text, a live
// entity, and the admission rules of the caller. ScopeArgs fill its
// placeholders in order. ConfigKey selects the model rows that may answer the
// search; an empty key keeps the caller's historical "any model row" rule.
type ShortSearchArm struct {
	EntityKind string
	Fields     []string
	Scope      string
	ScopeArgs  []any
	ConfigKey  string
}

// ShortTranslationSearchSQL is the single definition of a 1-2 character
// translation search. A trigram index cannot answer fewer than three
// characters, so the needle is matched with instr() over the covering short
// index. The scan covers the complete effective language/field range; only the
// number of returned rows is capped.
//
// A positive limit caps the returned rows and orders them by
// (language, entity_kind, field, id), which is the keyset order a caller can
// continue from and which makes the cap deterministic. Zero keeps the complete
// effective set unordered, which is what a count or a paginated total needs: a
// sort there would read the whole range before returning anything. The returned
// subquery exposes (entity_id, language, entity_kind, field, id).
func ShortTranslationSearchSQL(targets []DisplaySearchTarget, arms []ShortSearchArm, limit int) (string, []any) {
	if len(arms) == 0 || len(targets) == 0 {
		return "", nil
	}
	branches := []string{}
	args := []any{}
	for _, arm := range arms {
		if arm.EntityKind == "" || len(arm.Fields) == 0 {
			continue
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(arm.Fields)), ",")
		for _, target := range targets {
			needle := strings.TrimSpace(target.Query)
			if needle == "" {
				continue
			}
			branch := `SELECT ct.entity_id,ct.language,ct.entity_kind,ct.field,ct.id
				FROM content_translation ct INDEXED BY idx_content_translation_short_scan
				WHERE ct.language=? AND ct.entity_kind=? AND ct.field IN (` + placeholders + `)
				  AND ` + shortTranslationCurrentSQL(target.Language, arm.ConfigKey) + `
				  AND instr(ct.translated_text, ?) > 0`
			branchArgs := []any{target.Language, arm.EntityKind}
			for _, field := range arm.Fields {
				branchArgs = append(branchArgs, field)
			}
			if target.Language != translation.SourceHant && arm.ConfigKey != "" {
				branchArgs = append(branchArgs, arm.ConfigKey)
			}
			branchArgs = append(branchArgs, needle)
			if arm.Scope != "" {
				branch += ` AND (` + arm.Scope + `)`
				branchArgs = append(branchArgs, arm.ScopeArgs...)
			}
			branches = append(branches, branch)
			args = append(args, branchArgs...)
		}
	}
	if len(branches) == 0 {
		return "", nil
	}
	sqlText := `SELECT entity_id,language,entity_kind,field,id FROM (
		` + strings.Join(branches, `
		UNION ALL
		`) + `
	)`
	if limit > 0 {
		sqlText += ` ORDER BY language,entity_kind,field,id LIMIT ?`
		args = append(args, limit)
	}
	return sqlText, args
}

// shortTranslationCurrentSQL accepts a native row or a model row produced by
// the current output-shaping configuration. zh-Hant is native-only.
func shortTranslationCurrentSQL(language, configKey string) string {
	if language == translation.SourceHant {
		return `ct.origin='native'`
	}
	if configKey == "" {
		return `1=1`
	}
	return `(ct.origin='native' OR (ct.origin='model' AND ct.config_key=?))`
}
