package httpapi

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// The HTTP seam proves association, rather than merely testing a score.
func TestWorkMatchingUniqueEvidenceSharesReviewsWithoutMergingEditions(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice');
	INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	editor := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, editor, "PATCH", "/api/works/11/user-state", `{"rating":4,"note":"Example review"}`, 200, nil)
	var match map[string]any
	workReferenceRequest(t, app, editor, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &match)
	if match["status"] != "completed" || match["rule"] != "title_voice_release" {
		t.Fatalf("unique supported match not applied: %#v", match)
	}
	var work map[string]any
	workReferenceRequest(t, app, editor, "GET", "/api/works/12", "", 200, &work)
	assertReview(t, work, float64(4), "Example review")
	var library map[string]any
	workReferenceRequest(t, app, editor, "GET", "/api/works", "", 200, &library)
	if library["total"] != float64(1) {
		t.Fatalf("identity not shared: %#v", library)
	}
	for _, ref := range []string{"11", "12"} {
		var cursor map[string]any
		workReferenceRequest(t, app, editor, "GET", "/api/works/"+ref+"/playback-cursor", "", 200, &cursor)
		if cursor["cursor"] != nil {
			t.Fatal("matching invented playback equivalence")
		}
	}
	var split map[string]any
	workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/"+strconv.Itoa(int(match["operationId"].(float64)))+"/split-preview", `{}`, 201, &split)
	workReferenceRequest(t, app, editor, "POST", "/api/shared-splits/"+strconv.Itoa(int(split["id"].(float64)))+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, nil)
	workReferenceRequest(t, app, editor, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &match)
	if match["status"] != "review" || match["rule"] != "previously_split" {
		t.Fatalf("automatic matching undid an explicit split: %#v", match)
	}
}

func TestWorkMatchingRevalidatesNewCandidatesBeforeOwnerDecision(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	createAccountManagedTestUser(t, s, "synthetic-a", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice');
	INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	editor := workReferenceLogin(t, app, "synthetic-editor")
	owner := workReferenceLogin(t, app, "synthetic-a")
	workReferenceRequest(t, app, owner, "PATCH", "/api/works/11/user-state", `{"rating":1,"note":"Private left"}`, 200, nil)
	workReferenceRequest(t, app, owner, "PATCH", "/api/works/12/user-state", `{"rating":4,"note":"Private right"}`, 200, nil)
	var result map[string]any
	workReferenceRequest(t, app, owner, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 403, nil)
	workReferenceRequest(t, app, editor, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "pending" || strings.Contains(mustJSON(result), "Private") {
		t.Fatalf("unsafe automatic result: %#v", result)
	}
	url := "/api/shared-associations/" + strconv.Itoa(int(result["operationId"].(float64)))
	var op map[string]any
	workReferenceRequest(t, app, owner, "GET", url, "", 200, &op)
	workReferenceRequest(t, app, editor, "POST", url+"/decision", mustJSON(map[string]any{"version": op["version"], "rating": 3, "note": "Other user"}), 403, nil)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(13,'','Example Work','2020-01-02'); INSERT INTO work_credit(work_id,person_id,role) VALUES(13,1,'voice_actor');`)
	workReferenceRequest(t, app, owner, "POST", url+"/decision", mustJSON(map[string]any{"version": op["version"], "rating": 3, "note": "My choice"}), 409, nil)
	workReferenceRequest(t, app, owner, "GET", url, "", 200, &op)
	if op["status"] != "stale" {
		t.Fatalf("new candidate did not invalidate pending automatic match: %#v", op)
	}
	var work map[string]any
	workReferenceRequest(t, app, owner, "GET", "/api/works/11", "", 200, &work)
	assertReview(t, work, float64(1), "Private left")
	workReferenceRequest(t, app, owner, "GET", "/api/works/12", "", 200, &work)
	assertReview(t, work, float64(4), "Private right")
	metadataReviewExec(t, db, `DELETE FROM work_credit WHERE work_id=13; DELETE FROM work WHERE id=13;`)
	workReferenceRequest(t, app, owner, "PATCH", "/api/works/12/user-state", `{"note":"Private updated right"}`, 200, nil)
	previous := result["operationId"]
	workReferenceRequest(t, app, editor, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "pending" || result["operationId"] == previous {
		t.Fatalf("refresh reused a stale review snapshot: %#v", result)
	}
}

func TestWorkMatchingRejectsWeakAmbiguousAndContradictoryEvidence(t *testing.T) {
	for _, tc := range []struct{ name, sql, rule string }{
		{"same title", ``, "insufficient_evidence"},
		{"missing independent field", `INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`, "insufficient_evidence"},
		{"different voices", `UPDATE work SET release_date='2020-01-02'; INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,2,'voice_actor');`, "conflicting_evidence"},
		{"different dates", `UPDATE work SET release_date=CASE id WHEN 11 THEN '2020-01-02' ELSE '2021-01-02' END; INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`, "conflicting_evidence"},
		{"different editions", `UPDATE work SET release_date='2020-01-02'; INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor'); INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,11,''),(2,12,''); INSERT INTO work_edition(work_id,logical_work_id,primary_code,edition_label) VALUES(11,1,'','Original'),(12,2,'','Remaster');`, "conflicting_evidence"},
		{"ambiguous", `UPDATE work SET release_date='2020-01-02'; INSERT INTO work(id,primary_code,title,release_date) VALUES(13,'','Example Work','2020-01-02'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor'),(13,1,'voice_actor');`, "ambiguous_evidence"},
		{"series versus work", `INSERT INTO catalog_series(id,title,work_id) VALUES(1,'Example Work',12); UPDATE work SET release_date='2020-01-02'; INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`, "conflicting_evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigratedTestDB(t)
			s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
			defer func() { _ = s.Shutdown(context.Background()) }()
			createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
			metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example Work'),(12,'','Example Work'); INSERT INTO person(id,display_name) VALUES(1,'Example Voice A'),(2,'Example Voice B');`+tc.sql)
			app := httptest.NewServer(s.Routes())
			defer app.Close()
			cookie := workReferenceLogin(t, app, "synthetic-editor")
			var result map[string]any
			workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
			if result["status"] != "review" || result["rule"] != tc.rule {
				t.Fatalf("unsafe candidate result: %#v", result)
			}
			var library map[string]any
			workReferenceRequest(t, app, cookie, "GET", "/api/works", "", 200, &library)
			if library["total"].(float64) < 2 {
				t.Fatal("weak evidence merged identities")
			}
		})
	}
}

func TestWorkMatchingNumberedCatalogUsesExistingCodeBeforeTitleCandidates(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'RJ00000000','Example Work'),(12,'','Example Other');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title) VALUES(1,7,'/Example Other','RJ00000000','Example Other');
	INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'01.mp3','01.mp3','audio',1024);`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "PATCH", "/api/works/11/user-state", `{"rating":4,"note":"Example review"}`, 200, nil)
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"catalog","id":1}`, 200, &result)
	if result["status"] != "existing" || result["rule"] != "product_code" {
		t.Fatalf("known code not preferred: %#v", result)
	}
	var entry map[string]any
	workReferenceRequest(t, app, cookie, "GET", "/api/source-catalog/1", "", 200, &entry)
	assertReview(t, entry, float64(4), "Example review")
	var linked int
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM source_catalog_entry WHERE id=1 AND work_id=11)`).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked == 0 {
		t.Fatal("existing code match did not link the catalog entry to the registered work")
	}
}

func TestWorkMatchingDeclaredRelationOutranksHeuristicsAndKeepsExactEditions(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'RJ00000000','Example Work','2020-01-02'),(12,'RJ00000001','Example Edition','2021-02-03'),(13,'','Example Work','2020-01-02');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(13,1,'voice_actor');
	INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,11,'RJ00000000');
	INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical,metadata_language) VALUES(11,1,'RJ00000000',1,'ja'),(12,1,'RJ00000001',0,'en');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "PATCH", "/api/works/11/user-state", `{"note":"Example shared review"}`, 200, nil)
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "completed" || result["rule"] != "declared_relationship" {
		t.Fatalf("reliable relation not preferred: %#v", result)
	}
	var work map[string]any
	workReferenceRequest(t, app, cookie, "GET", "/api/works/12", "", 200, &work)
	assertReview(t, work, nil, "Example shared review")
	if work["primaryCode"] != "RJ00000001" {
		t.Fatalf("exact edition replaced: %#v", work)
	}
	workReferenceRequest(t, app, cookie, "GET", "/api/works/13", "", 200, &work)
	assertReview(t, work, nil, "")
}

func TestWorkMatchingCatalogHashesAssociateWithoutInventingCodes(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist'),(8,'example_remote_b','Example Remote B','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://a.example.invalid','https://a.example.invalid'),(8,'https://b.example.invalid','https://b.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,content_fingerprint) VALUES(1,7,'/Example A','Example Work','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'),(2,8,'/Example B','Example Work','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
	INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
		(1,'01.mp3','01.mp3','audio',1024),(2,'01.mp3','01.mp3','audio',1024);`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"catalog","id":1}`, 200, &result)
	if result["status"] != "completed" || result["rule"] != "title_content_hash" {
		t.Fatalf("hash evidence not applied: %#v", result)
	}
	workReferenceRequest(t, app, cookie, "PATCH", "/api/source-catalog/1/user-state", `{"rating":3,"note":"Example catalog review"}`, 200, nil)
	var entry map[string]any
	workReferenceRequest(t, app, cookie, "GET", "/api/source-catalog/2", "", 200, &entry)
	assertReview(t, entry, float64(3), "Example catalog review")
	if entry["primaryCode"] != "" {
		t.Fatal("matching invented a product code")
	}
}
