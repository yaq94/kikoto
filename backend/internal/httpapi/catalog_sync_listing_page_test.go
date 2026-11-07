package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestCatalogSyncListingCommitsPageBeforeNextRequest(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true, Modified: "v1"})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1, Modified: "v1"})
	var armed atomic.Bool
	var checked atomic.Bool
	var db *sql.DB
	handler := tree.handler(t)
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var input struct {
			Path string `json:"path"`
			Page int    `json:"page"`
		}
		_ = json.Unmarshal(body, &input)
		if armed.Load() && r.URL.Path == "/api/fs/list" && input.Path == "/Example Work" && input.Page == 2 {
			checked.Store(true)
			var count, next int
			err = db.QueryRow(`SELECT count(*), (SELECT next_page FROM source_catalog_listing WHERE directory_path='/Example Work')
				FROM source_catalog_listing_item WHERE directory_path='/Example Work'`).Scan(&count, &next)
			if err != nil || count != 2 || next != 2 {
				t.Errorf("first page not durable before second request: count=%d next=%d err=%v", count, next, err)
			}
			var files int
			if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE relative_path='old.mp3'`).Scan(&files); err != nil || files != 1 {
				t.Errorf("old publication lost during listing: files=%d err=%v", files, err)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	db = f.server.db
	f.runFullScan(t, `{"intervalMs":100,"concurrency":1,"pageSize":2}`)
	tree.setModified("/", "Example Work", "v2")
	tree.set("/Example Work", catalogSyncTestEntry{Name: "01.mp3", Size: 1}, catalogSyncTestEntry{Name: "02.mp3", Size: 2}, catalogSyncTestEntry{Name: "03.mp3", Size: 3})
	armed.Store(true)
	f.runCatalogSync(t, false)
	armed.Store(false)
	if !checked.Load() {
		t.Fatal("second page was not requested")
	}
}

func TestCatalogSyncListingRejectsAmbiguousPagesAndKeepsPublishedFiles(t *testing.T) {
	for _, failure := range []string{"duplicate", "total_changed", "early_empty", "unknown_total", "partial", "empty"} {
		t.Run(failure, func(t *testing.T) {
			tree := newCatalogSyncTestTree()
			tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true, Modified: "v1"})
			tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
			var armed atomic.Bool
			handler := tree.handler(t)
			f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				var input struct {
					Path string `json:"path"`
					Page int    `json:"page"`
				}
				_ = json.Unmarshal(body, &input)
				if armed.Load() && r.URL.Path == "/api/fs/list" && input.Path == "/Example Work" {
					if failure == "empty" {
						fmt.Fprint(w, `{"code":200,"data":{"content":[],"total":0}}`)
						return
					}
					if failure == "unknown_total" {
						fmt.Fprint(w, `{"code":200,"data":{"content":[]}}`)
						return
					}
					if input.Page == 2 {
						switch failure {
						case "duplicate":
							fmt.Fprint(w, `{"code":200,"data":{"content":[{"name":"01.mp3","size":1}],"total":3}}`)
						case "total_changed":
							fmt.Fprint(w, `{"code":200,"data":{"content":[{"name":"03.mp3","size":1}],"total":4}}`)
						case "early_empty":
							fmt.Fprint(w, `{"code":200,"data":{"content":[],"total":3}}`)
						case "partial":
							http.Error(w, "synthetic failure", 500)
						}
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			f.runFullScan(t, `{"intervalMs":100,"concurrency":1,"pageSize":2}`)
			var id int64
			if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			tree.setModified("/", "Example Work", "v2")
			tree.set("/Example Work", catalogSyncTestEntry{Name: "01.mp3", Size: 1}, catalogSyncTestEntry{Name: "02.mp3", Size: 2}, catalogSyncTestEntry{Name: "03.mp3", Size: 3})
			armed.Store(true)
			run := f.runCatalogSync(t, false)
			armed.Store(false)
			if failure != "empty" && run.Status != "partial" {
				t.Fatalf("invalid listing status=%s", run.Status)
			}
			files, err := f.server.loadSourceCatalogFiles(context.Background(), id)
			if failure == "empty" {
				if err != nil || len(files) != 0 {
					t.Fatalf("successful empty listing retained files: %+v %v", files, err)
				}
				var retained int
				if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=? AND availability_status='unconfirmed_empty'`, id).Scan(&retained); err != nil || retained != 1 {
					t.Fatalf("empty listing lost the stable evaluation object: %d %v", retained, err)
				}
			} else if err != nil || len(files) != 1 || files[0].Path != "old.mp3" {
				t.Fatalf("ambiguous listing changed publication: %+v %v", files, err)
			}
		})
	}
}

func TestCatalogSyncListingRestartsFromCommittedPage(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true, Modified: "v1"})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	var armed atomic.Bool
	var pageOne atomic.Int64
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true))
	defer cancel()
	handler := tree.handler(t)
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var input struct {
			Path string `json:"path"`
			Page int    `json:"page"`
		}
		_ = json.Unmarshal(body, &input)
		if armed.Load() && r.URL.Path == "/api/fs/list" && input.Path == "/Example Work" {
			if input.Page == 1 {
				pageOne.Add(1)
			}
			if input.Page == 2 {
				cancel()
			}
		}
		handler.ServeHTTP(w, r)
	}))
	f.runFullScan(t, `{"intervalMs":100,"concurrency":1,"pageSize":2}`)
	sourceID := f.sourceID(t)
	var id int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tree.setModified("/", "Example Work", "v2")
	tree.set("/Example Work", catalogSyncTestEntry{Name: "01.mp3", Size: 1}, catalogSyncTestEntry{Name: "02.mp3", Size: 2}, catalogSyncTestEntry{Name: "03.mp3", Size: 3})
	armed.Store(true)
	runID, err := f.server.runCatalogIncrementalSync(ctx, sourceID, false)
	armed.Store(false)
	if err == nil {
		t.Fatal("cancelled second page succeeded")
	}
	var next, received int
	if err = f.server.db.QueryRow(`SELECT next_page,received FROM source_catalog_listing WHERE directory_path='/Example Work' ORDER BY id DESC LIMIT 1`).Scan(&next, &received); err != nil {
		t.Fatal(err)
	}
	if next != 2 || received != 2 {
		t.Fatalf("checkpoint=%d/%d", next, received)
	}
	files, err := f.server.loadSourceCatalogFiles(context.Background(), id)
	if err != nil || len(files) != 1 || files[0].Path != "old.mp3" {
		t.Fatalf("cancel changed publication: %+v %v", files, err)
	}
	var seq int
	var name, dbPath string
	if err = f.server.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	cfg := f.server.cfg
	if err = f.server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = f.server.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewServer(db, cfg)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	tree.resetHits()
	f.server, f.handler = s, s.Routes()
	now := s.catalogTime().Add(2 * time.Hour)
	s.catalogNow = func() time.Time { return now }
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix() - 60, Weekly: now.Unix() - 60, ManualPending: true})
	dispatchCatalog(t, f)
	settled := waitCatalogSyncSettled(t, f)
	var resumedID int64
	var resumedStatus string
	if err = s.db.QueryRow(`SELECT id,status FROM source_catalog_sync_run WHERE file_source_id=? ORDER BY id DESC LIMIT 1`, sourceID).Scan(&resumedID, &resumedStatus); err != nil {
		t.Fatal(err)
	}
	if resumedID != runID || resumedStatus != "succeeded" {
		t.Fatalf("scheduler did not resume checkpoint: id=%d status=%s progress=%#v", resumedID, resumedStatus, settled)
	}
	if got := tree.listCount("/Example Work"); got != 1 {
		t.Fatalf("resume requests=%d, want only page2", got)
	}
	files, err = s.loadSourceCatalogFiles(context.Background(), id)
	if err != nil || len(files) != 3 {
		t.Fatalf("resumed set: %+v %v", files, err)
	}
}

func TestCatalogSyncRemovedFolderCommitsDeletionCursorWithFiles(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Example Work", "")
	metadataReviewExec(t, db, `UPDATE source_catalog_entry SET root_path='/Example Work' WHERE id=1`)
	for i := 0; i < 70; i++ {
		insertCatalogPlanAudio(t, db, 1, fmt.Sprintf("gone/%03d.mp3", i), "Example Track")
	}
	metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path) VALUES(7,'/Example Work',1,'/'); INSERT INTO source_catalog_listing(id,file_source_id,directory_path,generation,base_revision,observed_version,options_json,state) VALUES(1,7,'/Example Work',1,1,'v2','{}','applying'); INSERT INTO source_catalog_observation(file_source_id,directory_path,revision,name,kind,size_bytes) VALUES(7,'/Example Work',1,'gone','folder',0); CREATE TRIGGER synthetic_delete_failure BEFORE DELETE ON source_catalog_file WHEN OLD.relative_path='gone/064.mp3' BEGIN SELECT RAISE(ABORT,'synthetic deletion failure'); END`)
	s := &Server{db: db, catalogDiskSpace: func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{Available: 1 << 40}, nil }}
	ctx, _, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:folder-delete")
	if err != nil {
		t.Fatal(err)
	}
	dir := catalogSyncDirectoryState{Path: "/Example Work", Revision: 1}
	err = s.removeCatalogSyncObservation(ctx, 7, 1, dir, catalogSyncObservation{Name: "gone", Kind: "folder"}, defaultCatalogIndexOptions())
	if err == nil {
		t.Fatal("faulted deletion succeeded")
	}
	var cursor int64
	if err = db.QueryRow(`SELECT COALESCE(json_extract(remove_cursor,'$.fileAfter'),0) FROM source_catalog_listing WHERE id=1`).Scan(&cursor); err != nil || cursor == 0 {
		t.Fatalf("committed deletion cursor=%d %v", cursor, err)
	}
	files, err := s.loadSourceCatalogFiles(context.Background(), 1)
	if err != nil || len(files) != 70 {
		t.Fatalf("partial deletion exposed: %d %v", len(files), err)
	}
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_delete_failure`)
	if err = s.removeCatalogSyncObservation(ctx, 7, 1, dir, catalogSyncObservation{Name: "gone", Kind: "folder"}, defaultCatalogIndexOptions()); err != nil {
		t.Fatal(err)
	}
	var live int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("resumed deletion count=%d %v", live, err)
	}
	files, err = s.loadSourceCatalogFiles(context.Background(), 1)
	if err != nil || len(files) != 70 {
		t.Fatalf("uncommitted deletion exposed: %d %v", len(files), err)
	}
}

func TestCatalogSyncLowSpaceAfterWorkingBatchKeepsPublishedSnapshot(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true, Modified: "v1"})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	var id int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tree.setModified("/", "Example Work", "v2")
	tree.set("/Example Work", catalogSyncTestEntry{Name: "new.mp3", Size: 2})
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		var staged bool
		err := f.server.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM source_catalog_file WHERE relative_path='new.mp3')`).Scan(&staged)
		if staged {
			return diskSpaceSnapshot{}, err
		}
		return diskSpaceSnapshot{Available: 1 << 40}, err
	}
	run := f.runCatalogSync(t, false)
	if run.Status != "partial" {
		t.Fatalf("low-space publication status=%s", run.Status)
	}
	files, err := f.server.loadSourceCatalogFiles(context.Background(), id)
	if err != nil || len(files) != 1 || files[0].Path != "old.mp3" {
		t.Fatalf("low-space publication exposed %+v %v", files, err)
	}
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{Available: 1 << 40}, nil }
	resumed := f.runCatalogSync(t, false)
	if resumed.ID != run.ID || resumed.Status != "succeeded" {
		t.Fatalf("low-space recovery=%+v", resumed)
	}
	files, err = f.server.loadSourceCatalogFiles(context.Background(), id)
	if err != nil || len(files) != 1 || files[0].Path != "new.mp3" {
		t.Fatalf("recovered publication %+v %v", files, err)
	}
}
