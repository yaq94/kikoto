package httpapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/translation"
	"github.com/yexca/kikoto/backend/migrations"
)

func openContentTranslationTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{db: db}
}

func TestContentTranslationStoreMigrationCreatesSchema(t *testing.T) {
	s := openContentTranslationTestServer(t)

	for _, name := range []string{"content_translation", "translation_prefill_state"} {
		var kind string
		if err := s.db.QueryRow(`SELECT type FROM sqlite_master WHERE name = ?`, name).Scan(&kind); err != nil {
			t.Fatalf("table %s: %v", name, err)
		}
		if kind != "table" {
			t.Fatalf("%s type = %q, want table", name, kind)
		}
	}
	var ftsSQL string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'content_translation_search'`).Scan(&ftsSQL); err != nil {
		t.Fatalf("FTS table: %v", err)
	}
	for _, fragment := range []string{"fts5", "content='content_translation'", "content_rowid='id'", "trigram"} {
		if !strings.Contains(ftsSQL, fragment) {
			t.Fatalf("FTS DDL missing %q: %s", fragment, ftsSQL)
		}
	}
	for _, name := range []string{
		"idx_content_translation_entity",
		"idx_content_translation_language",
		"idx_content_translation_language_id",
	} {
		var type_ string
		if err := s.db.QueryRow(`SELECT type FROM sqlite_master WHERE name = ?`, name).Scan(&type_); err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
		if type_ != "index" {
			t.Fatalf("%s type = %q, want index", name, type_)
		}
	}
	for _, name := range []string{"content_translation_ai", "content_translation_ad", "content_translation_au"} {
		var type_ string
		if err := s.db.QueryRow(`SELECT type FROM sqlite_master WHERE name = ?`, name).Scan(&type_); err != nil {
			t.Fatalf("trigger %s: %v", name, err)
		}
		if type_ != "trigger" {
			t.Fatalf("%s type = %q, want trigger", name, type_)
		}
	}

	// The store is durable corpus, so the LRU column from the abandoned draft
	// must not come back.
	var lastUsed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('content_translation') WHERE name = 'last_used_at'`).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if lastUsed != 0 {
		t.Fatal("content_translation has a last_used_at column")
	}

	var cursor, status string
	if err := s.db.QueryRow(`SELECT cursor_entry_id, status FROM translation_prefill_state WHERE id = 1`).Scan(&cursor, &status); err != nil {
		t.Fatalf("prefill state row: %v", err)
	}
	if cursor != "0" || status != "idle" {
		t.Fatalf("prefill state = (%s, %s), want (0, idle)", cursor, status)
	}
}

func TestContentTranslationStoreFTSTriggersTrackChanges(t *testing.T) {
	s := openContentTranslationTestServer(t)
	ctx := context.Background()
	insert := func(id int64, kind, lang, text string) {
		t.Helper()
		if _, err := s.db.Exec(`
			INSERT INTO content_translation(id, cache_key, entity_kind, entity_id, field, language, source_text, translated_text, origin)
			VALUES(?,?,?,?, 'title', ?, ?, ?, 'model')`,
			id, fmt.Sprintf("key-%d", id), kind, id, lang, text, text); err != nil {
			t.Fatalf("insert %d: %v", id, err)
		}
	}
	insert(1, "entry", "zh-Hans", "深海少女的告白")
	insert(2, "entry", "zh-Hans", "浅海少年的告白")
	insert(3, "entry", "ja", "深海少女の告白")

	hits, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "深海少女", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := joinIDs(hits); got != "1" {
		t.Fatalf("insert search = %s, want 1", got)
	}

	if _, err := s.db.Exec(`UPDATE content_translation SET translated_text = ? WHERE id = 1`, "星空少女的告白"); err != nil {
		t.Fatalf("update: %v", err)
	}
	hits, err = s.translatedEntityIDs(ctx, "zh-Hans", "entry", "深海少女", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("old text still matches after update: %v", hits)
	}
	hits, err = s.translatedEntityIDs(ctx, "zh-Hans", "entry", "星空少女", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := joinIDs(hits); got != "1" {
		t.Fatalf("updated text search = %s, want 1", got)
	}

	if _, err := s.db.Exec(`DELETE FROM content_translation WHERE id = 2`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits, err = s.translatedEntityIDs(ctx, "zh-Hans", "entry", "浅海少年", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("deleted row still matches: %v", hits)
	}

	// An external content table can drift; rebuild must reproduce the same
	// answers from the content table.
	if _, err := s.db.Exec(`INSERT INTO content_translation_search(content_translation_search) VALUES('rebuild')`); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	hits, err = s.translatedEntityIDs(ctx, "zh-Hans", "entry", "星空少女", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := joinIDs(hits); got != "1" {
		t.Fatalf("search after rebuild = %s, want 1", got)
	}
	hits, err = s.translatedEntityIDs(ctx, "zh-Hans", "entry", "深海少女", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("rebuild resurrected old text: %v", hits)
	}
}

func TestContentTranslationCacheKeyTracksInputs(t *testing.T) {
	const (
		kind   = "entry"
		field  = "title"
		lang   = "zh-Hans"
		source = "深海少女の告白"
		origin = "model"
		model  = "gpt"
		base   = "https://api.example.test"
		tmpl   = "v1"
	)
	key := contentTranslationCacheKey(kind, 7, "code-7", field, lang, source, origin, model, base, tmpl, false)
	if len(key) != 64 {
		t.Fatalf("key length = %d, want 64 hex characters", len(key))
	}
	if _, err := hex.DecodeString(key); err != nil {
		t.Fatalf("key is not hex: %v", err)
	}
	if again := contentTranslationCacheKey(kind, 7, "code-7", field, lang, source, origin, model, base, tmpl, false); again != key {
		t.Fatalf("same inputs produced %q then %q", key, again)
	}

	cases := map[string]string{
		"content":         contentTranslationCacheKey(kind, 7, "code-7", field, lang, source+"。", origin, model, base, tmpl, false),
		"field":           contentTranslationCacheKey(kind, 7, "code-7", "description", lang, source, origin, model, base, tmpl, false),
		"language":        contentTranslationCacheKey(kind, 7, "code-7", field, "ja", source, origin, model, base, tmpl, false),
		"model":           contentTranslationCacheKey(kind, 7, "code-7", field, lang, source, origin, "other", base, tmpl, false),
		"template":        contentTranslationCacheKey(kind, 7, "code-7", field, lang, source, origin, model, base, "v2", false),
		"baseURL":         contentTranslationCacheKey(kind, 7, "code-7", field, lang, source, origin, model, "https://other.example.test", tmpl, false),
		"entityKind":      contentTranslationCacheKey("work", 7, "code-7", field, lang, source, origin, model, base, tmpl, false),
		"entityID":        contentTranslationCacheKey(kind, 8, "code-7", field, lang, source, origin, model, base, tmpl, false),
		"entityKey":       contentTranslationCacheKey(kind, 7, "code-8", field, lang, source, origin, model, base, tmpl, false),
		"origin":          contentTranslationCacheKey(kind, 7, "code-7", field, lang, source, "native", model, base, tmpl, false),
		"disableThinking": contentTranslationCacheKey(kind, 7, "code-7", field, lang, source, origin, model, base, tmpl, true),
	}
	for name, got := range cases {
		if got == key {
			t.Fatalf("%s change did not change the cache key", name)
		}
	}
}

func TestContentTranslationStoreSaveIsIdempotent(t *testing.T) {
	s := openContentTranslationTestServer(t)
	ctx := context.Background()
	key := contentTranslationCacheKey("entry", 1, "", "title", "zh-Hans", "src", "model", "gpt", "https://api.example.test", "v1", false)
	row := contentTranslationRow{
		CacheKey: key, EntityKind: "entry", EntityID: 1, Field: "title", Language: "zh-Hans",
		SourceText: "src", TranslatedText: "第一版", Origin: "model", Model: "gpt", TemplateVersion: "v1",
	}
	if _, err := s.saveContentTranslations(ctx, []contentTranslationRow{row}); err != nil {
		t.Fatal(err)
	}
	row.TranslatedText = "第二版"
	row.Model = "gpt-2"
	row.TemplateVersion = "v2"
	if _, err := s.saveContentTranslations(ctx, []contentTranslationRow{row}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM content_translation WHERE cache_key = ?`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows for key = %d, want 1", count)
	}
	found, err := s.lookupContentTranslations(ctx, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := found[key]
	if !ok {
		t.Fatal("saved row not found")
	}
	if got.TranslatedText != "第二版" || got.Model != "gpt-2" || got.TemplateVersion != "v2" {
		t.Fatalf("upsert result = %+v", got)
	}
	// The update trigger must have replaced the FTS entry, not appended one.
	ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "第二版", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("search after upsert = %v, want [1]", ids)
	}
	if ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "第一版", 10); err != nil {
		t.Fatal(err)
	} else if len(ids) != 0 {
		t.Fatalf("stale text still matches: %v", ids)
	}
}

func TestContentTranslationStoreSearchFiltersAndShortFallback(t *testing.T) {
	s := openContentTranslationTestServer(t)
	ctx := context.Background()
	rows := []contentTranslationRow{
		{CacheKey: "a", EntityKind: "entry", EntityID: 10, Field: "title", Language: "zh-Hans", TranslatedText: "深海少女的告白", Origin: "model"},
		{CacheKey: "b", EntityKind: "entry", EntityID: 11, Field: "title", Language: "zh-Hans", TranslatedText: "深海少女的告白 第二季", Origin: "model"},
		{CacheKey: "c", EntityKind: "entry", EntityID: 12, Field: "title", Language: "ja", TranslatedText: "深海少女の告白", Origin: "native"},
		{CacheKey: "d", EntityKind: "work", EntityID: 13, Field: "title", Language: "zh-Hans", TranslatedText: "深海少女的告白", Origin: "model"},
	}
	if _, err := s.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"深海少女", "深海"} {
		ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", query, 10)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if got := joinIDs(ids); got != "10,11" {
			t.Fatalf("%q ids = %s, want 10,11", query, got)
		}
	}
	// limit is honoured on both branches.
	for _, query := range []string{"深海少女", "深海"} {
		ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", query, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 {
			t.Fatalf("%q limit 1 returned %v", query, ids)
		}
	}
	// Unknown language and empty query return nothing.
	if ids, err := s.translatedEntityIDs(ctx, "ko", "entry", "深海少女", 10); err != nil {
		t.Fatal(err)
	} else if len(ids) != 0 {
		t.Fatalf("ko search returned %v", ids)
	}
	if ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "   ", 10); err != nil {
		t.Fatal(err)
	} else if len(ids) != 0 {
		t.Fatalf("blank search returned %v", ids)
	}
	count, err := s.countContentTranslations(ctx, "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("zh-Hans count = %d, want 3", count)
	}
}

func TestContentTranslationStoreLookupChunksLargeBatches(t *testing.T) {
	s := openContentTranslationTestServer(t)
	ctx := context.Background()
	const total = contentTranslationLookupChunk*2 + 1
	rows := make([]contentTranslationRow, 0, total)
	keys := make([]string, 0, total+1)
	for i := 0; i < total; i++ {
		key := contentTranslationCacheKey("entry", int64(i), "", "title", "en", fmt.Sprintf("source-%d", i), "model", "gpt", "", "v1", false)
		rows = append(rows, contentTranslationRow{
			CacheKey: key, EntityKind: "entry", EntityID: int64(i), Field: "title", Language: "en",
			SourceText: fmt.Sprintf("source-%d", i), TranslatedText: fmt.Sprintf("translated-%d", i), Origin: "model",
		})
		keys = append(keys, key)
	}
	keys = append(keys, "missing")
	if _, err := s.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	found, err := s.lookupContentTranslations(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != total {
		t.Fatalf("found %d rows, want %d", len(found), total)
	}
	last := rows[total-1].CacheKey
	if got := found[last]; got.ID == 0 || got.TranslatedText != fmt.Sprintf("translated-%d", total-1) {
		t.Fatalf("row from the last chunk = %+v", got)
	}
	if _, ok := found["missing"]; ok {
		t.Fatal("missing key was returned")
	}
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ",")
}

// The 1-2 character fallback cannot use the trigram index, so it must only
// inspect a bounded window of the newest rows. A marker older than that window
// is unreachable even though it is present in the table.
func TestContentTranslationShortQueryStaysBounded(t *testing.T) {
	s := openContentTranslationTestServer(t)
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO content_translation(
		cache_key, entity_kind, entity_id, field, language, source_text, translated_text, origin)
		VALUES(?, 'entry', ?, 'title', 'zh-Hans', ?, ?, 'model')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Exec("oldest", 1, "远古标记", "远古标记"); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= contentTranslationShortScanLimit; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("filler-%d", i), i, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	newest := contentTranslationShortScanLimit + 1
	if _, err := stmt.Exec("newest", newest, "近古标记", "近古标记"); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM content_translation`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != contentTranslationShortScanLimit+1 {
		t.Fatalf("seeded %d rows", total)
	}
	if ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "远古", 10); err != nil {
		t.Fatal(err)
	} else if len(ids) != 0 {
		t.Fatalf("short query reached past its window: %v", ids)
	}
	if ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "近古", 10); err != nil {
		t.Fatal(err)
	} else if len(ids) != 1 || ids[0] != int64(newest) {
		t.Fatalf("short query inside the window = %v", ids)
	}
}

// The corpus limits count and remove only model rows, oldest first, and a
// superseded row goes before a live one.
func TestContentTranslationRetentionHonoursModelRowBudget(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	rows := []contentTranslationRow{
		{CacheKey: "native-1", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "native", TranslatedText: "native", Origin: "native"},
	}
	for i := int64(1); i <= 5; i++ {
		rows = append(rows, contentTranslationRow{CacheKey: fmt.Sprintf("model-%d", i), EntityKind: "entry", EntityID: i, Field: "title", Language: "en", SourceText: "source", TranslatedText: "text", Origin: "model"})
	}
	if _, err := server.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, translation.Config{Limits: translation.Limits{MaxEntries: 3, CacheBytes: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	var model, native int
	if err := server.db.QueryRow(`SELECT (SELECT count(*) FROM content_translation WHERE origin='model'),(SELECT count(*) FROM content_translation WHERE origin='native')`).Scan(&model, &native); err != nil {
		t.Fatal(err)
	}
	if model != 3 || native != 1 {
		t.Fatalf("after trim model=%d native=%d, want 3/1 (budget applies to model rows)", model, native)
	}

	// A newer row for the same entity, field and language supersedes the old
	// one even when the budget itself is not exceeded.
	if _, err := server.saveContentTranslations(ctx, []contentTranslationRow{
		{CacheKey: "model-new", EntityKind: "entry", EntityID: 5, Field: "title", Language: "en", SourceText: "source", TranslatedText: "new", Origin: "model"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, translation.Config{Limits: translation.Limits{MaxEntries: 100, CacheBytes: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := server.db.QueryRow(`SELECT count(*) FROM content_translation WHERE cache_key='model-5'`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("superseded row still present: %d %v", old, err)
	}
}

func TestContentTranslationRetentionHonoursUTF8ByteBudget(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	rows := []contentTranslationRow{
		{CacheKey: "native-big", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "作者原文", TranslatedText: "作者原文", Origin: "native"},
	}
	for i := int64(1); i <= 3; i++ {
		rows = append(rows, contentTranslationRow{
			CacheKey: fmt.Sprintf("model-%d", i), EntityKind: "entry", EntityID: i,
			Field: "title", Language: "zh-Hans", SourceText: "深海少女", TranslatedText: "深海少女的告白", Origin: "model",
		})
	}
	if _, err := server.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, translation.Config{Limits: translation.Limits{CacheBytes: 48}}); err != nil {
		t.Fatal(err)
	}
	var model, native, bytes int64
	if err := server.db.QueryRow(`SELECT
		(SELECT count(*) FROM content_translation WHERE origin='model'),
		(SELECT count(*) FROM content_translation WHERE origin='native'),
		(SELECT COALESCE(sum(length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB))),0)
			FROM content_translation WHERE origin='model')`).Scan(&model, &native, &bytes); err != nil {
		t.Fatal(err)
	}
	if model != 1 || native != 1 || bytes > 48 {
		t.Fatalf("after byte trim model=%d native=%d bytes=%d, want 1/1 and <=48", model, native, bytes)
	}
}

// A large prefill burst may exceed one delete batch. The pass continues until
// the model-row budget is reached or its own bound is exhausted.
func TestContentTranslationRetentionDrainsSeveralBatches(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	rows := make([]contentTranslationRow, 0, 1200)
	for i := int64(1); i <= 1200; i++ {
		rows = append(rows, contentTranslationRow{
			CacheKey: fmt.Sprintf("model-%d", i), EntityKind: "entry", EntityID: i,
			Field: "title", Language: "en", SourceText: "source", TranslatedText: "text", Origin: "model",
		})
	}
	if _, err := server.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, translation.Config{Limits: translation.Limits{MaxEntries: 100}}); err != nil {
		t.Fatal(err)
	}
	var model int
	if err := server.db.QueryRow(`SELECT count(*) FROM content_translation WHERE origin='model'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != 100 {
		t.Fatalf("model rows after burst trim = %d, want 100", model)
	}
}
