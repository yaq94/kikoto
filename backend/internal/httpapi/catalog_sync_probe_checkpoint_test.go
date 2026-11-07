package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yexca/kikoto/backend/internal/storage"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestCatalogSyncProbeProgressIsDurable(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work", catalogSyncTestEntry{Name: "track.mp3", Size: 1, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	run := f.runCatalogSync(t, false)
	var after string
	err := f.server.db.QueryRowContext(context.Background(), `SELECT COALESCE(json_extract(checkpoint_json,'$.probeAfter'),'') FROM workflow_job WHERE worker_type='source_catalog_sync' AND json_extract(payload_json,'$.syncRunId')=? ORDER BY id DESC LIMIT 1`, run.ID).Scan(&after)
	if err != nil {
		t.Fatal(err)
	}
	if after != "/Partition A" {
		t.Fatalf("durable probe cursor=%q, want /Partition A", after)
	}
}

func TestCatalogSyncProbeFindsNewPathBeforeSavedCursor(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Z", IsDir: true, Modified: "p1"})
	tree.set("/Z", catalogSyncTestEntry{Name: "Work", IsDir: true, Modified: "w1"})
	tree.set("/Z/Work", catalogSyncTestEntry{Name: "track.mp3", Size: 1, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	run := f.runCatalogSync(t, false)
	sourceID := f.sourceID(t)
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	// This folder becomes probe-eligible after the saved cursor. Its root
	// observation already exists, so the root listing cannot substitute for the
	// non-leaf probe that finds its new Work child.
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,status,sync_state,observed_version)
		VALUES(?,'/A',?,'/A','complete','unchanged','p1');
		INSERT INTO source_catalog_observation(file_source_id,directory_path,revision,name,kind,size_bytes)
		SELECT file_source_id,'/A',revision,'Work','folder',0 FROM source_catalog_directory WHERE file_source_id=? AND path='/A';
		INSERT INTO source_catalog_observation(file_source_id,directory_path,revision,name,kind,size_bytes,modified_at)
		SELECT file_source_id,'/',revision,'A','folder',0,'p1' FROM source_catalog_directory WHERE file_source_id=? AND path='/'`, sourceID, generation, sourceID, sourceID)
	tree.set("/", catalogSyncTestEntry{Name: "A", IsDir: true, Modified: "p1"}, catalogSyncTestEntry{Name: "Z", IsDir: true, Modified: "p1"})
	tree.set("/A", catalogSyncTestEntry{Name: "Work", IsDir: true, Modified: "w1"})
	tree.set("/A/Work", catalogSyncTestEntry{Name: "track.mp3", Size: 1, Modified: "f1"})
	tree.resetHits()
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	if err := f.server.executeCatalogIncrementalSync(ctx, run.ID, sourceID); err != nil {
		t.Fatal(err)
	}
	tree.mu.Lock()
	a, z := tree.dirsHits["/A"], tree.dirsHits["/Z"]
	tree.mu.Unlock()
	if a != 1 || z != 0 {
		t.Fatalf("wrapped probes A=%d Z=%d, want1/0", a, z)
	}
	if tree.listCount("/A/Work") != 1 {
		t.Fatal("new smaller branch did not publish its full listing")
	}
}

func TestCatalogSyncProbeCheckpointSurvivesFailureCancellationAndRestart(t *testing.T) {
	for _, interrupted := range []string{"transaction_failure", "cancelled"} {
		t.Run(interrupted, func(t *testing.T) {
			tree := newCatalogSyncTestTree()
			parents := []catalogSyncTestEntry{}
			for i := 0; i < 40; i++ {
				name := fmt.Sprintf("P%02d", i)
				parents = append(parents, catalogSyncTestEntry{Name: name, IsDir: true, Modified: "p1"})
				tree.set("/"+name, catalogSyncTestEntry{Name: "Work", IsDir: true, Modified: "w1"})
				tree.set("/"+name+"/Work", catalogSyncTestEntry{Name: "track.mp3", Size: 1, Modified: "f1"})
			}
			tree.set("/", parents...)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true))
			defer cancel()
			var armed atomic.Bool
			handler := tree.handler(t)
			f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if armed.Load() && interrupted == "cancelled" && r.URL.Path == "/api/fs/dirs" {
					var input struct {
						Path string `json:"path"`
					}
					// Clone the bounded fixture request body for the normal handler.
					body, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						t.Error(readErr)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					_ = json.Unmarshal(body, &input)
					if input.Path == "/P31" {
						cancel()
					}
				}
				handler.ServeHTTP(w, r)
			}))
			f.runFullScan(t, "")
			sourceID := f.sourceID(t)
			if interrupted == "transaction_failure" {
				metadataReviewExec(t, f.server.db, `CREATE TRIGGER fail_probe_checkpoint BEFORE UPDATE OF last_verified_run ON source_catalog_directory WHEN NEW.path='/P31' BEGIN SELECT RAISE(ABORT,'synthetic probe commit failure'); END`)
			}
			armed.Store(true)
			runID, err := f.server.runCatalogIncrementalSync(ctx, sourceID, false)
			if err == nil {
				t.Fatal("interrupted run succeeded")
			}
			if interrupted == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error: %v", err)
			}
			armed.Store(false)
			var generation int64
			if err = f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			after, err := f.server.loadCatalogSyncProbeAfter(context.Background(), runID, generation)
			if err != nil || after != "/P30" {
				t.Fatalf("checkpoint=%q error=%v, want /P30", after, err)
			}
			var verified int
			if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND last_verified_run=?`, sourceID, runID).Scan(&verified); err != nil {
				t.Fatal(err)
			}
			if verified != 32 {
				t.Fatalf("committed probes=%d, want32", verified)
			}
			if interrupted == "transaction_failure" {
				metadataReviewExec(t, f.server.db, `DROP TRIGGER fail_probe_checkpoint`)
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
			if err = s.executeCatalogIncrementalSync(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true), runID, sourceID); err != nil {
				t.Fatal(err)
			}
			tree.mu.Lock()
			for i := 0; i < 40; i++ {
				path := fmt.Sprintf("/P%02d", i)
				want := 1
				if i < 31 {
					want = 0
				}
				if tree.dirsHits[path] != want {
					t.Errorf("resumed %s requests=%d, want%d", path, tree.dirsHits[path], want)
				}
			}
			tree.mu.Unlock()
		})
	}
}
