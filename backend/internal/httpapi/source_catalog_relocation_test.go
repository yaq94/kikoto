package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSourceCatalogRelocationRequiresUniqueConsistentEvidence(t *testing.T) {
	for _, name := range []string{"missing_hash", "conflicting_hash", "malformed_hash", "multiple_candidates", "reviewed_candidate"} {
		t.Run(name, func(t *testing.T) {
			var renamed atomic.Bool
			f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Path string `json:"path"`
				}
				_ = json.NewDecoder(r.Body).Decode(&in)
				var hash any = map[string]string{"sha256": strings.Repeat("c", 64)}
				if name == "missing_hash" {
					hash = nil
				}
				if renamed.Load() && name == "conflicting_hash" {
					hash = map[string]string{"sha256": strings.Repeat("d", 64)}
				}
				if renamed.Load() && name == "malformed_hash" {
					hash = map[string]any{"sha256": 99}
				}
				items := []map[string]any{{"name": "track.mp3", "size": 100, "hash_info": hash}}
				if in.Path == "/" {
					items = []map[string]any{{"name": "Original Work", "is_dir": true}}
					if renamed.Load() {
						items = []map[string]any{{"name": "Candidate A", "is_dir": true}}
						if name == "multiple_candidates" {
							items = append(items, map[string]any{"name": "Candidate B", "is_dir": true})
						}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
			}))
			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":1}`, 202)
			f.drain(t)
			old := f.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)
			base := "/api/source-catalog/" + strconv.Itoa(int(old["catalogEntryId"].(float64)))
			state := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example original note"}`, 200)
			renamed.Store(true)
			f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
			if name == "reviewed_candidate" {
				found := false
				for range 30 {
					if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
						t.Fatal(err)
					}
					list := f.request(t, http.MethodGet, "/works?q=Candidate%20A", "", 200)
					if list["total"] == float64(1) {
						candidate := list["works"].([]any)[0].(map[string]any)
						id := strconv.Itoa(int(candidate["catalogEntryId"].(float64)))
						reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+id+"/user-state", `{"rating":2,"note":"Example independent note"}`, 200)
						found = true
						break
					}
				}
				if !found {
					t.Fatal("candidate was not published before final reconciliation")
				}
			}
			f.drain(t)
			detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
			if detail["workId"] != state["workId"] || detail["title"] != "Original Work" || detail["status"] != "missing" {
				t.Fatalf("uncertain evidence moved identity: %#v", detail)
			}
			assertReview(t, detail, float64(4), "Example original note")
			candidates := detail["relocationCandidates"].([]any)
			want := 1
			if name == "multiple_candidates" {
				want = 2
			}
			if len(candidates) != want {
				t.Fatalf("candidates=%d, want %d", len(candidates), want)
			}
			reason := "insufficient_evidence"
			if name == "multiple_candidates" {
				reason = "ambiguous_evidence"
			}
			if name == "reviewed_candidate" {
				reason = "conflicting_identity"
			}
			if candidates[0].(map[string]any)["reason"] != reason {
				t.Fatalf("candidate reason: %#v", candidates)
			}
		})
	}
}

// OpenList commonly returns no provider hash. A multi-file release still has
// one stable manifest: the same relative paths and sizes. It updates the
// location only when that manifest is unique, and keeps the card, work and
// review.
func TestSourceCatalogRelocationUsesUniqueManifestWithoutProviderHashes(t *testing.T) {
	var renamed atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{{"name": "01 track.mp3", "size": 100}, {"name": "02 track.mp3", "size": 200}}
		if in.Path == "/" {
			name := "Original Work"
			if renamed.Load() {
				name = "Renamed Work"
			}
			items = []map[string]any{{"name": name, "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":1}`, 202)
	f.drain(t)
	old := f.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)
	base := "/api/source-catalog/" + strconv.Itoa(int(old["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example original note"}`, 200)
	renamed.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	f.drain(t)
	detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
	if detail["title"] != "Renamed Work" || detail["status"] != "available" {
		t.Fatalf("manifest rename did not update the location: %#v", detail)
	}
	assertReview(t, detail, float64(4), "Example original note")
	list := f.request(t, http.MethodGet, "/works", "", 200)
	if list["total"] != float64(1) {
		t.Fatalf("manifest rename duplicated the card: %#v", list)
	}
}

// A one-track recording is too weak: any same-sized single file would match.
func TestSourceCatalogRelocationKeepsSingleFileRenameUnconfirmed(t *testing.T) {
	var renamed atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{{"name": "track.mp3", "size": 100}}
		if in.Path == "/" {
			name := "Original Work"
			if renamed.Load() {
				name = "Renamed Work"
			}
			items = []map[string]any{{"name": name, "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":1}`, 202)
	f.drain(t)
	old := f.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)
	base := "/api/source-catalog/" + strconv.Itoa(int(old["catalogEntryId"].(float64)))
	renamed.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	f.drain(t)
	detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
	if detail["title"] != "Original Work" || detail["status"] != "missing" {
		t.Fatalf("single-file manifest moved identity: %#v", detail)
	}
}

// A confirmed rename carries the release's credit evidence with it. The new
// location must not keep an unrelated sidecar that happened to be published
// under the same title.
func TestSourceCatalogRelocationCarriesSidecarEvidence(t *testing.T) {
	var renamed atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		var items []map[string]any
		switch {
		case in.Path == "/":
			name := "Original Work"
			if renamed.Load() {
				name = "Renamed Work"
			}
			items = []map[string]any{{"name": name, "is_dir": true}}
		case r.URL.Path == "/api/fs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": "readme.txt", "size": 40, "raw_url": "http://" + r.Host + "/readme",
			}})
			return
		case r.URL.Path == "/readme":
			_, _ = w.Write([]byte("配音：Example Voice\r\n"))
			return
		default:
			items = []map[string]any{
				{"name": "01.mp3", "size": 100}, {"name": "02.mp3", "size": 200},
				{"name": "readme.txt", "size": 40},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	f.drain(t)
	var before string
	if err := f.server.db.QueryRow(`SELECT sidecar_json FROM source_catalog_entry WHERE title='Original Work'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == "" {
		t.Fatal("sidecar evidence was not stored")
	}
	renamed.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	f.drain(t)
	var after string
	if err := f.server.db.QueryRow(`SELECT sidecar_json FROM source_catalog_entry WHERE root_path LIKE '%Renamed Work%'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var beforeCache, afterCache catalogSidecarCache
	if json.Unmarshal([]byte(before), &beforeCache) != nil || json.Unmarshal([]byte(after), &afterCache) != nil {
		t.Fatal("invalid stored sidecar cache")
	}
	if len(beforeCache.Sidecars) != 1 || len(afterCache.Sidecars) != 1 ||
		strings.Join(afterCache.Sidecars[0].VoiceActors, "|") != "Example Voice" {
		t.Fatalf("sidecar evidence was not carried with the rename: before=%s after=%s", before, after)
	}
}
