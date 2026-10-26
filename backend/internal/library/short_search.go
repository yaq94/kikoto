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
// A positive limit caps the rows each branch returns. One branch is one
// (target language, entity kind) pair, ordered by (field, id): that is the
// order the scan index already provides after its equality prefix, so a capped
// branch needs no temporary b-tree and the cap is deterministic. Capping per
// branch also keeps a display language that reads two stored languages fair:
// `zh-tw` searches `zh-Hans` and native `zh-Hant`, and one cap over the union
// would let Simplified matches spend the budget and hide every Traditional row.
//
// The cap limits returned rows, never the scanned range. Zero keeps the complete
// effective set unordered, which is what a count or a diagnostic needs: a sort
// there would read the whole range before returning anything. The returned
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
		for _, target := range targets {
			needle := strings.TrimSpace(target.Query)
			if needle == "" {
				continue
			}
			branch, branchArgs := shortTranslationBranch(target, arm)
			if branch == "" {
				continue
			}
			if limit > 0 {
				// A LIMIT belongs to one SELECT, so the capped branch becomes
				// its own subquery. SQLite rejects ORDER BY/LIMIT directly
				// before a UNION ALL operator.
				branch = `SELECT * FROM (` + branch + ` ORDER BY ct.field,ct.id LIMIT ?)`
				branchArgs = append(branchArgs, limit)
			}
			branches = append(branches, branch)
			args = append(args, branchArgs...)
		}
	}
	if len(branches) == 0 {
		return "", nil
	}
	return `SELECT entity_id,language,entity_kind,field,id,exact FROM (
		` + strings.Join(branches, `
		UNION ALL
		`) + `
	)`, args
}

// ShortTranslationSearchOverflowSQL reports, per (target language, entity
// kind), whether more effective rows exist than the given budget. Each probe
// walks at most limit+1 rows of the same index range, so it costs one page
// rather than a full count. A caller uses it to mark a truncated result list
// without pretending the budget is the size of the effective set.
//
// The statement returns (language, entity_kind, overflow) per branch.
func ShortTranslationSearchOverflowSQL(targets []DisplaySearchTarget, arms []ShortSearchArm, limit int) (string, []any) {
	if limit <= 0 || len(arms) == 0 || len(targets) == 0 {
		return "", nil
	}
	probes := []string{}
	args := []any{}
	for _, arm := range arms {
		if arm.EntityKind == "" || len(arm.Fields) == 0 || arm.After > 0 {
			continue
		}
		for _, target := range targets {
			if strings.TrimSpace(target.Query) == "" {
				continue
			}
			branch, branchArgs := shortTranslationBranch(target, arm)
			if branch == "" {
				continue
			}
			probes = append(probes, `SELECT ? AS language,? AS entity_kind,
				EXISTS(SELECT 1 FROM (`+branch+` LIMIT ? OFFSET ?)) AS overflow`)
			probeArgs := append([]any{target.Language, arm.EntityKind}, branchArgs...)
			probeArgs = append(probeArgs, 1, limit)
			args = append(args, probeArgs...)
		}
	}
	if len(probes) == 0 {
		return "", nil
	}
	return strings.Join(probes, `
		UNION ALL
		`), args
}

// shortTranslationBranch builds one uncapped (target language, entity kind)
// scan. Its ordering is the scan index order; a caller that caps rows wraps it
// and adds LIMIT.
func shortTranslationBranch(target DisplaySearchTarget, arm ShortSearchArm) (string, []any) {
	needle := strings.TrimSpace(target.Query)
	if needle == "" || arm.EntityKind == "" || len(arm.Fields) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(arm.Fields)), ",")
	branch := `SELECT ct.entity_id,ct.language,ct.entity_kind,ct.field,ct.id,(ct.translated_text=?) AS exact
		FROM content_translation ct INDEXED BY idx_content_translation_short_scan
		WHERE ct.language=? AND ct.entity_kind=? AND ct.field IN (` + placeholders + `)
		  AND ct.browsable=1`
	args := []any{needle, target.Language, arm.EntityKind}
	for _, field := range arm.Fields {
		args = append(args, field)
	}
	if arm.SourceID != 0 {
		branch += ` AND ct.source_id=?`
		args = append(args, arm.SourceID)
	} else if arm.EntityKind == "work" {
		// A work row never belongs to a source; the scope view stores NULL
		// there. Naming that constant is not a filter change, but it lets the
		// scan read (field,id) straight from the index instead of sorting the
		// range.
		branch += ` AND ct.source_id IS NULL`
	}
	branch += ` AND ` + shortTranslationCurrentSQL(target.Language, arm.ConfigKey)
	if target.Language != translation.SourceHant && arm.ConfigKey != "" {
		args = append(args, arm.ConfigKey)
	}
	if arm.After > 0 {
		branch += ` AND ct.id>?`
		args = append(args, arm.After)
	}
	branch += ` AND instr(ct.translated_text, ?) > 0`
	args = append(args, needle)
	return branch, args
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
