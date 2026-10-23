package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// sourceID parses the id out of the fixture's base path. Tests seed rows
// directly, so they must name the same source the route resolves.
func (f catalogIndexFixture) sourceID(t *testing.T) int64 {
	t.Helper()
	id, err := strconv.ParseInt(strings.TrimPrefix(f.base, "/api/remote-sources/"), 10, 64)
	if err != nil {
		t.Fatalf("fixture base %q has no source id: %v", f.base, err)
	}
	return id
}

func sidecarFailuresFrom(t *testing.T, progress map[string]any) map[string]any {
	t.Helper()
	raw, ok := progress["sidecarFailures"].(map[string]any)
	if !ok {
		t.Fatalf("progress has no sidecarFailures object: %#v", progress)
	}
	return raw
}

// The index progress is the operator's only view of cards that stopped
// automatic credit-file probing. It must count exactly the settled rows of
// this source, split by the stored category, and it must not disturb evidence
// or personal records when the retry route reopens them.
func TestSourceCatalogIndexReportsStoppedSidecarFailures(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	sourceID := f.sourceID(t)
	// A second source proves the projection is scoped to one source.
	otherSource := metadataReviewExec(t, f.server.db, `INSERT INTO file_source(code,display_name,source_type)
		VALUES('example_remote_b','Example Remote B','openlist')`)
	metadataReviewExec(t, f.server.db, `INSERT INTO work(id,primary_code,title) VALUES(901,'RJ00000024','Example Work');`)
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_transient_attempts,sidecar_failure)
		VALUES(801,?,'/Example Work','Example Work',901,'{"sidecars":[]}',0,0,8,'temporarily_unavailable'),
			(802,?,'/Settled','Settled card',NULL,'',0,5,0,'unreadable'),
			(803,?,'/Waiting','Waiting card',NULL,'',1,0,1,'temporarily_unavailable'),
			(804,?,'/Clean','Clean card',NULL,'',0,0,0,''),
			(805,?,'/Moved','Moved card',NULL,'',0,5,0,'unreadable'),
			(807,?,'/Spent but pending','Spent card',NULL,'',1,5,0,'unreadable'),
			(806,?,'/Other source','Other source',NULL,'',0,5,0,'unreadable')`,
		sourceID, sourceID, sourceID, sourceID, sourceID, sourceID, otherSource)
	metadataReviewExec(t, f.server.db, `UPDATE source_catalog_entry SET relocated_to_id=801 WHERE id=805;
		INSERT INTO user_work_state(user_id,work_id,rating,note)
			SELECT id,901,4,'Private note' FROM user_account WHERE username='synthetic-user';
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
			VALUES(801,'readme.txt','readme.txt','text',24,1),
				(802,'readme.txt','readme.txt','text',24,1),
				(803,'readme.txt','readme.txt','text',24,1),
				(807,'readme.txt','readme.txt','text',24,1);`)

	progress := f.request(t, http.MethodGet, "/catalog/index", "", http.StatusOK)
	counts := sidecarFailuresFrom(t, progress)
	if counts["stopped"] != float64(3) || counts["temporarilyUnavailable"] != float64(1) || counts["unreadable"] != float64(2) {
		t.Fatalf("stopped counts = %#v", counts)
	}
	// A queued card, a clean card, a relocated card and another source's card
	// are not stopped work for this source.
	if counts["requestBudget"] != float64(0) {
		t.Fatalf("request budget count = %#v", counts)
	}

	// A reader without sources:write must not see the operator projection.
	createAccountManagedTestUser(t, f.server, "synthetic-listener", "synthetic-password")
	listener := loginTestSession(t, f.handler, "synthetic-listener", "synthetic-password")
	reviewRequest(t, f.handler, listener, http.MethodGet, f.base+"/catalog/index", "", http.StatusForbidden)

	reopened := f.request(t, http.MethodPost, "/catalog/sidecars/retry", `{}`, http.StatusOK)
	// 801, 802 and 807 are settled; 803 is still pending but records a failure,
	// so the route reopens it as well. Only the stopped count is the UI signal.
	if reopened["reopened"] != float64(4) {
		t.Fatalf("retry reopened = %#v", reopened)
	}
	after := sidecarFailuresFrom(t, f.request(t, http.MethodGet, "/catalog/index", "", http.StatusOK))
	if after["stopped"] != float64(0) || after["temporarilyUnavailable"] != float64(0) || after["unreadable"] != float64(0) {
		t.Fatalf("counts survived reopening: %#v", after)
	}

	// Reopening clears queue bookkeeping only. Evidence, the work association
	// and the personal review are untouched.
	var evidence string
	var workID *int64
	if err := f.server.db.QueryRow(`SELECT sidecar_json,work_id FROM source_catalog_entry WHERE id=801`).Scan(&evidence, &workID); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(evidence), &stored); err != nil {
		t.Fatalf("evidence was not preserved as JSON: %q (%v)", evidence, err)
	}
	if _, ok := stored["sidecars"]; !ok || workID == nil || *workID != 901 {
		t.Fatalf("reopening changed evidence or association: evidence=%q work=%v", evidence, workID)
	}
	var rating int
	var note string
	if err := f.server.db.QueryRow(`SELECT s.rating,s.note FROM user_work_state s
		JOIN user_account u ON u.id=s.user_id WHERE u.username='synthetic-user' AND s.work_id=901`).Scan(&rating, &note); err != nil {
		t.Fatal(err)
	}
	if rating != 4 || note != "Private note" {
		t.Fatalf("reopening changed the personal review: rating=%d note=%q", rating, note)
	}
}

// A source with no stopped cards still reports the projection, so the UI can
// read it without branching on a missing field. The route serves the idle
// shape when no scan exists yet, which is what a freshly created source sees.
func TestSourceCatalogIndexProgressIncludesZeroSidecarFailures(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	progress := f.request(t, http.MethodGet, "/catalog/index", "", http.StatusOK)
	if progress["status"] != "idle" {
		t.Fatalf("fresh source status = %#v", progress["status"])
	}
	counts := sidecarFailuresFrom(t, progress)
	if counts["stopped"] != float64(0) || counts["temporarilyUnavailable"] != float64(0) || counts["unreadable"] != float64(0) {
		t.Fatalf("fresh source counts = %#v", counts)
	}
}
