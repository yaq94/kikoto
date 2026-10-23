package httpapi

import (
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/personal"
)

func TestPersonalTransferLegacyAliasAndExactEditionRemainCompatible(t *testing.T) {
	db, app, admin, _, _ := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Original'),(2,'RJ00000001','Example Edition');
	 INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,1,'RJ00000000');
	 INSERT INTO work_edition(work_id,logical_work_id,provider_id,primary_code,is_canonical) VALUES(1,1,2,'RJ00000000',1),(2,1,2,'RJ00000001',0);
	 INSERT INTO work_code_alias(logical_work_id,provider_id,primary_code) VALUES(1,2,'RJ00000004');`)
	legacy := `{"format":"kikoto","data":{"format":"kikoto-user-data","version":1,"works":[{"primaryCode":"rj00000004","listeningStatus":"finished","rating":2,"note":"Legacy alias review"}]}}`
	var result personal.ImportResult
	workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", legacy, 200, &result)
	if result.ImportedWorks != 1 {
		t.Fatalf("legacy alias=%+v", result)
	}
	workReferenceRequest(t, app, admin, "PATCH", "/api/works/2/user-state", `{"rating":5,"note":"Exact edition review"}`, 200, nil)
	var backup personal.Backup
	workReferenceRequest(t, app, admin, "GET", "/api/user-data/export", "", 200, &backup)
	if len(backup.Works) != 2 {
		t.Fatalf("editions not exported independently: %+v", backup)
	}
	workReferenceRequest(t, app, admin, "PATCH", "/api/works/2/user-state", `{"rating":null,"note":""}`, 200, nil)
	workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "overwrite"}), 200, &result)
	if result.ImportedWorks != 2 {
		t.Fatalf("exact edition restore=%+v", result)
	}
	var work map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/works/1?includeMedia=false", "", 200, &work)
	assertReview(t, work, float64(2), "Legacy alias review")
	workReferenceRequest(t, app, admin, "GET", "/api/works/2?includeMedia=false", "", 200, &work)
	assertReview(t, work, float64(5), "Exact edition review")
}

func TestPersonalTransferKeepsExactReferencesAfterSharedSplit(t *testing.T) {
	db, app, admin, other, _ := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example A'),(2,'RJ00000001','Example B');`)
	var link map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"work","id":1},"right":{"kind":"work","id":2}}`, 201, &link)
	url := "/api/shared-associations/" + strconv.Itoa(int(link["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": link["version"]}), 200, &link)
	workReferenceRequest(t, app, admin, "PATCH", "/api/works/2/user-state", `{"rating":4,"note":"Example shared review"}`, 200, nil)
	var backup personal.Backup
	workReferenceRequest(t, app, admin, "GET", "/api/user-data/export", "", 200, &backup)
	if len(backup.Works) != 2 {
		t.Fatalf("exact references were collapsed: %+v", backup)
	}
	var split map[string]any
	workReferenceRequest(t, app, admin, "POST", url+"/split-preview", "{}", 201, &split)
	splitURL := "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", splitURL+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, &split)
	workReferenceRequest(t, app, admin, "POST", splitURL+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "left"}), 200, &split)
	if split["status"] != "completed" {
		t.Fatalf("split=%#v", split)
	}
	workReferenceRequest(t, app, other, "PATCH", "/api/works/2/user-state", `{"rating":1,"note":"Other private review"}`, 200, nil)
	for _, w := range backup.Works {
		w.Note = "Restored exact " + w.PrimaryCode
		one := backup
		one.Works = []personal.BackupWork{w}
		var result personal.ImportResult
		workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", mustJSON(map[string]any{"format": "kikoto", "data": one, "conflict": "overwrite"}), 200, &result)
		if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
			t.Fatalf("exact import=%+v", result)
		}
	}
	for index, code := range []string{"RJ00000000", "RJ00000001"} {
		var work map[string]any
		workReferenceRequest(t, app, admin, "GET", "/api/works/"+strconv.Itoa(index+1)+"?includeMedia=false", "", 200, &work)
		assertReview(t, work, float64(4), "Restored exact "+code)
	}
	var otherWork map[string]any
	workReferenceRequest(t, app, other, "GET", "/api/works/2?includeMedia=false", "", 200, &otherWork)
	assertReview(t, otherWork, float64(1), "Other private review")
	var library map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/works", "", 200, &library)
	if library["total"] != float64(2) {
		t.Fatal("personal import recreated a shared association")
	}
}

func TestPersonalTransferReportsContradictoryAndRetainedSourceReferences(t *testing.T) {
	db, app, admin, other, _ := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example A'),(2,'RJ00000001','Example B');
	 INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES(1,'/Example A','Example A',1);`)
	for _, id := range []string{"1", "2"} {
		workReferenceRequest(t, app, admin, "PATCH", "/api/works/"+id+"/user-state", `{"rating":2,"note":"Existing owner"}`, 200, nil)
	}
	workReferenceRequest(t, app, other, "PATCH", "/api/works/1/user-state", `{"rating":5,"note":"Other secret"}`, 200, nil)
	var backup personal.Backup
	workReferenceRequest(t, app, admin, "GET", "/api/user-data/export", "", 200, &backup)
	first := backup.Works[0]
	originalRef := first.Identity.Ref
	backup.Works[0].Identity.Ref = backup.Works[1].Identity.Ref
	backup.Works[0].Note = "Wrong identity"
	backup.Works[1].Identity = &personal.BackupIdentity{Ref: "w:00000000000000000000000000000001", Kind: "work", Title: "Example B"}
	backup.Works[1].Note = "Safe updated review"
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "overwrite"})
	var preview personal.ImportPreview
	workReferenceRequest(t, app, admin, "POST", "/api/user-data/import/preview", body, 200, &preview)
	if !strings.Contains(mustJSON(preview), "identity_conflict") || strings.Contains(mustJSON(preview), "Other secret") {
		t.Fatalf("private identity preview=%+v", preview)
	}
	var result personal.ImportResult
	workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", body, 200, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 1 {
		t.Fatalf("partial safe import=%+v", result)
	}
	var current map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/works/1?includeMedia=false", "", 200, &current)
	assertReview(t, current, float64(2), "Existing owner")
	workReferenceRequest(t, app, admin, "GET", "/api/works/2?includeMedia=false", "", 200, &current)
	assertReview(t, current, float64(2), "Safe updated review")
	// A retained source reference survives loss of its registration. The exact
	// work reference still wins, so import must not use the title or path to pick
	// an unrelated work with the same display name.
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET work_id=NULL WHERE id=1`)
	first.Identity = &personal.BackupIdentity{Ref: originalRef, References: []string{first.Identity.References[0]}, Kind: "work", Title: "Example A"}
	first.PrimaryCode = ""
	backup.Works = []personal.BackupWork{first}
	body = mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "overwrite"})
	result = personal.ImportResult{}
	workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", body, 200, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 || strings.Contains(mustJSON(result), "unregistered_source") {
		t.Fatalf("retained source reference=%+v", result)
	}
}
