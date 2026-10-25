package httpapi

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestSharedAssociationWaitsForEachRecordOwner(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	createAccountManagedTestUser(t, s, "synthetic-a", "synthetic-password")
	createAccountManagedTestUser(t, s, "synthetic-b", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_a','Example A','openlist'),(8,'example_b','Example B','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://a.example.invalid','https://a.example.invalid'),(8,'https://b.example.invalid','https://b.example.invalid');
	INSERT INTO work(id,primary_code,title) VALUES(11,'','Example work A'),(12,'RJ00000000','Example work B');
	INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(101,11,'EXAMPLE-A'),(102,12,'RJ00000000');
	INSERT INTO work_edition(work_id,logical_work_id,primary_code,edition_label,is_canonical) VALUES(11,101,'','Original',1),(12,102,'RJ00000000','Translated',1);
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,7,'/Example A','Example A',11),(2,8,'/Example B','Example B',12);
	INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
		(1,'01.mp3','01.mp3','audio',1024),(2,'01.mp3','01.mp3','audio',1024);`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	editor := workReferenceLogin(t, app, "synthetic-editor")
	a := workReferenceLogin(t, app, "synthetic-a")
	b := workReferenceLogin(t, app, "synthetic-b")
	workReferenceRequest(t, app, a, "PATCH", "/api/source-catalog/1/user-state", `{"rating":1,"note":"Private A left"}`, 200, nil)
	workReferenceRequest(t, app, a, "PATCH", "/api/source-catalog/2/user-state", `{"rating":4,"note":"Private A right"}`, 200, nil)
	workReferenceRequest(t, app, b, "PATCH", "/api/source-catalog/1/user-state", `{"rating":2,"note":"Private B left"}`, 200, nil)
	workReferenceRequest(t, app, b, "PATCH", "/api/source-catalog/2/user-state", `{"rating":5,"note":"Private B right"}`, 200, nil)
	var preview map[string]any
	body := `{"left":{"kind":"catalog","id":1},"right":{"kind":"catalog","id":2}}`
	workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", body, 201, &preview)
	if strings.Contains(mustJSON(preview), "Private") {
		t.Fatal("preview leaked another user's review")
	}
	url := "/api/shared-associations/" + strconv.Itoa(int(preview["id"].(float64)))
	version := preview["version"]
	workReferenceRequest(t, app, a, "POST", url+"/start", mustJSON(map[string]any{"version": version}), 403, nil)
	workReferenceRequest(t, app, editor, "POST", url+"/start", mustJSON(map[string]any{"version": version}), 200, &preview)
	if preview["status"] != "pending" {
		t.Fatalf("expected pending: %#v", preview)
	}
	workReferenceRequest(t, app, editor, "POST", url+"/decision", mustJSON(map[string]any{"version": version, "rating": 3, "note": "Cannot decide for owners"}), 403, nil)
	workReferenceRequest(t, app, a, "POST", url+"/decision", mustJSON(map[string]any{"version": version, "rating": 4, "note": "A resolved"}), 200, &preview)
	if preview["status"] != "pending" {
		t.Fatal("only one owner completed association")
	}
	var current map[string]any
	workReferenceRequest(t, app, a, "GET", "/api/source-catalog/1", "", 200, &current)
	assertReview(t, current, float64(1), "Private A left")
	workReferenceRequest(t, app, b, "POST", url+"/decision", mustJSON(map[string]any{"version": version, "rating": 2, "note": "B resolved"}), 200, &preview)
	if preview["status"] != "completed" {
		t.Fatalf("all owners did not complete: %#v", preview)
	}
	if strings.Contains(mustJSON(preview), "A resolved") {
		t.Fatal("result leaked another owner's decision")
	}
	for _, id := range []string{"1", "2"} {
		workReferenceRequest(t, app, a, "GET", "/api/source-catalog/"+id, "", 200, &current)
		assertReview(t, current, float64(4), "A resolved")
		workReferenceRequest(t, app, b, "GET", "/api/source-catalog/"+id, "", 200, &current)
		assertReview(t, current, float64(2), "B resolved")
	}
	// Association is an identity operation. Each work keeps its own edition
	// label and logical family instead of inheriting the other side's.
	var labelA, labelB string
	if err := db.QueryRow(`SELECT edition_label FROM work_edition WHERE work_id=11`).Scan(&labelA); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT edition_label FROM work_edition WHERE work_id=12`).Scan(&labelB); err != nil {
		t.Fatal(err)
	}
	if labelA != "Original" || labelB != "Translated" {
		t.Fatalf("association rewrote editions: %q/%q", labelA, labelB)
	}
	workReferenceRequest(t, app, a, "PATCH", "/api/source-catalog/2/user-state", `{"note":"Shared update"}`, 200, nil)
	workReferenceRequest(t, app, a, "GET", "/api/source-catalog/1", "", 200, &current)
	assertReview(t, current, float64(4), "Shared update")
	var directories map[string]any
	workReferenceRequest(t, app, a, "GET", "/api/works/11/catalog-directories", "", 200, &directories)
	if len(directories["directories"].([]any)) != 2 {
		t.Fatal("associated work does not share source directories")
	}
	for _, query := range []string{"", "?q=Example%20work%20B"} {
		var library map[string]any
		workReferenceRequest(t, app, a, "GET", "/api/works"+query, "", 200, &library)
		if library["total"] != float64(1) {
			t.Fatalf("shared identity count/search: %#v", library)
		}
	}
}

// Conflict fields are per field and per current user. A rating that agrees does
// not hide a differing note, and an empty side does not count as a conflict.
func TestSharedAssociationConflictFields(t *testing.T) {
	one, two, five := int64(1), int64(2), int64(5)
	cases := []struct {
		name string
		a, b associationReview
		want string
	}{
		{"rating", associationReview{Rating: &one}, associationReview{Rating: &two}, "rating"},
		{"note", associationReview{Note: "left"}, associationReview{Note: "right"}, "note"},
		{"both", associationReview{Rating: &one, Note: "left"}, associationReview{Rating: &two, Note: "right"}, "rating,note"},
		{"same", associationReview{Rating: &five, Note: "same"}, associationReview{Rating: &five, Note: "same"}, ""},
		{"empty", associationReview{}, associationReview{Rating: &five, Note: "right"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(associationConflicts(tc.a, tc.b), ",")
			if got != tc.want {
				t.Fatalf("conflicts=%q want %q", got, tc.want)
			}
		})
	}
}
