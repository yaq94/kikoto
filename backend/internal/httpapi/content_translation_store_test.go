package httpapi

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/translation"
)

func openContentTranslationTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := storage.MigrateFS(db, completeMigrationFS(t), buildinfo.Version); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{db: db}
}

func TestContentTranslationStoreMigrationCreatesSchema(t *testing.T) {
	s := openContentTranslationTestServer(t)

	for _, name := range []string{"content_translation", "source_field_translation", "translation_prefill_state"} {
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
		"idx_content_translation_effective",
		"idx_content_translation_short_scan",

		"translation_prefill_failure_due",
		"idx_source_field_translation_owner",
		"idx_source_field_translation_updated",
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
	for _, column := range []string{"config_key"} {
		var present int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('content_translation') WHERE name=?`, column).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present != 1 {
			t.Fatalf("content_translation.%s is missing", column)
		}
	}
	for _, column := range []string{"config_key", "cursor_language", "cursor_kind", "cursor_field", "cursor_id"} {
		var present int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('translation_prefill_state') WHERE name=?`, column).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present != 1 {
			t.Fatalf("translation_prefill_state.%s is missing", column)
		}
	}
	var failureColumns int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('translation_prefill_failure')
		WHERE name IN ('entity_kind','entity_id','field','language','config_key','source_hash','attempts','next_retry_at','last_error')`).Scan(&failureColumns); err != nil {
		t.Fatal(err)
	}
	if failureColumns != 9 {
		t.Fatalf("translation_prefill_failure columns = %d, want 9", failureColumns)
	}
	var retryNullable int
	if err := s.db.QueryRow(`SELECT "notnull" FROM pragma_table_info('translation_prefill_failure')
		WHERE name='next_retry_at'`).Scan(&retryNullable); err != nil {
		t.Fatal(err)
	}
	if retryNullable != 0 {
		t.Fatal("translation_prefill_failure.next_retry_at must accept NULL")
	}
	var entryDescription int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('source_catalog_entry') WHERE name='description'`).Scan(&entryDescription); err != nil {
		t.Fatal(err)
	}
	if entryDescription != 0 {
		t.Fatal("source_catalog_entry must not grow a description column")
	}
	var foreignKeyViolations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatal(err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("foreign key violations = %d", foreignKeyViolations)
	}

	var cursor int64
	var status string
	if err := s.db.QueryRow(`SELECT cursor_id, status FROM translation_prefill_state WHERE id = 1`).Scan(&cursor, &status); err != nil {
		t.Fatalf("prefill state row: %v", err)
	}
	if cursor != 0 || status != "idle" {
		t.Fatalf("prefill state = (%d, %s), want (0, idle)", cursor, status)
	}
}

func TestContentTranslationStoreFTSTriggersTrackChanges(t *testing.T) {
	s := openContentTranslationTestServer(t)
	ctx := context.Background()
	configKey := translation.ConfigKey(translation.Config{Model: translation.DefaultModel, DisableThinking: true})
	insert := func(id int64, kind, lang, text string) {
		t.Helper()
		if _, err := s.db.Exec(`
			INSERT INTO content_translation(id, cache_key, entity_kind, entity_id, field, language, source_text, translated_text, origin, config_key)
			VALUES(?,?,?,?, 'title', ?, ?, ?, 'model', ?)`,
			id, fmt.Sprintf("key-%d", id), kind, id, lang, text, text, configKey); err != nil {
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
	currentKey := translation.ConfigKey(translation.Config{Model: "gpt", DisableThinking: true})
	rows := []contentTranslationRow{
		{CacheKey: "a", EntityKind: "entry", EntityID: 10, Field: "title", Language: "zh-Hans", TranslatedText: "深海少女的告白", Origin: "model", ConfigKey: currentKey, Model: "gpt", TemplateVersion: translation.TemplateVersion},
		{CacheKey: "b", EntityKind: "entry", EntityID: 11, Field: "title", Language: "zh-Hans", TranslatedText: "深海少女的告白 第二季", Origin: "model", ConfigKey: currentKey, Model: "gpt", TemplateVersion: translation.TemplateVersion},
		{CacheKey: "c", EntityKind: "entry", EntityID: 12, Field: "title", Language: "ja", TranslatedText: "深海少女の告白", Origin: "native"},
		{CacheKey: "d", EntityKind: "work", EntityID: 13, Field: "title", Language: "zh-Hans", TranslatedText: "深海少女的告白", Origin: "model", ConfigKey: currentKey, Model: "gpt", TemplateVersion: translation.TemplateVersion},
		// A model row from another output-shaping configuration is stale and
		// must not answer a strict search even though the text matches.
		{CacheKey: "e", EntityKind: "entry", EntityID: 14, Field: "title", Language: "zh-Hans", TranslatedText: "深海少女的旧配置", Origin: "model", ConfigKey: "legacy", Model: "gpt", TemplateVersion: translation.TemplateVersion},
	}
	if _, err := s.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// translatedEntityIDs reads the current config key. Save accepted the
	// explicit row keys, so configure the same output-shaping identity.
	if _, err := s.db.Exec(`INSERT INTO app_setting(key,value_json) VALUES('translation_service',?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, `{"model":"gpt","disableThinking":true}`); err != nil {
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
	if count != 4 {
		t.Fatalf("zh-Hans historical row count = %d, want 4 (including the stale row)", count)
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

// The 1-2 character fallback cannot use the trigram index. Segmented index
// ranges keep each statement bounded while still reaching rows beyond any
// fixed newest-window cap.
func TestContentTranslationShortQueryCoversFullCorpus(t *testing.T) {
	s := openContentTranslationTestServer(t)
	ctx := context.Background()
	configKey := translation.ConfigKey(translation.Config{Model: translation.DefaultModel, DisableThinking: true})
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO content_translation(
		cache_key, entity_kind, entity_id, field, language, source_text, translated_text, origin, config_key)
		VALUES(?, 'entry', ?, 'title', 'zh-Hans', ?, ?, 'model', ?)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Exec("oldest", 1, "远古标记", "远古标记", configKey); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 50000; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("filler-%d", i), i, "", "", configKey); err != nil {
			t.Fatal(err)
		}
	}
	newest := int64(50001)
	if _, err := stmt.Exec("newest", newest, "近古标记", "近古标记", configKey); err != nil {
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
	if total != 50001 {
		t.Fatalf("seeded %d rows", total)
	}
	if ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "远古", 10); err != nil {
		t.Fatal(err)
	} else if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("short query missed the oldest row: %v", ids)
	}
	if ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "近古", 10); err != nil {
		t.Fatal(err)
	} else if len(ids) != 1 || ids[0] != int64(newest) {
		t.Fatalf("short query missed the newest row: %v", ids)
	}
	// The result limit is a result cap, not a scan cap: the oldest marker is
	// still reachable when the caller asks for more than the newest page.
	if ids, err := s.translatedEntityIDs(ctx, "zh-Hans", "entry", "标记", 50000); err != nil {
		t.Fatal(err)
	} else if len(ids) != 2 || ids[0] != 1 || ids[1] != newest {
		t.Fatalf("full-set keyset result = %v, want both markers", ids)
	}
}

func seedCurrentTranslationEntry(t *testing.T, db *sql.DB, id int64, title string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(1,'retention-fixture','Retention Source','openlist',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		VALUES(?,1,?,?,'available')`, id, fmt.Sprintf("/retention/%d", id), title); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(?,'01.mp3','01.mp3','audio',1)`, id); err != nil {
		t.Fatal(err)
	}
}

// The soft budgets never remove rows that still answer current search: a live
// catalog entry with the current source text and the configured model/template.
func TestContentTranslationRetentionKeepsCurrentCorpusOverBudget(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	cfg := translation.Config{Limits: translation.Limits{MaxEntries: 3, CacheBytes: 1 << 30}, Model: "m"}
	configKey := translation.ConfigKey(cfg)
	rows := []contentTranslationRow{
		{CacheKey: "native-1", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "native", TranslatedText: "native", Origin: "native"},
	}
	for i := int64(1); i <= 5; i++ {
		title := fmt.Sprintf("当前标题%d", i)
		seedCurrentTranslationEntry(t, server.db, i, title)
		rows = append(rows, contentTranslationRow{CacheKey: fmt.Sprintf("model-%d", i), EntityKind: "entry", EntityID: i, Field: "title", Language: "zh-Hans", SourceText: title, TranslatedText: "译文", Origin: "model", ConfigKey: configKey, Model: "m", TemplateVersion: translation.TemplateVersion})
	}
	if _, err := server.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var model, native int
	if err := server.db.QueryRow(`SELECT (SELECT count(*) FROM content_translation WHERE origin='model'),(SELECT count(*) FROM content_translation WHERE origin='native')`).Scan(&model, &native); err != nil {
		t.Fatal(err)
	}
	if model != 5 || native != 1 {
		t.Fatalf("after trim model=%d native=%d, want 5/1 (current rows are corpus)", model, native)
	}

	// A historical row for the same entity is neither current nor protected.
	if _, err := server.saveContentTranslations(ctx, []contentTranslationRow{
		{CacheKey: "model-old", EntityKind: "entry", EntityID: 5, Field: "title", Language: "en", SourceText: "旧标题", TranslatedText: "旧译文", Origin: "model", ConfigKey: "old", Model: "old", TemplateVersion: "old"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, translation.Config{Limits: translation.Limits{MaxEntries: 100, CacheBytes: 1 << 30}, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := server.db.QueryRow(`SELECT count(*) FROM content_translation WHERE cache_key='model-old'`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("historical row still present: %d %v", old, err)
	}
}

func TestContentTranslationRetentionKeepsCurrentCorpusUnderByteBudget(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	cfg := translation.Config{Limits: translation.Limits{CacheBytes: 48}, Model: "m"}
	configKey := translation.ConfigKey(cfg)
	rows := []contentTranslationRow{
		{CacheKey: "native-big", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "作者原文", TranslatedText: "作者原文", Origin: "native"},
	}
	for i := int64(1); i <= 3; i++ {
		title := fmt.Sprintf("当前原文%d", i)
		seedCurrentTranslationEntry(t, server.db, i, title)
		rows = append(rows, contentTranslationRow{
			CacheKey: fmt.Sprintf("model-%d", i), EntityKind: "entry", EntityID: i,
			Field: "title", Language: "zh-Hans", SourceText: title, TranslatedText: "深海少女的告白", Origin: "model",
			ConfigKey: configKey, Model: "m", TemplateVersion: translation.TemplateVersion,
		})
	}
	if _, err := server.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var model, native int64
	if err := server.db.QueryRow(`SELECT
		(SELECT count(*) FROM content_translation WHERE origin='model'),
			(SELECT count(*) FROM content_translation WHERE origin='native')`).Scan(&model, &native); err != nil {
		t.Fatal(err)
	}
	if model != 3 || native != 1 {
		t.Fatalf("after byte trim model=%d native=%d, want 3/1 (current rows survive)", model, native)
	}
	overBudget, _, err := server.translationBudgetReport(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !overBudget {
		t.Fatal("persistent corpus over a tiny soft budget must report over_budget")
	}
}

// Work and series title rows are current only while their source text equals
// the entity's current title. A stale title is historical and evictable.
func TestContentTranslationRetentionChecksCurrentWorkAndSeriesText(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	cfg := translation.Config{Limits: translation.Limits{CacheBytes: 1 << 30}, Model: "m"}
	configKey := translation.ConfigKey(cfg)
	if _, err := server.db.Exec(`
		INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000001','Current Work Title');
		INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(1,'series-source','Series Source','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) VALUES(1,1,'/one','Episode One','available'),(2,1,'/two','Episode Two','available');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'01.mp3','01.mp3','audio',1),(2,'01.mp3','01.mp3','audio',1);
		INSERT INTO catalog_series(id,title) VALUES(1,'Current Series Title');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual'),(1,2,2,'manual');`); err != nil {
		t.Fatal(err)
	}
	rows := []contentTranslationRow{
		{CacheKey: "work-old", EntityKind: "work", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: "Old Work Title", TranslatedText: "旧作品译文", Origin: "model", ConfigKey: configKey, Model: "m", TemplateVersion: translation.TemplateVersion},
		{CacheKey: "work-current", EntityKind: "work", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: "Current Work Title", TranslatedText: "当前作品译文", Origin: "model", ConfigKey: configKey, Model: "m", TemplateVersion: translation.TemplateVersion},
		{CacheKey: "series-old", EntityKind: "series", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: "Old Series Title", TranslatedText: "旧系列译文", Origin: "model", ConfigKey: configKey, Model: "m", TemplateVersion: translation.TemplateVersion},
		{CacheKey: "series-current", EntityKind: "series", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: "Current Series Title", TranslatedText: "当前系列译文", Origin: "model", ConfigKey: configKey, Model: "m", TemplateVersion: translation.TemplateVersion},
	}
	if _, err := server.saveContentTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var workCurrent, seriesCurrent, old int
	if err := server.db.QueryRow(`SELECT
		(SELECT count(*) FROM content_translation WHERE cache_key='work-current'),
		(SELECT count(*) FROM content_translation WHERE cache_key='series-current'),
		(SELECT count(*) FROM content_translation WHERE cache_key IN ('work-old','series-old'))`).Scan(&workCurrent, &seriesCurrent, &old); err != nil {
		t.Fatal(err)
	}
	if workCurrent != 1 || seriesCurrent != 1 || old != 0 {
		t.Fatalf("current work/series = %d/%d old = %d, want 1/1/0", workCurrent, seriesCurrent, old)
	}
}

// A large rebuildable source-cache backlog may exceed one delete batch. The
// pass continues until the evictable-row budget is reached or its own bound is
// exhausted.
func TestContentTranslationRetentionDrainsSeveralBatchesOfSourceCache(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	if _, err := server.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(1,'retention-source','Retention Source','openlist')`); err != nil {
		t.Fatal(err)
	}
	rows := make([]sourceFieldTranslationRow, 0, 1200)
	for i := int64(1); i <= 1200; i++ {
		rows = append(rows, sourceFieldTranslationRow{
			CacheKey: fmt.Sprintf("source-%d", i), SourceID: 1, WorkCode: fmt.Sprintf("RJ%08d", i),
			Field: "title", Language: "en", SourceText: "source", TranslatedText: "text",
			Model: "old", TemplateVersion: "old",
		})
	}
	if _, err := server.saveSourceFieldTranslations(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, translation.Config{Limits: translation.Limits{MaxEntries: 100}, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	var model int
	if err := server.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != 100 {
		t.Fatalf("source rows after burst trim = %d, want 100", model)
	}
}
