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
