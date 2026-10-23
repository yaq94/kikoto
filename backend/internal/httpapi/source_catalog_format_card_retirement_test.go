package httpapi

import (
	"database/sql"
	"net/http"
	"strconv"
	"testing"
)

// formatCardTree is a release whose playable audio only exists below a format
// copy. The release itself holds artwork and a readme, so an old card for the
// format directory cannot be excused by direct audio above it.
func formatCardTree() map[string][]map[string]any {
	return map[string][]map[string]any{
		"/":                    {looseDir("Creator")},
		"/Creator":             {looseDir("Release")},
		"/Creator/Release":     {looseDir("MP3"), looseDir("ART"), looseAudio("readme.txt")},
		"/Creator/Release/MP3": {looseAudio("01 first.mp3"), looseAudio("02 second.mp3")},
		"/Creator/Release/ART": {looseAudio("cover.jpg")},
	}
}

func fixtureSourceID(t *testing.T, f *looseWorkFixture) int64 {
	t.Helper()
	var id int64
	if err := f.server.db.QueryRow(`SELECT id FROM file_source ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedFormatCard recreates the row shape left by an older generation that
// admitted the format directory as its own root.
func seedFormatCard(t *testing.T, db *sql.DB, sourceID int64, root, title string, generation int64, files []string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`INSERT INTO source_catalog_entry(file_source_id,root_path,title,last_seen_generation,discovered_generation)
		VALUES(?,?,?,?,?) RETURNING id`, sourceID, root, title, generation, generation).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if _, err = db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
			VALUES(?,?,?,'audio',100,?)`, id, name, name, generation); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func catalogCardByID(t *testing.T, f *looseWorkFixture, id int64) map[string]any {
	t.Helper()
	return reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(id, 10), "", 200)
}

// A format card published by an older generation must not pin the directory.
// The release card must be published by the rescan, and the old card must become
// an alias of it so playback and old links keep working.
func TestSourceCatalogStaleFormatCardRetiresToReleaseOnRescan(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}

	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want only the release card", cards)
	}
	release := cards["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", cards)
	}
	releaseID := int64(release["catalogEntryId"].(float64))

	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if source.Valid || !relocated.Valid || relocated.Int64 != releaseID {
		t.Fatalf("stale format card source=%v relocated=%v, want the release card %d", source, relocated, releaseID)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=?`, staleID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("retired format card kept %d file rows", files)
	}
	if got := countCatalogAudio(f.tracks(t, release)); got != 2 {
		t.Fatalf("release tracks=%d, want both format-copy files", got)
	}
	for _, raw := range f.request(t, http.MethodGet, "/works?pageSize=100&q=MP3", "", 200)["works"].([]any) {
		if title := raw.(map[string]any)["title"].(string); title == "MP3" {
			t.Fatalf("retired format card is still searchable: %#v", raw)
		}
	}
	detail := catalogCardByID(t, f, staleID)
	if int64(detail["catalogEntryId"].(float64)) != releaseID || detail["title"] != "Release" {
		t.Fatalf("old card id resolves to %#v, want the release", detail)
	}
}

// The retirement path must keep personal records readable. A review saved on
// the release work stays reachable through the retired format card's id.
func TestSourceCatalogRetiredFormatCardKeepsPersonalReviewReadable(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4,"note":"Example retained note"}`, 200)

	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 1, []string{"01 first.mp3", "02 second.mp3"})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}

	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if source.Valid || !relocated.Valid || relocated.Int64 != releaseID {
		t.Fatalf("stale format card source=%v relocated=%v, want the release card %d", source, relocated, releaseID)
	}
	var refs int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id=?`, staleID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("personal refs=%d, want the retired id retained", refs)
	}
	detail := catalogCardByID(t, f, staleID)
	if int64(detail["catalogEntryId"].(float64)) != releaseID {
		t.Fatalf("old id resolves to %#v, want the release", detail)
	}
	assertReview(t, detail, float64(4), "Example retained note")
}

// A release that only ships track containers is still one work. Disc and
// chapter folders must not become one card each.
func TestSourceCatalogTrackContainersStayInOneCard(t *testing.T) {
	for name, children := range map[string][]string{
		"Disc":    {"Disc 1", "Disc 2"},
		"Chapter": {"Chapter 01", "Chapter 02"},
	} {
		container := make([]map[string]any, 0, len(children))
		for _, child := range children {
			container = append(container, looseDir(child))
		}
		tree := map[string][]map[string]any{
			"/":                {looseDir("Creator")},
			"/Creator":         {looseDir(name)},
			"/Creator/" + name: container,
		}
		for _, child := range children {
			tree["/Creator/"+name+"/"+child] = []map[string]any{looseAudio(child + ".mp3")}
		}
		t.Run(name, func(t *testing.T) {
			f := newLooseWorkFixture(t, tree)
			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
			if p := f.drain(t); p["status"] != "complete" {
				t.Fatalf("scan = %#v", p)
			}
			cards := f.cards(t)
			if len(cards) != 1 || cards[name] == nil {
				t.Fatalf("cards = %#v, want one %q card", cards, name)
			}
			if got := countCatalogAudio(f.tracks(t, cards[name])); got != len(children) {
				t.Fatalf("tracks = %d, want %d", got, len(children))
			}
		})
	}
}

// A format directory that failed on this rescan has no current evidence that
// its audio moved to the release. The old card keeps its files so a transient
// upstream failure never hides playable tracks.
func TestSourceCatalogFormatCardStaysWhenSubtreeFails(t *testing.T) {
	tree := formatCardTree()
	tree["/Creator/Release"] = append(tree["/Creator/Release"], looseAudio("00 intro.mp3"))
	f := newLooseWorkFixture(t, tree)
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 1, []string{"01 first.mp3", "02 second.mp3"})

	next := formatCardTree()
	next["/Creator/Release"] = append(next["/Creator/Release"], looseAudio("00 intro.mp3"))
	// Keep the directory in the tree but make the upstream fail: a 404 is a
	// confirmed deletion, while a transient error must retain the old card.
	f.updates(next)
	f.failPath("/Creator/Release/MP3")

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "partial" {
		t.Fatalf("rescan = %#v, want partial", p)
	}
	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if !source.Valid || source.Int64 != sourceID || relocated.Valid {
		t.Fatalf("stale card source=%v relocated=%v, want it kept for recovery", source, relocated)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=? AND kind='audio'`, staleID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("failed subtree lost %d of 2 audio rows", files)
	}
}

// A review saved on an old format card creates a work for that card. Once the
// release card owns the same audio, retirement must move that work to the
// release instead of leaving both cards visible.
func TestSourceCatalogReviewedFormatCardRetiresIntoReleaseWork(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	saved := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4,"note":"Format review"}`, 200)
	staleWorkID := int64(saved["workId"].(float64))

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards["Release"] == nil {
		t.Fatalf("cards=%v, want only the release card", cards)
	}
	var releaseWork, staleWork sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=?`, releaseID).Scan(&releaseWork); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&staleWork); err != nil {
		t.Fatal(err)
	}
	if !releaseWork.Valid || releaseWork.Int64 != staleWorkID || staleWork.Valid {
		t.Fatalf("release work=%v stale work=%v, want work %d moved to the release", releaseWork, staleWork, staleWorkID)
	}
	detail := catalogCardByID(t, f, staleID)
	if int64(detail["catalogEntryId"].(float64)) != releaseID {
		t.Fatalf("old id resolves to %#v, want the release", detail)
	}
	assertReview(t, detail, float64(4), "Format review")
}

// Conflicting reviews stay on their original works. The old card must remain
// visible so its owner can resolve the conflict instead of one value silently
// replacing the other.
func TestSourceCatalogReviewedFormatCardKeepsConflictingReviews(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4,"note":"Release review"}`, 200)
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":5,"note":"Format review"}`, 200)

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if !source.Valid || source.Int64 != sourceID || relocated.Valid {
		t.Fatalf("conflicting format card source=%v relocated=%v, want it retained", source, relocated)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=? AND kind='audio'`, staleID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("conflicting format card kept %d of 2 audio rows", files)
	}
	assertReview(t, catalogCardByID(t, f, releaseID), float64(4), "Release review")
	assertReview(t, catalogCardByID(t, f, staleID), float64(5), "Format review")
}

// A conflict is not a dead end: both cards stay readable, and the record owner
// can reconcile the old card through the normal review endpoint. The next scan
// then retires it and keeps the shared work reachable through the old id.
func TestSourceCatalogReviewedFormatCardConflictIsResolvableByOwner(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4,"note":"Release review"}`, 200)
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":5,"note":"Format review"}`, 200)

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("conflicting rescan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 2 {
		t.Fatalf("cards=%v, want both a release card and its conflicting format card", cards)
	}
	assertReview(t, catalogCardByID(t, f, releaseID), float64(4), "Release review")
	assertReview(t, catalogCardByID(t, f, staleID), float64(5), "Format review")

	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4,"note":"Release review"}`, 200)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("resolution rescan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 1 || cards["Release"] == nil {
		t.Fatalf("cards=%v, want only the release card after the owner reconciled", cards)
	}
	var staleSource sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&staleSource, &relocated); err != nil {
		t.Fatal(err)
	}
	if staleSource.Valid || !relocated.Valid || relocated.Int64 != releaseID {
		t.Fatalf("stale card source=%v relocated=%v, want the release card %d", staleSource, relocated, releaseID)
	}
	assertReview(t, catalogCardByID(t, f, staleID), float64(4), "Release review")
}

// Non-conflicting records from every user move to the release work. A value the
// release already stores wins; empty fields are filled from the retired work.
func TestSourceCatalogReviewedFormatCardMergesNonConflictingStates(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4}`, 200)
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	saved := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4,"note":"Format review"}`, 200)
	staleWorkID := int64(saved["workId"].(float64))

	createAccountManagedTestUser(t, f.server, "format-listener", "format-password")
	listener := loginTestSession(t, f.handler, "format-listener", "format-password")
	var listenerID int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-listener'`).Scan(&listenerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(?,?,5,'Listener note')`, listenerID, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list(id,user_id,name) VALUES(901,?,'Format list')`, listenerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note,sort_order) VALUES(901,?,'Format list note',7)`, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO user_tag(id,user_id,name) VALUES(901,?,'Format tag')`, listenerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO user_work_tag(user_id,work_id,user_tag_id) VALUES(?,?,901)`, listenerID, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO media_item(id,work_id,kind,title) VALUES(901,?,'audio','Format track')`, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO user_work_playback_cursor(
			user_id,work_id,media_item_id,position_seconds,duration_seconds,last_played_at,created_at,updated_at)
		VALUES(?,?,901,30,60,'2026-01-02 00:00:00','2026-01-02 00:00:00','2026-01-02 00:00:00')`, listenerID, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO user_listening_session(user_id,session_id,work_id,listened_seconds)
		VALUES(?,'format-session',?,90)`, listenerID, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO user_listening_import(user_id,work_id,listened_seconds,listen_count,last_played_at)
		VALUES(?,?,90,1,'2026-01-02 00:00:00')`, listenerID, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO personal_transfer_progress(user_id,work_id,progress_json)
		VALUES(?,?,'{}')`, listenerID, staleWorkID); err != nil {
		t.Fatal(err)
	}

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards["Release"] == nil {
		t.Fatalf("cards=%v, want only the release card", cards)
	}
	var releaseWork, staleWork sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=?`, releaseID).Scan(&releaseWork); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&staleWork); err != nil {
		t.Fatal(err)
	}
	if !releaseWork.Valid || releaseWork.Int64 == staleWorkID || staleWork.Valid {
		t.Fatalf("release work=%v stale work=%v, want records merged into the release work", releaseWork, staleWork)
	}
	releaseWorkID := releaseWork.Int64
	assertReview(t, catalogCardByID(t, f, releaseID), float64(4), "Format review")
	assertReview(t, catalogCardByID(t, f, staleID), float64(4), "Format review")
	assertReview(t, reviewRequest(t, f.handler, listener, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10), "", 200), float64(5), "Listener note")
	var favoriteNote string
	var favoriteOrder int
	if err := f.server.db.QueryRow(`SELECT note,sort_order FROM favorite_list_item WHERE list_id=901 AND work_id=?`, releaseWorkID).
		Scan(&favoriteNote, &favoriteOrder); err != nil {
		t.Fatal(err)
	}
	if favoriteNote != "Format list note" || favoriteOrder != 7 {
		t.Fatalf("favorite = (%q, %d), want preserved note and order", favoriteNote, favoriteOrder)
	}
	var cursorPosition float64
	var cursorMedia int64
	if err := f.server.db.QueryRow(`SELECT media_item_id,position_seconds FROM user_work_playback_cursor WHERE user_id=? AND work_id=?`, listenerID, releaseWorkID).
		Scan(&cursorMedia, &cursorPosition); err != nil {
		t.Fatal(err)
	}
	if cursorMedia != 901 || cursorPosition != 30 {
		t.Fatalf("playback cursor = (%d, %v), want preserved track and position", cursorMedia, cursorPosition)
	}
	var sessionWork, importedWork, transferWork int64
	if err := f.server.db.QueryRow(`SELECT work_id FROM user_listening_session WHERE user_id=? AND session_id='format-session'`, listenerID).Scan(&sessionWork); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT work_id FROM user_listening_import WHERE user_id=?`, listenerID).Scan(&importedWork); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT work_id FROM personal_transfer_progress WHERE user_id=?`, listenerID).Scan(&transferWork); err != nil {
		t.Fatal(err)
	}
	if sessionWork != releaseWorkID || importedWork != releaseWorkID || transferWork != releaseWorkID {
		t.Fatalf("dependent state works = session:%d import:%d transfer:%d, want %d", sessionWork, importedWork, transferWork, releaseWorkID)
	}
	for _, table := range []string{"user_work_state", "favorite_list_item", "user_work_tag", "user_work_playback_cursor", "user_listening_session", "user_listening_import", "personal_transfer_progress"} {
		var remaining int
		if err := f.server.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE work_id=?`, staleWorkID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("retired work kept %d rows in %s", remaining, table)
		}
	}
}

// Favorite-list notes and positions are owner-authored values. If both the old
// format card and the release card have a filled but different value, automatic
// retirement must pause with both records intact. An empty target entry can be
// filled from the old card instead of dropping the old value.
func TestSourceCatalogFormatCardFavoriteConflictsStayForOwner(t *testing.T) {
	cases := []struct {
		name          string
		targetNote    string
		targetOrder   int
		staleNote     string
		staleOrder    int
		wantRetired   bool
		wantNote      string
		wantOrder     int
		wantOldRemain bool
	}{
		{
			name:          "different notes",
			targetNote:    "Target list note",
			staleNote:     "Format list note",
			wantNote:      "Target list note",
			wantOldRemain: true,
		},
		{
			name:          "different positions",
			targetOrder:   3,
			staleNote:     "Format list note",
			staleOrder:    7,
			wantNote:      "",
			wantOrder:     3,
			wantOldRemain: true,
		},
		{
			name:        "empty target entry is filled",
			staleNote:   "Format list note",
			staleOrder:  7,
			wantRetired: true,
			wantNote:    "Format list note",
			wantOrder:   7,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLooseWorkFixture(t, formatCardTree())
			sourceID := fixtureSourceID(t, f)
			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
			if p := f.drain(t); p["status"] != "complete" {
				t.Fatalf("first scan = %#v", p)
			}
			release := f.cards(t)["Release"]
			if release == nil {
				t.Fatalf("cards=%v, want the release card", f.cards(t))
			}
			releaseID := int64(release["catalogEntryId"].(float64))
			reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4}`, 200)
			var userID, targetWorkID int64
			if err := f.server.db.QueryRow(`SELECT u.id,e.work_id FROM user_account u JOIN source_catalog_entry e ON e.id=? WHERE u.username='synthetic-user'`, releaseID).Scan(&userID, &targetWorkID); err != nil {
				t.Fatal(err)
			}
			staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
			saved := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4}`, 200)
			staleWorkID := int64(saved["workId"].(float64))
			if _, err := f.server.db.Exec(`INSERT INTO favorite_list(id,user_id,name,kind) VALUES(901,?,'Format list','user')`, userID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note,sort_order) VALUES(901,?,?,?)`, targetWorkID, tc.targetNote, tc.targetOrder); err != nil {
				t.Fatal(err)
			}
			if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note,sort_order) VALUES(901,?,?,?)`, staleWorkID, tc.staleNote, tc.staleOrder); err != nil {
				t.Fatal(err)
			}

			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
			if p := f.drain(t); p["status"] != "complete" {
				t.Fatalf("rescan = %#v", p)
			}
			var source sql.NullInt64
			var relocated sql.NullInt64
			if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
				t.Fatal(err)
			}
			if tc.wantRetired {
				if source.Valid || !relocated.Valid || relocated.Int64 != releaseID {
					t.Fatalf("retired card source=%v relocated=%v, want the release card %d", source, relocated, releaseID)
				}
			} else if !source.Valid || source.Int64 != sourceID || relocated.Valid {
				t.Fatalf("conflicting card source=%v relocated=%v, want both cards retained", source, relocated)
			}
			var note string
			var order int
			if err := f.server.db.QueryRow(`SELECT note,sort_order FROM favorite_list_item WHERE list_id=901 AND work_id=?`, targetWorkID).Scan(&note, &order); err != nil {
				t.Fatal(err)
			}
			if note != tc.wantNote || order != tc.wantOrder {
				t.Fatalf("target favorite = (%q, %d), want (%q, %d)", note, order, tc.wantNote, tc.wantOrder)
			}
			var oldRows int
			if err := f.server.db.QueryRow(`SELECT count(*) FROM favorite_list_item WHERE list_id=901 AND work_id=?`, staleWorkID).Scan(&oldRows); err != nil {
				t.Fatal(err)
			}
			if tc.wantOldRemain && oldRows != 1 {
				t.Fatalf("old favorite rows=%d, want the conflicting record retained", oldRows)
			}
			if !tc.wantOldRemain && oldRows != 0 {
				t.Fatalf("old favorite rows=%d, want the migrated record removed", oldRows)
			}
		})
	}
}

// A portable resume point is account-owned. When both works already carry one
// for the same user, retirement must keep both records and both cards so the
// owner can review which position to keep.
func TestSourceCatalogFormatCardTransferProgressConflictStaysForOwner(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4}`, 200)
	var userID, targetWorkID int64
	if err := f.server.db.QueryRow(`SELECT u.id,e.work_id FROM user_account u JOIN source_catalog_entry e ON e.id=? WHERE u.username='synthetic-user'`, releaseID).Scan(&userID, &targetWorkID); err != nil {
		t.Fatal(err)
	}
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	saved := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4}`, 200)
	staleWorkID := int64(saved["workId"].(float64))
	if _, err := f.server.db.Exec(`INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(?,?,'{"position":20}')`, userID, targetWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(?,?,'{"position":80}')`, userID, staleWorkID); err != nil {
		t.Fatal(err)
	}

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if !source.Valid || source.Int64 != sourceID || relocated.Valid {
		t.Fatalf("conflicting card source=%v relocated=%v, want both cards retained", source, relocated)
	}
	var targetProgress, staleProgress string
	if err := f.server.db.QueryRow(`SELECT progress_json FROM personal_transfer_progress WHERE user_id=? AND work_id=?`, userID, targetWorkID).Scan(&targetProgress); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT progress_json FROM personal_transfer_progress WHERE user_id=? AND work_id=?`, userID, staleWorkID).Scan(&staleProgress); err != nil {
		t.Fatal(err)
	}
	if targetProgress != `{"position":20}` || staleProgress != `{"position":80}` {
		t.Fatalf("transfer progress = target %s stale %s, want both retained", targetProgress, staleProgress)
	}
}

// Favorite lists and progress belong to different users, so an entry on one
// user's target work does not conflict with another user's entry on the old
// work. Non-conflicting rows from both users move to the surviving work.
func TestSourceCatalogFormatCardMovesDifferentUsersRecordsWithoutConflict(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4}`, 200)
	var targetWorkID int64
	if err := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=?`, releaseID).Scan(&targetWorkID); err != nil {
		t.Fatal(err)
	}
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	saved := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4}`, 200)
	staleWorkID := int64(saved["workId"].(float64))

	createAccountManagedTestUser(t, f.server, "format-owner-a", "format-password")
	createAccountManagedTestUser(t, f.server, "format-owner-b", "format-password")
	var userA, userB int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-owner-a'`).Scan(&userA); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-owner-b'`).Scan(&userB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list(id,user_id,name,kind) VALUES(901,?,'Owner A list','user'),(902,?,'Owner B list','user')`, userA, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note,sort_order) VALUES(901,?,'Owner A target note',2)`, targetWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note,sort_order) VALUES(902,?,'Owner B old note',5)`, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(?,?,'{"owner":"a-target"}')`, userA, targetWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(?,?,'{"owner":"b-old"}')`, userB, staleWorkID); err != nil {
		t.Fatal(err)
	}

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 1 || cards["Release"] == nil {
		t.Fatalf("cards=%v, want only the release card", cards)
	}
	var noteA, noteB string
	if err := f.server.db.QueryRow(`SELECT note FROM favorite_list_item WHERE list_id=901 AND work_id=?`, targetWorkID).Scan(&noteA); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT note FROM favorite_list_item WHERE list_id=902 AND work_id=?`, targetWorkID).Scan(&noteB); err != nil {
		t.Fatal(err)
	}
	if noteA != "Owner A target note" || noteB != "Owner B old note" {
		t.Fatalf("favorite notes = A %q B %q, want both users preserved", noteA, noteB)
	}
	var progressA, progressB string
	if err := f.server.db.QueryRow(`SELECT progress_json FROM personal_transfer_progress WHERE user_id=? AND work_id=?`, userA, targetWorkID).Scan(&progressA); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT progress_json FROM personal_transfer_progress WHERE user_id=? AND work_id=?`, userB, targetWorkID).Scan(&progressB); err != nil {
		t.Fatal(err)
	}
	if progressA != `{"owner":"a-target"}` || progressB != `{"owner":"b-old"}` {
		t.Fatalf("transfer progress = A %s B %s, want both users moved", progressA, progressB)
	}
}

// A conflict for any affected user keeps the card pending. Other users do not
// get to resolve it, and their own records remain untouched until it is clear.
func TestSourceCatalogFormatCardConflictBlocksRetirementForEveryUser(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4}`, 200)
	var targetWorkID int64
	if err := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=?`, releaseID).Scan(&targetWorkID); err != nil {
		t.Fatal(err)
	}
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	saved := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4}`, 200)
	staleWorkID := int64(saved["workId"].(float64))

	createAccountManagedTestUser(t, f.server, "format-conflict-a", "format-password")
	createAccountManagedTestUser(t, f.server, "format-conflict-b", "format-password")
	var userA, userB int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-conflict-a'`).Scan(&userA); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-conflict-b'`).Scan(&userB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list(id,user_id,name,kind) VALUES(901,?,'Owner A list','user'),(902,?,'Owner B list','user')`, userA, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note) VALUES(901,?,'Owner A target')`, targetWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note) VALUES(901,?,'Owner A old')`, staleWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO favorite_list_item(list_id,work_id,note) VALUES(902,?,'Owner B old')`, staleWorkID); err != nil {
		t.Fatal(err)
	}

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if !source.Valid || source.Int64 != sourceID || relocated.Valid {
		t.Fatalf("conflicting card source=%v relocated=%v, want both cards retained", source, relocated)
	}
	var targetNoteA, oldNoteA, noteB string
	if err := f.server.db.QueryRow(`SELECT note FROM favorite_list_item WHERE list_id=901 AND work_id=?`, targetWorkID).Scan(&targetNoteA); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT note FROM favorite_list_item WHERE list_id=901 AND work_id=?`, staleWorkID).Scan(&oldNoteA); err != nil {
		t.Fatal(err)
	}
	if targetNoteA != "Owner A target" || oldNoteA != "Owner A old" {
		t.Fatalf("owner A notes = target %q old %q, want both retained", targetNoteA, oldNoteA)
	}
	if err := f.server.db.QueryRow(`SELECT note FROM favorite_list_item WHERE list_id=902 AND work_id=?`, staleWorkID).Scan(&noteB); err != nil {
		t.Fatal(err)
	}
	if noteB != "Owner B old" {
		t.Fatalf("owner B note=%q, want retained while another user's conflict is pending", noteB)
	}

	// Only owner A has a conflict. Reconciling that user's own value clears the
	// pending state; owner B does not have to edit anything.
	if _, err := f.server.db.Exec(`UPDATE favorite_list_item SET note='Owner A old' WHERE list_id=901 AND work_id=?`, targetWorkID); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("resolved rescan = %#v", p)
	}
	var resolvedSource sql.NullInt64
	var resolvedRelocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&resolvedSource, &resolvedRelocated); err != nil {
		t.Fatal(err)
	}
	if resolvedSource.Valid || !resolvedRelocated.Valid || resolvedRelocated.Int64 != releaseID {
		t.Fatalf("resolved card source=%v relocated=%v, want the release card %d", resolvedSource, resolvedRelocated, releaseID)
	}
	if err := f.server.db.QueryRow(`SELECT note FROM favorite_list_item WHERE list_id=901 AND work_id=?`, targetWorkID).Scan(&targetNoteA); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT note FROM favorite_list_item WHERE list_id=902 AND work_id=?`, targetWorkID).Scan(&noteB); err != nil {
		t.Fatal(err)
	}
	if targetNoteA != "Owner A old" || noteB != "Owner B old" {
		t.Fatalf("resolved favorites = A %q B %q, want both users preserved", targetNoteA, noteB)
	}
}

// A series member is a separate evaluation object; moving it into a release
// card would merge two identities.
func TestSourceCatalogFormatCardSeriesMemberIsNotRetired(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	seriesCard := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/FLAC", "FLAC", 0, []string{"01 first.flac"})
	result, err := f.server.db.Exec(`INSERT INTO catalog_series(title,source_id,source_key) VALUES('Retained series',?,'retained-format-series')`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	seriesID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.db.Exec(`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(?,?,1,'manual')`, seriesID, seriesCard); err != nil {
		t.Fatal(err)
	}

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, seriesCard).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if !source.Valid || source.Int64 != sourceID || relocated.Valid {
		t.Fatalf("series card source=%v relocated=%v, want it retained", source, relocated)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=?`, seriesCard).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("series card lost its file rows")
	}
}
