package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestSharedAssociationPreviewUsesPublishedSourceEvidence(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{"/Example": {looseAudio("First.mp3")}})
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	db := f.server.db
	source := f.sourceID(t)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
		(101,?,'/Left','Example left'),(102,?,'/Right','Example right');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
		(101,'Left.mp3','Left','audio',1),(102,'Right.mp3','Right','audio',1);
		INSERT INTO catalog_series(id,title) VALUES(201,'Example series left'),(202,'Example series right');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
		(201,101,1,'manual'),(202,102,2,'manual')`, source, source)
	preview := func(kind string, left, right int64) map[string]any {
		t.Helper()
		return reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/shared-associations/previews",
			fmt.Sprintf(`{"left":{"kind":%q,"id":%d},"right":{"kind":%q,"id":%d}}`, kind, left, kind, right), 201)
	}
	assertLeft := func(value map[string]any, title, sourceTitle string, position float64) {
		t.Helper()
		left := value["left"].(map[string]any)
		locations := left["sources"].([]any)
		if left["title"] != title || len(locations) != 1 {
			t.Fatalf("preview subject=%v sources=%v", left["title"], locations)
		}
		location := locations[0].(map[string]any)
		if location["title"] != sourceTitle || location["position"] != position {
			t.Fatalf("preview source=%v, want %q position %v", location, sourceTitle, position)
		}
	}
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), source, "synthetic:association-preview")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET title='Prepared left' WHERE id=101;
		UPDATE catalog_series SET title='Prepared series left' WHERE id=201;
		UPDATE catalog_series_member SET position=9 WHERE catalog_entry_id=101`)
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		t.Fatal(err)
	}
	staged := preview("catalog", 101, 102)
	assertLeft(staged, "Example left", "Example left", 1)
	assertLeft(preview("series", 201, 202), "Example series left", "Example left", 1)
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	assertLeft(preview("catalog", 101, 102), "Prepared left", "Prepared left", 9)
	assertLeft(preview("series", 201, 202), "Prepared series left", "Prepared left", 9)
	reviewRequest(t, f.handler, f.cookie, http.MethodPost,
		fmt.Sprintf("/api/shared-associations/%.0f/start", staged["id"]),
		mustJSON(map[string]any{"version": staged["version"]}), http.StatusConflict)
}

func TestSharedAssociationRetirementRequiresPublishedSuccessor(t *testing.T) {
	db, file := openPublicationRetryDB(t)
	f := newLooseWorkFixtureWithDB(t, map[string][]map[string]any{"/Example": {looseAudio("Original.mp3")}}, db)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	var oldID int64
	if err := db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
		(101,?,'/Other','Example other');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
		(101,'Other.mp3','Other','audio',1)`, f.sourceID(t))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/101/user-state", `{"rating":4,"note":"Example retained review"}`, 200)
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:association-retirement")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := persistOpenListEntryTx(ctx, tx.Tx, f.sourceID(t), "/Successor", "", "Example successor")
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(?,'Original.mp3','Example Track','audio',1)`, newID)
	}
	if err == nil {
		err = refreshCatalogEntryAdmissionTx(ctx, tx.Tx, newID)
	}
	var retired bool
	if err == nil {
		retired, err = retireCatalogCardTx(ctx, tx.Tx, oldID, newID)
	}
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil || !retired {
		t.Fatalf("retire=%v err=%v", retired, err)
	}
	preview := func(id int64) map[string]any {
		t.Helper()
		return reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/shared-associations/previews",
			fmt.Sprintf(`{"left":{"kind":"catalog","id":%d},"right":{"kind":"catalog","id":101}}`, id), 201)
	}
	start := func(value map[string]any, status int) map[string]any {
		t.Helper()
		return reviewRequest(t, f.handler, f.cookie, http.MethodPost,
			fmt.Sprintf("/api/shared-associations/%.0f/start", value["id"]),
			mustJSON(map[string]any{"version": value["version"]}), status)
	}
	staged := preview(oldID)
	if rejected := start(staged, http.StatusConflict); rejected["error"] == nil {
		t.Fatal("retired preparation did not report a conflict")
	}
	var works, aliasLinks int
	if err = db.QueryRow(`SELECT count(*),(SELECT count(*) FROM source_catalog_entry WHERE id=? AND work_id IS NOT NULL) FROM work`, oldID).Scan(&works, &aliasLinks); err != nil || works != 1 || aliasLinks != 0 {
		t.Fatalf("rejected association changed identities: works=%d aliases=%d err=%v", works, aliasLinks, err)
	}
	reopenCatalogFiniteFixture(t, f, file)
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	if result := start(preview(newID), http.StatusOK); result["status"] != "completed" {
		t.Fatalf("published successor association did not complete: %v", result["status"])
	}
	reopenCatalogFiniteFixture(t, f, file)
	for _, id := range []int64{oldID, newID, 101} {
		assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet,
			fmt.Sprintf("/api/source-catalog/%d", id), "", 200), float64(4), "Example retained review")
	}
	if len(f.gets) != 0 {
		t.Fatal("association requested media")
	}
}
