package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// The operator request has the same 15-second budget as mixed-load. The fixture
// includes series membership, which a flat catalog does not exercise.
func TestTranslationSeriesCoverageRequestBudget(t *testing.T) {
	if os.Getenv("KIKOTO_TRANSLATION_RESOURCE_SCALE") != "1" {
		t.Skip("opt-in synthetic resource measurement")
	}
	s, host, admin, _ := translationHTTPFixture(t)
	cfg := mustTranslationConfig(t, s)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`INSERT INTO file_source(id,code,display_name,source_type,enabled)
		 VALUES(70,'example_series_source','Example Source','openlist',1)`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<42624)
		 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		 SELECT i,70,'/Example/'||i,'Example title '||i,'available' FROM n`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT id,'track.mp3','Example track','audio',128 FROM source_catalog_entry WHERE file_source_id=70`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<600)
		 INSERT INTO catalog_series(id,title) SELECT i,'Example series '||i FROM n`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		 SELECT (id-1)/10+1,id,(id-1)%10+1,'manual' FROM source_catalog_entry WHERE id<=6000`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, language := range translationModelLanguages {
		if _, err := tx.Exec(`INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,
			source_text,translated_text,origin,config_key,model,template_version)
			SELECT 'example:'||?||':'||kind||':'||id,kind,id,'title',?,title,
				'Example translated '||id,'model',?,?,'index-user-v1'
			FROM (SELECT 'entry' AS kind,id,title FROM source_catalog_entry WHERE file_source_id=70
				UNION ALL SELECT 'series',id,title FROM catalog_series)`,
			language, language, translation.ConfigKey(cfg), cfg.Model); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A background publication keeps the previous members visible while the
	// replacement order is prepared. Coverage must use that same snapshot.
	ctx, _, err := s.beginCatalogPublication(context.Background(), 70, "synthetic:coverage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.execCatalogWrite(ctx, `UPDATE catalog_series_member SET position=position+10`); err != nil {
		t.Fatal(err)
	}
	host.Client().Timeout = 15 * time.Second
	req, err := http.NewRequest(http.MethodGet, host.URL+"/api/settings/translation/prefill", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(admin)
	started := time.Now()
	res, err := host.Client().Do(req)
	if err != nil {
		t.Fatalf("cold series coverage request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var view translationPrefillView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	t.Logf("cold_coverage_ms=%.3f;entries=42624;series=600;members=6000;translations=172896", float64(time.Since(started).Microseconds())/1000)
	if res.StatusCode != http.StatusOK || view.Total != 43224 {
		t.Fatalf("coverage status=%d,total=%d, want 200/43224", res.StatusCode, view.Total)
	}
	if len(view.Languages) != 5 {
		t.Fatalf("reported languages = %d, want 5", len(view.Languages))
	}
	for _, coverage := range view.Languages {
		if coverage.Language == translation.SourceHant {
			continue
		}
		if coverage.Rows != 43224 || coverage.Missing != 0 || coverage.Stale != 0 || coverage.Failed != 0 {
			t.Fatalf("%s coverage = %+v", coverage.Language, coverage)
		}
	}
}
