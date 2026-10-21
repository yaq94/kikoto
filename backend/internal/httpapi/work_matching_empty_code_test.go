package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/storage"
)

func runMatchingEvaluation(t *testing.T, db *sql.DB, subject associationSubject) workMatchResult {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := evaluateWorkMatchTx(context.Background(), tx, subject)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A blank product code is absence of evidence, not an identity. Before the
// trim guard the equality arm matched every unnumbered work against every
// unnumbered entry. The fixture keeps the same shape as the reported database:
// 13 unnumbered works and tens of thousands of unnumbered entries.
func TestWorkMatchingBlankCodeDoesNotMatchUnnumberedWorks(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, entryCount = int64(90), 39042
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(90,'blank-code-source','Blank code source','openlist')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<13)
		INSERT INTO work(primary_code,title) SELECT '','Unnumbered work '||i FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO source_catalog_entry(file_source_id,root_path,primary_code,title)
		SELECT 90,'/blank/'||i,'','Blank card '||i FROM n`, entryCount); err != nil {
		t.Fatal(err)
	}
	var legacyRows int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_entry e CROSS JOIN work w
		WHERE e.primary_code='' AND w.primary_code=''`).Scan(&legacyRows); err != nil {
		t.Fatal(err)
	}
	// This is the raw equality join the defect produced. The reported 507,549
	// rows came from the same shape on the deployment database.
	if legacyRows != 13*entryCount {
		t.Fatalf("legacy equality join rows = %d, want %d", legacyRows, 13*entryCount)
	}
	result := runMatchingEvaluation(t, db, associationSubject{Kind: "catalog", ID: 1})
	t.Logf("blank-code candidates=%d legacy-equality-rows=%d", result.CandidateCount, legacyRows)
	if result.CandidateCount != 0 || len(result.Candidates) != 0 {
		t.Fatalf("blank code produced %d candidates (legacy join %d): %#v", result.CandidateCount, legacyRows, result)
	}
}

func TestWorkMatchingNonEmptyCodeStillMatches(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(91,'numbered-source','Numbered source','openlist');
		INSERT INTO work(id,primary_code,title) VALUES(501,'RJ00000501','Numbered work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title)
		VALUES(1,91,'/numbered','RJ00000501','A different card title')`); err != nil {
		t.Fatal(err)
	}
	result := runMatchingEvaluation(t, db, associationSubject{Kind: "catalog", ID: 1})
	if result.Status != "existing" || result.Rule != "product_code" || result.Target.ID != 501 {
		t.Fatalf("non-empty code result = %#v", result)
	}
}

func TestWorkMatchingBlankCodeKeepsExplicitWorkLink(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(92,'linked-source','Linked source','openlist');
		INSERT INTO work(id,primary_code,title) VALUES(601,'','Linked work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title,work_id)
		VALUES(1,92,'/linked','','Linked card',601)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	subject, err := matchingSubject(context.Background(), tx, associationSubject{Kind: "catalog", ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if subject.Kind != "work" || subject.ID != 601 {
		t.Fatalf("explicit link resolved to %#v", subject)
	}
	object, err := loadAssociationObject(context.Background(), tx, associationSubject{Kind: "catalog", ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if object.WorkID == nil || *object.WorkID != 601 || object.IdentityWorkID != 601 || object.Code != "" {
		t.Fatalf("explicit link object = %#v", object)
	}
}

func TestWorkMatchingWhitespaceCodeIsBlank(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(93,'space-source','Space source','openlist');
		INSERT INTO work(id,primary_code,title) VALUES(701,'   ','Padded work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title)
		VALUES(1,93,'/padded','   ','Unrelated card')`); err != nil {
		t.Fatal(err)
	}
	result := runMatchingEvaluation(t, db, associationSubject{Kind: "catalog", ID: 1})
	if result.CandidateCount != 0 || len(result.Candidates) != 0 || result.Status != "no_match" {
		t.Fatalf("whitespace code matched: %#v", result)
	}
}

func TestCatalogAssociationSnapshotDoesNotAdoptUnnumberedWork(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(94,'snapshot-source','Snapshot source','openlist');
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<13)
		INSERT INTO work(primary_code,title) SELECT '','Unnumbered '||i FROM n;
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title)
		VALUES(1,94,'/blank','','Blank card')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	object, err := loadAssociationObject(context.Background(), tx, associationSubject{Kind: "catalog", ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if object.WorkID != nil || object.IdentityWorkID != 0 {
		t.Fatalf("blank code adopted work %#v", object)
	}
}

func copyMigrationsThrough(t *testing.T, sourceDir string, maxVersion int) string {
	t.Helper()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "baseline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "compat"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(name, "%03d_", &version); err != nil || version > maxVersion {
			continue
		}
		if err := copyFile(filepath.Join(sourceDir, name), filepath.Join(target, name)); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func TestMigration089AllowsBlankCodesAndRejectsDuplicates(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES('','First manual'),('','Second manual')`); err != nil {
		t.Fatalf("empty codes must coexist: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES('RJ00000891','Numbered')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES('RJ00000891','Duplicate')`); err == nil {
		t.Fatal("duplicate non-empty code was accepted")
	} else if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestMigration089RejectsLegacyDuplicateBeforeIndex(t *testing.T) {
	for _, codes := range [][2]string{
		{"RJ00000892", "RJ00000892"},
		{" RJ00000892 ", "RJ00000892"},
	} {
		t.Run(strings.TrimSpace(codes[0])+"_"+strings.TrimSpace(codes[1]), func(t *testing.T) {
			sourceDir := filepath.Join("..", "..", "migrations")
			legacyDir := copyMigrationsThrough(t, sourceDir, 88)
			db, err := storage.Open(filepath.Join(t.TempDir(), "legacy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if err := storage.Migrate(db, legacyDir); err != nil {
				t.Fatalf("legacy migrate: %v", err)
			}
			if _, err := db.Exec(`DROP INDEX idx_work_numbered_code`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES(?,'One'),(?,'Two')`, codes[0], codes[1]); err != nil {
				t.Fatalf("seed legacy duplicates: %v", err)
			}
			if err := copyFile(filepath.Join(sourceDir, "089_work_primary_code_trimmed_unique.sql"), filepath.Join(legacyDir, "089_work_primary_code_trimmed_unique.sql")); err != nil {
				t.Fatal(err)
			}
			if err := storage.Migrate(db, legacyDir); err == nil {
				t.Fatal("migration accepted duplicate non-empty codes")
			} else if !strings.Contains(err.Error(), "duplicate non-empty work.primary_code") || !strings.Contains(err.Error(), "089") {
				t.Fatalf("duplicate migration error = %v", err)
			}
		})
	}
}

func TestMigration089NormalDataCreatesIndex(t *testing.T) {
	db := openMigratedTestDB(t)
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_work_primary_code_trimmed'`).Scan(&sqlText); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatal("089 unique index is missing")
		}
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "ON work(trim(primary_code))") || !strings.Contains(sqlText, "WHERE trim(primary_code) <> ''") {
		t.Fatalf("089 index = %s", sqlText)
	}
	if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES('RJ00000893','Normal')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES('  RJ00000894  ','Padded')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES('RJ00000894','Duplicate after trim')`); err == nil {
		t.Fatal("089 accepted a code that duplicates another code after trim")
	}
}
