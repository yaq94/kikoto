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
