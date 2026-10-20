package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A card that a finite scan proved missing leaves the series view until the
// source publishes it again. Its membership and personal records stay.
func TestCatalogSeriesOmitsMissingEpisodesUntilRepublished(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	app := httptest.NewServer(s.Routes())
	t.Cleanup(app.Close)
	cookie := workReferenceLogin(t, app, "synthetic-user")
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
			(1,7,'/Episode A','Episode A'),
			(2,7,'/Episode B','Episode B');
		INSERT INTO catalog_series(id,title) VALUES(1,'Example series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			VALUES(1,1,1,'manual'),(1,2,2,'manual');`)

	check := func(want ...int64) {
		t.Helper()
		var view catalogSeriesView
		workReferenceRequest(t, app, cookie, "GET", "/api/catalog-series/1", "", 200, &view)
		if len(view.Episodes) != len(want) {
			t.Fatalf("episodes=%d, want %d: %#v", len(view.Episodes), len(want), view.Episodes)
		}
		for i, id := range want {
			if view.Episodes[i].CatalogEntryID != id {
				t.Fatalf("episode %d=%d, want %d", i, view.Episodes[i].CatalogEntryID, id)
			}
		}
	}

	check(1, 2)
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='missing' WHERE id=2`)
	check(1)
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='available' WHERE id=2;
		UPDATE source_catalog_entry SET availability_status='missing' WHERE id=1`)
	check(2)
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='missing'`)
	check()
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='available'`)
	check(1, 2)
}
