package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestCatalogFinitePartitionInterruptionResumesAfterDatabaseRestart(t *testing.T) {
	for _, reason := range []string{"low_space", "cancel"} {
		t.Run(reason, func(t *testing.T) {
			db, file := openPublicationRetryDB(t)
			tree := map[string][]map[string]any{"/Example": {}}
			for i := 0; i < 521; i++ {
				tree["/Example"] = append(tree["/Example"], looseAudio(fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26)))
			}
			f := newLooseWorkFixtureWithDB(t, tree, db)
			source, err := f.server.loadRemoteSourceForUse(context.Background(), f.sourceID(t))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, err = f.server.catalogFiniteContext(ctx, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			listing, err := f.server.stageCatalogFiniteListing(ctx, source, "/Example")
			if err != nil {
				t.Fatal(err)
			}
			ctx, op, err := f.server.beginCatalogPublication(ctx, source.ID, "manual:/Example")
			if err != nil {
				t.Fatal(err)
			}
			f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
				var n int
				if e := db.QueryRow(`SELECT count(*) FROM source_catalog_finite_file`).Scan(&n); e != nil {
					return diskSpaceSnapshot{}, e
				}
				if n >= 64 {
					if reason == "cancel" {
						cancel()
					} else {
						return diskSpaceSnapshot{Available: 0}, nil
					}
				}
				return diskSpaceSnapshot{Available: 1 << 40}, nil
			}
			if err = f.server.applyCatalogFiniteListing(ctx, source, "/Example", listing, op); err == nil {
				t.Fatal("resource interruption ignored")
			}
			var n int
			var cursor string
			if err = db.QueryRow(`SELECT count(*),(SELECT partition_cursor FROM source_catalog_listing WHERE id=?) FROM source_catalog_finite_file`, listing).Scan(&n, &cursor); err != nil || n != 64 || cursor == "" {
				t.Fatalf("partition checkpoint=%d/%q %v", n, cursor, err)
			}
			if got := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200)["total"]; got != float64(0) {
				t.Fatalf("partial publication=%v", got)
			}
			reopenCatalogFiniteFixture(t, f, file)
			ctx, err = f.server.catalogFiniteContext(context.Background(), source.ID)
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := f.server.stageCatalogFiniteListing(ctx, source, "/Example")
			if err != nil || resumed != listing {
				t.Fatalf("listing=%d want=%d %v", resumed, listing, err)
			}
			ctx, resumed, err = f.server.beginCatalogPublication(ctx, source.ID, "manual:/Example")
			if err != nil || resumed != op {
				t.Fatalf("operation=%d want=%d %v", resumed, op, err)
			}
			if err = f.server.applyCatalogFiniteListing(ctx, source, "/Example", listing, op); err != nil {
				t.Fatal(err)
			}
			if err = f.server.commitCatalogPublication(ctx, op); err != nil {
				t.Fatal(err)
			}
			if got := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200)["total"]; got != float64(521) {
				t.Fatalf("restarted complete set=%v", got)
			}
		})
	}
}

func TestCatalogFiniteLargeEpisodeFormatsKeepCompleteSet(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprint(nested), func(t *testing.T) {
			tree := map[string][]map[string]any{"/Example": {}}
			wantEpisodes, wantFiles := 601, 1202
			if !nested {
				for i := 1; i <= 601; i++ {
					tree["/Example"] = append(tree["/Example"], looseAudio(fmt.Sprintf("Example Episode %d.mp3", i)), looseAudio(fmt.Sprintf("Example Episode %d (FLAC).flac", i)))
				}
			} else {
				wantEpisodes, wantFiles = 2, 804
				for _, name := range []string{"Episode 1", "Episode 1 (FLAC)", "Episode 2", "Episode 2 (FLAC)"} {
					tree["/Example"] = append(tree["/Example"], looseDir(name))
					for i := 1; i <= 201; i++ {
						tree["/Example/"+name] = append(tree["/Example/"+name], looseAudio(fmt.Sprintf("track-%03d.mp3", i)))
					}
				}
			}
			audit := &catalogWriteAudit{}
			f := newLooseWorkFixtureWithDB(t, tree, openCatalogWriteAuditedDB(t, audit))
			handler := f.handler
			f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), catalogWriteTimingKey{}, audit.recordTiming)))
			})
			audit.start()
			finishMemory := sampleCatalogFiniteHeap()
			if result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200); result["status"] != "complete" {
				t.Fatalf("scan=%#v", result)
			}
			audit.stop()
			baseHeap, peakHeap := finishMemory()
			writes, transactions, peak, tables := audit.allDetails()
			t.Logf("finite nested=%v total_writes=%d transactions=%d peak_rows=%d longest_commit_wall=%s tables=%v", nested, writes, transactions, peak, audit.duration(), tables)
			t.Logf("finite_heap nested=%v baseline=%d peak=%d delta=%d", nested, baseHeap, peakHeap, peakHeap-baseHeap)
			var seriesID int64
			if err := f.server.db.QueryRow(`SELECT series_id FROM catalog_series_member LIMIT 1`).Scan(&seriesID); err != nil {
				t.Fatal(err)
			}
			view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, fmt.Sprintf("/api/catalog-series/%d", seriesID), "", 200)
			if got := len(view["episodes"].([]any)); got != wantEpisodes {
				t.Fatalf("episodes=%d want=%d", got, wantEpisodes)
			}
			var files, entries int
			if err := f.server.db.QueryRow(`SELECT count(*),count(DISTINCT catalog_entry_id) FROM published_source_catalog_file WHERE kind='audio'`).Scan(&files, &entries); err != nil || files != wantFiles || entries != wantEpisodes {
				t.Fatalf("complete set=%d/%d want=%d/%d err=%v", files, entries, wantFiles, wantEpisodes, err)
			}
		})
	}
}

func sampleCatalogFiniteHeap() func() (uint64, uint64) {
	runtime.GC()
	var initial runtime.MemStats
	runtime.ReadMemStats(&initial)
	var peak atomic.Uint64
	peak.Store(initial.HeapAlloc)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		timer := time.NewTicker(5 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				var sample runtime.MemStats
				runtime.ReadMemStats(&sample)
				peak.Store(max(peak.Load(), sample.HeapAlloc))
			}
		}
	}()
	return func() (uint64, uint64) { close(stop); <-stopped; return initial.HeapAlloc, peak.Load() }
}

func reopenCatalogFiniteFixture(t *testing.T, f *looseWorkFixture, file string) {
	t.Helper()
	if err := f.server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.server = NewServer(db, f.server.cfg)
	t.Cleanup(func() { _ = f.server.Shutdown(context.Background()) })
	f.handler = f.server.Routes()
}

func TestCatalogFiniteSelectedCardResumesAfterDatabaseRestart(t *testing.T) {
	db, file := openPublicationRetryDB(t)
	tree := map[string][]map[string]any{"/Example": {}}
	for i := 0; i < 521; i++ {
		tree["/Example"] = append(tree["/Example"], looseAudio(fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26)))
	}
	f := newLooseWorkFixtureWithDB(t, tree, db)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	var id int64
	if err := db.QueryRow(`SELECT id FROM source_catalog_entry WHERE title='Example Recording aa'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_finite_marker_failure BEFORE UPDATE OF state ON source_catalog_publication WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'synthetic marker failure');END`)
	if _, err := f.server.refreshCatalogEntry(context.Background(), id); err == nil {
		t.Fatal("expected marker failure")
	}
	reopenCatalogFiniteFixture(t, f, file)
	metadataReviewExec(t, f.server.db, `DROP TRIGGER synthetic_finite_marker_failure`)
	if _, err := f.server.refreshCatalogEntry(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM published_source_catalog_file WHERE kind='audio'`).Scan(&count); err != nil || count != 521 {
		t.Fatalf("selected recovery lost siblings: %d %v", count, err)
	}
}
