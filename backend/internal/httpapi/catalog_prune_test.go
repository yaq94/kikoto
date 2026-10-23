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
		`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000001','Retained work'),(2,'RJ00000002','Work-backed empty card')`,
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
				(7,90,'/target','Alias target',NULL,'available'),
				(8,90,'/work-backed-empty','Work-backed empty',2,'available')`,
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
				(90,'/target',900,1,'/target','/target','complete'),
				(90,'/work-backed-empty',900,1,'/work-backed-empty','/work-backed-empty','complete')`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending) VALUES
			(90,'/bulk','n1.jpg',1,'other',1,1),
			(90,'/bulk','n2.jpg',1,'other',1,1),
			(90,'/bulk','a.mp3',1,'audio',1,1),
			(90,'/bulk/audio','a1.mp3',1,'audio',1,1),
			(90,'/bulk/audio','a2.mp3',1,'audio',1,1),
			(90,'/other','a.mp3',1,'audio',1,1),
				(90,'/target','n.mp3',1,'other',1,1),
				(90,'/work-backed-empty','track.mp3',1,'audio',1,1)`,
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
	if report.Totals.Entries != 1 || report.Totals.Files != 0 || report.Totals.TransferRefs != 1 {
		t.Fatalf("report totals = %+v, want the work-backed empty card with one ref", report.Totals)
	}
	// Cards with audio are playable regardless of attachment count. The
	// source-only empty card is an unresolved transfer alias, while the series
	// member and relocation target are protected by their own references.
	if report.Candidates.Entries != 4 || report.Candidates.Files != 12 || report.Candidates.TransferRefs != 4 {
		t.Fatalf("candidate totals = %+v, want 4 cards/12 files/4 refs", report.Candidates)
	}
	if report.Totals.Observations != 1 || report.Totals.Directories != 1 || report.Totals.AudioRoots != 0 {
		t.Fatalf("path totals = %+v, want 1 obs/1 dir/0 audio roots", report.Totals)
	}
	if report.Totals.KeptRows != 0 {
		t.Fatalf("kept rows = %d, want 0 for the empty card", report.Totals.KeptRows)
	}
	reasons := map[int64]string{}
	for _, entry := range report.Entries {
		reasons[entry.EntryID] = entry.Reason
	}
	if len(reasons) != 1 || reasons[8] != "empty_audio_card" {
		t.Fatalf("reasons = %+v", reasons)
	}
	skipped := map[int64]string{}
	for _, item := range report.Skipped {
		skipped[item.EntryID] = item.Reason
	}
	if skipped[3] != "transfer_alias" || skipped[5] != "series_member" || skipped[7] != "relocation_target" {
		t.Fatalf("skipped = %+v", skipped)
	}

	result, err := catalogprune.Apply(ctx, db, report, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(result.Batches))
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
	if scanDirectories != 4 || scanObserved != 7 || scanPending != 5 {
		t.Fatalf("scan counters = dirs %d obs %d pending %d, want 4/7/5", scanDirectories, scanObserved, scanPending)
	}

	var deleted, kept, files, refs int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=8`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=3`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=8`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id=8`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || kept != 1 || files != 0 || refs != 0 {
		t.Fatalf("prune remnants: deleted=%d kept=%d files=%d refs=%d", deleted, kept, files, refs)
	}
	var sourceOnlyRefs int
	if err = db.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id=3`).Scan(&sourceOnlyRefs); err != nil {
		t.Fatal(err)
	}
	if sourceOnlyRefs != 1 {
		t.Fatalf("source-only alias refs=%d, want the card and its ref retained", sourceOnlyRefs)
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
	if len(second.Batches) != 0 || second.AlreadyApplied != 1 {
		t.Fatalf("second apply = %+v, want 1 already applied and no batches", second)
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

func TestCatalogPrunePreservesWorkBackedTransferAlias(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, statement := range []struct {
		name, sql string
	}{
		{"work", `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000001','Alias work')`},
		{"source", `INSERT INTO file_source(id,code,display_name,source_type) VALUES(90,'alias-source','Alias source','openlist')`},
		{"run", `INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
			VALUES(900,'source_catalog_index','Alias fixture','succeeded','manual')`},
		{"scan", `INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,directories,observed,publish_pending_remaining)
			VALUES(90,900,'{}',1,1,1)`},
		{"entry", `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status)
			VALUES(10,90,'/alias','Empty alias',1,'available')`},
		{"directory", `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(90,'/alias',900,1,'/alias','/alias','complete')`},
		{"observation", `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
			VALUES(90,'/alias','track.mp3',1,'audio',1,1)`},
	} {
		if _, err := db.Exec(statement.sql); err != nil {
			t.Fatalf("%s: %v", statement.name, err)
		}
	}
	var ref string
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=10`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	report, err := catalogprune.BuildReport(context.Background(), db, catalogprune.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Entries != 1 {
		t.Fatalf("report entries = %d, want the empty card", report.Totals.Entries)
	}
	if _, err = catalogprune.Apply(context.Background(), db, report, catalogprune.Options{}); err != nil {
		t.Fatal(err)
	}
	var workID int64
	var catalogID sql.NullInt64
	if err = db.QueryRow(`SELECT work_id,catalog_entry_id FROM personal_transfer_ref WHERE ref=?`, ref).Scan(&workID, &catalogID); err != nil {
		t.Fatalf("transfer alias was deleted: %v", err)
	}
	if workID != 1 || catalogID.Valid {
		t.Fatalf("transfer alias = work:%d catalog:%v, want work 1", workID, catalogID)
	}
}

// A source-only card has no surviving work that can inherit its opaque transfer
// reference. The card and alias must both stay present rather than deleting the
// only row that the reference can resolve through.
func TestCatalogPrunePreservesSourceOnlyTransferAlias(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, statement := range []struct {
		name, sql string
	}{
		{"source", `INSERT INTO file_source(id,code,display_name,source_type) VALUES(90,'alias-source','Alias source','openlist')`},
		{"run", `INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
			VALUES(900,'source_catalog_index','Alias fixture','succeeded','manual')`},
		{"scan", `INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,directories,observed,publish_pending_remaining)
			VALUES(90,900,'{}',1,1,1)`},
		{"entry", `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status)
			VALUES(11,90,'/source-only','Source-only alias',NULL,'available')`},
		{"directory", `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(90,'/source-only',900,1,'/source-only','/source-only','complete')`},
		{"observation", `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
			VALUES(90,'/source-only','track.mp3',1,'audio',1,1)`},
	} {
		if _, err := db.Exec(statement.sql); err != nil {
			t.Fatalf("%s: %v", statement.name, err)
		}
	}
	var ref string
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=11`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	report, err := catalogprune.BuildReport(context.Background(), db, catalogprune.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Entries != 0 || report.Candidates.Entries != 1 || report.Candidates.TransferRefs != 1 {
		t.Fatalf("report = totals %+v candidates %+v, want a skipped source-only alias", report.Totals, report.Candidates)
	}
	skipped := map[int64]string{}
	for _, item := range report.Skipped {
		skipped[item.EntryID] = item.Reason
	}
	if skipped[11] != "transfer_alias" {
		t.Fatalf("skipped = %+v, want transfer_alias", skipped)
	}
	result, err := catalogprune.Apply(context.Background(), db, report, catalogprune.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batches) != 0 || result.AlreadyApplied != 0 {
		t.Fatalf("apply = %+v, want no deletion for a source-only alias", result)
	}
	var entries, refs int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=11`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE ref=?`, ref).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || refs != 1 {
		t.Fatalf("source-only alias remnants: entry=%d ref=%d, want both retained", entries, refs)
	}
}
