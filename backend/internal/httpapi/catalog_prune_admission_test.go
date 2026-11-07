package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/catalogprune"
)

func TestCatalogPruneUsesCurrentPublishedFiles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		generation int
		count      int
		audio      int
	}{
		{"folders", "folder", 1, 5001, 1},
		{"old_generation", "other", 0, 5001, 1},
		{"non_audio_boundary", "other", 1, 2000, 1},
		{"files_boundary", "other", 1, 4995, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, host, admin, _ := translationHTTPFixture(t)
			metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
			INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
			INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
			INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,last_seen_generation) VALUES(1,7,'/Example Work','Example Work',1,1);
			INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) VALUES(1,'track.mp3','track.mp3','audio',128,1);`)
			metadataReviewExec(t, s.db, fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<%d)
			INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
			SELECT 1,'item'||i,'item'||i,'%s',0,%d FROM n`, tc.count, tc.kind, tc.generation))
			for i := 1; i < tc.audio; i++ {
				metadataReviewExec(t, s.db, fmt.Sprintf(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) VALUES(1,'track%d.mp3','track','audio',128,1)`, i))
			}
			translationRequest(t, host, admin, "GET", "/api/source-catalog/1", "", 200)
			report, err := catalogprune.BuildReport(context.Background(), s.db, catalogprune.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Entries) != 0 {
				t.Fatalf("readable card selected for deletion: %+v", report.Entries)
			}
			if _, err = catalogprune.Apply(context.Background(), s.db, report, catalogprune.Options{}); err != nil {
				t.Fatal(err)
			}
			translationRequest(t, host, admin, "GET", "/api/source-catalog/1", "", 200)
		})
	}
}

// A trigger represents a catalog publication between the separately committed
// prune batches. Total row count stays unchanged while the second card becomes
// playable, so checking only the report's file count cannot protect it.
func TestCatalogPruneRechecksAdmissionInsideEachBatch(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work A'),(2,'RJ00000001','Example Work B');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,7,'/Example A','Example Work A',1),(2,7,'/Example B','Example Work B',2);
	WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2006)
	INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
	SELECT e.id,'item'||n.i,'item'||n.i,CASE WHEN n.i<=4 THEN 'audio' ELSE 'other' END,128 FROM n CROSS JOIN source_catalog_entry e;
	CREATE TRIGGER synthetic_publish_between_prune_batches AFTER DELETE ON source_catalog_entry WHEN OLD.id=1 BEGIN
	UPDATE source_catalog_file SET kind='audio' WHERE catalog_entry_id=2 AND relative_path='item5'; END;`)
	translationRequest(t, host, admin, "PATCH", "/api/works/2/user-state", `{"rating":4,"note":"Example personal note"}`, 200)
	report, err := catalogprune.BuildReport(context.Background(), s.db, catalogprune.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 2 {
		t.Fatalf("planned entries = %+v", report.Entries)
	}
	result, err := catalogprune.Apply(context.Background(), s.db, report, catalogprune.Options{BatchSize: 1})
	if err == nil {
		t.Fatalf("accepted stale second batch: %+v", result)
	}
	if result.Totals.Entries != 1 {
		t.Fatalf("completed batches = %+v", result)
	}
	translationRequest(t, host, admin, "GET", "/api/source-catalog/2", "", 200)
	current := translationRequest(t, host, admin, "GET", "/api/works/2?includeMedia=false", "", 200)
	assertReview(t, current, float64(4), "Example personal note")
}

func TestCatalogPruneIgnoresStaleAudioAndKeepsPersonalState(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,last_seen_generation) VALUES(1,7,'/Example Work','Example Work',1,1);
	INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) VALUES
	(1,'old.mp3','old.mp3','audio',128,0),(1,'readme.txt','readme.txt','other',128,1);`)
	translationRequest(t, host, admin, "PATCH", "/api/works/1/user-state", `{"rating":3,"note":"Example retained note"}`, 200)
	translationRequest(t, host, admin, "GET", "/api/source-catalog/1", "", 404)
	report, err := catalogprune.BuildReport(context.Background(), s.db, catalogprune.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Entries[0].Reason != "no_audio" || report.Entries[0].Files != 1 {
		t.Fatalf("stale audio changed admission: %+v", report.Entries)
	}
	if _, err = catalogprune.Apply(context.Background(), s.db, report, catalogprune.Options{}); err != nil {
		t.Fatal(err)
	}
	current := translationRequest(t, host, admin, "GET", "/api/works/1?includeMedia=false", "", 200)
	assertReview(t, current, float64(3), "Example retained note")
	backup := translationRequest(t, host, admin, "GET", "/api/user-data/export", "", 200)
	if !strings.Contains(mustJSON(backup), "Example retained note") {
		t.Fatal("pruned work lost its portable personal record")
	}
}
