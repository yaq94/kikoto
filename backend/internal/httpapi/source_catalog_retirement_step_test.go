package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

// newRetireTestServer creates the minimum workflow lease the retirement step
// verifies. The step itself never reads a scan row unless it commits a
// retirement, but the fixture seeds one for the sources that use it.
func newRetireTestServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES
		(1,'retire-source-one','Retire Source One','openlist'),
		(2,'retire-source-two','Retire Source Two','openlist'),
		(3,'retire-source-three','Retire Source Three','openlist'),
		(4,'retire-source-four','Retire Source Four','openlist')`)
	metadataReviewExec(t, db, `INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
		VALUES(1,'source_catalog_index','Index source catalog','running','manual')`)
	metadataReviewExec(t, db, `INSERT INTO workflow_job(id,workflow_run_id,worker_type,status,payload_json,locked_by,locked_at)
		VALUES(1,1,?,'running','{}','retire-test',CURRENT_TIMESTAMP)`, sourceCatalogIndexWorkerType)
	for _, sourceID := range []int64{1, 3, 4} {
		metadataReviewExec(t, db, `INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,indexed)
			VALUES(?,1,'{}',0)`, sourceID)
	}
	return s, db
}

func insertRetireEntry(t *testing.T, tx *sql.Tx, sourceID int64, root, sourceRoot, title string, generation int64) int64 {
	t.Helper()
	var id int64
	err := tx.QueryRow(`INSERT INTO source_catalog_entry(file_source_id,root_path,source_root,title,last_seen_generation,discovered_generation)
		VALUES(?,?,?,?,?,?) RETURNING id`, sourceID, root, sourceRoot, title, generation, generation).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRetireFile(t *testing.T, tx *sql.Tx, entryID int64, relative string, generation int64) {
	t.Helper()
	if _, err := tx.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(?,?,?,'audio',100,?)`, entryID, relative, relative, generation); err != nil {
		t.Fatal(err)
	}
}

// seedRetireLayout creates two shapes below one source:
//   - partial: the stale container owns two audio rows but the current card
//     publishes only one, so retirement must leave the stale card alone;
//   - covered: the stale container's one audio row is republished by exactly
//     one current card below the same root.
func seedRetireLayout(t *testing.T, db *sql.DB, sourceID, generation int64, partial, covered int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	staleGeneration := generation - 1
	for i := 0; i < partial; i++ {
		root := fmt.Sprintf("/Example/P%04d", i)
		staleID := insertRetireEntry(t, tx, sourceID, root, "", fmt.Sprintf("Partial %04d", i), staleGeneration)
		liveID := insertRetireEntry(t, tx, sourceID, root+"/Release", "", fmt.Sprintf("Partial %04d", i), generation)
		insertRetireFile(t, tx, staleID, "Release/a.mp3", staleGeneration)
		insertRetireFile(t, tx, staleID, "Release/b.mp3", staleGeneration)
		insertRetireFile(t, tx, liveID, "a.mp3", generation)
	}
	for i := 0; i < covered; i++ {
		root := fmt.Sprintf("/Example/C%04d", i)
		staleID := insertRetireEntry(t, tx, sourceID, root, "", fmt.Sprintf("Covered %04d", i), staleGeneration)
		liveID := insertRetireEntry(t, tx, sourceID, root+"/Release", "", fmt.Sprintf("Covered %04d", i), generation)
		insertRetireFile(t, tx, staleID, "Release/track.mp3", staleGeneration)
		insertRetireFile(t, tx, liveID, "track.mp3", generation)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func retireCardState(t *testing.T, db *sql.DB, id int64) (source sql.NullInt64, relocated sql.NullInt64, files int) {
	t.Helper()
	if err := db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, id).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=?`, id).Scan(&files); err != nil {
		t.Fatal(err)
	}
	return source, relocated, files
}

func countRetireRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A blocked prefix must not stall the pass. The first stale cards are partial
// coverage and cannot retire, so the local cursor must walk past them and
// reach the covered cards in later pages. The same layout is retired once by
// the old one-shot function to compare the final state.
func TestCatalogRetireStepPagesByIDAndMatchesOneShot(t *testing.T) {
	s, db := newRetireTestServer(t)
	const generation = int64(1)
	const partial, covered = 110, 110
	seedRetireLayout(t, db, 1, generation, partial, covered)
	seedRetireLayout(t, db, 2, generation, partial, covered)
	job := workflowJobRecord{ID: 1, RunID: generation, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "retire-test"}

	var cursor int64
	total, pages := 0, 0
	for {
		started := time.Now()
		retired, lastID, more, err := s.retireCatalogCardPage(context.Background(), job, 1, catalogRetireCovered, cursor, catalogRetireCardsPerBatch)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("retire page %d cursor=%d retired=%d took=%s", pages+1, cursor, retired, time.Since(started))
		if retired > catalogRetireCardsPerBatch {
			t.Fatalf("page retired %d cards, want at most %d", retired, catalogRetireCardsPerBatch)
		}
		total += retired
		pages++
		if !more {
			break
		}
		if lastID <= cursor {
			t.Fatalf("cursor did not advance: %d -> %d", cursor, lastID)
		}
		cursor = lastID
	}
	if total != covered {
		t.Fatalf("retired %d cards, want %d", total, covered)
	}
	if pages < 3 {
		t.Fatalf("pages=%d, want the pass to cross the blocked prefix", pages)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = retireCoveredContainerCardsTx(context.Background(), tx, 2, generation); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, sourceID := range []int64{1, 2} {
		if got := countRetireRows(t, db, `SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL`, sourceID); got != partial+2*covered {
			t.Fatalf("source %d live cards=%d, want %d", sourceID, got, partial+2*covered)
		}
		if got := countRetireRows(t, db, `SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND last_seen_generation<>1 AND root_path LIKE '/Example/C%'`, sourceID); got != 0 {
			t.Fatalf("source %d covered stale cards left=%d, want 0", sourceID, got)
		}
		if got := countRetireRows(t, db, `SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND last_seen_generation<>1 AND root_path LIKE '/Example/P%'`, sourceID); got != partial {
			t.Fatalf("source %d partial stale cards=%d, want %d", sourceID, got, partial)
		}
		if got := countRetireRows(t, db, `SELECT count(*) FROM source_catalog_entry a
			JOIN source_catalog_entry t ON t.id=a.relocated_to_id
			WHERE t.file_source_id=? AND a.root_path LIKE '/Example/C%'`, sourceID); got != covered {
			t.Fatalf("source %d relocated aliases=%d, want %d", sourceID, got, covered)
		}
	}
}

// retireCatalogCardsBatch commits at most 100 retirements per call. The first
// call on 150 covered cards must leave work for the next invocation instead of
// hiding it in the publish transaction.
func TestCatalogRetireBatchStopsAfterOneHundredRetirements(t *testing.T) {
	s, db := newRetireTestServer(t)
	const generation = int64(1)
	seedRetireLayout(t, db, 3, generation, 0, 150)
	job := workflowJobRecord{ID: 1, RunID: generation, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "retire-test"}

	started := time.Now()
	first, done, err := s.retireCatalogCardsBatch(context.Background(), job, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retire batch 1 retired=%d took=%s", first, time.Since(started))
	if first != catalogRetireCardsPerBatch || done {
		t.Fatalf("first batch retired=%d done=%v, want %d and false", first, done, catalogRetireCardsPerBatch)
	}

	started = time.Now()
	second, done, err := s.retireCatalogCardsBatch(context.Background(), job, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retire batch 2 retired=%d took=%s", second, time.Since(started))
	if second != 50 || !done {
		t.Fatalf("second batch retired=%d done=%v, want 50 and true", second, done)
	}

	third, done, err := s.retireCatalogCardsBatch(context.Background(), job, 3)
	if err != nil {
		t.Fatal(err)
	}
	if third != 0 || !done {
		t.Fatalf("third batch retired=%d done=%v, want 0 and true", third, done)
	}
}

// Identity and coverage keep their old meaning after the move out of publish:
// work, series and inbound aliases retain the stale card, partial coverage
// retains it, and only the covered container becomes an alias.
func TestCatalogRetireBatchKeepsIdentityAndPartialCoverage(t *testing.T) {
	s, db := newRetireTestServer(t)
	const generation = int64(1)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(77,'','Retained work')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	coveredStale := insertRetireEntry(t, tx, 4, "/Example/Covered", "", "Covered", 0)
	coveredLive := insertRetireEntry(t, tx, 4, "/Example/Covered/Release", "", "Covered", generation)
	insertRetireFile(t, tx, coveredStale, "Release/track.mp3", 0)
	insertRetireFile(t, tx, coveredLive, "track.mp3", generation)

	workStale := insertRetireEntry(t, tx, 4, "/Example/Work", "", "Work", 0)
	workLive := insertRetireEntry(t, tx, 4, "/Example/Work/Release", "", "Work", generation)
	insertRetireFile(t, tx, workStale, "Release/track.mp3", 0)
	insertRetireFile(t, tx, workLive, "track.mp3", generation)
	if _, err = tx.Exec(`UPDATE source_catalog_entry SET work_id=77 WHERE id=?`, workStale); err != nil {
		t.Fatal(err)
	}

	seriesStale := insertRetireEntry(t, tx, 4, "/Example/Series", "", "Series", 0)
	seriesLive := insertRetireEntry(t, tx, 4, "/Example/Series/Release", "", "Series", generation)
	insertRetireFile(t, tx, seriesStale, "Release/track.mp3", 0)
	insertRetireFile(t, tx, seriesLive, "track.mp3", generation)
	result, err := tx.Exec(`INSERT INTO catalog_series(title,source_id,source_key) VALUES('Retained series',4,'retire-series')`)
	if err != nil {
		t.Fatal(err)
	}
	seriesID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(?,?,1,'manual')`, seriesID, seriesStale); err != nil {
		t.Fatal(err)
	}

	aliasStale := insertRetireEntry(t, tx, 4, "/Example/Alias", "", "Alias", 0)
	aliasLive := insertRetireEntry(t, tx, 4, "/Example/Alias/Release", "", "Alias", generation)
	insertRetireFile(t, tx, aliasStale, "Release/track.mp3", 0)
	insertRetireFile(t, tx, aliasLive, "track.mp3", generation)
	if _, err = tx.Exec(`INSERT INTO source_catalog_entry(file_source_id,root_path,title,last_seen_generation,relocated_to_id)
		VALUES(4,'/Example/Alias/Release/Old','Old alias',?,?)`, generation, aliasStale); err != nil {
		t.Fatal(err)
	}

	partialStale := insertRetireEntry(t, tx, 4, "/Example/Partial", "", "Partial", 0)
	partialLive := insertRetireEntry(t, tx, 4, "/Example/Partial/Release", "", "Partial", generation)
	insertRetireFile(t, tx, partialStale, "Release/a.mp3", 0)
	insertRetireFile(t, tx, partialStale, "Release/b.mp3", 0)
	insertRetireFile(t, tx, partialLive, "a.mp3", generation)

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	job := workflowJobRecord{ID: 1, RunID: generation, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "retire-test"}
	retired, done, err := s.retireCatalogCardsBatch(context.Background(), job, 4)
	if err != nil {
		t.Fatal(err)
	}
	if retired != 1 || !done {
		t.Fatalf("retired=%d done=%v, want 1 and true", retired, done)
	}

	source, relocated, files := retireCardState(t, db, coveredStale)
	if source.Valid || !relocated.Valid || relocated.Int64 != coveredLive || files != 0 {
		t.Fatalf("covered stale source=%v relocated=%v files=%d, want alias of %d", source, relocated, files, coveredLive)
	}
	for name, id := range map[string]int64{"work": workStale, "series": seriesStale, "alias": aliasStale, "partial": partialStale} {
		source, relocated, files = retireCardState(t, db, id)
		if !source.Valid || source.Int64 != 4 || relocated.Valid {
			t.Fatalf("%s stale source=%v relocated=%v, want it retained", name, source, relocated)
		}
		if files == 0 {
			t.Fatalf("%s stale lost its file rows", name)
		}
	}
}

// Fingerprints become explicit at the row limit: 10,000 rows are complete,
// 10,001 rows hash the first 10,000 and carry a marker that cannot match a
// complete manifest. Repeated reads must produce the same value.
func TestCatalogFingerprintTruncationBoundaries(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()
	ids := map[int]int64{}
	for _, count := range []int{9999, 10000, 10001} {
		var id int64
		if err := db.QueryRow(`INSERT INTO source_catalog_entry(root_path,title,last_seen_generation,discovered_generation)
			VALUES(?,?,1,1) RETURNING id`, fmt.Sprintf("/N%d", count), fmt.Sprintf("N%d", count)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[count] = id
		if _, err := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<?)
			INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation)
			SELECT ?,printf('t%05d.mp3',x),printf('t%05d',x),'audio',100+x,printf('%064d',x),1 FROM n`, count, id); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, count := range []int{9999, 10000, 10001} {
		strong, layout, truncated, err := catalogFingerprintsDetailedTx(ctx, tx, ids[count], 1)
		if err != nil {
			t.Fatal(err)
		}
		wantTruncated := count > 10000
		if truncated != wantTruncated {
			t.Fatalf("count=%d truncated=%v, want %v", count, truncated, wantTruncated)
		}
		if strong == "" || layout == "" {
			t.Fatalf("count=%d produced empty fingerprints", count)
		}
		if got := catalogFingerprintUsable(strong); got == wantTruncated {
			t.Fatalf("count=%d usable=%v, want its inverse", count, got)
		}
		if wantTruncated && !strings.HasPrefix(layout, catalogFingerprintTruncatedPrefix) {
			t.Fatalf("count=%d layout=%q lacks the truncation marker", count, layout)
		}
		strong2, layout2, truncated2, err := catalogFingerprintsDetailedTx(ctx, tx, ids[count], 1)
		if err != nil {
			t.Fatal(err)
		}
		if strong2 != strong || layout2 != layout || truncated2 != truncated {
			t.Fatalf("count=%d fingerprint is not deterministic", count)
		}
	}
}

// Relocation clears file_source_id on the established row. The scan counter
// must be recomputed in the same transaction, or the progress readout keeps
// counting the alias.
func TestCatalogRelocationRecountsIndexed(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'relocation-source','Relocation Source','openlist');
		INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type) VALUES(1,'source_catalog_index','Index source catalog','running','manual');
		INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,indexed) VALUES(1,1,'{}',2);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,last_seen_generation,discovered_generation) VALUES
			(10,1,'/Old','Old',1,1),
			(11,1,'/New','New',1,1)`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	old := catalogRelocationEntry{id: 10, root: "/Old", title: "Old", discovered: 1, lastSeen: 1}
	target := catalogRelocationEntry{id: 11, root: "/New", title: "New", discovered: 1, lastSeen: 1}
	if err = relocateCatalogEntryTx(context.Background(), tx, old, target, 1); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var indexed, actual int
	if err = db.QueryRow(`SELECT indexed FROM source_catalog_scan WHERE file_source_id=1`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE file_source_id=1 AND last_seen_generation=1`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if indexed != actual || actual != 1 {
		t.Fatalf("indexed=%d actual=%d, want both 1", indexed, actual)
	}
}
