package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// seedAdmissionRows appends a bounded set of file rows to one card. The path
// prefix and suffix keep every relative_path unique, and the generation is
// explicit so a test can model a row left by an older publish.
func seedAdmissionRows(t *testing.T, db *sql.DB, entryID int64, prefix, suffix, kind string, count int, generation int64) {
	t.Helper()
	if count == 0 {
		return
	}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		SELECT ?, ?||i||?, ?||i, ?, 1, ? FROM n`,
		count, entryID, prefix, suffix, prefix, kind, generation); err != nil {
		t.Fatalf("seed %d %s rows for entry %d: %v", count, kind, entryID, err)
	}
}

// Every read entry point must judge the same current published file set. The
// refused cards are reachable by id for provenance but must not accept a
// review, prepare playback, serve media or appear as a directory tab.
func TestCatalogAdmissionEntryPointsUsePublishedSet(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(89,'admission-entry-points','Admission entry points','openlist',1);
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url)
		VALUES(89,'https://entry.example.invalid','https://entry.example.invalid');
		INSERT INTO work(id,primary_code,title) VALUES(901,'RJ00000901','Admission work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,last_seen_generation) VALUES
			(8901,89,'/admitted','Admitted',901,7),
			(8902,89,'/oversize','Oversize',901,7),
			(8903,89,'/no-audio','No audio',901,7),
			(8904,89,'/zero','Zero file',901,7),
			(8905,89,'/old-generation','Old generation',901,7),
			(8906,89,'/folder-only','Folder only',901,7);`)
	seedAdmissionRows(t, db, 8901, "a", ".mp3", "audio", 3, 7)
	seedAdmissionRows(t, db, 8901, "n", ".jpg", "other", 100, 7)
	seedAdmissionRows(t, db, 8902, "a", ".mp3", "audio", 1, 7)
	seedAdmissionRows(t, db, 8902, "n", ".jpg", "other", 6000, 7)
	seedAdmissionRows(t, db, 8903, "n", ".jpg", "other", 10, 7)
	// Only an older generation observed this card's files. They stay for the
	// tracks view but are not the current published release.
	seedAdmissionRows(t, db, 8905, "a", ".mp3", "audio", 1, 6)
	seedAdmissionRows(t, db, 8905, "n", ".jpg", "other", 10, 6)
	seedAdmissionRows(t, db, 8906, "folder", "", "folder", 6000, 7)

	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	admin := loginTestSession(t, handler, "synthetic-user", "synthetic-password")

	for _, entryID := range []string{"8902", "8903", "8904", "8905", "8906"} {
		reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/"+entryID+"/user-state", `{"rating":4}`, http.StatusNotFound)
		reviewRequest(t, handler, admin, http.MethodPost, "/api/source-catalog/"+entryID+"/playback", `{"path":"a1.mp3"}`, http.StatusNotFound)
		reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/"+entryID+"/media?path=a1.mp3", "", http.StatusNotFound)
	}

	// The admitted card still accepts every one of those operations.
	reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/8901/user-state", `{"rating":4}`, http.StatusOK)
	reviewRequest(t, handler, admin, http.MethodPost, "/api/source-catalog/8901/playback", `{"path":"a1.mp3"}`, http.StatusOK)
	reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/8901", "", http.StatusOK)
	reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/8901/tracks", "", http.StatusOK)

	directories := reviewRequest(t, handler, admin, http.MethodGet, "/api/works/901/catalog-directories", "", http.StatusOK)["directories"].([]any)
	if len(directories) != 1 {
		t.Fatalf("directory tabs = %#v, want only the admitted card", directories)
	}
	if got := int64(directories[0].(map[string]any)["catalogEntryId"].(float64)); got != 8901 {
		t.Fatalf("directory tab = %d, want 8901", got)
	}
}

// The single-card count, the browse SQL predicate and the publisher's rule all
// read the current published file set: current generation, folder rows
// excluded. Each case is one threshold boundary.
func TestCatalogAdmissionCountsCurrentPublishedFiles(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(88,'admission-counts','Admission counts','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,last_seen_generation) VALUES
			(8801,88,'/old-generation-oversize','Old generation oversize',5),
			(8802,88,'/old-generation-only','Old generation only',5),
			(8803,88,'/folders-only','Folders only',5),
			(8804,88,'/zero','Zero',5),
			(8805,88,'/files-5001','Files 5001',5),
			(8806,88,'/audio-4-non-audio-2001','Audio 4 non-audio 2001',5),
			(8807,88,'/files-5000','Files 5000',5),
			(8808,88,'/non-audio-2000','Non-audio 2000',5),
			(8809,88,'/audio-5-non-audio-2001','Audio 5 non-audio 2001',5),
			(8810,88,'/audio-1-files-5001','Audio 1 files 5001',5);`)

	// 8801: the old generation would be over the ceiling, the current one is not.
	seedAdmissionRows(t, db, 8801, "a", ".mp3", "audio", 5, 5)
	seedAdmissionRows(t, db, 8801, "n", ".jpg", "other", 4995, 5)
	seedAdmissionRows(t, db, 8801, "old", ".jpg", "other", 6000, 4)
	// 8802: only one current audio row; the old audio and attachments are not
	// part of this card's published release.
	seedAdmissionRows(t, db, 8802, "a", ".mp3", "audio", 1, 5)
	seedAdmissionRows(t, db, 8802, "olda", ".mp3", "audio", 1, 4)
	seedAdmissionRows(t, db, 8802, "oldn", ".jpg", "other", 6000, 4)
	// 8803: folder rows are directory markers, not files.
	seedAdmissionRows(t, db, 8803, "folder", "", "folder", 6000, 5)
	// 8804: no rows at all.
	seedAdmissionRows(t, db, 8805, "a", ".mp3", "audio", 1, 5)
	seedAdmissionRows(t, db, 8805, "n", ".jpg", "other", 5000, 5)
	seedAdmissionRows(t, db, 8806, "a", ".mp3", "audio", 4, 5)
	seedAdmissionRows(t, db, 8806, "n", ".jpg", "other", 2001, 5)
	seedAdmissionRows(t, db, 8807, "a", ".mp3", "audio", 5, 5)
	seedAdmissionRows(t, db, 8807, "n", ".jpg", "other", 4995, 5)
	seedAdmissionRows(t, db, 8808, "a", ".mp3", "audio", 1, 5)
	seedAdmissionRows(t, db, 8808, "n", ".jpg", "other", 2000, 5)
	seedAdmissionRows(t, db, 8809, "a", ".mp3", "audio", 5, 5)
	seedAdmissionRows(t, db, 8809, "n", ".jpg", "other", 2001, 5)
	seedAdmissionRows(t, db, 8810, "a", ".mp3", "audio", 1, 5)
	seedAdmissionRows(t, db, 8810, "n", ".jpg", "other", 5000, 5)

	cases := []struct {
		id                     int64
		files, audio, nonAudio int
		reason                 string
		admitted               bool
	}{
		// Counts stop one row past each bound: files at 5001, non-audio at
		// 2001 and audio at 5. A refusal decision never needs the exact total.
		{8801, 5000, 5, 2001, "", true},
		{8802, 1, 1, 0, "", true},
		{8803, 0, 0, 0, "no_audio", false},
		{8804, 0, 0, 0, "no_audio", false},
		{8805, 5001, 1, 2001, "too_many_files", false},
		{8806, 2005, 4, 2001, "too_many_non_audio", false},
		{8807, 5000, 5, 2001, "", true},
		{8808, 2001, 1, 2000, "", true},
		{8809, 2006, 5, 2001, "", true},
		{8810, 5001, 1, 2001, "too_many_files", false},
	}
	for _, tc := range cases {
		adm, err := catalogPublishedEntryAdmission(context.Background(), db, tc.id)
		if err != nil {
			t.Fatalf("entry %d admission: %v", tc.id, err)
		}
		if adm.Files != tc.files || adm.Audio != tc.audio || adm.NonAudio != tc.nonAudio || adm.Reason != tc.reason || adm.Admitted() != tc.admitted {
			t.Fatalf("entry %d admission = %+v, want files=%d audio=%d nonAudio=%d reason=%q admitted=%v",
				tc.id, adm, tc.files, tc.audio, tc.nonAudio, tc.reason, tc.admitted)
		}
		var sqlAdmitted int
		if err := db.QueryRow(`SELECT `+catalogEntryAdmittedSQL("e")+` FROM source_catalog_entry e WHERE e.id=?`, tc.id).Scan(&sqlAdmitted); err != nil {
			t.Fatalf("entry %d SQL admission: %v", tc.id, err)
		}
		if (sqlAdmitted != 0) != tc.admitted {
			t.Fatalf("entry %d SQL admitted=%d, want %v", tc.id, sqlAdmitted, tc.admitted)
		}
	}
}

// A zero-file card is no longer implicitly readable, while a retained missing
// record remains addressable for its personal state.
func TestCatalogAdmissionZeroFileCardIsRefused(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(87,'admission-zero','Admission zero','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) VALUES
			(8701,87,'/zero','Zero','available'),
			(8702,87,'/missing','Missing','missing');`)
	server := &Server{db: db}
	for _, entryID := range []int64{8701, 8702} {
		request := newCatalogEntryRequest(t, entryID)
		response := httptest.NewRecorder()
		server.getSourceCatalogEntry(response, request)
		want := http.StatusNotFound
		if entryID == 8702 {
			want = http.StatusOK
		}
		if response.Code != want {
			t.Fatalf("entry %d detail status = %d, want %d body=%s", entryID, response.Code, want, response.Body.String())
		}
	}
}

func newCatalogEntryRequest(t *testing.T, id int64) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/source-catalog/%d", id), nil)
	request.SetPathValue("id", fmt.Sprintf("%d", id))
	return request
}
