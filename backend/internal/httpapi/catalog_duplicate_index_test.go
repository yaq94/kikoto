package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestCatalogDuplicateSizeFallbackUsesRange(t *testing.T) {
	db := openMigratedTestDB(t)
	plan := explainCatalogPlan(t, db, catalogDuplicateSizeQuery, int64(1), int64(1000), int64(2000))
	if !strings.Contains(plan, "size_bytes>?") || !strings.Contains(plan, "size_bytes<?") {
		t.Fatalf("unmatched size fallback scans unrelated candidates: %s", plan)
	}
}

func TestCatalogIndexedEqualHashCandidatesAreConsumedOnce(t *testing.T) {
	audit := &catalogWriteAudit{}
	db := openCatalogWriteAuditedDB(t, audit)
	seedCatalogPlanSource(t, db)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status) VALUES(7,'/Creator',1,1,'/Creator','complete'),(7,'/Creator/Package',1,1,'/Creator','complete');
	WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1025) INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash) SELECT 7,d.path,printf('Example Recording %04d.mp3',i),1,'audio',1,'synthetic-identical-hash' FROM n CROSS JOIN source_catalog_directory d`)
	s := &Server{db: db}
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	in := catalogGroupingIndexInput{SourceID: 7, Generation: 1, Root: "/Creator/Package", Revision: 1}
	audit.start()
	if err := s.prepareCatalogDuplicateIndex(ctx, in); err != nil {
		t.Fatal(err)
	}
	audit.stop()
	var matched, available int
	var duplicate bool
	if err := db.QueryRow(`SELECT matched,duplicate,(SELECT count(*) FROM source_catalog_duplicate_available) FROM source_catalog_duplicate_comparison`).Scan(&matched, &duplicate, &available); err != nil || matched != 1025 || !duplicate || available != 0 {
		t.Fatalf("one-to-one candidates=%d/%v/%d %v", matched, duplicate, available, err)
	}
	if err := s.prepareCatalogDuplicateIndex(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_duplicate_match`).Scan(&matched); err != nil || matched != 1025 {
		t.Fatalf("retry matches=%d %v", matched, err)
	}
	writes, transactions, peak, tables := audit.allDetails()
	if peak > 129 {
		t.Fatalf("candidate batch=%d %v", peak, tables)
	}
	t.Logf("equal_hash=1025 complete_matches=%d total_writes=%d transactions=%d peak_rows=%d tables=%v", matched, writes, transactions, peak, tables)
}

func TestCatalogIndexedPackageKeepsUniqueBonusAndCompleteOriginalSet(t *testing.T) {
	for _, hashed := range []bool{false, true} {
		t.Run(fmt.Sprint(hashed), func(t *testing.T) {
			const count = 130
			original := []map[string]any{looseDir("Package")}
			copy := []map[string]any{}
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26)
				file := looseAudioSized(name, 10000+i*10000)
				if hashed {
					file = looseAudioHashed(name, 10000+i*10000, fmt.Sprintf("synthetic-digest-%d", i))
				}
				original = append(original, file)
				copy = append(copy, file)
			}
			copy = append(copy, looseAudioSized("Unique bonus.mp3", 90000000))
			f, closeRemote := newContractIndexFixture(t, map[string][]map[string]any{"/": {looseDir("Creator")}, "/Creator": original, "/Creator/Package": copy})
			defer closeRemote()
			f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":100,"pagesPerBatch":32,"intervalMs":100}`, 202)
			if p := f.drainIndex(t); p["status"] != "complete" {
				t.Fatalf("index=%#v", p)
			}
			var files, bonus int
			if err := f.server.db.QueryRow(`SELECT count(*),sum(relative_path LIKE '%Unique bonus.mp3') FROM published_source_catalog_file WHERE kind='audio'`).Scan(&files, &bonus); err != nil || files != count+1 || bonus != 1 {
				t.Fatalf("complete canonical audio=%d bonus=%d err=%v", files, bonus, err)
			}
			list := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200)
			if list["total"] != float64(count+1) {
				t.Fatalf("work count=%v, want %d", list["total"], count+1)
			}
			var matched, unique int
			if err := f.server.db.QueryRow(`SELECT matched,unique_count FROM source_catalog_duplicate_comparison WHERE duplicate=1`).Scan(&matched, &unique); err != nil || matched != count || unique != 1 {
				t.Fatalf("durable comparison=%d/%d %v", matched, unique, err)
			}
		})
	}
}

func TestCatalogIndexedAncestorUsesUnicodeSimpleFold(t *testing.T) {
	original := []map[string]any{looseDir("Package"), looseAudioSized("Σ.mp3", 12345), looseAudioSized("K.mp3", 67890)}
	for i := 0; i < 128; i++ {
		original = append(original, looseAudioSized(fmt.Sprintf("Example Recording %04d.mp3", i), 100000+i*10000))
	}
	f, closeRemote := newContractIndexFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")}, "/Creator": original,
		"/Creator/Package":       {looseDir("wav"), looseDir("Other")},
		"/Creator/Package/wav":   {looseAudioSized("ς.mp3", 12345), looseAudioSized("K.mp3", 67890)},
		"/Creator/Package/Other": {{"name": "notes.txt", "is_dir": false, "size": 1}},
	})
	defer closeRemote()
	f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":100,"pagesPerBatch":32,"intervalMs":100}`, 202)
	if p := f.drainIndex(t); p["status"] != "complete" {
		t.Fatalf("index=%#v", p)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM published_source_catalog_file WHERE kind='audio'`).Scan(&files); err != nil || files != 130 {
		t.Fatalf("Unicode-equivalent ancestor copies survived: files=%d err=%v", files, err)
	}
}
