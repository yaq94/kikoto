package httpapi

import (
	"context"
	"strings"
	"testing"
)

// A card above a directory that became its own entry keeps the folder row for
// that directory. Both forms of the real root are covered: a split card stores
// it in source_root, while a plain card stores it in root_path.
func TestCatalogCleanupRemovesFolderRowsThatBecameEntries(t *testing.T) {
	db := openMigratedTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(83), int64(803)
	job := seedCatalogCleanupJob(t, db, sourceID, runID)
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)

	for _, q := range []string{
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		 VALUES(83,'/creator',803,1,'/creator','/creator','complete'),
		       (83,'/creator/sub',803,1,'/creator','/creator','complete'),
		       (83,'/creator/sub/deeper',803,1,'/creator','/creator','complete')`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,last_seen_generation,discovered_generation)
		 VALUES(10,83,'/creator','','Creator',803,803),
		       (11,83,'/creator/sub::work','/creator/sub','Work',803,803),
		       (12,83,'/creator::direct','/creator','Direct',803,803),
		       (13,83,'/creator/sub/deeper','','Deeper',803,803)`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		 VALUES(10,'sub','sub','other',1,803),
		       (10,'sub/deeper','sub/deeper','other',1,803),
		       (10,'sub/keep.mp3','keep.mp3','audio',1,803),
		       (10,'other','other','other',1,803)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	scanned, removed, err := server.cleanupCatalogPublishBatch(ctx, job, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 4 || removed != 2 {
		t.Fatalf("cleanup scanned=%d removed=%d, want 4 and 2", scanned, removed)
	}

	rows, err := db.Query(`SELECT relative_path FROM source_catalog_file WHERE catalog_entry_id=10 ORDER BY relative_path`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	kept := []string{}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, path)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"other", "sub/keep.mp3"}
	if len(kept) != len(want) || kept[0] != want[0] || kept[1] != want[1] {
		t.Fatalf("kept rows = %v, want %v", kept, want)
	}
}

// The child lookup runs once per published card, so it must seek both forms of
// the real root. A plan that scans source_catalog_entry turns the cleanup pass
// into a table scan per card.
func TestCatalogCleanupChildLookupUsesRootIndexes(t *testing.T) {
	db := openMigratedTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN DELETE FROM source_catalog_file
		WHERE catalog_entry_id=? AND relative_path IN (
			SELECT substr(root_path,?) FROM source_catalog_entry
			WHERE file_source_id=? AND id<>? AND relocated_to_id IS NULL
				AND source_root='' AND root_path>=? AND root_path<?
			UNION ALL
			SELECT substr(source_root,?) FROM source_catalog_entry
			WHERE file_source_id=? AND id<>? AND relocated_to_id IS NULL
				AND source_root<>'' AND source_root>=? AND source_root<?
		)`, 1, 2, 3, 4, "/a/", "/a0", 2, 3, 4, "/a/", "/a0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	plan := []string{}
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err = rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	for _, index := range []string{"source_catalog_entry_live_root", "source_catalog_entry_source_root"} {
		if !strings.Contains(joined, index) {
			t.Fatalf("cleanup child plan does not use %s:\n%s", index, joined)
		}
	}
	if strings.Contains(joined, "SCAN source_catalog_entry") {
		t.Fatalf("cleanup child plan scans the entry table:\n%s", joined)
	}
}
