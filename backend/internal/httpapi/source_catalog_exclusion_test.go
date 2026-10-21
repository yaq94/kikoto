package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

func insertCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) (int64, error) {
	return sqlutil.InsertID(ctx, tx, `INSERT INTO source_catalog_exclusion
		(file_source_id,path,mode,action,note,enabled) VALUES(?,?,?,?,'',1)`,
		sourceID, p, catalogExclusionModeWhole, catalogExclusionAction)
}

func seedExclusionSource(t *testing.T, db *sql.DB, sourceID int64, sourceType string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(?,?,?,?)`, sourceID, fmt.Sprintf("exclusion-%d", sourceID), "Exclusion source", sourceType); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCatalogExclusionPathNormalization(t *testing.T) {
	for _, testCase := range []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "empty", in: "", ok: false},
		{name: "root", in: "/", ok: false},
		{name: "relative", in: "A/B", ok: false},
		{name: "parent segment", in: "/A/../B", ok: false},
		{name: "parent segment at end", in: "/A/B/..", ok: false},
		{name: "repeated slashes", in: "//A///B//", want: "/A/B", ok: true},
		{name: "trailing slash", in: "/A/B/", want: "/A/B", ok: true},
		{name: "single dot", in: "/A/./B", want: "/A/B", ok: true},
		{name: "already clean", in: "/A/B", want: "/A/B", ok: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := normalizeCatalogExclusionPath(testCase.in)
			if testCase.ok {
				if err != nil || got != testCase.want {
					t.Fatalf("normalize(%q) = %q, %v; want %q", testCase.in, got, err, testCase.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("normalize(%q) = %q, want error", testCase.in, got)
			}
		})
	}
}

func TestSourceCatalogExclusionEnqueueSkipsSubtree(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(71)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = insertCatalogExclusionTx(context.Background(), tx, sourceID, "/A"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range []string{"/A", "/A/B", "/AB", "/A2"} {
		if _, err = enqueueCatalogDirectoryTx(ctx, tx, sourceID, 9, p, "/A", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, testCase := range []struct {
		path string
		want int
	}{{"/A", 0}, {"/A/B", 0}, {"/AB", 1}, {"/A2", 1}} {
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path=?`, sourceID, testCase.path).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != testCase.want {
			t.Fatalf("directory %q count = %d, want %d", testCase.path, count, testCase.want)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DELETE FROM source_catalog_exclusion WHERE file_source_id=? AND path='/A'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = enqueueCatalogDirectoryTx(ctx, tx, sourceID, 9, "/A/B", "/A", ""); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/A/B'`, sourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("revoked path still refused enqueue: count=%d", count)
	}
}

func TestSourceCatalogExclusionBlocksPublish(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, generation = int64(72), int64(9)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	for _, statement := range []string{
		`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
			VALUES(9,'source_catalog_index','Index source catalog','running','manual')`,
		`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json) VALUES(72,9,'{}')`,
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(72,'/A',9,1,'/A','/A','complete')`,
		`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(72,9,'/A')`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
			VALUES(72,'/A','track.mp3',1,'audio',100,1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	countPublishable := func(t *testing.T) int {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM (`+catalogPublishQuery+`)`, sourceID, generation, catalogPublishRows).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := countPublishable(t); got != 1 {
		t.Fatalf("baseline publishable rows = %d, want 1", got)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = insertCatalogExclusionTx(context.Background(), tx, sourceID, "/A/track.mp3"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countPublishable(t); got != 0 {
		t.Fatalf("publishable rows below an exact exclusion = %d, want 0", got)
	}
	if _, err = db.Exec(`DELETE FROM source_catalog_exclusion WHERE file_source_id=? AND path='/A/track.mp3'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if got := countPublishable(t); got != 1 {
		t.Fatalf("publishable rows after revoke = %d, want 1", got)
	}
	if _, err = db.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path) VALUES(?, '/A')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if got := countPublishable(t); got != 0 {
		t.Fatalf("publishable rows below an ancestor exclusion = %d, want 0", got)
	}
}

func TestSourceCatalogExclusionCleansExistingSubtree(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(73)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	for _, statement := range []string{
		`INSERT INTO user_account(id,username,display_name,role) VALUES(1,'review-user','Review User','user')`,
		`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000001','Excluded work')`,
		`INSERT INTO catalog_series(id,title) VALUES(1,'Excluded series')`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status) VALUES
			(1,73,'/A/B','Entry B',1,'available'),
			(2,73,'/A/B/C','Entry C',1,'available'),
			(3,73,'/A/BC','Entry BC',1,'available'),
			(4,73,'/Outside','Entry outside',1,'available')`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status,relocated_to_id) VALUES
			(5,73,'/Alias','Relocated alias',1,'available',1)`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(1,'track.mp3','Track','audio',100),
			(2,'track.mp3','Track','audio',100),
			(3,'other.mp3','Other','audio',100)`,
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status) VALUES
			(73,'/A',9,1,'/A','/A','complete'),
			(73,'/A/B',9,1,'/A','/A/B','complete'),
			(73,'/A/B/C',9,1,'/A','/A/B','complete'),
			(73,'/A/BC',9,1,'/A','/A/BC','complete')`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending) VALUES
			(73,'/A','B',1,'folder',0,1),
			(73,'/A/B','track.mp3',1,'audio',100,1),
			(73,'/A/BC','other.mp3',1,'audio',100,1)`,
		`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(73,9,'/A/B'),(73,9,'/A/BC')`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual')`,
		`INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,1,5,'keep me')`,
		`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
			VALUES(9,'source_catalog_index','Index source catalog','running','manual')`,
		`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json) VALUES(73,9,'{}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = insertCatalogExclusionTx(context.Background(), tx, sourceID, "/A/B"); err != nil {
		t.Fatal(err)
	}
	if err = applyCatalogExclusionTx(context.Background(), tx, sourceID, "/A/B"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for id, want := range map[int64]string{1: "excluded", 2: "excluded", 3: "available", 4: "available", 5: "available"} {
		var status string
		if err = db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Fatalf("entry %d status = %q, want %q", id, status, want)
		}
	}
	var files, observations, directories, audioRoots, members, personal, works, aliases int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id IN (1,2)`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=3`).Scan(&directories); err != nil {
		t.Fatal(err)
	}
	if files != 0 || directories != 1 {
		t.Fatalf("files below exclusion=%d, boundary file=%d; want 0/1", files, directories)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_observation`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 1 {
		t.Fatalf("observations after cleanup = %d, want only /A/BC", observations)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_directory`).Scan(&directories); err != nil {
		t.Fatal(err)
	}
	if directories != 2 {
		t.Fatalf("directories after cleanup = %d, want /A and /A/BC", directories)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_audio_root`).Scan(&audioRoots); err != nil {
		t.Fatal(err)
	}
	if audioRoots != 1 {
		t.Fatalf("audio roots after cleanup = %d, want /A/BC only", audioRoots)
	}
	if err = db.QueryRow(`SELECT count(*) FROM catalog_series_member`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM user_work_state WHERE user_id=1 AND work_id=1 AND rating=5 AND note='keep me'`).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM work WHERE id=1`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=5 AND relocated_to_id=1`).Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if members != 1 || personal != 1 || works != 1 || aliases != 1 {
		t.Fatalf("retained references: series=%d personal=%d work=%d alias=%d", members, personal, works, aliases)
	}
	var scanDirectories, scanPending, scanObserved, scanRemaining int
	if err = db.QueryRow(`SELECT directories,pending,observed,publish_pending_remaining
		FROM source_catalog_scan WHERE file_source_id=73`).
		Scan(&scanDirectories, &scanPending, &scanObserved, &scanRemaining); err != nil {
		t.Fatal(err)
	}
	if scanDirectories != 2 || scanPending != 0 || scanObserved != 1 || scanRemaining != 1 {
		t.Fatalf("scan projection after cleanup = dirs %d pending %d observed %d remaining %d; want 2/0/1/1",
			scanDirectories, scanPending, scanObserved, scanRemaining)
	}
}

func TestSourceCatalogExclusionRevocationRestoresDiscovery(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(74)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	for _, statement := range []string{
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status,status_checked_at) VALUES
			(1,74,'/A/B','Never checked','excluded',NULL),
			(2,74,'/A/B/C','Previously checked','excluded','2026-01-01 00:00:00'),
			(3,74,'/A/BC','Boundary','available',NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = insertCatalogExclusionTx(context.Background(), tx, sourceID, "/A/B"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM source_catalog_exclusion WHERE file_source_id=? AND path='/A/B'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = revokeCatalogExclusionTx(context.Background(), tx, sourceID, "/A/B"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{1: "available", 2: "unavailable", 3: "available"} {
		var status string
		if err = db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Fatalf("revoked entry %d status = %q, want %q", id, status, want)
		}
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = enqueueCatalogDirectoryTx(context.Background(), tx, sourceID, 9, "/A/B", "/A", ""); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/A/B'`, sourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("next scan cannot rediscover the revoked path: count=%d", count)
	}

	// A narrower revocation must not restore a row still covered by an ancestor.
	if _, err = tx.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path) VALUES(?, '/A')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE source_catalog_entry SET availability_status='excluded' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = revokeCatalogExclusionTx(context.Background(), tx, sourceID, "/A/B"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = tx.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "excluded" {
		t.Fatalf("ancestor exclusion was ignored on revoke: status=%q", status)
	}
}

func exclusionRequest(t *testing.T, handler http.Handler, cookie *http.Cookie, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSourceCatalogExclusionAPI(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(75)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	server := NewServer(db, config.Config{RootUsername: "exclusion-root"})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "exclusion-root", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "exclusion-root", "synthetic-password")

	response := exclusionRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/75/catalog/exclusions", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("initial list = %d %s", response.Code, response.Body.String())
	}
	response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/75/catalog/exclusions", `{"path":"/A//B/","note":" keep "}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", response.Code, response.Body.String())
	}
	var created catalogExclusion
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Path != "/A/B" || created.Note != "keep" || created.ID == 0 {
		t.Fatalf("created exclusion = %+v", created)
	}
	for _, testCase := range []struct {
		body   string
		status int
	}{
		{`{"path":"/A/B"}`, http.StatusConflict},
		{`{"path":"/A/../B"}`, http.StatusBadRequest},
		{`{"path":"/"}`, http.StatusBadRequest},
		{`{"path":"A/B"}`, http.StatusBadRequest},
		{`{"path":"/A/B","mode":"children"}`, http.StatusBadRequest},
	} {
		response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/75/catalog/exclusions", testCase.body)
		if response.Code != testCase.status {
			t.Fatalf("POST %s status = %d, want %d body=%s", testCase.body, response.Code, testCase.status, response.Body.String())
		}
	}
	response = exclusionRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/75/catalog/exclusions", "")
	var listed []catalogExclusion
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list after create = %d %+v", response.Code, listed)
	}
	response = exclusionRequest(t, handler, cookie, http.MethodDelete,
		fmt.Sprintf("/api/remote-sources/75/catalog/exclusions/%d", created.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", response.Code, response.Body.String())
	}
	response = exclusionRequest(t, handler, cookie, http.MethodDelete,
		fmt.Sprintf("/api/remote-sources/75/catalog/exclusions/%d", created.ID), "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", response.Code)
	}
	response = exclusionRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/999/catalog/exclusions", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown source status = %d, want 404", response.Code)
	}
	response = exclusionRequest(t, handler, nil, http.MethodGet, "/api/remote-sources/75/catalog/exclusions", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", response.Code)
	}
	createAccountManagedTestUser(t, server, "exclusion-reader", "reader-password")
	reader := loginTestSession(t, handler, "exclusion-reader", "reader-password")
	response = exclusionRequest(t, handler, reader, http.MethodGet, "/api/remote-sources/75/catalog/exclusions", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("read-only list status = %d, want 403", response.Code)
	}
	response = exclusionRequest(t, handler, reader, http.MethodPost, "/api/remote-sources/75/catalog/exclusions", `{"path":"/ReadOnly"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("read-only create status = %d, want 403", response.Code)
	}
}

func TestSourceCatalogExclusionExpandSkipsChild(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, runID = int64(76), int64(760)
	job := seedCatalogPendingPurgeJob(t, db, sourceID, runID)
	for _, statement := range []string{
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(76,'/A',760,1,'/A','','listing_complete')`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes)
			VALUES(76,'/A','B',1,'folder',0)`,
		`INSERT INTO source_catalog_exclusion(file_source_id,path) VALUES(76,'/A/B')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{db: db}
	advanced, err := server.expandCatalogDirectory(context.Background(), job, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !advanced {
		t.Fatal("expand did not process the listing_complete directory")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/A/B'`, sourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expand enqueued an excluded child: count=%d", count)
	}
	var cursor, status string
	if err = db.QueryRow(`SELECT children_cursor,status FROM source_catalog_directory WHERE file_source_id=? AND path='/A'`, sourceID).Scan(&cursor, &status); err != nil {
		t.Fatal(err)
	}
	if cursor != "B" || status != "complete" {
		t.Fatalf("expand cursor/status = %q/%q, want B/complete", cursor, status)
	}
}

func TestSourceCatalogExclusionSuppressesPendingObservation(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(77)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	for _, statement := range []string{
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(77,'/A',9,1,'/A','/A','complete')`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending) VALUES
			(77,'/A','B',1,'folder',0,1),
			(77,'/A','BC',1,'folder',0,1)`,
		`INSERT INTO source_catalog_exclusion(file_source_id,path) VALUES(77,'/A/B')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = suppressExcludedCatalogObservationsTx(context.Background(), tx, sourceID, "/A", 1); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"B": 0, "BC": 1} {
		var pending int
		if err = tx.QueryRow(`SELECT publish_pending FROM source_catalog_observation
			WHERE file_source_id=? AND directory_path='/A' AND name=? AND revision=1`, sourceID, name).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending != want {
			t.Fatalf("observation %q pending = %d, want %d", name, pending, want)
		}
	}
}
