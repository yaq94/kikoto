package httpapi

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/personal"
)

// A restored episode must be reachable from its personal-only series card.
// The earlier restore test verified work rows, refs and a second export but
// never read GET /api/catalog-series/{id}, so a series with no member rows
// looked empty even though the episode existed.
func TestAuditPersonalTransferSeriesViewListsRestoredEpisode(t *testing.T) {
	db, source, owner, _, closeSource := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `
		INSERT INTO source_catalog_entry(id,root_path,title,primary_code)
			VALUES(101,'/Example Series/Episode 1','Example Episode','');
		INSERT INTO catalog_series(id,title) VALUES(201,'Example Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			VALUES(201,101,2,'directory');`)
	workReferenceRequest(t, source, owner, http.MethodPatch,
		"/api/source-catalog/101/user-state",
		`{"rating":4,"note":"Episode private note"}`, http.StatusOK, nil)

	var exported personal.Backup
	workReferenceRequest(t, source, owner, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &exported)
	if exported.Version != 2 || len(exported.Works) != 1 {
		t.Fatalf("exported backup = version %d, %d works", exported.Version, len(exported.Works))
	}
	identity := exported.Works[0].Identity
	if identity == nil || identity.Kind != "episode" || identity.Parent == nil {
		t.Fatalf("exported identity = %#v, want an episode with a parent", identity)
	}
	parent := identity.Parent
	if !strings.HasPrefix(parent.Ref, "s:") || parent.Kind != "series" || parent.Title != "Example Series" || parent.Parent != nil {
		t.Fatalf("exported parent = %#v, want a minimal series identity", parent)
	}
	if len(parent.References) != 0 {
		t.Fatalf("parent references = %v, want none for an unreviewed series", parent.References)
	}
	for _, ref := range identity.References {
		if ref == parent.Ref || strings.HasPrefix(ref, "s:") {
			t.Fatalf("episode references = %v, want no parent alias", identity.References)
		}
	}
	closeSource()

	targetRoot := t.TempDir()
	targetDB, target, admin, _, closeTarget := openTransferApplication(t, targetRoot, true)
	defer closeTarget()
	body := mustJSON(map[string]any{"format": "kikoto", "data": exported, "conflict": "keep"})
	var preview personal.ImportPreview
	workReferenceRequest(t, target, admin, http.MethodPost, "/api/user-data/import/preview", body,
		http.StatusOK, &preview)
	for _, issue := range preview.Issues {
		if issue.Reason == "relationships_not_imported" {
			t.Fatalf("version 2 preview kept an unresolved parent issue: %+v", preview.Issues)
		}
	}
	var result personal.ImportResult
	workReferenceRequest(t, target, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v, want the episode imported", result)
	}
	for _, issue := range result.Issues {
		if issue.Reason == "relationships_not_imported" {
			t.Fatalf("version 2 import kept an unresolved parent issue: %+v", result.Issues)
		}
	}

	var seriesID int64
	var seriesWork, episodeWork sql.NullInt64
	if err := targetDB.QueryRow(`SELECT id,work_id FROM catalog_series WHERE title='Example Series'`).Scan(&seriesID, &seriesWork); err != nil {
		t.Fatal(err)
	}
	if !seriesWork.Valid {
		t.Fatal("restored series has no work")
	}
	if err := targetDB.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, seriesWork.Int64).Scan(&episodeWork); err != nil {
		t.Fatalf("episode work with parent %d: %v", seriesWork.Int64, err)
	}
	if !episodeWork.Valid || episodeWork.Int64 == seriesWork.Int64 {
		t.Fatalf("episode work = %v, series work = %d", episodeWork, seriesWork.Int64)
	}
	var seriesWorkParent sql.NullInt64
	if err := targetDB.QueryRow(`SELECT parent_work_id FROM work WHERE id=?`, seriesWork.Int64).Scan(&seriesWorkParent); err != nil {
		t.Fatal(err)
	}
	if seriesWorkParent.Valid {
		t.Fatalf("series work %d has parent %d", seriesWork.Int64, seriesWorkParent.Int64)
	}

	var view map[string]any
	workReferenceRequest(t, target, admin, http.MethodGet,
		fmt.Sprintf("/api/catalog-series/%d", seriesID), "", http.StatusOK, &view)
	episodes, ok := view["episodes"].([]any)
	if !ok || len(episodes) != 1 {
		t.Fatalf("series view episodes = %#v, want the restored episode", view["episodes"])
	}
	episode := episodes[0].(map[string]any)
	if episode["catalogEntryId"] != float64(0) || episode["workId"] != float64(episodeWork.Int64) {
		t.Fatalf("restored episode = %#v, want catalogEntryId=0 and work %d", episode, episodeWork.Int64)
	}

	// A repeated import reuses the same parent and episode.
	workReferenceRequest(t, target, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("duplicate restore = %+v", result)
	}
	var seriesCount, parentCount, childCount int
	if err := targetDB.QueryRow(`SELECT
			(SELECT count(*) FROM catalog_series WHERE title='Example Series'),
			(SELECT count(*) FROM work WHERE id=?),
			(SELECT count(*) FROM work WHERE parent_work_id=?)`, seriesWork.Int64, seriesWork.Int64).
		Scan(&seriesCount, &parentCount, &childCount); err != nil {
		t.Fatal(err)
	}
	if seriesCount != 1 || parentCount != 1 || childCount != 1 {
		t.Fatalf("duplicate restore series=%d parent=%d children=%d", seriesCount, parentCount, childCount)
	}
	workReferenceRequest(t, target, admin, http.MethodGet,
		fmt.Sprintf("/api/catalog-series/%d", seriesID), "", http.StatusOK, &view)
	if episodes, ok = view["episodes"].([]any); !ok || len(episodes) != 1 {
		t.Fatalf("series view after duplicate = %#v", view["episodes"])
	}

	// A real restart keeps the parent relation and the personal-only series
	// readable through the same public endpoint.
	closeTarget()
	restartedDB, restarted, restartedAdmin, _, closeRestarted := openTransferApplication(t, targetRoot, false)
	defer closeRestarted()
	var seriesWorkAfter, episodeWorkAfter sql.NullInt64
	if err := restartedDB.QueryRow(`SELECT work_id FROM catalog_series WHERE title='Example Series'`).Scan(&seriesWorkAfter); err != nil {
		t.Fatal(err)
	}
	if err := restartedDB.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, seriesWorkAfter.Int64).Scan(&episodeWorkAfter); err != nil {
		t.Fatalf("restarted episode work: %v", err)
	}
	if seriesWorkAfter != seriesWork || episodeWorkAfter.Int64 != episodeWork.Int64 {
		t.Fatalf("restart changed series/episode %v/%v -> %v/%v", seriesWork, episodeWork, seriesWorkAfter, episodeWorkAfter)
	}
	var restartedView map[string]any
	workReferenceRequest(t, restarted, restartedAdmin, http.MethodGet,
		fmt.Sprintf("/api/catalog-series/%d", seriesID), "", http.StatusOK, &restartedView)
	if episodes, ok = restartedView["episodes"].([]any); !ok || len(episodes) != 1 {
		t.Fatalf("restarted series view = %#v", restartedView["episodes"])
	}
	var restartedExport personal.Backup
	workReferenceRequest(t, restarted, restartedAdmin, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &restartedExport)
	if len(restartedExport.Works) != 1 || restartedExport.Works[0].Identity.Parent == nil ||
		restartedExport.Works[0].Identity.Parent.Ref != parent.Ref {
		t.Fatalf("restarted export parent = %#v", restartedExport.Works)
	}
}

// The export must use work.parent_work_id, not source_catalog_entry.work_id.
// Real scans often leave the entry unregistered, and an episode review can still
// carry the persisted parent relation.
func TestAuditPersonalTransferExportsParentWithoutRegisteredEntry(t *testing.T) {
	db, source, owner, _, closeSource := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `
		INSERT INTO source_catalog_entry(id,root_path,title,primary_code)
			VALUES(101,'/Example Series/Episode 1','Example Episode','');
		INSERT INTO catalog_series(id,title) VALUES(201,'Example Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			VALUES(201,101,2,'directory');
		INSERT INTO work(id,primary_code,title) VALUES(301,'','Example Series'),(302,'','Example Episode');
		UPDATE catalog_series SET work_id=301 WHERE id=201;
		UPDATE work SET parent_work_id=301 WHERE id=302;
		UPDATE personal_transfer_ref SET object_kind='episode' WHERE work_id=302 AND is_primary=1;
		INSERT INTO user_work_state(user_id,work_id,rating,note)
			SELECT id,302,4,'Episode private note' FROM user_account WHERE username='synthetic-user';`)
	var sourceEntryWork sql.NullInt64
	if err := db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=101`).Scan(&sourceEntryWork); err != nil {
		t.Fatal(err)
	}
	if sourceEntryWork.Valid {
		t.Fatalf("fixture entry work_id = %d, want NULL", sourceEntryWork.Int64)
	}
	var exported personal.Backup
	workReferenceRequest(t, source, owner, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &exported)
	if len(exported.Works) != 1 || exported.Works[0].Identity.Parent == nil {
		t.Fatalf("exported works = %#v, want one episode with a parent", exported.Works)
	}
	parent := exported.Works[0].Identity.Parent
	if !strings.HasPrefix(parent.Ref, "s:") || parent.Kind != "series" || parent.Title != "Example Series" {
		t.Fatalf("exported parent = %#v", parent)
	}
	closeSource()

	targetDB, target, admin, _, closeTarget := openTransferApplication(t, t.TempDir(), true)
	defer closeTarget()
	body := mustJSON(map[string]any{"format": "kikoto", "data": exported, "conflict": "keep"})
	var result personal.ImportResult
	workReferenceRequest(t, target, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	var seriesID, seriesWork, episodeWork int64
	if err := targetDB.QueryRow(`SELECT id,work_id FROM catalog_series WHERE title='Example Series'`).Scan(&seriesID, &seriesWork); err != nil {
		t.Fatal(err)
	}
	if err := targetDB.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, seriesWork).Scan(&episodeWork); err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	workReferenceRequest(t, target, admin, http.MethodGet,
		fmt.Sprintf("/api/catalog-series/%d", seriesID), "", http.StatusOK, &view)
	episodes := view["episodes"].([]any)
	if len(episodes) != 1 {
		t.Fatalf("series view episodes = %#v", view["episodes"])
	}
	if episode := episodes[0].(map[string]any); episode["workId"] != float64(episodeWork) || episode["catalogEntryId"] != float64(0) {
		t.Fatalf("episode = %#v", episode)
	}
}

// A work already reachable through a source membership is listed once, not
// once per read arm.
func TestAuditPersonalTransferSeriesViewDeduplicatesSourceAndParentEpisode(t *testing.T) {
	db, app, admin, _, _ := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `
		INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote','Example Remote','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO work(id,primary_code,title) VALUES(401,'','Example Series'),(402,'','Example Episode');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(101,7,'/Example Episode','Example Episode',402);
		INSERT INTO catalog_series(id,title,work_id) VALUES(201,'Example Series',401);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(201,101,1,'directory');
		UPDATE work SET parent_work_id=401 WHERE id=402;`)
	var view map[string]any
	workReferenceRequest(t, app, admin, http.MethodGet, "/api/catalog-series/201", "", http.StatusOK, &view)
	episodes := view["episodes"].([]any)
	if len(episodes) != 1 {
		t.Fatalf("series view episodes = %#v, want one source-backed episode", view["episodes"])
	}
	episode := episodes[0].(map[string]any)
	if episode["catalogEntryId"] != float64(101) || episode["workId"] != float64(402) {
		t.Fatalf("episode = %#v, want the source-backed representation", episode)
	}
}
