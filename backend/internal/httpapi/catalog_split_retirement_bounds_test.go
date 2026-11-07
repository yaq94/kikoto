package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/storage"
)

// This starts from the full HTTP index, not pre-seeded split rows: a creator
// directory can legitimately publish more cards than a retirement batch.
func TestCatalogSplitRetirementBoundsAfterFullReindex(t *testing.T) {
	const cards = 1103
	audit := &catalogWriteAudit{}
	db := openCatalogWriteAuditedDB(t, audit)
	items := make([]map[string]any, cards)
	for i := range items {
		items[i] = looseAudio(fmt.Sprintf("Example Recording %c%c%c.mp3", 'a'+i/676, 'a'+i/26%26, 'a'+i%26))
	}
	f := newLooseWorkFixtureWithDB(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": items,
	}, db)
	if result := f.runFullScan(t, ""); result["status"] != "complete" {
		t.Fatalf("initial index: %#v", result)
	}
	if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(cards) {
		t.Fatalf("initial complete set=%v want=%d", view["total"], cards)
	}
	f.updates(map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseAudio("01.mp3")},
	})
	audit.start()
	result := f.runFullScan(t, `{"intervalMs":100,"concurrency":1,"force":true}`)
	audit.stop()
	if result["status"] != "complete" {
		t.Fatalf("replacement index: %#v", result)
	}
	if view := f.request(t, http.MethodGet, "/works?pageSize=100", "", 200); view["total"] != float64(1) {
		t.Errorf("replacement visible set=%v want=1", view["total"])
	}
	rows, entries, files, transactions := audit.details()
	writes, allTransactions, peak, tables := audit.allDetails()
	t.Logf("old_cards=%d max_entry_file_rows=%d entries=%d files=%d entry_file_transactions=%d all_writes=%d all_transactions=%d peak_all_rows=%d peak_tables=%v", cards, rows, entries, files, transactions, writes, allTransactions, peak, tables)
	if entries > 100 {
		t.Errorf("retirement transaction wrote %d entries; want at most 100", entries)
	}
}

func TestCatalogSplitRetirementResumesAfterRestart(t *testing.T) {
	for _, interruption := range []string{"transaction", "low_space", "cancel"} {
		t.Run(interruption, func(t *testing.T) {
			const cards = 133
			items := make([]map[string]any, cards)
			for i := range items {
				items[i] = looseAudio(fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26))
			}
			f := newLooseWorkFixture(t, map[string][]map[string]any{
				"/": {looseDir("Creator")}, "/Creator": items,
			})
			if result := f.runFullScan(t, ""); result["status"] != "complete" {
				t.Fatalf("initial index: %#v", result)
			}
			personalCards := f.request(t, http.MethodGet, "/works?q=Example%20Recording%20aa", "", 200)["works"].([]any)
			if len(personalCards) != 1 {
				t.Fatalf("personal card search returned %d cards", len(personalCards))
			}
			card := personalCards[0].(map[string]any)
			personalPath := "/api/source-catalog/" + strconv.FormatInt(int64(card["catalogEntryId"].(float64)), 10)
			reviewRequest(t, f.handler, f.cookie, http.MethodPatch, personalPath+"/user-state", `{"rating":4,"note":"Example retained review"}`, 200)
			f.updates(map[string][]map[string]any{
				"/": {looseDir("Creator")}, "/Creator": {looseAudio("01.mp3")},
			})
			// Abort only after a retirement transaction has committed. This
			// observes real SQLite writes, not calls to retirement helpers.
			metadataReviewExec(t, f.server.db, `CREATE TABLE synthetic_split_retired(id INTEGER PRIMARY KEY);
			 CREATE TRIGGER synthetic_record_split_retirement AFTER UPDATE OF file_source_id ON source_catalog_entry
			 WHEN OLD.file_source_id IS NOT NULL AND NEW.file_source_id IS NULL
			 BEGIN INSERT INTO synthetic_split_retired VALUES(NEW.id); END;`)
			if interruption == "transaction" {
				metadataReviewExec(t, f.server.db, `CREATE TRIGGER synthetic_split_failure BEFORE UPDATE OF file_source_id ON source_catalog_entry
				 WHEN OLD.file_source_id IS NOT NULL AND NEW.file_source_id IS NULL AND (SELECT count(*) FROM synthetic_split_retired)>=64
				 BEGIN SELECT RAISE(ABORT,'synthetic split retirement failure'); END;`)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
				var retired int
				err := f.server.db.QueryRow(`SELECT count(*) FROM synthetic_split_retired`).Scan(&retired)
				if retired > 0 {
					if interruption == "low_space" {
						return diskSpaceSnapshot{}, err
					}
					if interruption == "cancel" {
						cancel()
					}
				}
				return diskSpaceSnapshot{Available: 1 << 40}, err
			}
			started := f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"concurrency":1,"force":true}`, 202)
			for range 100 {
				_ = f.server.runNextQueuedWorkflowJob(ctx)
				var retired int
				if err := f.server.db.QueryRow(`SELECT count(*) FROM synthetic_split_retired`).Scan(&retired); err != nil {
					t.Fatal(err)
				}
				if retired > 0 {
					break
				}
			}
			var op, cursor int64
			if err := f.server.db.QueryRow(`SELECT r.operation_id,r.after_entry_id FROM source_catalog_split_retirement r JOIN source_catalog_publication p ON p.id=r.operation_id WHERE p.state='preparing' AND r.root_path='/Creator'`).Scan(&op, &cursor); err != nil || cursor == 0 {
				t.Fatalf("retirement did not preserve its checkpoint: op=%d cursor=%d err=%v", op, cursor, err)
			}
			var retired int
			if err := f.server.db.QueryRow(`SELECT count(*) FROM synthetic_split_retired`).Scan(&retired); err != nil || retired <= 0 || retired >= cards-1 {
				t.Fatalf("committed retirement prefix=%d err=%v", retired, err)
			}
			if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(cards) {
				t.Fatalf("interruption exposed a partial set=%v", view["total"])
			}
			var sequence int
			var name, databasePath string
			if err := f.server.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
				t.Fatal(err)
			}
			cfg := f.server.cfg
			if err := f.server.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := f.server.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := storage.Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			s := NewServer(db, cfg)
			t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
			f.server, f.handler = s, s.Routes()
			metadataReviewExec(t, db, `DROP TRIGGER IF EXISTS synthetic_split_failure`)
			// Re-reading a committed keyset prefix is a regression even if the
			// protected card produces no UPDATE. The checkpoint cannot rewind.
			metadataReviewExec(t, db, fmt.Sprintf(`CREATE TRIGGER synthetic_split_cursor_rewind BEFORE UPDATE OF after_entry_id ON source_catalog_split_retirement
			 WHEN OLD.operation_id=%d AND NEW.after_entry_id<%d BEGIN SELECT RAISE(ABORT,'split retirement cursor rewound'); END;`, op, cursor))
			resumed := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
			if resumed["runId"] != started["runId"] {
				t.Fatalf("resume replaced generation: %v/%v", resumed, started)
			}
			var sameOp int64
			if err = db.QueryRow(`SELECT id FROM source_catalog_publication WHERE state='preparing'`).Scan(&sameOp); err != nil || sameOp != op {
				t.Fatalf("resume operation=%d want=%d err=%v", sameOp, op, err)
			}
			if result := f.drain(t); result["status"] != "complete" {
				t.Fatalf("resumed index: %#v", result)
			}
			if err = db.QueryRow(`SELECT count(*) FROM synthetic_split_retired`).Scan(&retired); err != nil || retired != cards-1 {
				t.Fatalf("retired complete set=%d want=%d err=%v", retired, cards-1, err)
			}
			if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(2) {
				t.Fatalf("replacement plus retained personal card=%v want=2", view["total"])
			}
			assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, personalPath, "", 200), float64(4), "Example retained review")
			t.Logf("interruption=%s operation=%d checkpoint=%d final_retired=%d final_visible=2", interruption, op, cursor, retired)
		})
	}
}

func TestCatalogSplitRetirementOrphanCursorResume(t *testing.T) {
	items := make([]map[string]any, 133)
	for i := range items {
		items[i] = looseAudio(fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{"/": {looseDir("Creator")}, "/Creator": items})
	f.runFullScan(t, "")
	personalCards := f.request(t, http.MethodGet, "/works?q=Example%20Recording%20aa", "", 200)["works"].([]any)
	if len(personalCards) != 1 {
		t.Fatalf("personal card search returned %d cards", len(personalCards))
	}
	card := personalCards[0].(map[string]any)
	personalPath := "/api/source-catalog/" + strconv.FormatInt(int64(card["catalogEntryId"].(float64)), 10)
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, personalPath+"/user-state", `{"rating":4,"note":"Example retained review"}`, 200)
	f.updates(map[string][]map[string]any{"/": {looseDir("Creator")}, "/Creator": {looseAudio("example.txt")}})
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		var cursor int64
		err := f.server.db.QueryRow(`SELECT orphan_cleanup_cursor FROM source_catalog_scan`).Scan(&cursor)
		if cursor > 0 {
			return diskSpaceSnapshot{}, err
		}
		return diskSpaceSnapshot{Available: 1 << 40}, err
	}
	if result := f.runFullScan(t, `{"intervalMs":100,"concurrency":1,"force":true}`); result["status"] != "paused" {
		t.Fatalf("orphan pause=%v", result)
	}
	var before, after int64
	if err := f.server.db.QueryRow(`SELECT orphan_cleanup_cursor FROM source_catalog_scan`).Scan(&before); err != nil || before == 0 {
		t.Fatalf("orphan checkpoint=%d err=%v", before, err)
	}
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{Available: 1 << 40}, nil }
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	if err := f.server.db.QueryRow(`SELECT orphan_cleanup_cursor FROM source_catalog_scan`).Scan(&after); err != nil || after != before {
		t.Fatalf("resume rewound orphan prefix: before=%d after=%d err=%v", before, after, err)
	}
	if result := f.drain(t); result["status"] != "complete" {
		t.Fatalf("orphan resume=%v", result)
	}
	if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(1) {
		t.Fatalf("orphan retained set=%v want=1", view["total"])
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, personalPath, "", 200), float64(4), "Example retained review")
}

func TestCatalogSplitRetirementReobservedFailedRoot(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")}, "/Creator": {looseAudio("Example Alpha.mp3"), looseAudio("Example Beta.mp3")},
	})
	f.runFullScan(t, "")
	f.failPath("/Creator")
	if result := f.runFullScan(t, `{"intervalMs":100,"concurrency":1,"force":true}`); result["status"] != "partial" {
		t.Fatalf("failed root status=%v", result)
	}
	f.mu.Lock()
	delete(f.failing, "/Creator")
	f.mu.Unlock()
	f.updates(map[string][]map[string]any{"/": {looseDir("Creator")}, "/Creator": {looseAudio("example.txt")}})
	if result := f.runFullScan(t, ""); result["status"] != "complete" {
		t.Fatalf("reobserved root status=%v", result)
	}
	if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(0) {
		t.Fatalf("reobserved obsolete cards=%v want=0", view["total"])
	}
}

func TestCatalogSplitRetirementAfterIncrementalUpdate(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Creator", IsDir: true, Modified: "v1"})
	items := make([]catalogSyncTestEntry, 133)
	for i := range items {
		items[i] = catalogSyncTestEntry{Name: fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26), Size: 100}
	}
	tree.set("/Creator", items...)
	audit := &catalogWriteAudit{}
	f := newCatalogIndexFixtureWithDB(t, tree.handler(t), openCatalogWriteAuditedDB(t, audit))
	if result := f.runFullScan(t, ""); result["status"] != "complete" {
		t.Fatalf("initial index=%v", result)
	}
	// An interrupted next full generation leaves old split cards eligible for
	// retirement when the incremental updater successfully revisits the root.
	tree.mu.Lock()
	tree.listFail["/Creator"] = true
	tree.mu.Unlock()
	if result := f.runFullScan(t, `{"intervalMs":100,"concurrency":1,"force":true}`); result["status"] != "partial" {
		t.Fatalf("partial generation=%v", result)
	}
	tree.mu.Lock()
	delete(tree.listFail, "/Creator")
	tree.mu.Unlock()
	tree.setModified("/", "Creator", "v2")
	tree.set("/Creator", catalogSyncTestEntry{Name: "01.mp3", Size: 100})
	metadataReviewExec(t, f.server.db, `CREATE TABLE synthetic_split_budget(n INTEGER NOT NULL,peak INTEGER NOT NULL); INSERT INTO synthetic_split_budget VALUES(0,0);
	 CREATE TRIGGER synthetic_split_budget_reset AFTER UPDATE OF operation_id ON source_catalog_publication_writer
	 BEGIN UPDATE synthetic_split_budget SET n=0; END;
	 CREATE TRIGGER synthetic_split_budget_row AFTER UPDATE OF file_source_id ON source_catalog_entry
	 WHEN OLD.file_source_id IS NOT NULL AND NEW.file_source_id IS NULL
	 BEGIN UPDATE synthetic_split_budget SET n=n+1,peak=max(peak,n+1);
	 SELECT CASE WHEN (SELECT n FROM synthetic_split_budget)>100 THEN RAISE(ABORT,'split retirement exceeded 100 entries') END; END;`)
	audit.start()
	result := f.runCatalogSync(t, true)
	audit.stop()
	if result.Status != "succeeded" {
		t.Fatalf("incremental update=%+v", result)
	}
	if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(1) {
		t.Fatalf("incremental visible set=%v want=1", view["total"])
	}
	rows, entries, files, transactions := audit.details()
	var retirementPeak int
	if err := f.server.db.QueryRow(`SELECT peak FROM synthetic_split_budget`).Scan(&retirementPeak); err != nil {
		t.Fatal(err)
	}
	if retirementPeak == 0 {
		t.Fatal("incremental update did not exercise stale split retirement")
	}
	t.Logf("incremental old_cards=133 all_entry_file_rows=%d entries=%d files=%d transactions=%d retirement_peak=%d", rows, entries, files, transactions, retirementPeak)
}
