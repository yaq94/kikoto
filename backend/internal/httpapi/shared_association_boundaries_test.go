package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestSharedAssociationConcurrentPreviewsCommitOnlyOnce(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example A'),(12,'','Example B');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	admin := workReferenceLogin(t, app, "synthetic-user")
	var previews [2]map[string]any
	for i := range previews {
		workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"work","id":11},"right":{"kind":"work","id":12}}`, 201, &previews[i])
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, preview := range previews {
		go func() {
			<-start
			url := app.URL + "/api/shared-associations/" + strconv.Itoa(int(preview["id"].(float64))) + "/start"
			request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(mustJSON(map[string]any{"version": preview["version"]})))
			if err != nil {
				results <- 0
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(admin)
			response, err := app.Client().Do(request)
			if err != nil {
				results <- 0
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			results <- response.StatusCode
		}()
	}
	close(start)
	statuses := map[int]int{}
	for range 2 {
		statuses[<-results]++
	}
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatalf("concurrent outcomes: %v", statuses)
	}
	var library map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/works", "", 200, &library)
	if library["total"] != float64(1) {
		t.Fatal("concurrent association published partial identity")
	}
}

func TestSharedAssociationSeriesEpisodesAndRegistration(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, s, "synthetic-listener", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO catalog_series(id,title) VALUES(1,'Example series A'),(2,'Example series B');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Episode A','Episode A'),(2,7,'/Episode B','Episode B'),(3,7,'/Independent A','Independent A'),(4,7,'/Independent B','Independent B');
	INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual'),(2,2,1,'manual');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	admin := workReferenceLogin(t, app, "synthetic-user")
	listener := workReferenceLogin(t, app, "synthetic-listener")
	workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/1/user-state", `{"rating":4}`, 200, nil)
	workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/2/user-state", `{"note":"Complementary note"}`, 200, nil)
	workReferenceRequest(t, app, listener, "PATCH", "/api/catalog-series/2/user-state", `{"rating":1,"note":"Different user"}`, 200, nil)
	var op map[string]any
	input := `{"left":{"kind":"series","id":1},"right":{"kind":"series","id":2}}`
	// Browsing a preview does not grant permission to start it.
	workReferenceRequest(t, app, listener, "POST", "/api/shared-associations/previews", input, 201, &op)
	url := "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
	workReferenceRequest(t, app, listener, "POST", url+"/start", mustJSON(map[string]any{"version": op["version"]}), 403, nil)
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", input, 201, &op)
	url = "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": op["version"]}), 200, &op)
	if op["status"] != "completed" {
		t.Fatal("complementary fields or different users incorrectly conflict")
	}
	for _, id := range []string{"1", "2"} {
		var series map[string]any
		workReferenceRequest(t, app, admin, "GET", "/api/catalog-series/"+id, "", 200, &series)
		assertReview(t, series, float64(4), "Complementary note")
		if len(series["episodes"].([]any)) != 2 {
			t.Fatal("associated series lost independent episodes")
		}
		workReferenceRequest(t, app, listener, "GET", "/api/catalog-series/"+id, "", 200, &series)
		assertReview(t, series, float64(1), "Different user")
	}
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"series","id":1},"right":{"kind":"catalog","id":1}}`, 409, nil)
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":1},"right":{"kind":"catalog","id":3}}`, 409, nil)
	for _, pair := range [][2]int{{1, 2}, {3, 4}} {
		workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", mustJSON(map[string]any{"left": associationSubject{"catalog", int64(pair[0])}, "right": associationSubject{"catalog", int64(pair[1])}}), 201, &op)
		url = "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
		workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": op["version"]}), 200, &op)
		for _, id := range pair {
			workReferenceRequest(t, app, admin, "PATCH", "/api/source-catalog/"+strconv.Itoa(id)+"/user-state", `{"rating":5,"note":"Shared episode"}`, 200, nil)
		}
		var entry map[string]any
		workReferenceRequest(t, app, listener, "GET", "/api/source-catalog/"+strconv.Itoa(pair[0]), "", 200, &entry)
		assertReview(t, entry, nil, "")
	}
	var cards map[string]any
	var associatedSeries map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/catalog-series/1", "", 200, &associatedSeries)
	if len(associatedSeries["episodes"].([]any)) != 1 {
		t.Fatal("same associated episode appears more than once")
	}
	workReferenceRequest(t, app, admin, "GET", "/api/remote-sources/7/works", "", 200, &cards)
	if cards["total"] != float64(2) {
		t.Fatalf("source cards are not grouped by shared identity: %#v", cards)
	}
	workReferenceRequest(t, app, admin, "PATCH", "/api/access-policy", `{"anonymousAccessEnabled":true}`, 200, nil)
	workReferenceRequest(t, app, nil, "GET", url, "", 200, &op)
	if op["myLeftReview"].(map[string]any)["note"] != "" {
		t.Fatal("anonymous association response has private data")
	}
	workReferenceRequest(t, app, nil, "POST", url+"/decision", mustJSON(map[string]any{"version": op["version"], "note": "denied"}), 401, nil)
}

func TestSharedAssociationFailedCommitAndChangedSourceKeepOriginalState(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example A'),(12,'','Example B');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,7,'/A','Example A',11),(2,7,'/B','Example B',12);`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	admin := workReferenceLogin(t, app, "synthetic-user")
	workReferenceRequest(t, app, admin, "PATCH", "/api/source-catalog/1/user-state", `{"rating":3}`, 200, nil)
	workReferenceRequest(t, app, admin, "PATCH", "/api/source-catalog/2/user-state", `{"note":"Keep note"}`, 200, nil)
	input := `{"left":{"kind":"catalog","id":1},"right":{"kind":"catalog","id":2}}`
	var op map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", input, 201, &op)
	url := "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_association_failure BEFORE INSERT ON shared_identity_member BEGIN SELECT RAISE(ABORT,'synthetic commit failure'); END;`)
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": op["version"]}), 500, nil)
	var entry map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/source-catalog/1", "", 200, &entry)
	assertReview(t, entry, float64(3), "")
	workReferenceRequest(t, app, admin, "GET", "/api/source-catalog/2", "", 200, &entry)
	assertReview(t, entry, nil, "Keep note")
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_association_failure; UPDATE source_catalog_entry SET root_path='/Moved' WHERE id=2;`)
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": op["version"]}), 409, nil)
	workReferenceRequest(t, app, admin, "GET", url, "", 200, &op)
	if op["status"] != "stale" {
		t.Fatal("source change did not expire preview")
	}
}
