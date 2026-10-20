package httpapi

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestSharedAssociationCatalogRelocationUsesSuccessor(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Retired work'),(12,'','Live work');
	INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES(2,'/Live','Live title',12);
	INSERT INTO source_catalog_entry(id,root_path,title,work_id,relocated_to_id) VALUES(1,'/Retired','Retired title',11,2);`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	editor := workReferenceLogin(t, app, "synthetic-editor")

	var apiError map[string]any
	workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":1},"right":{"kind":"work","id":12}}`, 409, &apiError)
	if apiError["code"] != "association_stale" {
		t.Fatalf("relocated catalog preview = %#v", apiError)
	}
	workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":999},"right":{"kind":"work","id":12}}`, 409, &apiError)
	if apiError["code"] != "association_stale" {
		t.Fatalf("missing catalog preview = %#v", apiError)
	}

	// The successor row must supply the object, never the retired row, which
	// still carries work 11 and a different title.
	var preview map[string]any
	workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":2},"right":{"kind":"work","id":11}}`, 201, &preview)
	left, _ := preview["left"].(map[string]any)
	subject, _ := left["subject"].(map[string]any)
	if subject["id"] != float64(2) || left["title"] != "Live title" || left["workId"] != float64(12) {
		t.Fatalf("preview did not use the successor row: %#v", left)
	}

	// Clear the successor's work link so a successful submit proves which row
	// receives the link.
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET work_id=NULL WHERE id=2`)
	var op map[string]any
	workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":2},"right":{"kind":"work","id":12}}`, 201, &op)
	opURL := "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
	workReferenceRequest(t, app, editor, "POST", opURL+"/start", mustJSON(map[string]any{"version": op["version"]}), 200, &op)
	if op["status"] != "completed" {
		t.Fatalf("successor association = %#v", op)
	}
	var successorWork, retiredWork sql.NullInt64
	if err := db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=2`).Scan(&successorWork); err != nil {
		t.Fatal(err)
	}
	if !successorWork.Valid {
		t.Fatal("commit did not link the successor row")
	}
	if err := db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=1`).Scan(&retiredWork); err != nil {
		t.Fatal(err)
	}
	if !retiredWork.Valid || retiredWork.Int64 != 11 {
		t.Fatalf("retired row work_id = %v, want 11", retiredWork)
	}

	// A preview captured before relocation must not submit afterwards.
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,root_path,title) VALUES(3,'/Next','Next title')`)
	workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":2},"right":{"kind":"work","id":11}}`, 201, &op)
	opURL = "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET relocated_to_id=3 WHERE id=2`)
	workReferenceRequest(t, app, editor, "POST", opURL+"/start", mustJSON(map[string]any{"version": op["version"]}), 409, &apiError)
	if apiError["code"] != "association_stale" {
		t.Fatalf("relocated submit = %#v", apiError)
	}
	var unchanged sql.NullInt64
	if err := db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=2`).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if !unchanged.Valid || unchanged.Int64 != successorWork.Int64 {
		t.Fatalf("stale submit changed the successor link: %v", unchanged)
	}
}
