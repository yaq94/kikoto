package library

import (
	"strings"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// ShortSearchArm is one entity kind that a 1-2 character translation search
// covers. SourceID narrows the scan to one source; zero keeps every source,
// which is what a local library search needs. ConfigKey selects the model rows
// that may answer the search; an empty key keeps the caller's historical "any
// model row" rule.
//
// After continues a single-field arm after one translation row id. A
// multi-field arm (work title and description) cannot resume from one id
// cursor, because the index order is (field, id) and one cursor would skip the
// rows of the other field; such an arm is omitted when After is set.
type ShortSearchArm struct {
	EntityKind string
	Fields     []string
	SourceID   int64
	After      int64
	ConfigKey  string
}

// ShortTranslationSearchSQL is the single definition of a 1-2 character
// translation search. A trigram index cannot answer fewer than three
// characters, so the needle is matched with instr() over the covering short
// index. The scan covers the complete effective language/field range; only the
// number of returned rows is capped.
//
// Effectiveness is stored on the row: `browsable` says the stored source text
// still matches a live, playable entity, and `source_id` says which source the
// entity belongs to. Both live in the index prefix, so the scan answers its
// range without joining a wide entity row per candidate.
//
// A positive limit caps the returned rows and orders them by
// (language, entity_kind, field, id), which is the keyset order a caller can
// continue from and which makes the cap deterministic. Zero keeps the complete
// effective set unordered, which is what a count or a paginated total needs: a
// sort there would read the whole range before returning anything. The returned
// subquery exposes (entity_id, language, entity_kind, field, id, exact), where
// `exact` says the stored text equals the needle. The comparison rides on the
// row the scan already read, so exact-first ordering costs no second pass.
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
		if arm.After > 0 && len(arm.Fields) != 1 {
			continue
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(arm.Fields)), ",")
		for _, target := range targets {
			needle := strings.TrimSpace(target.Query)
			if needle == "" {
				continue
			}
			branch := `SELECT ct.entity_id,ct.language,ct.entity_kind,ct.field,ct.id,(ct.translated_text=?) AS exact
				FROM content_translation ct INDEXED BY idx_content_translation_short_scan
				WHERE ct.language=? AND ct.entity_kind=? AND ct.field IN (` + placeholders + `)
				  AND ct.browsable=1`
			branchArgs := []any{needle, target.Language, arm.EntityKind}
			for _, field := range arm.Fields {
				branchArgs = append(branchArgs, field)
			}
			if arm.SourceID != 0 {
				branch += ` AND ct.source_id=?`
				branchArgs = append(branchArgs, arm.SourceID)
			} else if arm.EntityKind == "work" {
				// A work row never belongs to a source; the scope view stores
				// NULL there. Naming that constant is not a filter change, but
				// it lets the scan read (field,id) straight from the index
				// instead of sorting the range.
				branch += ` AND ct.source_id IS NULL`
			}
			branch += ` AND ` + shortTranslationCurrentSQL(target.Language, arm.ConfigKey)
			if target.Language != translation.SourceHant && arm.ConfigKey != "" {
				branchArgs = append(branchArgs, arm.ConfigKey)
			}
			if arm.After > 0 {
				branch += ` AND ct.id>?`
				branchArgs = append(branchArgs, arm.After)
			}
			branch += ` AND instr(ct.translated_text, ?) > 0`
			branchArgs = append(branchArgs, needle)
			branches = append(branches, branch)
			args = append(args, branchArgs...)
		}
	}
	if len(branches) == 0 {
		return "", nil
	}
	sqlText := `SELECT entity_id,language,entity_kind,field,id,exact FROM (
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
