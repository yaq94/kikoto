package httpapi

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogGroupingResourceBudget(t *testing.T) {
	for _, count := range []int{2000, 8000, 32000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			audit := &catalogWriteAudit{}
			db := openCatalogWriteAuditedDB(t, audit)
			seedCatalogPlanSource(t, db)
			metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status,work_split) VALUES(7,'/Creator',1,1,'/Creator','/Creator','complete','file')`)
			metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?) INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes) SELECT 7,'/Creator',printf('Example Recording %08d.mp3',i),1,'audio',1 FROM n`, count)
			ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
			classified, maxBatch := 0, 0
			phases := map[string]int{}
			activePhase := "initial"
			durations := map[string][]time.Duration{}
			probe := newCatalogGroupingLockProbe(t, db)
			var unlocked <-chan time.Time
			lockDurations := []time.Duration{}
			segment, segmentStart := "begin", time.Now()
			segments := map[string]time.Duration{}
			var memBefore runtime.MemStats
			runtime.ReadMemStats(&memBefore)
			lastGC := memBefore.PauseTotalNs
			ctx = context.WithValue(ctx, catalogGroupingObserveKey{}, func(phase string, n int) {
				if strings.HasPrefix(phase, "timing:") {
					segments[segment] += time.Since(segmentStart)
					segment, segmentStart = phase, time.Now()
					if phase == "timing:commit" {
						unlocked = probe()
					}
					return
				}
				if strings.HasPrefix(phase, "phase:") {
					segmentStart = time.Now()
					segment = "statements"

					activePhase = strings.TrimPrefix(phase, "phase:")
					return
				}
				phases[phase] += n
				if phase == "classify" {
					classified += n
				}
				if phase != "sequence" {
					maxBatch = max(maxBatch, n)
				}
			})
			ctx = context.WithValue(ctx, catalogWriteTimingKey{}, func(d time.Duration) {
				audit.recordTiming(d)
				durations[activePhase] = append(durations[activePhase], d)
				segments[segment] += time.Since(segmentStart)
				lockHeld := d
				if unlocked != nil {
					commitReturned := time.Now()
					acquired := <-unlocked
					if acquired.IsZero() {
						t.Error("writer probe failed")
					} else if acquired.Before(commitReturned) {
						lockHeld -= commitReturned.Sub(acquired)
					}
					unlocked = nil
					lockDurations = append(lockDurations, lockHeld)
				}
				if d > 100*time.Millisecond {
					var sample runtime.MemStats
					runtime.ReadMemStats(&sample)
					t.Logf("slow_writer phase=%s batch=%d wall=%s lock_upper_bound=%s segments=%v gc_since_previous_slow=%s", activePhase, len(durations[activePhase]), d, lockHeld, segments, time.Duration(sample.PauseTotalNs-lastGC))
					lastGC = sample.PauseTotalNs
				}
				clear(segments)
			})
			runtime.GC()
			var base runtime.MemStats
			runtime.ReadMemStats(&base)
			var peak atomic.Uint64
			peak.Store(base.HeapAlloc)
			stop, stopped := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(stopped)
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						var sample runtime.MemStats
						runtime.ReadMemStats(&sample)
						for old := peak.Load(); sample.HeapAlloc > old && !peak.CompareAndSwap(old, sample.HeapAlloc); old = peak.Load() {
						}
					}
				}
			}()
			s := &Server{db: db}
			audit.start()
			started := time.Now()
			id, err := s.prepareCatalogGrouping(ctx, catalogGroupingIndexInput{SourceID: 7, Generation: 1, Root: "/Creator", Revision: 1})
			elapsed := time.Since(started)
			audit.stop()
			close(stop)
			<-stopped
			if err != nil {
				t.Fatal(err)
			}
			if classified != count || maxBatch > 64 {
				t.Fatalf("classified=%d batch=%d want=%d/64", classified, maxBatch, count)
			}
			var targets int
			if err = db.QueryRow(`SELECT count(*) FROM source_catalog_grouping_target WHERE grouping_id=?`, id).Scan(&targets); err != nil || targets != count {
				t.Fatalf("targets=%d %v", targets, err)
			}
			// Re-reading the plan for every publication batch must not parse names or
			// copy the directory's direct/group maps again.
			started = time.Now()
			for batch := 0; batch < (count+49)/50; batch++ {
				tx, e := db.Begin()
				if e != nil {
					t.Fatal(e)
				}
				plan, _, e := s.catalogSplitPlanForRoot(ctx, tx, 7, 1, "/Creator")
				if e == nil {
					_, ok, targetErr := plan.indexedTargetFor(ctx, tx, fmt.Sprintf("Example Recording %08d.mp3", min(count, batch*50+1)))
					e = targetErr
					if !ok && e == nil {
						t.Error("known target disappeared")
					}
				}
				if e == nil {
					e = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			if classified != count {
				t.Fatalf("publication reclassified names=%d", classified)
			}
			if phases["sequence"] != count || phases["target"] != count {
				t.Fatalf("nonlinear classification passes=%v", phases)
			}
			for phase, samples := range durations {
				slices.Sort(samples)
				t.Logf("writer_phase=%s samples=%d p95=%s max=%s", phase, len(samples), samples[(len(samples)-1)*95/100], samples[len(samples)-1])
			}
			slices.Sort(lockDurations)
			if lockDurations[len(lockDurations)-1] > 500*time.Millisecond {
				t.Errorf("writer lock exceeded 500ms: %s", lockDurations[len(lockDurations)-1])
			}
			t.Logf("sqlite_writer_lock samples=%d p95=%s max=%s (BEGIN IMMEDIATE probe, 1ms polling upper bound)", len(lockDurations), lockDurations[(len(lockDurations)-1)*95/100], lockDurations[len(lockDurations)-1])
			t.Logf("total_algorithm_rows=%v", phases)
			writes, transactions, maxRows, tables := audit.allDetails()
			t.Logf("names=%d classified=%d batch_max=%d targets=%d prepare=%s repeated_plans=%d repeated_elapsed=%s heap_baseline=%d heap_peak=%d heap_delta=%d total_writes=%d transactions=%d peak_tx_rows=%d longest_write=%s peak_tables=%v", count, classified, maxBatch, targets, elapsed, (count+49)/50, time.Since(started), base.HeapAlloc, peak.Load(), peak.Load()-base.HeapAlloc, writes, transactions, maxRows, audit.duration(), tables)
			t.Logf("target_plan=%s", explainCatalogPlan(t, db, `SELECT target_json FROM source_catalog_grouping_name WHERE grouping_id=? AND name=?`, id, "Example Recording 00000001.mp3"))
			t.Logf("bucket_plan=%s", explainCatalogPlan(t, db, `SELECT work_prefix,prefix_position,name FROM source_catalog_grouping_name WHERE grouping_id=? AND work_prefix<>'' AND (work_prefix,prefix_position,name)>(?,?,?) ORDER BY work_prefix,prefix_position,name LIMIT 64`, id, "", 0, ""))
			t.Logf("parent_plan=%s", explainCatalogPlan(t, db, `SELECT o.directory_path,o.name,o.kind FROM source_catalog_directory d JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=? AND d.path>=? AND o.name>CASE WHEN d.path=? THEN ? ELSE '' END AND o.kind<>'folder' ORDER BY d.path,o.name LIMIT 64`, 7, 1, "/Creator", "/Creator", "/Creator", "Example Recording 00016000.mp3"))
			if count == 2000 {
				names := make([]string, count)
				for i := range names {
					names[i] = fmt.Sprintf("Example Recording %08d.mp3", i+1)
				}
				started := time.Now()
				inputs := 0
				for batch := 0; batch < (count+49)/50; batch++ {
					legacy := newCatalogSplitPlan("/Creator", names, catalogWorkSplitFile)
					inputs += len(names)
					if _, ok := legacy.targetFor(names[batch*50]); !ok {
						t.Fatal("baseline target missing")
					}
				}
				t.Logf("legacy_same_input names=%d plans=%d complete_name_inputs=%d elapsed=%s", count, (count+49)/50, inputs, time.Since(started))
			}
		})
	}
}
