package httpapi

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/personal"
)

// A personal backup keeps the series parent of an episode even when the user has
// no series review. Restoring into an empty library must recreate only the
// explicit episode-to-series relationship and keep both evaluation levels
// independently reviewable after a real process restart.
func TestPersonalTransferRestoresEpisodeParentAcrossEmptyLibraryRestart(t *testing.T) {
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

	var exported map[string]any
	workReferenceRequest(t, source, owner, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &exported)
	works := exported["works"].([]any)
	if len(works) != 1 {
		t.Fatalf("exported works = %#v, want only the reviewed episode", works)
	}
	identity := works[0].(map[string]any)["identity"].(map[string]any)
	parent, ok := identity["parent"].(map[string]any)
	if !ok {
		t.Fatalf("episode identity has no parent: %#v", identity)
	}
	if parent["ref"] == "" || parent["title"] != "Example Series" ||
		parent["position"] != float64(2) || parent["evidence"] != "directory" ||
		parent["entryRef"] == "" {
		t.Fatalf("episode parent = %#v, want the explicit series member identity", parent)
	}
	entryRef := parent["entryRef"].(string)
	parentRef := parent["ref"].(string)
	if !strings.HasPrefix(parentRef, "s:") || !strings.HasPrefix(entryRef, "c:") {
		t.Fatalf("parent refs = %q/%q, want series/member references", parentRef, entryRef)
	}
	closeSource()

	targetRoot := t.TempDir()
	targetDB, target, admin, listener, closeTarget := openTransferApplication(t, targetRoot, true)
	body := mustJSON(map[string]any{"format": "kikoto", "data": exported, "conflict": "keep"})

	var preview personal.ImportPreview
	workReferenceRequest(t, target, admin, http.MethodPost, "/api/user-data/import/preview", body,
		http.StatusOK, &preview)
	if len(preview.Issues) != 0 {
		t.Fatalf("empty-library preview issues = %+v, want a fully resolved episode parent", preview.Issues)
	}
	var result personal.ImportResult
	workReferenceRequest(t, target, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v, want the reviewed episode imported with its parent", result)
	}
	workReferenceRequest(t, target, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("duplicate restore = %+v", result)
	}
	var targetSeriesID int64
	if err := targetDB.QueryRow(`SELECT id FROM catalog_series WHERE title='Example Series'`).Scan(&targetSeriesID); err != nil {
		t.Fatal(err)
	}
	var refSeries, refWork, refCatalog sql.NullInt64
	if err := targetDB.QueryRow(`SELECT series_id,work_id,catalog_entry_id FROM personal_transfer_ref WHERE ref=?`, parentRef).
		Scan(&refSeries, &refWork, &refCatalog); err != nil {
		t.Fatal(err)
	}
	if !refSeries.Valid || refSeries.Int64 != targetSeriesID || refWork.Valid || refCatalog.Valid {
		t.Fatalf("restored parent ref = series %v work %v catalog %v, want only series %d", refSeries, refWork, refCatalog, targetSeriesID)
	}
	var entryID, memberSeries int64
	if err := targetDB.QueryRow(`SELECT catalog_entry_id FROM personal_transfer_ref WHERE ref=?`, entryRef).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if err := targetDB.QueryRow(`SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=?`, entryID).Scan(&memberSeries); err != nil {
		t.Fatal(err)
	}
	if memberSeries != targetSeriesID {
		t.Fatalf("episode member series = %d, want %d", memberSeries, targetSeriesID)
	}
	var library map[string]any
	workReferenceRequest(t, target, admin, http.MethodGet, "/api/works", "",
		http.StatusOK, &library)
	if library["total"] != float64(2) {
		t.Fatalf("registered works = %#v, want the episode and its series only", library["total"])
	}
	closeTarget()

	restartedDB, restarted, admin, listener, closeRestarted := openTransferApplication(t, targetRoot, false)
	defer closeRestarted()
	var restored map[string]any
	workReferenceRequest(t, restarted, admin, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &restored)
	restoredWorks := restored["works"].([]any)
	if len(restoredWorks) != 1 {
		t.Fatalf("restarted export works = %#v, want the episode", restoredWorks)
	}
	restoredEpisode := restoredWorks[0].(map[string]any)
	restoredIdentity := restoredEpisode["identity"].(map[string]any)
	restoredParent, ok := restoredIdentity["parent"].(map[string]any)
	if !ok || restoredParent["ref"] != parentRef || restoredParent["title"] != "Example Series" ||
		restoredParent["position"] != float64(2) || restoredParent["evidence"] != "directory" ||
		restoredParent["entryRef"] != entryRef {
		t.Fatalf("restarted episode parent = %#v, want %#v", restoredParent, parent)
	}
	if restoredEpisode["rating"] != float64(4) || restoredEpisode["note"] != "Episode private note" {
		t.Fatalf("restarted episode review = %#v", restoredEpisode)
	}

	var seriesID int64
	if err := restartedDB.QueryRow(`SELECT id FROM catalog_series WHERE title='Example Series'`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	workReferenceRequest(t, restarted, admin, http.MethodPatch,
		fmt.Sprintf("/api/catalog-series/%d/user-state", seriesID),
		`{"rating":5,"note":"Series private note"}`, http.StatusOK, nil)
	var both personal.Backup
	workReferenceRequest(t, restarted, admin, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &both)
	if len(both.Works) != 2 {
		t.Fatalf("exported works after series review = %+v, want independent series and episode", both.Works)
	}
	var episodeReview, seriesReview *personal.BackupWork
	for i := range both.Works {
		switch both.Works[i].Identity.Kind {
		case "episode":
			episodeReview = &both.Works[i]
		case "series":
			seriesReview = &both.Works[i]
		}
	}
	if episodeReview == nil || episodeReview.Rating == nil || *episodeReview.Rating != 4 ||
		episodeReview.Note != "Episode private note" || episodeReview.Identity.Parent == nil ||
		episodeReview.Identity.Parent.Ref != parentRef || episodeReview.Identity.Parent.EntryRef != entryRef {
		t.Fatalf("episode review after series update = %+v", episodeReview)
	}
	if seriesReview == nil || seriesReview.Rating == nil || *seriesReview.Rating != 5 ||
		seriesReview.Note != "Series private note" {
		t.Fatalf("series review = %+v", seriesReview)
	}

	var other personal.Backup
	workReferenceRequest(t, restarted, listener, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &other)
	if len(other.Works) != 0 {
		t.Fatalf("another user's export = %+v, want isolation", other.Works)
	}
	var media, workflows int
	if err := restartedDB.QueryRow(`SELECT count(*) FROM media_item`).Scan(&media); err != nil {
		t.Fatal(err)
	}
	if err := restartedDB.QueryRow(`SELECT count(*) FROM workflow_run`).Scan(&workflows); err != nil {
		t.Fatal(err)
	}
	if media != 0 || workflows != 0 {
		t.Fatalf("import created media=%d workflows=%d", media, workflows)
	}
	files, err := os.ReadDir(filepath.Join(targetRoot, "data"))
	if err != nil || len(files) != 0 {
		t.Fatalf("import wrote media files: %v %v", files, err)
	}
}

// An episode whose relationship cannot be recovered is not a standalone work.
// The preview and result keep it reviewable, and no partial identity is written.
func TestPersonalTransferBlocksEpisodeWithoutRestorableParent(t *testing.T) {
	db, app, admin, _, _ := openTransferApplication(t, t.TempDir(), true)
	rating := 3
	backup := personal.Backup{Format: "kikoto-user-data", Version: 2, Works: []personal.BackupWork{{
		Identity: &personal.BackupIdentity{
			Ref:   "w:" + strings.Repeat("1", 32),
			Kind:  "episode",
			Title: "Example Episode",
		},
		ListeningStatus: "finished",
		Rating:          &rating,
		Note:            "Unresolved episode note",
	}}}
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "keep"})

	var preview personal.ImportPreview
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import/preview", body,
		http.StatusOK, &preview)
	if preview.NewWorks != 0 || len(preview.Issues) != 1 || preview.Issues[0].Reason != "parent_missing" {
		t.Fatalf("preview = %+v, want one explicit missing-parent outcome", preview)
	}
	var result personal.ImportResult
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 0 || result.SkippedWorks != 1 ||
		len(result.Issues) != 1 || result.Issues[0].Reason != "parent_missing" {
		t.Fatalf("restore = %+v, want the unresolved episode skipped for review", result)
	}
	var works, series int
	if err := db.QueryRow(`SELECT count(*) FROM work`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil {
		t.Fatal(err)
	}
	if works != 0 || series != 0 {
		t.Fatalf("unresolved episode wrote works=%d series=%d", works, series)
	}
}

// A personal restore never replaces an existing shared series membership. The
// contradiction is reported and the stored review and relationship stay intact.
func TestPersonalTransferKeepsExistingDifferentEpisodeParent(t *testing.T) {
	sourceDB, source, owner, _, _ := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, sourceDB, `
		INSERT INTO source_catalog_entry(id,root_path,title,primary_code)
			VALUES(101,'/Example Series/Episode 1','Example Episode','');
		INSERT INTO catalog_series(id,title) VALUES(201,'Example Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			VALUES(201,101,1,'manual');`)
	workReferenceRequest(t, source, owner, http.MethodPatch,
		"/api/source-catalog/101/user-state",
		`{"rating":4,"note":"Original episode note"}`, http.StatusOK, nil)
	var backup personal.Backup
	workReferenceRequest(t, source, owner, http.MethodGet, "/api/user-data/export", "",
		http.StatusOK, &backup)
	if len(backup.Works) != 1 || backup.Works[0].Identity.Parent == nil {
		t.Fatalf("backup = %+v, want one episode with parent", backup.Works)
	}

	db, app, admin, _, _ := openTransferApplication(t, t.TempDir(), true)
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "overwrite"})
	var result personal.ImportResult
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("initial restore = %+v", result)
	}
	metadataReviewExec(t, db, `
		INSERT INTO catalog_series(id,title) VALUES(202,'Different Shared Series');
		UPDATE catalog_series_member
		SET series_id=202
		WHERE catalog_entry_id=(SELECT catalog_entry_id FROM catalog_series_member LIMIT 1);`)
	incomingRating := 2
	backup.Works[0].Rating = &incomingRating
	backup.Works[0].Note = "Contradictory incoming note"
	body = mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "overwrite"})

	var preview personal.ImportPreview
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import/preview", body,
		http.StatusOK, &preview)
	if preview.NewWorks != 0 || len(preview.Issues) != 1 || preview.Issues[0].Reason != "parent_conflict" {
		t.Fatalf("preview = %+v, want one parent conflict", preview)
	}
	result = personal.ImportResult{}
	workReferenceRequest(t, app, admin, http.MethodPost, "/api/user-data/import", body,
		http.StatusOK, &result)
	if result.ImportedWorks != 0 || result.SkippedWorks != 1 ||
		len(result.Issues) != 1 || result.Issues[0].Reason != "parent_conflict" {
		t.Fatalf("restore = %+v, want the contradictory episode retained for review", result)
	}

	var seriesTitle, note string
	var rating int
	err := db.QueryRow(`SELECT series.title,state.rating,state.note
		FROM catalog_series_member member
		JOIN catalog_series series ON series.id=member.series_id
		JOIN source_catalog_entry entry ON entry.id=member.catalog_entry_id
		JOIN user_work_state state ON state.work_id=entry.work_id
		WHERE entry.title='Example Episode'`).Scan(&seriesTitle, &rating, &note)
	if err != nil {
		t.Fatal(err)
	}
	if seriesTitle != "Different Shared Series" || rating != 4 || note != "Original episode note" {
		t.Fatalf("stored membership/review = %q/%d/%q, want unchanged", seriesTitle, rating, note)
	}
	var works int
	if err := db.QueryRow(`SELECT count(*) FROM work`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if works != 2 {
		t.Fatalf("conflicting import left %d works, want the existing episode and series", works)
	}
}
