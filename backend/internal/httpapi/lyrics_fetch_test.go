package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/kikoeru"
	"github.com/yexca/kikoto/backend/internal/localfs"
	"github.com/yexca/kikoto/backend/internal/storagepool"
)

const lyricsFetchVTT = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nSynthetic translated line\n"

type lyricsFetchFixture struct {
	server   *Server
	db       *sql.DB
	dataRoot string
	workID   int64
	audioID  int64
	folderID int64
	root     string
	requests *atomic.Int32
}

// newLyricsFetchFixture builds a scanned local origin edition, a translated
// sibling edition known only as metadata, and a configured remote source whose
// cached tree lists the sibling's lyrics at textURL.
func newLyricsFetchFixture(t *testing.T, handler http.HandlerFunc, textURL func(upstream string) string) lyricsFetchFixture {
	t.Helper()
	requests := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)

	dataRoot := t.TempDir()
	audioDir := filepath.Join(dataRoot, "RJ00000001", "mp3")
	if err := os.MkdirAll(audioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(audioDir, "Track1.Original name.mp3"), []byte("synthetic audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := storagepool.WriteMarker(dataRoot, "synthetic-library"); err != nil {
		t.Fatal(err)
	}
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{DataRoot: dataRoot, LocalScanDepth: 2})
	executeLocalScanForTest(t, server)

	ctx := context.Background()
	fixture := lyricsFetchFixture{server: server, db: db, dataRoot: dataRoot, requests: requests}
	var fileSourceID int64
	if err := db.QueryRow(`
		SELECT folder.id, folder.work_id, folder.file_source_id, folder.root_path
		FROM work_folder_location AS folder
		INNER JOIN work ON work.id = folder.work_id
		WHERE work.primary_code = 'RJ00000001'
	`).Scan(&fixture.folderID, &fixture.workID, &fileSourceID, &fixture.root); err != nil {
		t.Fatal(err)
	}
	if err := server.indexLocalMediaForWork(ctx, fixture.workID, fileSourceID, fixture.root); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT id FROM media_item WHERE work_id = ? AND kind = 'audio'", fixture.workID).Scan(&fixture.audioID); err != nil {
		t.Fatal(err)
	}
	// One statement per Exec: placeholders do not bind across statements.
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{query: "INSERT INTO work (id, primary_code, title) VALUES (902, 'RJ00000002', 'Example Work 2'), (903, 'RJ00000003', 'Example Work 3')"},
		{query: "INSERT INTO logical_work (id, canonical_work_id, canonical_code) VALUES (?, ?, 'RJ00000001')", args: []any{fixture.workID, fixture.workID}},
		{query: `INSERT INTO work_edition (work_id, logical_work_id, primary_code, base_code, metadata_language, is_canonical) VALUES
			(?, ?, 'RJ00000001', 'RJ00000001', 'JPN', 1),
			(902, ?, 'RJ00000002', 'RJ00000001', 'CHI_HANS', 0)`, args: []any{fixture.workID, fixture.workID, fixture.workID}},
		{query: "INSERT INTO file_source (id, code, display_name, source_type, enabled) VALUES (7, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible', 1)"},
		{query: "INSERT INTO file_source_endpoint (file_source_id, api_url, base_url, restrict_outbound_hosts) VALUES (7, ?, ?, 1)", args: []any{upstream.URL, upstream.URL}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	source, err := server.currentRemoteCacheSource(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"RJ00000002", "RJ00000003"} {
		work := kikoeru.Work{ID: 70, SourceID: code, Title: "Example Work"}
		tracks := []kikoeru.Track{
			{Type: "folder", Title: "Translated", Children: []kikoeru.Track{
				{Type: "text", Title: "Track1.Translated name.mp3.vtt", MediaDownloadURL: textURL(upstream.URL)},
				{Type: "audio", Title: "Track1.Translated name.mp3", MediaDownloadURL: upstream.URL + "/media/audio.mp3"},
			}},
		}
		key := remoteWorkCacheKeyForLanguages(source.ID, code, server.instanceMetadataLanguages(ctx))
		expiresAt := time.Now().Add(time.Minute)
		server.remoteWorkCache[key] = remoteWorkSnapshot{Source: source, Work: work, ExpiresAt: expiresAt}
		server.remoteWorkTracksCache[key] = remoteWorkTracksSnapshot{Source: source, Work: work, Tracks: tracks, ExpiresAt: expiresAt}
	}
	return fixture
}

func (fixture lyricsFetchFixture) post(t *testing.T, body map[string]any, permissions ...string) *httptest.ResponseRecorder {
	t.Helper()
	if len(permissions) == 0 {
		permissions = []string{"downloads:manage", "library:write"}
	}
	payload := map[string]any{
		"sourceId":   7,
		"remoteCode": "RJ00000002",
		"folderId":   fixture.folderID,
		"files":      []string{"Translated/Track1.Translated name.mp3.vtt"},
		"assignments": []map[string]any{
			{"audioMediaItemId": fixture.audioID, "path": "Translated/Track1.Translated name.mp3.vtt"},
		},
	}
	for key, value := range body {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	workID := jsonNumber(fixture.workID)
	request := httptest.NewRequest(http.MethodPost, "/api/works/"+workID+"/lyrics-fetch", strings.NewReader(string(encoded)))
	request.SetPathValue("id", workID)
	request = request.WithContext(context.WithValue(request.Context(), currentUserKey, currentUser{ID: 0, Permissions: permissions}))
	response := httptest.NewRecorder()
	fixture.server.fetchWorkLyrics(response, request)
	return response
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (fixture lyricsFetchFixture) lyricsFolders(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixture.dataRoot, filepath.FromSlash(fixture.root)))
	if err != nil {
		t.Fatal(err)
	}
	folders := []string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "lyrics") {
			folders = append(folders, entry.Name())
		}
	}
	return folders
}

func (fixture lyricsFetchFixture) decode(t *testing.T, response *httptest.ResponseRecorder) lyricsFetchResult {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result lyricsFetchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// assignedLanguages lists the track's library lyrics as "language=path".
func (fixture lyricsFetchFixture) assignedLanguages(t *testing.T) []string {
	t.Helper()
	rows, err := fixture.db.Query(`
		SELECT assignment.language, assignment.origin, location.path
		FROM media_lyrics_assignment AS assignment
		INNER JOIN media_file_location AS location ON location.media_item_id = assignment.lyrics_media_item_id
		WHERE assignment.audio_media_item_id = ?
		ORDER BY assignment.language
	`, fixture.audioID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	result := []string{}
	for rows.Next() {
		var language, origin, lyricsPath string
		if err := rows.Scan(&language, &origin, &lyricsPath); err != nil {
			t.Fatal(err)
		}
		result = append(result, language+"="+origin+":"+lyricsPath)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func serveLyricsFetchVTT(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/media/1.vtt" {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(lyricsFetchVTT))
}

func lyricsFetchVTTURL(upstream string) string { return upstream + "/media/1.vtt" }

func (fixture lyricsFetchFixture) assertNoStagingLeft(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixture.dataRoot, ".kikoto-staging"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "lyrics-") {
			t.Fatalf("lyrics staging %q was left behind", entry.Name())
		}
	}
}

func TestLyricsFetchPublishesTheLanguageFolderAndAssigns(t *testing.T) {
	fixture := newLyricsFetchFixture(t, serveLyricsFetchVTT, lyricsFetchVTTURL)

	result := fixture.decode(t, fixture.post(t, nil))
	// The folder is named after the edition's language and carries no work
	// code, so a scan cannot take it for another work.
	if folders := fixture.lyricsFolders(t); len(folders) != 1 || folders[0] != "lyrics_zh_hans" {
		t.Fatalf("lyrics folders = %v", folders)
	}
	if result.Folder != fixture.root+"/lyrics_zh_hans" || result.Language != "zh-cn" || result.Downloaded != 1 || result.Skipped != 0 || result.Assigned != 1 {
		t.Fatalf("result = %+v", result)
	}
	published := filepath.Join(fixture.dataRoot, filepath.FromSlash(result.Folder), "Translated", "Track1.Translated name.mp3.vtt")
	if content, err := os.ReadFile(published); err != nil || string(content) != lyricsFetchVTT {
		t.Fatalf("published lyrics = %q, %v", content, err)
	}
	if fixture.requests.Load() != 1 {
		t.Fatalf("upstream requests = %d, want only the selected lyrics file", fixture.requests.Load())
	}
	want := "zh-cn=remote_fetch:" + result.Folder + "/Translated/Track1.Translated name.mp3.vtt"
	if assigned := fixture.assignedLanguages(t); len(assigned) != 1 || assigned[0] != want {
		t.Fatalf("assignments = %v, want [%s]", assigned, want)
	}
	var sibling int
	if err := fixture.db.QueryRow("SELECT COUNT(*) FROM media_item WHERE work_id = 902").Scan(&sibling); err != nil || sibling != 0 {
		t.Fatalf("sibling edition gained media items: %d, %v", sibling, err)
	}
	fixture.assertNoStagingLeft(t)
}

func TestLyricsFetchKeepsLanguagesApart(t *testing.T) {
	fixture := newLyricsFetchFixture(t, serveLyricsFetchVTT, lyricsFetchVTTURL)

	fixture.decode(t, fixture.post(t, nil))
	// The same edition's file declared as another language is that language's
	// lyrics: it lands in its own folder and leaves the first assignment alone.
	english := fixture.decode(t, fixture.post(t, map[string]any{"language": "en-us"}))
	universal := fixture.decode(t, fixture.post(t, map[string]any{"language": ""}))
	if english.Folder != fixture.root+"/lyrics_en" || universal.Folder != fixture.root+"/lyrics" {
		t.Fatalf("folders = %q, %q", english.Folder, universal.Folder)
	}
	file := "/Translated/Track1.Translated name.mp3.vtt"
	want := []string{
		"=remote_fetch:" + fixture.root + "/lyrics" + file,
		"en-us=remote_fetch:" + fixture.root + "/lyrics_en" + file,
		"zh-cn=remote_fetch:" + fixture.root + "/lyrics_zh_hans" + file,
	}
	if assigned := fixture.assignedLanguages(t); strings.Join(assigned, "\n") != strings.Join(want, "\n") {
		t.Fatalf("assignments = %v, want %v", assigned, want)
	}
	if response := fixture.post(t, map[string]any{"language": "tlh"}); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported language status = %d, want 400", response.Code)
	}
}

func TestLyricsFetchNeverReplacesAnExistingFile(t *testing.T) {
	fixture := newLyricsFetchFixture(t, serveLyricsFetchVTT, lyricsFetchVTTURL)
	fixture.decode(t, fixture.post(t, nil))

	// Fetching a file the folder already holds changes nothing.
	repeated := fixture.decode(t, fixture.post(t, nil))
	if repeated.Downloaded != 0 || repeated.Skipped != 1 || repeated.Assigned != 1 {
		t.Fatalf("repeated result = %+v", repeated)
	}

	published := filepath.Join(fixture.dataRoot, filepath.FromSlash(repeated.Folder), "Translated", "Track1.Translated name.mp3.vtt")
	if err := os.WriteFile(published, []byte("edited by the user"), 0o644); err != nil {
		t.Fatal(err)
	}
	response := fixture.post(t, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), fixture.dataRoot) || strings.Contains(response.Body.String(), "Translated") {
		t.Fatalf("conflict exposes a path: %s", response.Body.String())
	}
	if content, err := os.ReadFile(published); err != nil || string(content) != "edited by the user" {
		t.Fatalf("existing lyrics changed: %q, %v", content, err)
	}
	fixture.assertNoStagingLeft(t)
}

func TestLyricsFetchCopiesFromAnotherLocalFolderOfTheFamily(t *testing.T) {
	fixture := newLyricsFetchFixture(t, serveLyricsFetchVTT, lyricsFetchVTTURL)
	translated := filepath.Join(fixture.dataRoot, "RJ00000002", "Translated")
	if err := os.MkdirAll(translated, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"Track1.Translated name.mp3.vtt": lyricsFetchVTT,
		"Track1.Translated name.mp3":     "synthetic audio",
	} {
		if err := os.WriteFile(filepath.Join(translated, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	executeLocalScanForTest(t, fixture.server)
	var sourceFolderID int64
	if err := fixture.db.QueryRow("SELECT id FROM work_folder_location WHERE work_id = 902").Scan(&sourceFolderID); err != nil {
		t.Fatal(err)
	}
	local := map[string]any{"sourceId": 0, "remoteCode": "", "sourceFolderId": sourceFolderID}

	result := fixture.decode(t, fixture.post(t, local))
	if result.Folder != fixture.root+"/lyrics_zh_hans" || result.Language != "zh-cn" || result.Downloaded != 1 || result.Assigned != 1 {
		t.Fatalf("result = %+v", result)
	}
	if fixture.requests.Load() != 0 {
		t.Fatalf("a local copy made %d upstream requests", fixture.requests.Load())
	}
	want := "zh-cn=library_import:" + result.Folder + "/Translated/Track1.Translated name.mp3.vtt"
	if assigned := fixture.assignedLanguages(t); len(assigned) != 1 || assigned[0] != want {
		t.Fatalf("assignments = %v, want [%s]", assigned, want)
	}
	if _, err := os.Stat(filepath.Join(translated, "Track1.Translated name.mp3.vtt")); err != nil {
		t.Fatalf("the source folder lost its lyrics: %v", err)
	}

	for name, body := range map[string]map[string]any{
		"the target folder itself":    {"sourceId": 0, "remoteCode": "", "sourceFolderId": fixture.folderID},
		"an audio file":               {"sourceId": 0, "remoteCode": "", "sourceFolderId": sourceFolderID, "files": []string{"Translated/Track1.Translated name.mp3"}, "assignments": []any{}},
		"a file that is not there":    {"sourceId": 0, "remoteCode": "", "sourceFolderId": sourceFolderID, "files": []string{"Translated/missing.vtt"}, "assignments": []any{}},
		"a remote and a local source": {"sourceFolderId": sourceFolderID},
	} {
		if response := fixture.post(t, body); response.Code < 400 || response.Code >= 500 {
			t.Fatalf("%s: status = %d, body = %s", name, response.Code, response.Body.String())
		}
	}
	fixture.assertNoStagingLeft(t)
}

func TestLyricsLanguageFolderNamesCarryNoWorkCode(t *testing.T) {
	for language, want := range map[string]string{
		"":      "lyrics",
		"ja-jp": "lyrics_ja",
		"en-us": "lyrics_en",
		"zh-cn": "lyrics_zh_hans",
		"zh-tw": "lyrics_zh_hant",
		"ko-kr": "lyrics_ko",
	} {
		got := lyricsLanguageFolderName(language)
		if got != want {
			t.Fatalf("folder for %q = %q, want %q", language, got, want)
		}
		if code, _ := localfs.ExtractWorkCode(got); code != "" {
			t.Fatalf("folder %q reads as work code %q", got, code)
		}
	}
}

func TestLyricsFetchRejectsUnsafeRequestsWithoutWriting(t *testing.T) {
	okHandler := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(lyricsFetchVTT)) }
	sameOrigin := func(upstream string) string { return upstream + "/media/1.vtt" }
	for _, testCase := range []struct {
		name         string
		handler      http.HandlerFunc
		textURL      func(string) string
		body         map[string]any
		permissions  []string
		status       int
		wantRequests int32
	}{
		{name: "edition outside the family", handler: okHandler, textURL: sameOrigin, body: map[string]any{"remoteCode": "RJ00000003"}, status: http.StatusBadRequest},
		{name: "file outside the remote tree", handler: okHandler, textURL: sameOrigin, body: map[string]any{"files": []string{"Translated/other.vtt"}, "assignments": []any{}}, status: http.StatusNotFound},
		{name: "non-lyrics file", handler: okHandler, textURL: sameOrigin, body: map[string]any{"files": []string{"Translated/Track1.Translated name.mp3"}, "assignments": []any{}}, status: http.StatusBadRequest},
		{name: "unconfigured host", handler: okHandler, textURL: func(string) string { return "https://media.invalid/1.vtt" }, status: http.StatusBadGateway},
		{name: "credentials in URL", handler: okHandler, textURL: func(upstream string) string {
			withCredentials, err := url.Parse(upstream + "/media/1.vtt")
			if err != nil {
				t.Fatal(err)
			}
			withCredentials.User = url.UserPassword("synthetic-user", "synthetic-password")
			return withCredentials.String()
		}, status: http.StatusBadGateway},
		{name: "redirect to another origin", handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://media.invalid/1.vtt", http.StatusFound)
		}, textURL: sameOrigin, status: http.StatusBadGateway, wantRequests: 1},
		{name: "oversized lyrics", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxLyricsFetchFileBytes+1)))
		}, textURL: sameOrigin, status: http.StatusBadRequest, wantRequests: 1},
		{name: "upstream error", handler: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "synthetic failure", http.StatusInternalServerError)
		}, textURL: sameOrigin, status: http.StatusBadGateway, wantRequests: 1},
		{name: "assignment without library write", handler: okHandler, textURL: sameOrigin, permissions: []string{"downloads:manage"}, status: http.StatusForbidden},
		{name: "assignment of another work's audio", handler: okHandler, textURL: sameOrigin, body: map[string]any{"assignments": []map[string]any{{"audioMediaItemId": 999999, "path": "Translated/Track1.Translated name.mp3.vtt"}}}, status: http.StatusBadRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newLyricsFetchFixture(t, testCase.handler, testCase.textURL)
			response := fixture.post(t, testCase.body, testCase.permissions...)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, testCase.status, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "127.0.0.1") || strings.Contains(response.Body.String(), "media.invalid") || strings.Contains(response.Body.String(), fixture.dataRoot) {
				t.Fatalf("error exposes a URL or local path: %s", response.Body.String())
			}
			if got := fixture.requests.Load(); got != testCase.wantRequests {
				t.Fatalf("upstream requests = %d, want %d", got, testCase.wantRequests)
			}
			if folders := fixture.lyricsFolders(t); len(folders) != 0 {
				t.Fatalf("a rejected fetch created %v", folders)
			}
			fixture.assertNoStagingLeft(t)
		})
	}
}

func TestLyricsFetchStopsWhenCancelled(t *testing.T) {
	fixture := newLyricsFetchFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(lyricsFetchVTT))
	}, func(upstream string) string { return upstream + "/media/1.vtt" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source, err := fixture.server.currentRemoteCacheSource(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	target := lyricsFetchTarget{WorkID: fixture.workID, RootPath: fixture.root}
	stage := func(ctx context.Context, _ string, destination string) (int64, error) {
		return fixture.server.downloadLyricsFile(ctx, source, "http://127.0.0.1:1/never", destination)
	}
	if _, _, err := fixture.server.stageAndPublishLyrics(ctx, target, "lyrics_cancelled", []string{"Translated/1.vtt"}, stage); err == nil {
		t.Fatal("a cancelled fetch published lyrics")
	}
	if folders := fixture.lyricsFolders(t); len(folders) != 0 {
		t.Fatalf("a cancelled fetch created %v", folders)
	}
	fixture.assertNoStagingLeft(t)
}
