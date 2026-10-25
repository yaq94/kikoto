package httpapi

import (
	"context"
	"testing"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// The cache limit is a disk budget, not just a count of text bytes. A large
// table and its indexes must be trimmed by their measured page footprint.
func TestContentTranslationRetentionMeasuresCachePages(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	if _, err := server.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(1,'cache-disk','Cache disk','openlist')`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.db.Exec(`WITH RECURSIVE n(i) AS (
			SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<12000
		)
		INSERT INTO source_field_translation(
			cache_key,source_id,work_code,field,language,source_text,translated_text,model,template_version
		)
		SELECT printf('cache-%05d',i),1,printf('RJ%08d',i),'title','zh-Hans',
			printf('source-%05d',i),printf('target-%05d',i),'m','v1' FROM n`); err != nil {
		t.Fatal(err)
	}
	before, measured := server.translationCacheDiskBytes(ctx)
	if !measured || before <= 1<<20 {
		t.Fatalf("cache pages=%d measured=%v, want a footprint above the test budget", before, measured)
	}
	if err := server.pruneContentTranslations(ctx, translation.Config{
		Limits: translation.Limits{CacheBytes: 1 << 20},
	}); err != nil {
		t.Fatal(err)
	}
	after, measured := server.translationCacheDiskBytes(ctx)
	var rowsLeft int
	if err := server.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&rowsLeft); err != nil {
		t.Fatal(err)
	}
	if !measured || after > 1<<20 {
		t.Fatalf("cache pages after trim=%d measured=%v rows=%d, want <= 1 MiB", after, measured, rowsLeft)
	}
	var rows int
	if err := server.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("disk trim removed every cache row")
	}
}

// Authored native rows share the content_translation table but are not cache.
// They must not consume the model cache budget or cause model rows to be
// evicted when their own pages exceed the configured limit.
func TestContentTranslationRetentionKeepsModelRowsWhenNativePagesDominate(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	cfg := translation.Config{Limits: translation.Limits{CacheBytes: 1 << 20}, Model: "m"}
	configKey := translation.ConfigKey(cfg)
	seedCurrentTranslationEntry(t, server.db, 999999, "model source")
	if _, err := server.db.Exec(`WITH RECURSIVE n(i) AS (
			SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<12000
		)
		INSERT INTO content_translation(
			cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,model,template_version
		)
		SELECT printf('native-%05d',i),'entry',i,'title','ja',
			printf('native source %05d',i),printf('native target %05d',i),'native','','' FROM n;
		INSERT INTO content_translation(
			cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version
			) VALUES('model-keep','entry',999999,'title','zh-Hans','model source','model target','model',?,'m',?)`, configKey, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneContentTranslations(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var model, native int
	if err := server.db.QueryRow(`SELECT
		(SELECT count(*) FROM content_translation WHERE origin='model'),
		(SELECT count(*) FROM content_translation WHERE origin='native')`).Scan(&model, &native); err != nil {
		t.Fatal(err)
	}
	if model != 1 || native != 12000 {
		t.Fatalf("cache retention model=%d native=%d, want 1/12000", model, native)
	}
}
