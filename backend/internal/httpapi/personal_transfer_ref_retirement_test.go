package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/personal"
)

func personalTransferRequest(t *testing.T, handler http.Handler, cookie *http.Cookie, method, path, body string, status int, target any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s: status %d, want %d; %s", method, path, response.Code, status, response.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersonalTransferRetirementReferencesRestoreToSuccessor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		catalogRef bool
		dropWork   bool
	}{
		{name: "old work reference"},
		{name: "old catalog reference after work cleanup", catalogRef: true, dropWork: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLooseWorkFixture(t, formatCardTree())
			sourceID := fixtureSourceID(t, f)
			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
			if progress := f.drain(t); progress["status"] != "complete" {
				t.Fatalf("first scan = %#v", progress)
			}
			release := f.cards(t)["Release"]
			if release == nil {
				t.Fatalf("cards=%v, want the release card", f.cards(t))
			}
			releaseID := int64(release["catalogEntryId"].(float64))
			savedRelease := reviewRequest(t, f.handler, f.cookie, http.MethodPatch,
				"/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4}`, 200)
			targetWorkID := int64(savedRelease["workId"].(float64))

			staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
			savedStale := reviewRequest(t, f.handler, f.cookie, http.MethodPatch,
				"/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state",
				`{"rating":4,"note":"Format transfer note"}`, 200)
			staleWorkID := int64(savedStale["workId"].(float64))
			if staleWorkID == targetWorkID {
				t.Fatalf("stale and target share work %d", targetWorkID)
			}

			var userID, tagID, listID int64
			if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='synthetic-user'`).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			if err := f.server.db.QueryRow(`INSERT INTO user_tag(user_id,name,color) VALUES(?,?,?) RETURNING id`,
				userID, "Format tag", "blue").Scan(&tagID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.server.db.Exec(`INSERT INTO user_work_tag(user_id,work_id,user_tag_id) VALUES(?,?,?)`,
				userID, staleWorkID, tagID); err != nil {
				t.Fatal(err)
			}
			if err := f.server.db.QueryRow(`INSERT INTO favorite_list(user_id,name,kind) VALUES(?,?,'user') RETURNING id`,
				userID, "Format favorites").Scan(&listID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note,sort_order) VALUES(?,?,?,?)`,
				listID, staleWorkID, "Format favorite note", 3); err != nil {
				t.Fatal(err)
			}
			if _, err := f.server.db.Exec(`INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(?,?,?)`,
				userID, staleWorkID, `{"mediaTitle":"Format portable take","positionSeconds":41,"lastPlayedAt":"2026-02-01 00:00:00"}`); err != nil {
				t.Fatal(err)
			}

			var oldWorkRef, oldCatalogRef string
			if err := f.server.db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE work_id=? AND is_primary=1`, staleWorkID).Scan(&oldWorkRef); err != nil {
				t.Fatal(err)
			}
			if err := f.server.db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=?`, staleID).Scan(&oldCatalogRef); err != nil {
				t.Fatal(err)
			}

			var backup personal.Backup
			personalTransferRequest(t, f.handler, f.cookie, http.MethodGet, "/api/user-data/export", "", 200, &backup)
			var staleWork personal.BackupWork
			found := false
			for _, work := range backup.Works {
				if work.Identity != nil && work.Identity.Ref == oldWorkRef {
					staleWork, found = work, true
					break
				}
			}
			if !found {
				t.Fatalf("backup does not contain stale work %d: %+v", staleWorkID, backup.Works)
			}
			if staleWork.Progress == nil || len(backup.Playlists) != 1 {
				t.Fatalf("backup has no portable progress or favorite list: %+v", backup)
			}
			ref := oldWorkRef
			if tc.catalogRef {
				ref = oldCatalogRef
			}
			restoredRating := 5
			staleWork.Rating = &restoredRating
			staleWork.Note = "Restored transfer note"
			staleWork.Tags = []string{"Restored tag"}
			staleWork.Progress.PositionSeconds = 77.5
			staleWork.Progress.MediaTitle = "Restored portable take"
			staleWork.Identity.Ref = ref
			staleWork.Identity.References = nil
			staleWork.Progress.MediaIdentity = staleWork.Identity
			backup.Works = []personal.BackupWork{staleWork}
			backup.Tags = []personal.BackupTag{{Scope: "work", Name: "Restored tag", Color: "green"}}
			backup.Playlists[0].Items = []personal.BackupPlaylistItem{{
				PrimaryCode: staleWork.PrimaryCode,
				WorkRef:     ref,
				Note:        "Restored favorite note",
				SortOrder:   1,
			}}

			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
			if progress := f.drain(t); progress["status"] != "complete" {
				t.Fatalf("retirement scan = %#v", progress)
			}
			if detail := catalogCardByID(t, f, staleID); int64(detail["catalogEntryId"].(float64)) != releaseID {
				t.Fatalf("retired card resolves to %#v, want release %d", detail, releaseID)
			}
			var staleEntryWork, relocated int64
			if err := f.server.db.QueryRow(`SELECT COALESCE(work_id,0),COALESCE(relocated_to_id,0) FROM source_catalog_entry WHERE id=?`, staleID).Scan(&staleEntryWork, &relocated); err != nil {
				t.Fatal(err)
			}
			if staleEntryWork != 0 || relocated != releaseID {
				t.Fatalf("retired entry work=%d relocated=%d; want work cleared and release %d", staleEntryWork, relocated, releaseID)
			}

			if tc.catalogRef {
				if _, err := f.server.db.Exec(`DELETE FROM work WHERE id=?`, staleWorkID); err != nil {
					t.Fatal(err)
				}
			} else {
				var refWork, primary int64
				if err := f.server.db.QueryRow(`SELECT work_id,is_primary FROM personal_transfer_ref WHERE ref=?`, oldWorkRef).Scan(&refWork, &primary); err != nil {
					t.Fatal(err)
				}
				if refWork != targetWorkID || primary != 0 {
					t.Fatalf("old work reference = %d/%d; want target %d as a non-primary alias", refWork, primary, targetWorkID)
				}
				var primaries int
				if err := f.server.db.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE work_id=? AND is_primary=1`, targetWorkID).Scan(&primaries); err != nil || primaries != 1 {
					t.Fatalf("target primary references = %d, %v; want exactly one", primaries, err)
				}
			}

			var worksBefore int
			if err := f.server.db.QueryRow(`SELECT count(*) FROM work`).Scan(&worksBefore); err != nil {
				t.Fatal(err)
			}
			body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "overwrite"})
			var result personal.ImportResult
			personalTransferRequest(t, f.handler, f.cookie, http.MethodPost, "/api/user-data/import", body, 200, &result)
			if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
				t.Fatalf("restore = %#v", result)
			}
			var worksAfter int
			if err := f.server.db.QueryRow(`SELECT count(*) FROM work`).Scan(&worksAfter); err != nil {
				t.Fatal(err)
			}
			if worksAfter != worksBefore {
				t.Fatalf("restore created %d works, want %d", worksAfter-worksBefore, 0)
			}

			var rating, favorite int
			var note string
			if err := f.server.db.QueryRow(`SELECT COALESCE(rating,0),note,favorite FROM user_work_state WHERE user_id=? AND work_id=?`,
				userID, targetWorkID).Scan(&rating, &note, &favorite); err != nil {
				t.Fatal(err)
			}
			if rating != 5 || note != "Restored transfer note" || favorite != 1 {
				t.Fatalf("target review = %d/%q/%d; want 5/restored/1", rating, note, favorite)
			}
			var tags int
			if err := f.server.db.QueryRow(`SELECT count(*) FROM user_work_tag a JOIN user_tag t ON t.id=a.user_tag_id
				WHERE a.user_id=? AND a.work_id=? AND t.name='Restored tag'`, userID, targetWorkID).Scan(&tags); err != nil || tags != 1 {
				t.Fatalf("target restored tags = %d, %v; want one", tags, err)
			}
			var favorites int
			if err := f.server.db.QueryRow(`SELECT count(*) FROM favorite_list_item i JOIN favorite_list l ON l.id=i.list_id
				WHERE l.user_id=? AND l.name='Format favorites' AND i.work_id=? AND i.note='Restored favorite note'`,
				userID, targetWorkID).Scan(&favorites); err != nil || favorites != 1 {
				t.Fatalf("target restored favorite items = %d, %v; want one", favorites, err)
			}
			var raw string
			if err := f.server.db.QueryRow(`SELECT progress_json FROM personal_transfer_progress WHERE user_id=? AND work_id=?`,
				userID, targetWorkID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var restored personal.BackupProgress
			if err := json.Unmarshal([]byte(raw), &restored); err != nil {
				t.Fatal(err)
			}
			if restored.PositionSeconds != 77.5 || restored.MediaTitle != "Restored portable take" {
				t.Fatalf("target portable progress = %+v", restored)
			}
			if tc.dropWork {
				var old int
				if err := f.server.db.QueryRow(`SELECT count(*) FROM work WHERE id=?`, staleWorkID).Scan(&old); err != nil || old != 0 {
					t.Fatalf("old work still exists: %d, %v", old, err)
				}
			} else {
				var oldRecords int
				if err := f.server.db.QueryRow(`SELECT
					(SELECT count(*) FROM user_work_state WHERE work_id=?1) +
					(SELECT count(*) FROM user_work_tag WHERE work_id=?1) +
					(SELECT count(*) FROM favorite_list_item WHERE work_id=?1) +
					(SELECT count(*) FROM personal_transfer_progress WHERE work_id=?1) +
					(SELECT count(*) FROM user_work_playback_cursor WHERE work_id=?1)`,
					staleWorkID).Scan(&oldRecords); err != nil || oldRecords != 0 {
					t.Fatalf("old work received %d personal rows, %v", oldRecords, err)
				}
			}
		})
	}
}
