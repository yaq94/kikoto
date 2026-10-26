package httpapi

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/personal"
)

// A work reachable from two different series has no single parent. The import
// must report the conflict on an otherwise successful response and keep the
// stored relation untouched instead of choosing one series.
func TestPersonalTransferMultipleDifferentParentsReportConflict(t *testing.T) {
	db, app, admin, _, _ := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `
		INSERT INTO work(id,primary_code,title) VALUES
			(301,'','Series A'),(302,'','Episode One'),(303,'','Series B');
		INSERT INTO catalog_series(id,title,work_id) VALUES
			(201,'Series A',301),(202,'Series B',303);
		INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES
			(101,'/A/Episode','Episode One',302),
			(102,'/B/Episode','Episode One',302);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(201,101,1,'directory'),(202,102,1,'directory');
		UPDATE work SET parent_work_id=301 WHERE id=302;
		INSERT INTO user_work_state(user_id,work_id,rating,note)
			SELECT id,302,2,'Original note' FROM user_account WHERE username='synthetic-user';`)
	var episodeRef, parentRef string
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE work_id=302 AND is_primary=1`).Scan(&episodeRef); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE series_id=201`).Scan(&parentRef); err != nil {
		t.Fatal(err)
	}
	rating := 4
	backup := personal.Backup{Format: "kikoto-user-data", Version: 2, Works: []personal.BackupWork{{
		Identity: &personal.BackupIdentity{Ref: episodeRef, Kind: "episode", Title: "Episode One",
			Parent: &personal.BackupIdentity{Ref: parentRef, Kind: "series", Title: "Series A"}},
		ListeningStatus: "none", Rating: &rating, Note: "Contradictory note",
	}}}
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "keep"})
	var preview personal.ImportPreview
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import/preview", body,
		http.StatusOK, &preview)
	assertParentConflictIssue(t, preview.Issues)
	var result personal.ImportResult
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 0 || result.SkippedWorks != 1 {
		t.Fatalf("restore = %+v, want the conflicted episode skipped", result)
	}
	assertParentConflictIssue(t, result.Issues)

	var parent int64
	if err := db.QueryRow(`SELECT parent_work_id FROM work WHERE id=302`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != 301 {
		t.Fatalf("episode parent = %d, want the original series 301", parent)
	}
	var works, series, storedRating int
	var storedNote string
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM work),(SELECT COUNT(*) FROM catalog_series),
		(SELECT rating FROM user_work_state WHERE user_id=(SELECT id FROM user_account WHERE username='synthetic-user') AND work_id=302),
		(SELECT note FROM user_work_state WHERE user_id=(SELECT id FROM user_account WHERE username='synthetic-user') AND work_id=302)`).Scan(&works, &series, &storedRating, &storedNote); err != nil {
		t.Fatal(err)
	}
	if works != 3 || series != 2 || storedRating != 2 || storedNote != "Original note" {
		t.Fatalf("conflict changed shared or personal state: works=%d series=%d rating=%d note=%q", works, series, storedRating, storedNote)
	}
}

func assertParentConflictIssue(t *testing.T, issues []personal.ImportIssue) {
	t.Helper()
	for _, issue := range issues {
		if issue.Reason == "parent_conflict" {
			return
		}
	}
	t.Fatalf("issues = %+v, want parent_conflict", issues)
}

// An episode reachable from two series has no single exportable parent. The
// backup must still download with its review and exact references, and the
// ambiguity must be visible in the export itself.
func TestPersonalTransferExportReportsMultipleParentsWithoutFailing(t *testing.T) {
	db, app, admin, _, _ := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `
		INSERT INTO work(id,primary_code,title) VALUES
			(311,'','Series A'),(312,'','Episode One'),(313,'','Series B');
		INSERT INTO catalog_series(id,title,work_id) VALUES
			(211,'Series A',311),(212,'Series B',313);
		INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES
			(111,'/A/Episode One','Episode One',312),
			(112,'/B/Episode One','Episode One',312);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(211,111,1,'directory'),(212,112,1,'directory');
		INSERT INTO user_work_state(user_id,work_id,rating,note)
			SELECT id,312,4,'Episode private note' FROM user_account WHERE username='synthetic-user';`)
	var exported personal.Backup
	workReferenceRequest(t, app, admin, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &exported)
	if exported.Version != 2 || len(exported.Works) != 1 {
		t.Fatalf("export = version %d, %d works", exported.Version, len(exported.Works))
	}
	w := exported.Works[0]
	if w.Identity == nil || w.Identity.Kind != "episode" || w.Identity.Parent != nil {
		t.Fatalf("identity = %#v, want an episode with no parent", w.Identity)
	}
	if w.Rating == nil || *w.Rating != 4 || w.Note != "Episode private note" {
		t.Fatalf("review = %v/%q, want it preserved", w.Rating, w.Note)
	}
	catalogRefs := 0
	for _, ref := range w.Identity.References {
		if strings.HasPrefix(ref, "c:") {
			catalogRefs++
		}
	}
	if catalogRefs != 2 {
		t.Fatalf("references = %v, want both catalog references", w.Identity.References)
	}
	assertParentConflictIssue(t, exported.Issues)

	// The exported document still imports. The episode becomes a standalone work
	// with a visible relationships_not_imported note rather than a guessed series.
	targetDB, target, targetAdmin, _, closeTarget := openTransferApplication(t, t.TempDir(), true)
	defer closeTarget()
	body := mustJSON(map[string]any{"format": "kikoto", "data": exported, "conflict": "keep"})
	var preview personal.ImportPreview
	workReferenceRequest(t, target, targetAdmin, http.MethodPost, "/api/user-data/import/preview", body,
		http.StatusOK, &preview)
	assertIssueReason(t, preview.Issues, "relationships_not_imported")
	var result personal.ImportResult
	workReferenceRequest(t, target, targetAdmin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	assertIssueReason(t, result.Issues, "relationships_not_imported")
	var parent sql.NullInt64
	if err := targetDB.QueryRow(`SELECT parent_work_id FROM work WHERE title='Episode One'`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent.Valid {
		t.Fatalf("restored episode invented parent %d", parent.Int64)
	}
}

func assertIssueReason(t *testing.T, issues []personal.ImportIssue, reason string) {
	t.Helper()
	for _, issue := range issues {
		if issue.Reason == reason {
			return
		}
	}
	t.Fatalf("issues = %+v, want %s", issues, reason)
}

// Creating a personal-only series parent registers shared catalog objects, so
// it needs registration permission. The same import must not leak the owner's
// records into another account's export.
func TestPersonalTransferParentRegistrationRequiresPermissionAndStaysUserScoped(t *testing.T) {
	db, app, admin, listener, _ := openTransferApplication(t, t.TempDir(), true)
	rating := 5
	backup := personal.Backup{Format: "kikoto-user-data", Version: 2, Works: []personal.BackupWork{{
		Identity: &personal.BackupIdentity{Ref: parentTestRef("w", '1'), Kind: "episode", Title: "Episode One",
			Parent: &personal.BackupIdentity{Ref: parentTestRef("s", '2'), Kind: "series", Title: "Series A"}},
		ListeningStatus: "none", Rating: &rating, Note: "Episode note",
	}}}
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "keep"})

	var preview personal.ImportPreview
	workReferenceRequest(t, app, listener, http.MethodPost, "/api/user-data/import/preview", body,
		http.StatusOK, &preview)
	if preview.NewWorks != 2 || len(preview.Issues) != 0 {
		t.Fatalf("preview = %+v, want the episode and its parent to require registration", preview)
	}
	workReferenceRequest(t, app, listener, http.MethodPost, "/api/user-data/import", body,
		http.StatusForbidden, nil)
	var works, series int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM work),(SELECT COUNT(*) FROM catalog_series)`).Scan(&works, &series); err != nil {
		t.Fatal(err)
	}
	if works != 0 || series != 0 {
		t.Fatalf("denied import registered works=%d series=%d", works, series)
	}

	var result personal.ImportResult
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 || len(result.Issues) != 0 {
		t.Fatalf("owner restore = %+v", result)
	}
	var seriesWork, episodeWork int64
	if err := db.QueryRow(`SELECT work_id FROM catalog_series WHERE title='Series A'`).Scan(&seriesWork); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, seriesWork).Scan(&episodeWork); err != nil {
		t.Fatal(err)
	}

	var ownerBackup, otherBackup personal.Backup
	workReferenceRequest(t, app, admin, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &ownerBackup)
	if len(ownerBackup.Works) != 1 || ownerBackup.Works[0].Identity.Parent == nil {
		t.Fatalf("owner export = %+v, want the episode with its parent", ownerBackup.Works)
	}
	workReferenceRequest(t, app, listener, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &otherBackup)
	if len(otherBackup.Works) != 0 {
		t.Fatalf("other user's export = %+v, want no owner records", otherBackup.Works)
	}
	var listenerNotes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_work_state state
		JOIN user_account account ON account.id=state.user_id
		WHERE state.work_id=? AND account.username='synthetic-listener'`, episodeWork).Scan(&listenerNotes); err != nil {
		t.Fatal(err)
	}
	if listenerNotes != 0 {
		t.Fatalf("denied import left %d personal rows on the other account", listenerNotes)
	}
}

// After an empty-library restore both the personal-only series card and the
// episode accept their own review through the public endpoints. The restore
// itself must not fabricate media or a workflow.
func TestPersonalTransferRestoredSeriesAndEpisodeReviewIndependently(t *testing.T) {
	db, app, admin, _, _ := openTransferApplication(t, t.TempDir(), true)
	rating := 2
	backup := personal.Backup{Format: "kikoto-user-data", Version: 2, Works: []personal.BackupWork{{
		Identity: &personal.BackupIdentity{Ref: parentTestRef("w", '3'), Kind: "episode", Title: "Episode One",
			Parent: &personal.BackupIdentity{Ref: parentTestRef("s", '4'), Kind: "series", Title: "Series A"}},
		ListeningStatus: "none", Rating: &rating, Note: "Episode note",
	}}}
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "keep"})
	var result personal.ImportResult
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	var seriesID, seriesWork, episodeWork int64
	if err := db.QueryRow(`SELECT id,work_id FROM catalog_series WHERE title='Series A'`).Scan(&seriesID, &seriesWork); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, seriesWork).Scan(&episodeWork); err != nil {
		t.Fatal(err)
	}
	workReferenceRequest(t, app, admin, http.MethodPatch,
		fmt.Sprintf("/api/catalog-series/%d/user-state", seriesID), `{"rating":5,"note":"Series note"}`, http.StatusOK, nil)
	var series map[string]any
	workReferenceRequest(t, app, admin, http.MethodGet, fmt.Sprintf("/api/catalog-series/%d", seriesID), "", http.StatusOK, &series)
	if series["personalRating"] != float64(5) || series["personalNote"] != "Series note" {
		t.Fatalf("series review = %v/%v", series["personalRating"], series["personalNote"])
	}
	var episode map[string]any
	workReferenceRequest(t, app, admin, http.MethodGet, fmt.Sprintf("/api/works/%d?includeMedia=false", episodeWork), "", http.StatusOK, &episode)
	assertReview(t, episode, float64(2), "Episode note")
	var media, runs, members int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM media_item),(SELECT COUNT(*) FROM workflow_run),
		(SELECT COUNT(*) FROM catalog_series_member)`).Scan(&media, &runs, &members); err != nil {
		t.Fatal(err)
	}
	if media != 0 || runs != 0 || members != 0 {
		t.Fatalf("restore created media=%d runs=%d members=%d", media, runs, members)
	}
}

func parentTestRef(prefix string, digit byte) string {
	return prefix + ":" + strings.Repeat(string(digit), 32)
}
