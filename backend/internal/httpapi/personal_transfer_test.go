package httpapi

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// The backup is consumed through the same authenticated HTTP boundary as a file
// uploaded from Settings. Empty codes must not make registered reviews unportable.
func TestPersonalTransferExportsEveryEvaluationLevel(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	app := httptest.NewServer(s.Routes())
	t.Cleanup(func() { app.Close(); _ = s.Shutdown(context.Background()) })
	admin := workReferenceLogin(t, app, "synthetic-user")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
		INSERT INTO source_catalog_entry(id,root_path,title) VALUES(1,'/Example Standalone','Example Standalone'),(2,'/Example Episode','Example Episode');
		INSERT INTO catalog_series(id,title) VALUES(1,'Example Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,2,1,'manual');`)
	for _, path := range []string{"/api/works/1", "/api/source-catalog/1", "/api/source-catalog/2", "/api/catalog-series/1"} {
		workReferenceRequest(t, app, admin, "PATCH", path+"/user-state", `{"rating":3,"note":"Example private review"}`, 200, nil)
	}
	var backup map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/user-data/export", "", 200, &backup)
	if backup["version"] != float64(3) {
		t.Fatalf("portable backup version = %v, want 3", backup["version"])
	}
	works := backup["works"].([]any)
	if len(works) != 4 {
		t.Fatalf("exported %d evaluation objects, want 4", len(works))
	}
	kinds := map[string]int{}
	refs := map[string]bool{}
	for _, raw := range works {
		w := raw.(map[string]any)
		identity := w["identity"].(map[string]any)
		ref := identity["ref"].(string)
		if ref == "" || refs[ref] {
			t.Fatalf("missing or duplicate stable reference: %q", ref)
		}
		refs[ref] = true
		kinds[identity["kind"].(string)]++
	}
	if kinds["work"] != 2 || kinds["episode"] != 1 || kinds["series"] != 1 {
		t.Fatalf("evaluation kinds = %#v", kinds)
	}
}

func TestPersonalTransferGroupsConflictingIncomingSharedReviews(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	app := httptest.NewServer(s.Routes())
	t.Cleanup(func() { app.Close(); _ = s.Shutdown(context.Background()) })
	admin := workReferenceLogin(t, app, "synthetic-user")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example A'),(2,'RJ00000001','Example B');`)
	var operation map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"work","id":1},"right":{"kind":"work","id":2}}`, 201, &operation)
	url := "/api/shared-associations/" + strconv.Itoa(int(operation["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": operation["version"]}), 200, &operation)
	body := `{"format":"kikoto","conflict":"overwrite","data":{"format":"kikoto-user-data","version":1,"works":[{"primaryCode":"RJ00000000","listeningStatus":"none","rating":1,"note":"Left incoming"},{"primaryCode":"RJ00000001","listeningStatus":"none","rating":5,"note":"Right incoming"}]}}`
	var preview map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/user-data/import/preview", body, 200, &preview)
	if preview["conflicts"] != float64(1) {
		t.Fatalf("incoming shared review conflicts = %#v", preview)
	}
	var result map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", body, 200, &result)
	if result["importedWorks"] != float64(0) || result["skippedWorks"] != float64(2) {
		t.Fatalf("conflicting group must stay untouched: %#v", result)
	}
	for _, id := range []string{"1", "2"} {
		var current map[string]any
		workReferenceRequest(t, app, admin, "GET", "/api/works/"+id+"?includeMedia=false", "", 200, &current)
		assertReview(t, current, nil, "")
	}
}
