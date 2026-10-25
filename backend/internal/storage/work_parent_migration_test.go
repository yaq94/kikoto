package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// The parent relation is schema, not an import-only convention: a fresh
// database and an upgraded one must expose the same nullable column and index.
func TestWorkParentMigrationCreatesColumnAndIndex(t *testing.T) {
	db := openMigrationManagerDB(t)
	if err := Migrate(db, completeMigrationDir(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var columnSQL string
	if err := db.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_schema WHERE type='table' AND name='work'`).Scan(&columnSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(columnSQL, "parent_work_id") {
		t.Fatalf("work schema is missing parent_work_id: %s", columnSQL)
	}
	var indexCount, foreignKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='idx_work_parent_work'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('work') WHERE "from"='parent_work_id' AND "table"='work' AND on_delete='RESTRICT'`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 || foreignKeys != 1 {
		t.Fatalf("work parent index=%d restrict foreign key=%d", indexCount, foreignKeys)
	}
}

// Only unambiguous catalog evidence is backfilled: both works are registered,
// the series work differs from the episode work, and the entry belongs to one
// series. Every other shape stays NULL for explicit review.
func TestWorkParentMigrationBackfillsOnlyUnambiguousMembership(t *testing.T) {
	sourceDir := filepath.Join("..", "..", "migrations")
	previous := copyNumberedMigrationsThrough(t, sourceDir, 99)
	db := openMigrationManagerDB(t)
	if err := Migrate(db, previous); err != nil {
		t.Fatalf("migrate through 099: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO work(id,primary_code,title) VALUES
			(1,'','Episode One'),(2,'','Series A'),(3,'','Episode Two'),
			(4,'','Episode Three'),(5,'','Series B'),(6,'','Series C'),(7,'','Episode Four'),
			(8,'','Series D')`,
		`INSERT INTO catalog_series(id,title,work_id) VALUES
			(10,'Series A',2),(11,'Series B',5),(12,'Series C',6),(13,'Unregistered series',NULL),
			(14,'Series D',8)`,
		`INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES
			(100,'/A/Episode 1','Episode One',1),
			(101,'/A/Episode 2','Episode Two',3),
			(102,'/A/Episode 3','Episode Three',4),
			(103,'/A/Episode 4','Episode Four',7),
			(105,'/A/Series D','Series D',8)`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(10,100,1,'directory'),
			(10,101,2,'directory'),
			(13,102,1,'directory'),
			(10,103,3,'directory'),
			(10,105,4,'directory')`,
		// One entry cannot belong to two series; the contradictory shape is an
		// episode work reachable from two different series works.
		`INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES
			(104,'/B/Episode Two','Episode Two',3)`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(11,104,1,'manual')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(db, completeMigrationDir(t)); err != nil {
		t.Fatalf("upgrade to 100: %v", err)
	}
	want := map[int64]sql.NullInt64{
		1: {Int64: 2, Valid: true},
		3: {}, // the entry belongs to two series
		4: {}, // the series has no registered work
		7: {Int64: 2, Valid: true},
		8: {}, // the member is itself a registered series work
	}
	for id, expected := range want {
		var got sql.NullInt64
		if err := db.QueryRow(`SELECT parent_work_id FROM work WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != expected {
			t.Fatalf("work %d parent = %v, want %v", id, got, expected)
		}
	}
}
