package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// A parent card can span several directories. Excluding one child directory
// must remove the files that live there, even though the card's own root is
// above the excluded path.
func TestSourceCatalogExclusionRemovesFilesOfAParentCard(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(84)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(?, 'parent_source', 'Parent Source', 'openlist')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(840,?,'/A','Parent Card')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
		(840,'Keep/01.mp3','Keep','audio',16),(840,'Drop/02.mp3','Drop','audio',16)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path,mode) VALUES(?, '/A/Drop', 'whole')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = applyCatalogExclusionTx(context.Background(), tx, sourceID, "/A/Drop"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var kept, dropped int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=840 AND relative_path='Keep/01.mp3'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=840 AND relative_path='Drop/02.mp3'`).Scan(&dropped); err != nil {
		t.Fatal(err)
	}
	if kept != 1 || dropped != 0 {
		t.Fatalf("parent card files kept=%d dropped=%d, want 1/0", kept, dropped)
	}
}

// Adding a whitelist after a whole rule deleted its rows must schedule a
// re-list of the bridge directory, otherwise the exception stays empty until
// the next day.
func TestSourceCatalogWhitelistCreationSchedulesABridgeRelist(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(85)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status,sync_state,last_verified_run)
		VALUES(?,'/A/Drop',850,1,'/A','/A/Drop','complete','listed',0)`, sourceID); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{RootUsername: "whitelist-root"})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "whitelist-root", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "whitelist-root", "synthetic-password")
	if res := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/85/catalog/exclusions", `{"path":"/A","mode":"whole"}`); res.Code != http.StatusCreated {
		t.Fatalf("whole rule = %d %s", res.Code, res.Body.String())
	}
	if res := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/85/catalog/exclusions", `{"path":"/A/Drop","mode":"whitelist"}`); res.Code != http.StatusCreated {
		t.Fatalf("whitelist rule = %d %s", res.Code, res.Body.String())
	}
	var state string
	if err := db.QueryRow(`SELECT sync_state FROM source_catalog_directory WHERE file_source_id=? AND path='/A/Drop'`, sourceID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The whole rule removed the row, which is also a valid starting
			// point: the next sync re-lists from the bridge.
			return
		}
		t.Fatal(err)
	}
	if state != "changed" {
		t.Fatalf("whitelist bridge state = %q, want changed", state)
	}
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

// A whitelist is per exclusion rule. A shallower exception releases only the
// rows it covers, and a deeper whole rule is still an exclusion because the
// exception is not at least as specific as that rule.
func TestSourceCatalogExclusionWhitelistNestedRules(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(81)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
		(811,?, '/A/Parent','Parent'),
		(812,?, '/A/Keep','Keep'),
		(813,?, '/A/Keep/Deep','Deep'),
		(814,?, '/A/Drop','Drop'),
		(815,?, '/A/Other','Other'),
		(816,?, '/B','Outside'),
		(817,?, '/A/Keep/Deep/Inner','Nested exception')`, sourceID, sourceID, sourceID, sourceID, sourceID, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT id, 'track.mp3', 'Track', 'audio', 100 FROM source_catalog_entry WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := context.Background()
	if _, err = tx.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path,mode) VALUES
		(?, '/A', 'whole'),
		(?, '/A/Keep', 'whitelist'),
		(?, '/A/Keep/Deep', 'whole'),
		(?, '/A/Keep/Deep/Inner', 'whitelist')`, sourceID, sourceID, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/A", "/A/Keep/Deep"} {
		if err = applyCatalogExclusionTx(ctx, tx, sourceID, p); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{
		811: "excluded", 812: "available", 813: "excluded", 814: "excluded", 815: "excluded", 816: "available",
		817: "available",
	} {
		var status string
		if err = db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Fatalf("entry %d status = %q, want %q", id, status, want)
		}
	}
	var kept, dropped int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=812`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id IN (811,813,814,815)`).Scan(&dropped); err != nil {
		t.Fatal(err)
	}
	var nested int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=817`).Scan(&nested); err != nil {
		t.Fatal(err)
	}
	if kept != 1 || dropped != 0 || nested != 1 {
		t.Fatalf("files kept=%d excluded=%d nested=%d, want 1/0/1", kept, dropped, nested)
	}
}

// Removing a whitelist re-excludes the subtree under the whole rule. Removing
// the whole rule afterwards restores the entry because nothing covers it.
func TestSourceCatalogExclusionWhitelistRemovalReexcludes(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(82)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
		(821,?, '/A','Parent'),(822,?, '/A/Keep','Keep')`, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
		(821,'drop.mp3','Drop','audio',1),(822,'keep.mp3','Keep','audio',1)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path,mode) VALUES(?, '/A', 'whole')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = applyCatalogExclusionTx(ctx, tx, sourceID, "/A"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path,mode) VALUES(?, '/A/Keep', 'whitelist')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = revokeCatalogExclusionTx(ctx, tx, sourceID, "/A/Keep"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=822`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "available" {
		t.Fatalf("whitelisted entry = %q, want available", status)
	}

	// Deleting the whitelist hides the subtree again.
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM source_catalog_exclusion WHERE file_source_id=? AND mode='whitelist'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = applyCatalogExclusionTx(ctx, tx, sourceID, "/A/Keep"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=822`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "excluded" {
		t.Fatalf("entry after whitelist removal = %q, want excluded", status)
	}
	var files int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=822`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("files after whitelist removal = %d, want 0", files)
	}

	// Deleting the whole rule restores both cards.
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM source_catalog_exclusion WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = revokeCatalogExclusionTx(ctx, tx, sourceID, "/A"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{821, 822} {
		if err = db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "available" {
			t.Fatalf("entry %d after whole-rule removal = %q, want available", id, status)
		}
	}
}

// A whitelisted card keeps everything playback needs: its 24 files, its
// directory rows and its audio root. The excluded sibling still disappears.
func TestSourceCatalogExclusionWhitelistKeepsFilesTracksAndRange(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, generation = int64(83), int64(830)
	var remote *httptest.Server
	remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/get" {
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Path != "/A/Keep/07.mp3" {
				t.Errorf("wrong real path: %s", input.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"name": "07.mp3", "size": 16, "raw_url": remote.URL + "/audio"},
			})
			return
		}
		http.ServeContent(w, r, "07.mp3", time.Unix(0, 0), bytes.NewReader([]byte("0123456789abcdef")))
	}))
	defer remote.Close()

	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO file_source(id,code,display_name,source_type) VALUES(?, 'keep_source', 'Keep Source', 'openlist')`, []any{sourceID}},
		{`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(?, ?, ?)`, []any{sourceID, remote.URL, remote.URL}},
		{`INSERT INTO work(id,primary_code,title) VALUES(830,'RJ00000083','Keep Work')`, nil},
		{`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(830,?, '/A/Keep','Keep Card',830)`, []any{sourceID}},
		{`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(831,?, '/A/Drop','Drop Card')`, []any{sourceID}},
		{`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status) VALUES
			(?, '/A', 830, 1, '/A', '', 'complete'),
			(?, '/A/Keep', 830, 1, '/A', '/A/Keep', 'complete'),
			(?, '/A/Drop', 830, 1, '/A', '/A/Drop', 'complete')`, []any{sourceID, sourceID, sourceID}},
		{`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?, 830, '/A/Keep')`, []any{sourceID}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.query, err)
		}
	}
	for index := 1; index <= 24; index++ {
		name := fmt.Sprintf("%02d.mp3", index)
		if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(830,?,?,'audio',16)`, name, name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
			VALUES(?,'/A/Keep',?,1,'audio',16,1)`, sourceID, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(831,'drop.mp3','Drop','audio',16);
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		VALUES(?, '/A/Drop','drop.mp3',1,'audio',16,1);`, sourceID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path,mode) VALUES(?, '/A', 'whole'), (?, '/A/Keep', 'whitelist')`, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = applyCatalogExclusionTx(context.Background(), tx, sourceID, "/A"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var keptFiles, keptDirs, keptRoots, keptObservations int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=830`).Scan(&keptFiles); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/A/Keep'`, sourceID).Scan(&keptDirs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_audio_root WHERE file_source_id=? AND catalog_root='/A/Keep'`, sourceID).Scan(&keptRoots); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND directory_path='/A/Keep'`, sourceID).Scan(&keptObservations); err != nil {
		t.Fatal(err)
	}
	if keptFiles != 24 || keptDirs != 1 || keptRoots != 1 || keptObservations != 24 {
		t.Fatalf("whitelisted rows files=%d dirs=%d roots=%d observations=%d, want 24/1/1/24",
			keptFiles, keptDirs, keptRoots, keptObservations)
	}

	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "whitelist-root", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "whitelist-root", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "whitelist-root", "synthetic-password")

	tracks := reviewRequest(t, handler, cookie, http.MethodGet, "/api/source-catalog/830/tracks", "", http.StatusOK)["tracks"].([]any)
	if len(tracks) != 24 {
		t.Fatalf("whitelisted card tracks = %d, want 24", len(tracks))
	}
	target := reviewRequest(t, handler, cookie, http.MethodPost, "/api/source-catalog/830/playback", `{"paths":["07.mp3"]}`, http.StatusOK)["targets"].([]any)[0].(map[string]any)
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/media/%.0f/stream", target["locationId"]), nil)
	request.AddCookie(cookie)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "2345" || response.Header().Get("Content-Range") != "bytes 2-5/16" {
		t.Fatalf("range: %d %q %s", response.Code, response.Body.String(), response.Header().Get("Content-Range"))
	}

	list := reviewRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/remote-sources/%d/works", sourceID), "", http.StatusOK)
	works := list["works"].([]any)
	if list["total"] != float64(1) || len(works) != 1 || works[0].(map[string]any)["catalogEntryId"] != float64(830) {
		t.Fatalf("excluded sibling still listed: %#v", list)
	}
}

// A split card stores its real directory in source_root while root_path keeps
// the group suffix. Excluding the directory must remove the split card's files
// and hide the card, and personal records must survive.
func TestSourceCatalogExclusionSplitCardUsesSourceRoot(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(84)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	if _, err := db.Exec(`INSERT INTO user_account(id,username,display_name,role) VALUES(1,'split-user','Split User','user');
		INSERT INTO work(id,primary_code,title) VALUES(840,'RJ00000084','Split Work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,work_id) VALUES
			(841,?, '/Creator::RJ00000084','/Creator','Split card',840);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(841,'a.mp3','A','audio',10),(841,'b.mp3','B','audio',10);
		INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,840,5,'keep me');`, sourceID); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "split-root", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "split-root", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "split-root", "synthetic-password")
	before := reviewRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/remote-sources/%d/works", sourceID), "", http.StatusOK)
	if before["total"] != float64(1) {
		t.Fatalf("split card not visible before exclusion: %#v", before)
	}
	response := exclusionRequest(t, handler, cookie, http.MethodPost,
		fmt.Sprintf("/api/remote-sources/%d/catalog/exclusions", sourceID), `{"path":"/Creator","mode":"whole"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create exclusion = %d %s", response.Code, response.Body.String())
	}
	var files int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=841`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("split card files = %d, want 0", files)
	}
	after := reviewRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/remote-sources/%d/works", sourceID), "", http.StatusOK)
	if after["total"] != float64(0) || len(after["works"].([]any)) != 0 {
		t.Fatalf("split card still visible: %#v", after)
	}
	var personal int
	if err := db.QueryRow(`SELECT count(*) FROM user_work_state WHERE work_id=840 AND rating=5 AND note='keep me'`).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if personal != 1 {
		t.Fatal("personal record was removed with the split card")
	}
}

// Creating a whitelist returns mode and applies the exception; deleting it
// re-applies the whole rule. An unknown mode is rejected.
func TestSourceCatalogExclusionAPIMode(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(85)
	seedExclusionSource(t, db, sourceID, sourceTypeOpenList)
	server := NewServer(db, config.Config{RootUsername: "mode-root", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "mode-root", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "mode-root", "synthetic-password")
	base := fmt.Sprintf("/api/remote-sources/%d/catalog/exclusions", sourceID)

	response := exclusionRequest(t, handler, cookie, http.MethodPost, base, `{"path":"/A","mode":"whole"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create whole = %d %s", response.Code, response.Body.String())
	}
	var whole catalogExclusion
	if err := json.Unmarshal(response.Body.Bytes(), &whole); err != nil {
		t.Fatal(err)
	}
	if whole.Mode != catalogExclusionModeWhole {
		t.Fatalf("whole mode = %q", whole.Mode)
	}
	response = exclusionRequest(t, handler, cookie, http.MethodPost, base, `{"path":"/A/Keep","mode":"whitelist","note":"audio"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create whitelist = %d %s", response.Code, response.Body.String())
	}
	var whitelist catalogExclusion
	if err := json.Unmarshal(response.Body.Bytes(), &whitelist); err != nil {
		t.Fatal(err)
	}
	if whitelist.Mode != catalogExclusionModeWhitelist || whitelist.Path != "/A/Keep" {
		t.Fatalf("whitelist = %+v", whitelist)
	}
	response = exclusionRequest(t, handler, cookie, http.MethodPost, base, `{"path":"/A/Keep","mode":"subtree"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode = %d %s", response.Code, response.Body.String())
	}
	response = exclusionRequest(t, handler, cookie, http.MethodGet, base, "")
	var listed []catalogExclusion
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(listed) != 2 {
		t.Fatalf("list = %d %+v", response.Code, listed)
	}
	modes := map[string]string{}
	for _, item := range listed {
		modes[item.Path] = item.Mode
	}
	if modes["/A"] != catalogExclusionModeWhole || modes["/A/Keep"] != catalogExclusionModeWhitelist {
		t.Fatalf("listed modes = %#v", modes)
	}
	response = exclusionRequest(t, handler, cookie, http.MethodDelete,
		fmt.Sprintf("%s/%d", base, whitelist.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete whitelist = %d %s", response.Code, response.Body.String())
	}
	response = exclusionRequest(t, handler, cookie, http.MethodGet, base, "")
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Path != "/A" {
		t.Fatalf("after whitelist delete = %+v", listed)
	}
}

// Incremental sync must not recreate the excluded subtree, but it must still
// discover the whitelisted child inside it: the ancestor is listed only as a
// bridge, and the excluded sibling receives no request.
func TestCatalogSyncReachesWhitelistInsideExcludedSubtree(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A",
		catalogSyncTestEntry{Name: "Keep", IsDir: true, Modified: "k1"},
		catalogSyncTestEntry{Name: "Drop", IsDir: true, Modified: "d1"})
	tree.set("/Partition A/Keep", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "kf"})
	tree.set("/Partition A/Drop", catalogSyncTestEntry{Name: "02.mp3", Size: 100, Modified: "df"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	sourceID := f.sourceID(t)
	tx, err := f.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path,mode) VALUES(?, '/Partition A', 'whole'), (?, '/Partition A/Keep', 'whitelist')`, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = applyCatalogExclusionTx(context.Background(), tx, sourceID, "/Partition A"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Force the next sync to re-discover the whitelisted child from its parent.
	if _, err = f.server.db.Exec(`DELETE FROM source_catalog_directory WHERE file_source_id=? AND path IN ('/Partition A/Keep','/Partition A/Drop')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.db.Exec(`UPDATE source_catalog_directory SET observed_version='', last_verified_run=0 WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	tree.resetHits()
	state := f.runCatalogSync(t, false)
	if state.Status != "succeeded" {
		t.Fatalf("sync = %#v", state)
	}
	if hits := tree.listCount("/Partition A/Drop"); hits != 0 {
		t.Fatalf("excluded sibling listed %d times", hits)
	}
	var keepDirs, dropDirs int
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/Partition A/Keep'`, sourceID).Scan(&keepDirs); err != nil {
		t.Fatal(err)
	}
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/Partition A/Drop'`, sourceID).Scan(&dropDirs); err != nil {
		t.Fatal(err)
	}
	if keepDirs != 1 || dropDirs != 0 {
		t.Fatalf("directories keep=%d drop=%d, want 1/0", keepDirs, dropDirs)
	}
	var keepFiles, dropFiles int
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id WHERE e.file_source_id=? AND e.root_path='/Partition A/Keep'`, sourceID).Scan(&keepFiles); err != nil {
		t.Fatal(err)
	}
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id WHERE e.file_source_id=? AND e.root_path='/Partition A/Drop'`, sourceID).Scan(&dropFiles); err != nil {
		t.Fatal(err)
	}
	if keepFiles != 1 || dropFiles != 0 {
		t.Fatalf("files keep=%d drop=%d, want 1/0", keepFiles, dropFiles)
	}
}
