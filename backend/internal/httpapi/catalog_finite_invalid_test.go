package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestCatalogFiniteInvalidPagesKeepPublishedSnapshot(t *testing.T) {
	for _, reason := range []string{"duplicate", "total_changed", "early_empty", "unknown_total"} {
		t.Run(reason, func(t *testing.T) {
			var changed atomic.Bool
			f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Page    int `json:"page"`
					PerPage int `json:"per_page"`
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
					return
				}
				items, total := []map[string]any{looseAudio("Original.mp3")}, 1
				if changed.Load() {
					total = 151
					items = nil
					start := (in.Page - 1) * in.PerPage
					if reason == "duplicate" && in.Page == 2 {
						start = 0
					}
					for i := start; i < min(start+in.PerPage, 151); i++ {
						items = append(items, looseAudio(fmt.Sprintf("Example Recording %03d.mp3", i)))
					}
					if in.Page == 2 {
						switch reason {
						case "total_changed":
							total = 150
						case "early_empty":
							items = nil
						}
					}
					if reason == "unknown_total" {
						total = -1
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": total, "content": items}})
			}))
			f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
			changed.Store(true)
			if result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200); result["status"] != "partial" {
				t.Fatalf("invalid listing=%#v", result)
			}
			if got := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200)["total"]; got != float64(1) {
				t.Fatalf("invalid pages removed old card: %v", got)
			}
			var file string
			if err := f.server.db.QueryRow(`SELECT relative_path FROM published_source_catalog_file WHERE kind='audio'`).Scan(&file); err != nil || file != "Original.mp3" {
				t.Fatalf("old file=%q %v", file, err)
			}
		})
	}
}

func TestCatalogFiniteMultipleRootsKeepSuccessfulSets(t *testing.T) {
	tree := map[string][]map[string]any{"/Example": {looseAudio("Original.mp3")}, "/Other": {}}
	for i := 0; i < 521; i++ {
		tree["/Other"] = append(tree["/Other"], looseAudio(fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26)))
	}
	f := newLooseWorkFixture(t, tree)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	metadataReviewExec(t, f.server.db, `UPDATE file_source SET config_json=json_set(config_json,'$.openListRoots',json('["/Example","/Other"]')) WHERE id=?`, f.sourceID(t))
	f.failPath("/Example")
	if result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200); result["status"] != "partial" {
		t.Fatalf("multi root=%#v", result)
	}
	if got := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200)["total"]; got != float64(522) {
		t.Fatalf("old and successful sets=%v", got)
	}
}
