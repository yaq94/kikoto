package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// Retained identities and a pending source publication must not make loading
// one candidate's locations scan the entire catalog for every comparison.
func TestWorkMatchingPublishedCatalogRequestBudget(t *testing.T) {
	if os.Getenv("KIKOTO_CATALOG_RESOURCE_SCALE") != "1" {
		t.Skip("opt-in synthetic resource measurement")
	}
	s, host, admin, _ := translationHTTPFixture(t)
	for _, query := range []string{
		`INSERT INTO file_source(id,code,display_name,source_type) VALUES(70,'example_remote_a','Example Remote A','openlist')`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<120000)
		 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		 SELECT i,CASE WHEN i<=42624 THEN 70 ELSE NULL END,'/Example/'||i,'Example title '||i,'available' FROM n`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT id,'track.mp3','Example track','audio',128 FROM source_catalog_entry WHERE file_source_id=70`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<600)
		 INSERT INTO catalog_series(id,title) SELECT i,'Example series '||i FROM n`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		 SELECT (id-1)/10+1,id,(id-1)%10+1,'manual' FROM source_catalog_entry WHERE id<=6000`,
		`WITH RECURSIVE n(i) AS(SELECT 11 UNION ALL SELECT i+1 FROM n WHERE i<113)
		 INSERT INTO work(id,title,release_date) SELECT i,'Example Work','2020-01-02' FROM n`,
		`INSERT INTO person(id,display_name) VALUES(1,'Example Voice');
		 INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor'),(113,1,'voice_actor')`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	ctx, _, err := s.beginCatalogPublication(context.Background(), 70, "synthetic:matching")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET title='Example pending title' WHERE id<=14000`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execCatalogWrite(ctx, `UPDATE catalog_series_member SET position=position+10`); err != nil {
		t.Fatal(err)
	}
	workReferenceRequest(t, host, admin, http.MethodPut, "/api/work-matching/settings", `{"candidateLimit":200}`, http.StatusOK, nil)
	var result map[string]any
	started := time.Now()
	workReferenceRequest(t, host, admin, http.MethodPost, "/api/work-matching", `{"kind":"work","id":11}`, http.StatusOK, &result)
	t.Logf("matching_request_ms=%.3f;entries=120000;source_entries=42624;candidates=102", float64(time.Since(started).Microseconds())/1000)
	if result["status"] != "review" || result["rule"] != "ambiguous_evidence" || result["candidateCount"] != float64(102) {
		t.Fatalf("matching lost ambiguous candidates: %#v", result)
	}
	var operations []any
	workReferenceRequest(t, host, admin, http.MethodGet, "/api/shared-associations?kind=work&id=11", "", http.StatusOK, &operations)
	if len(operations) != 0 {
		t.Fatal("matching created an association from ambiguous evidence")
	}
}
