package httpapi

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// This is an explicit local measurement, not a target-host latency SLO. It uses
// the same cardinalities as the resource baseline and never calls a model.
func TestTranslationResourceScale(t *testing.T) {
	if os.Getenv("KIKOTO_TRANSLATION_RESOURCE_SCALE") != "1" {
		t.Skip("opt-in synthetic resource measurement")
	}
	for _, enabled := range []bool{false, true} {
		for _, count := range []int{2000, 8000, 42624} {
			t.Run(fmt.Sprintf("revision=%t/entries=%d", enabled, count), func(t *testing.T) {
				s, _, _, _ := translationHTTPFixture(t)
				if !enabled {
					rows, err := s.db.Query(`SELECT name FROM sqlite_schema WHERE type='trigger' AND name LIKE 'translation_revision_%'`)
					if err != nil {
						t.Fatal(err)
					}
					var names []string
					for rows.Next() {
						var name string
						if err := rows.Scan(&name); err != nil {
							t.Fatal(err)
						}
						names = append(names, name)
					}
					if err := rows.Close(); err != nil {
						t.Fatal(err)
					}
					for _, name := range names {
						if _, err := s.db.Exec(`DROP TRIGGER ` + name); err != nil {
							t.Fatal(err)
						}
					}
				}
				cfg := mustTranslationConfig(t, s)
				start := time.Now()
				tx, err := s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				for _, statement := range []string{
					`INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(70,'example_remote_a','Example Remote A','openlist',1)`,
					fmt.Sprintf(`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<%d) INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) SELECT i,70,'/Example/'||i,'Example title '||i,'available' FROM n`, count),
					`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) SELECT id,'track.mp3','Example track','audio',128 FROM source_catalog_entry WHERE file_source_id=70`,
				} {
					if _, err = tx.Exec(statement); err != nil {
						t.Fatal(err)
					}
				}
				for _, language := range translationModelLanguages {
					if _, err := tx.Exec(`INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
						SELECT 'example:'||?||':'||id,'entry',id,'title',?,title,'Example translated '||id,'model',?,?,'index-user-v1' FROM source_catalog_entry WHERE file_source_id=70`, language, language, translation.ConfigKey(cfg), cfg.Model); err != nil {
						t.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				t.Logf("seed_ms=%.3f", float64(time.Since(start).Microseconds())/1000)
				start = time.Now()
				for i := range 20 {
					if _, err := s.db.Exec(`UPDATE source_catalog_entry SET title=? WHERE id=1`, fmt.Sprintf("Changed title %d", i)); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("title_update_mean_ms=%.3f", float64(time.Since(start).Microseconds())/20000)
				if _, err := s.db.Exec(`UPDATE source_catalog_entry SET title='Example title 1' WHERE id=1`); err != nil {
					t.Fatal(err)
				}
				start = time.Now()
				if _, err := s.db.Exec(`UPDATE source_catalog_entry SET status_checked_at='2000-01-01' WHERE file_source_id=70`); err != nil {
					t.Fatal(err)
				}
				t.Logf("unrelated_bulk_update_ms=%.3f", float64(time.Since(start).Microseconds())/1000)
				if !enabled {
					return
				}
				coverage, err := s.translationCorpusCoverageFor(context.Background(), cfg)
				if err != nil || !coverage.complete() {
					t.Fatalf("coverage not complete: %v", err)
				}
				if _, err := s.db.Exec(`UPDATE translation_prefill_state SET status='complete',config_key=?,checked_revision=? WHERE id=1`, translation.ConfigKey(cfg), coverage.Revision); err != nil {
					t.Fatal(err)
				}
				start = time.Now()
				for range 100 {
					if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("idle_tick_mean_ms=%.3f", float64(time.Since(start).Microseconds())/100000)
				if _, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				start = time.Now()
				for range 100 {
					if _, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("operator_hit_mean_ms=%.3f", float64(time.Since(start).Microseconds())/100000)
				start = time.Now()
				rows := make([]contentTranslationRow, 16)
				for i := range rows {
					rows[i] = contentTranslationRow{CacheKey: fmt.Sprintf("display:%d", i), EntityKind: "entry", EntityID: 1, Field: "tag", Language: "ja", SourceText: "Example tag", TranslatedText: "Example text", Origin: "model", ConfigKey: translation.ConfigKey(cfg)}
				}
				if _, err := s.saveContentTranslations(context.Background(), rows); err != nil {
					t.Fatal(err)
				}
				t.Logf("interactive_16_write_ms=%.3f", float64(time.Since(start).Microseconds())/1000)
			})
		}
	}
}
