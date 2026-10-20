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
		"/":                     {looseDir("Creator")},
		"/Creator":              {looseDir("Release")},
		"/Creator/Release":      {looseDir("MP3"), looseDir("ART"), looseAudio("readme.txt")},
		"/Creator/Release/MP3":  {looseAudio("01 first.mp3"), looseAudio("02 second.mp3")},
		"/Creator/Release/ART":  {looseAudio("cover.jpg")},
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

// A card that already owns a work or a series episode is never retired
// automatically; choosing a parent would merge two identities.
func TestSourceCatalogFormatCardWithWorkOrSeriesIsNotRetired(t *testing.T) {
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)

	result, err := f.server.db.Exec(`INSERT INTO work(primary_code,title) VALUES('','Retained format work')`)
	if err != nil {
		t.Fatal(err)
	}
	workID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	workCard := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3"})
	if _, err = f.server.db.Exec(`UPDATE source_catalog_entry SET work_id=? WHERE id=?`, workID, workCard); err != nil {
		t.Fatal(err)
	}

	seriesCard := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/FLAC", "FLAC", 0, []string{"01 first.flac"})
	result, err = f.server.db.Exec(`INSERT INTO catalog_series(title,source_id,source_key) VALUES('Retained series',?,'retained-format-series')`, sourceID)
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
	for name, id := range map[string]int64{"work": workCard, "series": seriesCard} {
		var source sql.NullInt64
		var relocated sql.NullInt64
		if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, id).Scan(&source, &relocated); err != nil {
			t.Fatal(err)
		}
		if !source.Valid || source.Int64 != sourceID || relocated.Valid {
			t.Fatalf("%s card source=%v relocated=%v, want it retained", name, source, relocated)
		}
		var files int
		if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=?`, id).Scan(&files); err != nil {
			t.Fatal(err)
		}
		if files == 0 {
			t.Fatalf("%s card lost its file rows", name)
		}
	}
}
