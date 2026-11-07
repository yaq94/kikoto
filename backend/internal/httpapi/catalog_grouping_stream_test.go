package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestCatalogPersistedGroupingPreservesTitleEvidence(t *testing.T) {
	cases := [][]string{
		{"RJ00000001 上.mp3", "RJ00000001 下.mp3", "B011 First.mp3", "B011 Second.mp3", "B20 Disc1 Opening.mp3", "B20 Disc2 Ending.mp3"},
		{"Example Episode 1.mp3", "Example Episode 2.mp3", "Example Episode 2 (FLAC).flac", "Other.mp3"},
		{"Example 前編.mp3", "Example 後編.mp3", "Example 中篇.mp3", "Vol.1.mp3", "Vol.2.mp3", "T001.mp3", "T002.mp3"},
		{"01.mp3", "02.mp3", "Bonus.mp3"},
	}
	for i, names := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			db := openMigratedTestDB(t)
			seedCatalogPlanSource(t, db)
			metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status) VALUES(7,'/Creator',1,1,'/Creator','complete')`)
			for _, name := range names {
				metadataReviewExec(t, db, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes) VALUES(7,'/Creator',?,1,'audio',1)`, name)
			}
			s := &Server{db: db}
			ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
			id, err := s.prepareCatalogGrouping(ctx, catalogGroupingIndexInput{SourceID: 7, Generation: 1, Root: "/Creator", Revision: 1})
			if err != nil {
				t.Fatal(err)
			}
			var mode string
			if err = db.QueryRow(`SELECT mode FROM source_catalog_grouping WHERE id=?`, id).Scan(&mode); err != nil {
				t.Fatal(err)
			}
			wantMode := catalogWorkSplitModeNames(names)
			if mode != wantMode {
				t.Fatalf("mode=%s want=%s", mode, wantMode)
			}
			if mode == catalogWorkSplitSingle {
				return
			}
			legacy := newCatalogSplitPlan("/Creator", names, mode)
			indexed := catalogSplitPlan{root: "/Creator", groupingID: id}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			for _, name := range names {
				want, has := legacy.targetFor(name)
				got, ok, err := indexed.indexedTargetFor(ctx, tx, name)
				if err != nil || ok != has || !reflect.DeepEqual(got, want) {
					t.Errorf("target %q=%+v/%v err=%v, want %+v/%v", name, got, ok, err, want, has)
				}
			}
		})
	}
}

func TestCatalogLargeDirectoryPublishesPersistedGroups(t *testing.T) {
	const count = 5101
	items := []map[string]any{}
	for i := 0; i < count; i++ {
		items = append(items, looseAudio(fmt.Sprintf("Example Recording %c%c%c.mp3", 'a'+i/(26*26), 'a'+(i/26)%26, 'a'+i%26)))
	}
	f, closeRemote := newContractIndexFixture(t, map[string][]map[string]any{"/": {looseDir("Creator")}, "/Creator": items})
	defer closeRemote()
	f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":100,"pagesPerBatch":32,"intervalMs":100}`, 202)
	if progress := f.drainIndex(t); progress["status"] != "complete" {
		t.Fatalf("full scan=%#v", progress)
	}
	list := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200)
	if list["total"] != float64(count) {
		t.Fatalf("works=%v, want %d", list["total"], count)
	}
	var names, targets int
	if err := f.server.db.QueryRow(`SELECT count(*),(SELECT count(*) FROM source_catalog_grouping_target) FROM source_catalog_grouping_name`).Scan(&names, &targets); err != nil || names != count || targets != count {
		t.Fatalf("derived complete set=%d/%d, err=%v", names, targets, err)
	}
	var sourceID, generation int64
	var revision int
	if err := f.server.db.QueryRow(`SELECT file_source_id,generation,revision FROM source_catalog_directory WHERE path='/Creator'`).Scan(&sourceID, &generation, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.prepareCatalogGrouping(context.Background(), catalogGroupingIndexInput{SourceID: sourceID, Generation: generation, Root: "/Creator", Revision: revision}); err != nil {
		t.Fatal(err)
	}
	var versions int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_grouping WHERE root_path='/Creator'`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("retry grouping versions=%d err=%v", versions, err)
	}
}

// The finite request budget bounds remote work; a creator's item count is not
// the admission count of each independently playable card within it.
func TestCatalogFiniteScanKeepsEveryWorkAboveLegacyRootLimit(t *testing.T) {
	const count = 2101
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Path    string `json:"path"`
			Page    int    `json:"page"`
			PerPage int    `json:"per_page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.Path != "/Example" {
			t.Errorf("unexpected path %q", input.Path)
			http.NotFound(w, r)
			return
		}
		start := max(0, (input.Page-1)*input.PerPage)
		items := []map[string]any{}
		for i := start; i < min(count, start+input.PerPage); i++ {
			name := fmt.Sprintf("Example Recording %c%c%c.mp3", 'a'+i/(26*26), 'a'+(i/26)%26, 'a'+i%26)
			items = append(items, looseAudio(name))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": count}})
	}))
	result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	if result["status"] != "complete" {
		t.Fatalf("finite creator scan=%#v, want complete", result)
	}
	list := f.request(t, http.MethodGet, "/works?pageSize=1", "", http.StatusOK)
	if list["total"] != float64(count) {
		t.Fatalf("complete independent works=%v, want %d", list["total"], count)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM published_source_catalog_file WHERE kind='audio'`).Scan(&files); err != nil || files != count {
		t.Fatalf("published audio=%d err=%v, want %d", files, err, count)
	}
}
