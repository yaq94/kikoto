package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A card that a finite scan proved missing stays visible so its personal
// records remain reachable. The episode reports its unavailable state rather
// than pretending that the series lost a member.
func TestCatalogSeriesKeepsMissingEpisodesAccessible(t *testing.T) {
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
			VALUES(1,1,1,'manual'),(1,2,2,'manual');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(1,'a.mp3','A','audio',1),(2,'b.mp3','B','audio',1);`)

	type expectedEpisode struct {
		id     int64
		status string
	}
	check := func(want ...expectedEpisode) {
		t.Helper()
		var view catalogSeriesView
		workReferenceRequest(t, app, cookie, "GET", "/api/catalog-series/1", "", 200, &view)
		if len(view.Episodes) != len(want) {
			t.Fatalf("episodes=%d, want %d: %#v", len(view.Episodes), len(want), view.Episodes)
		}
		for i, episode := range want {
			if view.Episodes[i].CatalogEntryID != episode.id || view.Episodes[i].Status != episode.status {
				t.Fatalf("episode %d=%+v, want id=%d status=%q", i, view.Episodes[i], episode.id, episode.status)
			}
		}
	}

	check(expectedEpisode{1, "available"}, expectedEpisode{2, "available"})
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='missing' WHERE id=2`)
	check(expectedEpisode{1, "available"}, expectedEpisode{2, "missing"})

	var missing map[string]any
	workReferenceRequest(t, app, cookie, "GET", "/api/source-catalog/2", "", 200, &missing)
	if missing["status"] != "missing" {
		t.Fatalf("missing episode detail status=%v", missing["status"])
	}
	workReferenceRequest(t, app, cookie, "PATCH", "/api/source-catalog/2/user-state", `{"rating":4,"note":"Still reachable"}`, 200, nil)
	missing = nil
	workReferenceRequest(t, app, cookie, "GET", "/api/source-catalog/2", "", 200, &missing)
	assertReview(t, missing, float64(4), "Still reachable")

	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='available' WHERE id=2;
		UPDATE source_catalog_entry SET availability_status='missing' WHERE id=1`)
	check(expectedEpisode{1, "missing"}, expectedEpisode{2, "available"})
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='missing'`)
	check(expectedEpisode{1, "missing"}, expectedEpisode{2, "missing"})
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='available'`)
	check(expectedEpisode{1, "available"}, expectedEpisode{2, "available"})
}

// A renamed episode is relocated to its successor; the series keeps listing it
// and reports the successor's availability without hiding the personal records.
func TestCatalogSeriesFollowsRelocatedEpisodeToItsSuccessor(t *testing.T) {
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
			(2,7,'/Episode B','Episode B'),
			(3,7,'/Renamed B','Renamed B');
		UPDATE source_catalog_entry SET file_source_id=NULL, relocated_to_id=3, title='Renamed B' WHERE id=2;
		INSERT INTO catalog_series(id,title) VALUES(1,'Example series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
				VALUES(1,1,1,'manual'),(1,2,2,'manual');
			INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
				(1,'a.mp3','A','audio',1),(3,'b.mp3','B','audio',1);`)

	var view catalogSeriesView
	workReferenceRequest(t, app, cookie, "GET", "/api/catalog-series/1", "", 200, &view)
	if len(view.Episodes) != 2 || view.Episodes[1].Title != "Renamed B" {
		t.Fatalf("relocated member disappeared: %#v", view.Episodes)
	}
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET availability_status='missing' WHERE id=3`)
	view = catalogSeriesView{}
	workReferenceRequest(t, app, cookie, "GET", "/api/catalog-series/1", "", 200, &view)
	if len(view.Episodes) != 2 || view.Episodes[1].CatalogEntryID != 3 || view.Episodes[1].Status != "missing" {
		t.Fatalf("missing successor was not retained with its status: %#v", view.Episodes)
	}
}
