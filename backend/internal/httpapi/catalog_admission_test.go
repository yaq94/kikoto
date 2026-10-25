package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/catalogadmission"
	"github.com/yexca/kikoto/backend/internal/config"
)

func seedAdmissionJob(t *testing.T, db *sql.DB, sourceID, runID int64) workflowJobRecord {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(?,?,?,'openlist')`, sourceID, fmt.Sprintf("admission-%d", sourceID), "Admission source"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
		VALUES(?,?,'Admission fixture','running','manual')`, runID, sourceCatalogIndexWorkerType); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO workflow_job(workflow_run_id,worker_type,status,locked_by)
		VALUES(?,?,'running','admission-test')`, runID, sourceCatalogIndexWorkerType)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return workflowJobRecord{ID: id, RunID: runID, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "admission-test"}
}

func seedCatalogAdmissionCard(t *testing.T, db *sql.DB, sourceID, runID int64, root string, audio, nonAudio int) workflowJobRecord {
	t.Helper()
	job := seedAdmissionJob(t, db, sourceID, runID)
	// The projection only counts rows the publisher can select: a root with no
	// audio never enters publish_pending_remaining.
	publishable := 0
	if audio > 0 {
		publishable = audio + nonAudio
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,publish_pending_remaining)
		VALUES(?,?,'{}',?)`, sourceID, runID, publishable); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		VALUES(?,?,?,1,?,?,'complete')`, sourceID, root, runID, root, root); err != nil {
		t.Fatal(err)
	}
	if audio > 0 {
		if _, err := db.Exec(`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?,?,?)`, sourceID, runID, root); err != nil {
			t.Fatal(err)
		}
	}
	if audio > 0 {
		if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
			INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
			SELECT ?,?,'a'||i,1,'audio',1,1 FROM n`, audio, sourceID, root); err != nil {
			t.Fatal(err)
		}
	}
	if nonAudio > 0 {
		if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
			INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
			SELECT ?,?,'n'||i,1,'other',1,1 FROM n`, nonAudio, sourceID, root); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

func countCatalogEntries(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_entry`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCatalogAdmissionPublishingPublishesEveryCardWithAudio(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		audio, nonAudio int
		wantEntries     int
	}{
		{name: "no audio", audio: 0, nonAudio: 10, wantEntries: 0},
		{name: "large attachment set", audio: 1, nonAudio: 5000, wantEntries: 0},
		{name: "audio beside many attachments", audio: 4, nonAudio: 2001, wantEntries: 0},
		{name: "five thousand files", audio: 5, nonAudio: 4995, wantEntries: 1},
		{name: "five audio and two thousand non-audio", audio: 5, nonAudio: 2000, wantEntries: 1},
		{name: "normal multi track", audio: 3, nonAudio: 1278, wantEntries: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openMigratedTestDB(t)
			const sourceID, runID = int64(81), int64(810)
			job := seedCatalogAdmissionCard(t, db, sourceID, runID, "/card", testCase.audio, testCase.nonAudio)
			server := &Server{db: db}
			var remaining int
			for attempt := 0; attempt < 200; attempt++ {
				if _, err := server.publishCatalogBatch(context.Background(), job, sourceID); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT publish_pending_remaining FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&remaining); err != nil {
					t.Fatal(err)
				}
				if remaining == 0 {
					break
				}
			}
			if got := countCatalogEntries(t, db); got != testCase.wantEntries {
				t.Fatalf("entries = %d, want %d", got, testCase.wantEntries)
			}
			var pending, observations int
			if err := db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND publish_pending=1`, sourceID).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != 0 {
				t.Fatalf("pending observations = %d, want 0", pending)
			}
			if err := db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=?`, sourceID).Scan(&observations); err != nil {
				t.Fatal(err)
			}
			if observations != testCase.audio+testCase.nonAudio {
				t.Fatalf("observations = %d, want %d (cleanup must keep the row)", observations, testCase.audio+testCase.nonAudio)
			}
			if err := db.QueryRow(`SELECT publish_pending_remaining FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 0 {
				t.Fatalf("publish_pending_remaining = %d, want 0", remaining)
			}
			if testCase.wantEntries == 1 {
				var files, audio int
				if err := db.QueryRow(`SELECT count(*), sum(CASE WHEN f.kind='audio' THEN 1 ELSE 0 END)
					FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id
					WHERE e.file_source_id=?`, sourceID).Scan(&files, &audio); err != nil {
					t.Fatal(err)
				}
				if files != testCase.audio+testCase.nonAudio || audio != testCase.audio {
					t.Fatalf("published files = %d/%d, want %d/%d", files, audio, testCase.audio+testCase.nonAudio, testCase.audio)
				}
			}
		})
	}
}

func TestCatalogAdmissionPublishLayerChecksExistingObservation(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, runID = int64(82), int64(820)
	seedCatalogAdmissionCard(t, db, sourceID, runID, "/card", 1, 10)
	server := &Server{db: db}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<6000)
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		SELECT ?,?,'x'||i,1,'other',1,1 FROM n`, sourceID, "/card"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	blocked, adm, err := server.catalogRootBlockedTx(context.Background(), tx, sourceID, runID, "/card")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked || adm.Reason != catalogadmission.ReasonTooManyFiles {
		t.Fatalf("blocked=%v reason=%q, want the publish layer to refuse oversized files", blocked, adm.Reason)
	}
}

func TestCatalogAdmissionPendingCleanupOnlyClearsTheFlag(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, runID = int64(84), int64(840)
	job := seedCatalogAdmissionCard(t, db, sourceID, runID, "/no-audio", 0, 12)
	server := &Server{db: db}
	before := map[string]string{}
	rows, err := db.Query(`SELECT name,kind||'|'||size_bytes||'|'||revision||'|'||published_generation||'|'||COALESCE(content_hash,'') FROM source_catalog_observation WHERE file_source_id=?`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, value string
		if err = rows.Scan(&name, &value); err != nil {
			t.Fatal(err)
		}
		before[name] = value
	}
	_ = rows.Close()
	if _, err = server.publishCatalogBatch(context.Background(), job, sourceID); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query(`SELECT name,kind||'|'||size_bytes||'|'||revision||'|'||published_generation||'|'||COALESCE(content_hash,'') FROM source_catalog_observation WHERE file_source_id=?`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	after := map[string]string{}
	for rows.Next() {
		var name, value string
		if err = rows.Scan(&name, &value); err != nil {
			t.Fatal(err)
		}
		after[name] = value
	}
	_ = rows.Close()
	if len(after) != len(before) {
		t.Fatalf("observation rows = %d, want %d", len(after), len(before))
	}
	for name, value := range before {
		if after[name] != value {
			t.Fatalf("observation %s changed: %q -> %q", name, value, after[name])
		}
	}
	var pending int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND publish_pending=1`, sourceID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending observations = %d, want 0", pending)
	}
	progress, err := server.sourceCatalogProgress(context.Background(), sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if progress.PublishPending != 0 {
		t.Fatalf("API publishPending = %d, want 0", progress.PublishPending)
	}
}

func publishAdmissionFixtures(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	const sourceID = int64(83)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(83,'admission-api','Admission API','openlist',1)`)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		VALUES(8301,83,'/good','Good card','available'),(8302,83,'/bad','Bad card','available'),
			(8303,83,'/mixed-bad','Mixed bad card','available'),(8304,83,'/mixed-good','Mixed good card','available')`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<3)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8301,'a'||i||'.mp3','a'||i,'audio',1 FROM n`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1278)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8301,'n'||i||'.jpg','n'||i,'other',1 FROM n`)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(8302,'a.mp3','a','audio',1)`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<6000)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8302,'n'||i||'.jpg','n'||i,'other',1 FROM n`)
	// One non-audio row over the inclusive 2,000 bound with only 4 audio
	// rows: the audio floor decides the refusal.
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<4)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8303,'a'||i||'.mp3','a'||i,'audio',1 FROM n`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2001)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8303,'n'||i||'.jpg','n'||i,'other',1 FROM n`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<5)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8304,'a'||i||'.mp3','a'||i,'audio',1 FROM n`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2000)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8304,'n'||i||'.jpg','n'||i,'other',1 FROM n`)
	return sourceID
}

func catalogEntrySet(works []remoteWorkSummary) map[int64]bool {
	set := map[int64]bool{}
	for _, work := range works {
		set[work.CatalogEntryID] = true
	}
	return set
}

func TestCatalogAdmissionAPIShowsEveryCardWithAudio(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := publishAdmissionFixtures(t, db)
	server := NewServer(db, config.Config{})
	source := remoteSourceForUse{ID: sourceID, Enabled: true, SourceType: "openlist"}

	request := httptest.NewRequest(http.MethodGet, "/api/remote-sources/83/works", nil)
	response := httptest.NewRecorder()
	server.listOpenListWorks(response, request, source)
	if response.Code != http.StatusOK {
		t.Fatalf("browse status = %d body=%s", response.Code, response.Body.String())
	}
	var works remoteWorksResponse
	if err := json.Unmarshal(response.Body.Bytes(), &works); err != nil {
		t.Fatal(err)
	}
	if len(works.Works) != 2 {
		t.Fatalf("browse cards = %+v, want the two admissible cards", works.Works)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/remote-sources/83/works?q=card", nil)
	response = httptest.NewRecorder()
	server.listOpenListWorks(response, request, source)
	if err := json.Unmarshal(response.Body.Bytes(), &works); err != nil {
		t.Fatal(err)
	}
	if len(works.Works) != 2 {
		t.Fatalf("search cards = %+v, want the two admissible cards", works.Works)
	}

	for _, entryID := range []string{"8302", "8303"} {
		tracks := httptest.NewRequest(http.MethodGet, "/api/source-catalog/"+entryID+"/tracks", nil)
		tracks.SetPathValue("id", entryID)
		response = httptest.NewRecorder()
		server.getSourceCatalogTracks(response, tracks)
		if response.Code != http.StatusNotFound {
			t.Fatalf("refused tracks status = %d, body=%s", response.Code, response.Body.String())
		}
	}
	tracks := httptest.NewRequest(http.MethodGet, "/api/source-catalog/8304/tracks", nil)
	tracks.SetPathValue("id", "8304")
	response = httptest.NewRecorder()
	server.getSourceCatalogTracks(response, tracks)
	if response.Code != http.StatusOK {
		t.Fatalf("admissible tracks status = %d, body=%s", response.Code, response.Body.String())
	}
}

// Series membership is evaluated per member. An over-limit episode disappears
// from the series card, the series detail and its own tracks route, while the
// qualified sibling stays visible. A series with no qualified member is not
// listed at all.
func TestCatalogAdmissionSeriesFiltersEveryMember(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(85,'admission-series','Admission Series','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) VALUES
			(8501,85,'/series/good-1','Good 1','available'),
			(8502,85,'/series/bad','Bad','available'),
			(8503,85,'/series/good-2','Good 2','available'),
			(8504,85,'/only-bad/bad','Only bad','available');
		INSERT INTO catalog_series(id,title) VALUES(1,'Mixed series'),(2,'Only refused');`)
	metadataReviewExec(t, db, `INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
		(1,8501,1,'manual'),(1,8502,2,'manual'),(1,8503,3,'manual'),(2,8504,1,'manual')`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8501,'a'||i||'.mp3','a'||i,'audio',1 FROM n;
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(8502,'a.mp3','a','audio',1)`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<6000)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8502,'n'||i||'.jpg','n'||i,'other',1 FROM n;
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(8503,'a.mp3','a','audio',1)`)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(8504,'a.mp3','a','audio',1)`)
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<6000)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT 8504,'n'||i||'.jpg','n'||i,'other',1 FROM n`)

	server := NewServer(db, config.Config{})
	source := remoteSourceForUse{ID: 85, Enabled: true, SourceType: "openlist"}
	request := httptest.NewRequest(http.MethodGet, "/api/remote-sources/85/works", nil)
	response := httptest.NewRecorder()
	server.listOpenListWorks(response, request, source)
	var works remoteWorksResponse
	if err := json.Unmarshal(response.Body.Bytes(), &works); err != nil {
		t.Fatal(err)
	}
	if len(works.Works) != 1 || works.Works[0].SeriesID != 1 {
		t.Fatalf("browse cards = %+v, want only the mixed series", works.Works)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/catalog-series/1", nil)
	request.SetPathValue("id", "1")
	response = httptest.NewRecorder()
	server.getCatalogSeries(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("series detail status = %d body=%s", response.Code, response.Body.String())
	}
	var view catalogSeriesView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Episodes) != 2 {
		t.Fatalf("series detail episodes = %+v, want the two qualified members", view.Episodes)
	}
	for _, episode := range view.Episodes {
		if episode.CatalogEntryID == 8502 {
			t.Fatal("over-limit member remained in the series detail")
		}
	}

	tracks := httptest.NewRequest(http.MethodGet, "/api/source-catalog/8502/tracks", nil)
	tracks.SetPathValue("id", "8502")
	response = httptest.NewRecorder()
	server.getSourceCatalogTracks(response, tracks)
	if response.Code != http.StatusNotFound {
		t.Fatalf("over-limit member tracks status = %d", response.Code)
	}
	tracks = httptest.NewRequest(http.MethodGet, "/api/source-catalog/8501/tracks", nil)
	tracks.SetPathValue("id", "8501")
	response = httptest.NewRecorder()
	server.getSourceCatalogTracks(response, tracks)
	if response.Code != http.StatusOK {
		t.Fatalf("qualified member tracks status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/catalog-series/2", nil)
	request.SetPathValue("id", "2")
	response = httptest.NewRecorder()
	server.getCatalogSeries(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("series without a qualified member status = %d body=%s", response.Code, response.Body.String())
	}
}
