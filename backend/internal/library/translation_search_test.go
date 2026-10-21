package library

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/storage"
)

func openTranslationSearchDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(db, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return db
}

func explainQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, query)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

// A one-character query cannot form a trigram. It must read a bounded window in
// index order instead of scanning every translated work.
func TestContentTranslationSearchShortQueryBounded(t *testing.T) {
	db := openTranslationSearchDB(t)
	where, args := SearchWhereForUserInLanguage("夜", 0, "zh-cn")
	if where == "" {
		t.Fatal("short query produced no predicate")
	}
	plan := explainQueryPlan(t, db, "SELECT COUNT(*) FROM work WHERE "+where, args...)
	t.Logf("short query plan:\n%s", plan)
	if !strings.Contains(plan, "idx_content_translation_language_id") {
		t.Fatalf("short query does not use the language index: %s", plan)
	}
	if strings.Contains(plan, "SCAN content_translation\n") {
		t.Fatalf("short query scans the whole translation table: %s", plan)
	}
}

// Three characters go through the trigram index, still filtered by language.
func TestContentTranslationSearchLongQueryUsesFTS(t *testing.T) {
	db := openTranslationSearchDB(t)
	where, args := SearchWhereForUserInLanguage("雨夜的故事", 0, "zh-cn")
	if where == "" {
		t.Fatal("long query produced no predicate")
	}
	plan := explainQueryPlan(t, db, "SELECT COUNT(*) FROM work WHERE "+where, args...)
	t.Logf("long query plan:\n%s", plan)
	if !strings.Contains(plan, "VIRTUAL TABLE INDEX") {
		t.Fatalf("long query does not use the trigram index: %s", plan)
	}
}

// Traditional search covers a t2s-normalised Simplified target and the native
// Traditional rows in one predicate.
func TestContentTranslationSearchTraditionalBuildsTwoTargets(t *testing.T) {
	targets, ok := DisplaySearchTargets("zh-tw", "戀愛")
	if !ok || len(targets) != 2 {
		t.Fatalf("targets = %#v, ok = %v; want Simplified and Traditional", targets, ok)
	}
	if targets[0].Language != "zh-Hans" || targets[0].Query != "恋爱" {
		t.Fatalf("first target = %#v, want zh-Hans/恋爱", targets[0])
	}
	if targets[1].Language != "zh-Hant" || targets[1].Query != "戀愛" {
		t.Fatalf("second target = %#v, want zh-Hant/戀愛", targets[1])
	}
	where, args := SearchWhereForUserInLanguage("戀愛", 0, "zh-tw")
	if strings.Count(where, "work.id IN") != 2 || len(args) != 4 {
		t.Fatalf("zh-tw predicate = %s args=%v; want two branches", where, args)
	}
	if origin, _ := SearchWhereForUserInLanguage("戀愛", 0, "origin"); strings.Contains(origin, "content_translation") {
		t.Fatalf("origin language touched translations: %s", origin)
	}
}
