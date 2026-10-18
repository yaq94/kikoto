package httpapi

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestSharedSplitStaleChoicesAndFailedCommitPreserveRecords(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, s, "synthetic-listener", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example A'),(12,'','Example B');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	admin := workReferenceLogin(t, app, "synthetic-user")
	listener := workReferenceLogin(t, app, "synthetic-listener")
	workReferenceRequest(t, app, admin, "PATCH", "/api/works/11/user-state", `{"note":"Admin before"}`, 200, nil)
	workReferenceRequest(t, app, listener, "PATCH", "/api/works/12/user-state", `{"note":"Listener before"}`, 200, nil)
	var link, split map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"work","id":11},"right":{"kind":"work","id":12}}`, 201, &link)
	linkURL := "/api/shared-associations/" + strconv.Itoa(int(link["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", linkURL+"/start", mustJSON(map[string]any{"version": link["version"]}), 200, &link)
	workReferenceRequest(t, app, admin, "PATCH", "/api/works/12/user-state", `{"note":"Admin after"}`, 200, nil)
	workReferenceRequest(t, app, listener, "PATCH", "/api/works/11/user-state", `{"note":"Listener after"}`, 200, nil)
	workReferenceRequest(t, app, listener, "POST", linkURL+"/split-preview", "{}", 403, nil)
	workReferenceRequest(t, app, admin, "POST", linkURL+"/split-preview", "{}", 201, &split)
	url := "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, &split)
	workReferenceRequest(t, app, admin, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "left"}), 200, &split)
	workReferenceRequest(t, app, listener, "PATCH", "/api/works/11/user-state", `{"note":"Changed again"}`, 200, nil)
	workReferenceRequest(t, app, listener, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both"}), 409, nil)
	workReferenceRequest(t, app, admin, "GET", url, "", 200, &split)
	if split["status"] != "stale" || split["myAllocation"] != "" {
		t.Fatal("stale allocation still actionable")
	}
	workReferenceRequest(t, app, admin, "POST", linkURL+"/split-preview", "{}", 201, &split)
	url = "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, &split)
	workReferenceRequest(t, app, admin, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "left"}), 200, nil)
	workReferenceRequest(t, app, listener, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both", "userId": 1}), 400, nil)
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_split_failure BEFORE DELETE ON shared_identity_member BEGIN SELECT RAISE(ABORT,'synthetic split failure'); END;`)
	workReferenceRequest(t, app, listener, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both"}), 500, nil)
	var current map[string]any
	workReferenceRequest(t, app, listener, "GET", url, "", 200, &current)
	if current["myAllocation"] != "" || current["status"] != "pending" {
		t.Fatal("failed split partially committed")
	}
	workReferenceRequest(t, app, listener, "GET", "/api/works/12", "", 200, &current)
	assertReview(t, current, nil, "Changed again")
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_split_failure;`)
	workReferenceRequest(t, app, listener, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both"}), 200, &split)
	if split["status"] != "completed" {
		t.Fatal("retry did not complete")
	}
	workReferenceRequest(t, app, admin, "PATCH", "/api/access-policy", `{"anonymousAccessEnabled":true}`, 200, nil)
	workReferenceRequest(t, app, nil, "GET", url, "", 200, &split)
	if split["myCurrentReview"].(map[string]any)["note"] != "" {
		t.Fatal("anonymous split leaks review")
	}
	workReferenceRequest(t, app, nil, "POST", url+"/decision", `{"version":"synthetic","allocation":"both"}`, 401, nil)
}

func TestSharedSplitUnchangedRecordsAndNestedIdentityRestoration(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example A'),(12,'','Example B'),(13,'','Example C');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	admin := workReferenceLogin(t, app, "synthetic-user")
	links := []string{}
	workReferenceRequest(t, app, admin, "PATCH", "/api/works/11/user-state", `{"rating":3,"note":"Original first entry"}`, 200, nil)
	for _, right := range []int64{12, 13} {
		var link map[string]any
		workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", mustJSON(map[string]any{"left": associationSubject{"work", 11}, "right": associationSubject{"work", right}}), 201, &link)
		url := "/api/shared-associations/" + strconv.Itoa(int(link["id"].(float64)))
		workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": link["version"]}), 200, &link)
		links = append(links, url)
	}
	workReferenceRequest(t, app, admin, "POST", links[0]+"/split-preview", "{}", 409, nil)
	for _, link := range []string{links[1], links[0]} {
		var split map[string]any
		workReferenceRequest(t, app, admin, "POST", link+"/split-preview", "{}", 201, &split)
		url := "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
		workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, &split)
		if split["status"] != "completed" {
			t.Fatal("unchanged split needs unnecessary allocation")
		}
	}
	var library map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/works", "", 200, &library)
	if library["total"] != float64(3) {
		t.Fatal("nested split lost original identities")
	}
	var current map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/works/11", "", 200, &current)
	assertReview(t, current, float64(3), "Original first entry")
	for _, id := range []string{"12", "13"} {
		workReferenceRequest(t, app, admin, "GET", "/api/works/"+id, "", 200, &current)
		assertReview(t, current, nil, "")
	}
}
