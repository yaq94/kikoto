package httpapi

import (
	"context"
	"database/sql"
	"testing"

	"github.com/yexca/kikoto/backend/internal/catalogprune"
)

// seedCatalogPruneFixture builds two removable cards, one whitelisted child,
// one normal multi-track card, and one card of each protected reference kind.
func seedCatalogPruneFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO user_account(id,username,display_name,role) VALUES(1,'prune-user','Prune User','user')`,
		`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ90000001','Retained work')`,
		`INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,1,5,'keep this review')`,
		`INSERT INTO file_source(id,code,display_name,source_type) VALUES(90,'prune-source','Prune source','openlist')`,
		`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
			VALUES(900,'source_catalog_index','Prune fixture','succeeded','manual')`,
		`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,directories,publish_pending_remaining)
			VALUES(90,900,'{}',99,99)`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status) VALUES
			(1,90,'/bulk','Bulk',1,'available'),
			(2,90,'/bulk/audio','Audio',NULL,'available'),
			(3,90,'/other','Other',NULL,'available'),
			(4,90,'/keep','Keep',NULL,'available'),
			(5,90,'/series-card','Series card',NULL,'available'),
			(7,90,'/target','Alias target',NULL,'available')`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status,relocated_to_id) VALUES
			(6,NULL,'/alias','Alias',NULL,'available',7)`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(1,'a.mp3','a','audio',1),
			(2,'a1.mp3','a1','audio',1),
			(2,'a2.mp3','a2','audio',1),
			(4,'a1.mp3','a1','audio',1),
			(4,'a2.mp3','a2','audio',1),
			(4,'a3.mp3','a3','audio',1),
			(5,'n.mp3','n','other',1),
			(7,'n.mp3','n','other',1)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<6000)
		 INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT 1,'n'||i||'.jpg','n'||i,'other',1 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1278)
		 INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT 4,'x'||i||'.jpg','x'||i,'other',1 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<10)
		 INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT 5,'s'||i||'.jpg','s'||i,'other',1 FROM n`,
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status) VALUES
			(90,'/bulk',900,1,'/bulk','/bulk','complete'),
			(90,'/bulk/audio',900,1,'/bulk','/bulk/audio','complete'),
			(90,'/other',900,1,'/other','/other','complete'),
			(90,'/target',900,1,'/target','/target','complete')`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending) VALUES
			(90,'/bulk','n1.jpg',1,'other',1,1),
			(90,'/bulk','n2.jpg',1,'other',1,1),
			(90,'/bulk','a.mp3',1,'audio',1,1),
			(90,'/bulk/audio','a1.mp3',1,'audio',1,1),
			(90,'/bulk/audio','a2.mp3',1,'audio',1,1),
			(90,'/other','a.mp3',1,'audio',1,1),
			(90,'/target','n.mp3',1,'other',1,1)`,
		`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES
			(90,900,'/bulk'),(90,900,'/bulk/audio')`,
		`INSERT INTO catalog_series(id,title) VALUES(1,'Retained series')`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,5,1,'manual')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCatalogPruneDryRunAndApply(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPruneFixture(t, db)
	ctx := context.Background()
	options := catalogprune.Options{Whitelist: []string{"/bulk/audio"}, BatchSize: 1}

	report, err := catalogprune.BuildReport(ctx, db, options)
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Entries != 2 || report.Totals.Files != 6001 || report.Totals.TransferRefs != 2 {
		t.Fatalf("report totals = %+v, want 2 cards/6001 files/2 refs", report.Totals)
	}
	// Four cards fail admission; the series member and the alias target are
	// reported as candidates but preserved by reference.
	if report.Candidates.Entries != 4 || report.Candidates.Files != 6013 || report.Candidates.TransferRefs != 4 {
		t.Fatalf("candidate totals = %+v, want 4 cards/6013 files/4 refs", report.Candidates)
	}
	if report.Totals.Observations != 4 || report.Totals.Directories != 2 || report.Totals.AudioRoots != 1 {
		t.Fatalf("path totals = %+v, want 4 obs/2 dirs/1 audio root", report.Totals)
	}
	if report.Totals.KeptRows != 4 {
		t.Fatalf("kept rows = %d, want 4 whitelisted rows", report.Totals.KeptRows)
	}
	reasons := map[int64]string{}
	for _, entry := range report.Entries {
		reasons[entry.EntryID] = entry.Reason
	}
	if reasons[1] != "too_many_files" || reasons[3] != "empty_audio_card" {
		t.Fatalf("reasons = %+v", reasons)
	}
	skipped := map[int64]string{}
	for _, item := range report.Skipped {
		skipped[item.EntryID] = item.Reason
	}
	if skipped[5] != "series_member" || skipped[7] != "relocation_target" {
		t.Fatalf("skipped = %+v", skipped)
	}

	result, err := catalogprune.Apply(ctx, db, report, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(result.Batches))
	}
	if result.Totals.Entries != report.Totals.Entries ||
		result.Totals.Files != report.Totals.Files ||
		result.Totals.Observations != report.Totals.Observations ||
		result.Totals.Directories != report.Totals.Directories ||
		result.Totals.AudioRoots != report.Totals.AudioRoots ||
		result.Totals.TransferRefs != report.Totals.TransferRefs {
		t.Fatalf("apply totals = %+v, report = %+v", result.Totals, report.Totals)
	}

	var scanDirectories, scanObserved, scanPending int
	if err = db.QueryRow(`SELECT directories,observed,publish_pending_remaining FROM source_catalog_scan WHERE file_source_id=90`).
		Scan(&scanDirectories, &scanObserved, &scanPending); err != nil {
		t.Fatal(err)
	}
	if scanDirectories != 2 || scanObserved != 3 || scanPending != 2 {
		t.Fatalf("scan counters = dirs %d obs %d pending %d, want 2/3/2", scanDirectories, scanObserved, scanPending)
	}

	var entries, files, refs int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id IN (1,3)`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id IN (1,3)`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id IN (1,3)`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if entries != 0 || files != 0 || refs != 0 {
		t.Fatalf("deleted remnants: entries=%d files=%d refs=%d", entries, files, refs)
	}

	// Whitelisted child keeps its entry, audio files, observations, directory
	// rows and audio root.
	var wlFiles, wlAudio, wlObs, wlDirs, wlRoots int
	if err = db.QueryRow(`SELECT count(*), sum(CASE WHEN kind='audio' THEN 1 ELSE 0 END) FROM source_catalog_file WHERE catalog_entry_id=2`).Scan(&wlFiles, &wlAudio); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE directory_path='/bulk/audio'`).Scan(&wlObs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE path='/bulk/audio'`).Scan(&wlDirs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_audio_root WHERE catalog_root='/bulk/audio'`).Scan(&wlRoots); err != nil {
		t.Fatal(err)
	}
	if wlFiles != 2 || wlAudio != 2 || wlObs != 2 || wlDirs != 1 || wlRoots != 1 {
		t.Fatalf("whitelist damaged: files=%d audio=%d obs=%d dirs=%d roots=%d", wlFiles, wlAudio, wlObs, wlDirs, wlRoots)
	}

	var work, review, member, aliasTarget, aliasRows, seriesRows int
	if err = db.QueryRow(`SELECT count(*) FROM work WHERE id=1`).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM user_work_state WHERE user_id=1 AND work_id=1 AND rating=5 AND note='keep this review'`).Scan(&review); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM catalog_series_member WHERE series_id=1 AND catalog_entry_id=5`).Scan(&member); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=7 AND relocated_to_id IS NULL`).Scan(&aliasTarget); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=6 AND relocated_to_id=7`).Scan(&aliasRows); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM catalog_series WHERE id=1`).Scan(&seriesRows); err != nil {
		t.Fatal(err)
	}
	var seriesFiles, targetObs, targetDirs int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=5`).Scan(&seriesFiles); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE directory_path='/target'`).Scan(&targetObs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE path='/target'`).Scan(&targetDirs); err != nil {
		t.Fatal(err)
	}
	if work != 1 || review != 1 || member != 1 || aliasTarget != 1 || aliasRows != 1 || seriesRows != 1 ||
		seriesFiles != 11 || targetObs != 1 || targetDirs != 1 {
		t.Fatalf("retained references: work=%d review=%d member=%d target=%d alias=%d series=%d seriesFiles=%d targetObs=%d targetDirs=%d",
			work, review, member, aliasTarget, aliasRows, seriesRows, seriesFiles, targetObs, targetDirs)
	}

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key check is not empty after apply")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}

	// A rerun with the same reviewed report is a no-op: every planned entry is
	// already gone, which is what an interrupted apply leaves behind.
	second, err := catalogprune.Apply(ctx, db, report, options)
	if err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	if len(second.Batches) != 0 || second.AlreadyApplied != 2 {
		t.Fatalf("second apply = %+v, want 2 already applied and no batches", second)
	}
}

func TestCatalogPruneRefusesChangedReport(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPruneFixture(t, db)
	ctx := context.Background()
	options := catalogprune.Options{Whitelist: []string{"/bulk/audio"}, BatchSize: 100}
	report, err := catalogprune.BuildReport(ctx, db, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM source_catalog_file WHERE catalog_entry_id=1 AND relative_path='a.mp3'`); err != nil {
		t.Fatal(err)
	}
	if _, err = catalogprune.Apply(ctx, db, report, options); err == nil {
		t.Fatal("apply accepted a report that no longer matches the database")
	}
	var entries int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id IN (1,3)`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 2 {
		t.Fatalf("a refused apply deleted %d rows", 2-entries)
	}
}
