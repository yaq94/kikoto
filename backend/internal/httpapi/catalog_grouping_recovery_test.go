package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestCatalogFiniteListingResumesCommittedPage(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var firstHits atomic.Int64
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path    string `json:"path"`
			Page    int    `json:"page"`
			PerPage int    `json:"per_page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.Page == 1 {
			firstHits.Add(1)
		}
		if input.Page == 2 && fail.Load() {
			http.Error(w, "synthetic page failure", 500)
			return
		}
		items := []map[string]any{}
		start := (input.Page - 1) * input.PerPage
		for i := start; i < min(151, start+input.PerPage); i++ {
			items = append(items, looseAudio(fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26)))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": 151}})
	}))
	result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	if result["status"] != "partial" {
		t.Fatalf("failed listing=%#v", result)
	}
	var items, page int
	if err := f.server.db.QueryRow(`SELECT received,next_page FROM source_catalog_listing WHERE observed_version='finite'`).Scan(&items, &page); err != nil || items != 100 || page != 2 {
		t.Fatalf("checkpoint=%d/%d err=%v", items, page, err)
	}
	if list := f.request(t, http.MethodGet, "/works", "", 200); list["total"] != float64(0) {
		t.Fatalf("partial input published=%v", list)
	}
	fail.Store(false)
	result = f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	if result["status"] != "complete" {
		t.Fatalf("recovered listing=%#v", result)
	}
	if firstHits.Load() != 1 {
		t.Fatalf("committed page fetched %d times", firstHits.Load())
	}
	if list := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); list["total"] != float64(151) {
		t.Fatalf("recovered complete set=%v", list["total"])
	}
}

func TestCatalogGroupingRestartDoesNotReclassifyCommittedNames(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status) VALUES(7,'/Creator',1,1,'/Creator','complete');
	 WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<257) INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes) SELECT 7,'/Creator','Example Episode '||i||'.mp3',1,'audio',1 FROM n;
	 CREATE TRIGGER synthetic_grouping_failure BEFORE INSERT ON source_catalog_grouping_name WHEN (SELECT count(*) FROM source_catalog_grouping_name)>=64 BEGIN SELECT RAISE(ABORT,'synthetic grouping interruption'); END;`)
	s := &Server{db: db}
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	in := catalogGroupingIndexInput{SourceID: 7, Generation: 1, Root: "/Creator", Revision: 1}
	if _, err := s.prepareCatalogGrouping(ctx, in); err == nil {
		t.Fatal("interruption was not reached")
	}
	var count int
	var cursor string
	if err := db.QueryRow(`SELECT count(*),(SELECT cursor FROM source_catalog_grouping LIMIT 1) FROM source_catalog_grouping_name`).Scan(&count, &cursor); err != nil || count != 64 || cursor == "" {
		t.Fatalf("grouping checkpoint=%d/%q %v", count, cursor, err)
	}
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_grouping_failure`)
	id, err := (&Server{db: db}).prepareCatalogGrouping(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_grouping_name WHERE grouping_id=?`, id).Scan(&count); err != nil || count != 257 {
		t.Fatalf("complete grouping=%d %v", count, err)
	}
}
