package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// catalogStatementCounter records statements sent through one test pool.
type catalogStatementCounter struct {
	mu   sync.Mutex
	text []string
}

func (c *catalogStatementCounter) add(query string) {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "PRAGMA") {
		return
	}
	c.mu.Lock()
	c.text = append(c.text, query)
	c.mu.Unlock()
}

func (c *catalogStatementCounter) reset() {
	c.mu.Lock()
	c.text = nil
	c.mu.Unlock()
}

func (c *catalogStatementCounter) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.text...)
}

type catalogCountingConnector struct {
	base    driver.Driver
	dsn     string
	counter *catalogStatementCounter
}

func (c *catalogCountingConnector) Driver() driver.Driver { return c.base }

func (c *catalogCountingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &catalogCountingConn{Conn: conn, counter: c.counter}, nil
}

type catalogCountingConn struct {
	driver.Conn
	counter *catalogStatementCounter
}

func (c *catalogCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.add(query)
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *catalogCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.add(query)
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func (c *catalogCountingConn) Prepare(query string) (driver.Stmt, error) {
	c.counter.add(query)
	return c.Conn.Prepare(query)
}

func openCatalogCountingTestDB(t testing.TB) (*sql.DB, *catalogStatementCounter) {
	t.Helper()
	image, err := migratedTestDatabaseImage()
	if err != nil {
		t.Fatalf("prepare test database: %v", err)
	}
	path := filepath.Join(t.TempDir(), "test.db")
	if err = os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	base, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = base.Close() }()
	counter := &catalogStatementCounter{}
	db := sql.OpenDB(&catalogCountingConnector{base: base.Driver(), dsn: dsn, counter: counter})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, counter
}

// The observation arm must seek the pending index by directory_path. A scan of
// source_catalog_observation turns every lifecycle check into O(pending).
func TestCatalogRootIncompleteSeeksPendingRange(t *testing.T) {
	db := openMigratedTestDB(t)
	query, args := catalogRootIncompleteQuery(7, 9, "/Example Work")
	plan := queryPlan(t, db, query, args...)
	joined := strings.Join(plan, "\n")
	t.Log(joined)
	if !strings.Contains(joined, "source_catalog_observation_pending") {
		t.Fatalf("observation arm does not use the pending index:\n%s", joined)
	}
	for _, scan := range []string{"SCAN o", "SCAN source_catalog_observation"} {
		if strings.Contains(joined, scan) {
			t.Fatalf("observation arm still scans pending rows (%s):\n%s", scan, joined)
		}
	}
}

func TestCatalogCleanupBatchMatchesPerCardSemantics(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(93), int64(903)
	job := seedCatalogCleanupJob(t, db, sourceID, runID)
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)

	for _, statement := range []string{
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		 VALUES(93,'/a',903,1,'/a','/a','complete'),
		       (93,'/b',903,1,'/b','/b','complete'),
		       (93,'/c',903,1,'/c','/c','complete'),
		       (93,'/c/sub',903,1,'/c','/c','failed'),
		       (93,'/d',903,1,'/d','/d','complete'),
		       (93,'/d/sub',903,1,'/d/sub','/d/sub','complete')`,
		`INSERT INTO work(id,primary_code,title) VALUES(99,'RJBATCH','Batch work')`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,work_id,last_seen_generation,discovered_generation)
		 VALUES(20,93,'/a','','A',NULL,903,903),
		       (21,93,'/b','','B',99,903,903),
		       (22,93,'/c','','C',NULL,903,903),
		       (23,93,'/d','','D',NULL,903,903),
		       (24,93,'/d/sub::work','/d/sub','Sub',NULL,903,903)`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		 VALUES(20,'old.mp3','old.mp3','audio',1,900),
		       (20,'new.mp3','new.mp3','audio',1,903),
		       (21,'old.mp3','old.mp3','audio',1,900),
		       (21,'new.mp3','new.mp3','audio',1,903),
		       (22,'old.mp3','old.mp3','audio',1,900),
		       (23,'sub','sub','other',1,903),
		       (23,'other','other','other',1,903),
		       (23,'old.mp3','old.mp3','audio',1,900),
		       (23,'new.mp3','new.mp3','audio',1,903),
		       (24,'new.mp3','new.mp3','audio',1,903)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	counter.reset()
	scanned, removed, err := server.cleanupCatalogPublishBatch(ctx, job, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 5 || removed != 4 {
		t.Fatalf("cleanup scanned=%d removed=%d, want 5 and 4", scanned, removed)
	}
	statements := counter.snapshot()
	assertCatalogFileRows := func(entryID int64, want ...string) {
		t.Helper()
		rows, queryErr := db.Query(`SELECT relative_path FROM source_catalog_file WHERE catalog_entry_id=? ORDER BY relative_path`, entryID)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		defer func() { _ = rows.Close() }()
		got := []string{}
		for rows.Next() {
			var relative string
			if queryErr = rows.Scan(&relative); queryErr != nil {
				t.Fatal(queryErr)
			}
			got = append(got, relative)
		}
		if queryErr = rows.Err(); queryErr != nil {
			t.Fatal(queryErr)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("entry %d kept %v, want %v", entryID, got, want)
		}
	}
	assertCatalogFileRows(20, "new.mp3")
	assertCatalogFileRows(21, "new.mp3")
	assertCatalogFileRows(22, "old.mp3")
	assertCatalogFileRows(23, "new.mp3", "other")
	assertCatalogFileRows(24, "new.mp3")
	if len(statements) > 9 {
		t.Fatalf("cleanup used %d statements for 5 cards; batch regressed:\n%s", len(statements), strings.Join(statements, "\n"))
	}
}

func TestSetCatalogEntryStatusBatchMatchesSequential(t *testing.T) {
	for _, statuses := range [][]catalogEntryStatusUpdate{
		{{ID: 40, Status: "missing"}, {ID: 41, Status: "available"}},
		{{ID: 40, Status: "missing"}, {ID: 41, Status: "restricted"}},
	} {
		t.Run(statuses[0].Status+"_"+statuses[1].Status, func(t *testing.T) {
			batchDB := openMigratedTestDB(t)
			sequentialDB := openMigratedTestDB(t)
			for _, db := range []*sql.DB{batchDB, sequentialDB} {
				seedStatusFixture(t, db)
			}
			batchTx, err := batchDB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = setCatalogEntryStatusesTx(context.Background(), batchTx, statuses); err != nil {
				t.Fatal(err)
			}
			if err = batchTx.Commit(); err != nil {
				t.Fatal(err)
			}
			sequentialTx, err := sequentialDB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for _, update := range statuses {
				if err = setCatalogEntryStatusTx(context.Background(), sequentialTx, update.ID, update.Status); err != nil {
					t.Fatal(err)
				}
			}
			if err = sequentialTx.Commit(); err != nil {
				t.Fatal(err)
			}
			if got, want := readStatusState(t, batchDB), readStatusState(t, sequentialDB); got != want {
				t.Fatalf("batch state\n%s\nsequential state\n%s", got, want)
			}
		})
	}
}

func seedStatusFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO file_source(id,code,display_name,source_type) VALUES(71,'status-source','Status source','openlist')`,
		`INSERT INTO work(id,primary_code,title) VALUES(70,'RJSTATUS','Status work')`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status,last_seen_generation,discovered_generation)
		 VALUES(40,71,'/status','Status',70,'available',1,1),
		       (41,71,'/status-2','Status 2',70,'available',1,1)`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 VALUES(40,'a.mp3','a.mp3','audio',1),(41,'b.mp3','b.mp3','audio',1)`,
		`INSERT INTO media_item(id,work_id,kind,title,fingerprint)
		 VALUES(400,70,'audio','a.mp3','catalog:40:a.mp3'),(401,70,'audio','b.mp3','catalog:41:b.mp3')`,
		`INSERT INTO media_file_location(media_item_id,file_source_id,location_type,path,availability)
		 VALUES(400,71,'catalog_stream','a.mp3','available'),(401,71,'catalog_stream','b.mp3','available')`,
		`INSERT INTO work_source_presence(work_id,file_source_id,presence_type,availability)
		 VALUES(70,71,'source','available')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func readStatusState(t *testing.T, db *sql.DB) string {
	t.Helper()
	parts := []string{}
	rows, err := db.Query(`SELECT id,availability_status FROM source_catalog_entry WHERE id IN (40,41) ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var status string
		if err = rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("entry:%d=%s", id, status))
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	rows, err = db.Query(`SELECT media_item_id,availability FROM media_file_location ORDER BY media_item_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var status string
		if err = rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("location:%d=%s", id, status))
	}
	_ = rows.Close()
	var presence string
	if err = db.QueryRow(`SELECT availability FROM work_source_presence WHERE work_id=70 AND file_source_id=71`).Scan(&presence); err != nil {
		t.Fatal(err)
	}
	return strings.Join(append(parts, "presence="+presence), ";")
}

func TestPurgeStaleCatalogPendingSkipsCleanQueueEveryStep(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(94), int64(904)
	job := seedCatalogCleanupJob(t, db, sourceID, runID)
	if _, err := db.Exec(`CREATE TABLE purge_probe (rows INTEGER);
		CREATE TRIGGER probe_purge_update AFTER UPDATE ON source_catalog_observation
		BEGIN INSERT INTO purge_probe(rows) VALUES(1); END`); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	counter.reset()
	if err := server.purgeStaleCatalogPending(ctx, job, sourceID); err != nil {
		t.Fatal(err)
	}
	if first := len(counter.snapshot()); first == 0 {
		t.Fatal("first check for a generation did not inspect the queue")
	}
	counter.reset()
	if err := server.purgeStaleCatalogPending(ctx, job, sourceID); err != nil {
		t.Fatal(err)
	}
	if statements := counter.snapshot(); len(statements) != 0 {
		t.Fatalf("a clean generation still queried on the next step:\n%s", strings.Join(statements, "\n"))
	}
	var updates int
	if err := db.QueryRow(`SELECT COUNT(*) FROM purge_probe`).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("empty queue executed %d observation updates", updates)
	}
}

func TestCatalogRangeUncertainSetMatchesSingle(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, generation = int64(95), int64(905)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(95,'uncertain-set','Uncertain set','openlist');
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status,error_code)
		VALUES(95,'/',905,1,'','','complete',''),
		      (95,'/ok',905,1,'/ok','/ok','complete',''),
		      (95,'/ok/sub',905,1,'/ok','/ok','complete',''),
		      (95,'/gone',905,1,'/gone','/gone','empty','not_found'),
		      (95,'/failed',905,1,'/failed','/failed','complete',''),
		      (95,'/failed/child',905,1,'/failed','/failed','failed','')`); err != nil {
		t.Fatal(err)
	}
	bases := []string{"/", "/ok", "/ok/sub", "/gone", "/failed", "/failed/child", "/never-listed"}
	plan := queryPlan(t, db, catalogRangeUncertainSetSQL(len(bases)),
		"/", "/ok", "/ok/sub", "/gone", "/failed", "/failed/child", "/never-listed", sourceID, generation)
	planText := strings.Join(plan, " | ")
	t.Log(planText)
	if !strings.Contains(planText, "source_catalog_directory_pending") {
		t.Fatalf("uncertainty set does not use the pending index: %s", planText)
	}
	if strings.Contains(planText, "SCAN source_catalog_directory") {
		t.Fatalf("uncertainty set scans the directory table: %s", planText)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	batch, err := catalogRangeUncertainSetTx(context.Background(), tx, sourceID, generation, bases)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range bases {
		want, err := catalogRangeUncertain(context.Background(), tx, sourceID, generation, base)
		if err != nil {
			t.Fatal(err)
		}
		if batch[base] != want {
			t.Fatalf("base %q batch uncertain=%v, single=%v", base, batch[base], want)
		}
	}
}

// BenchmarkCatalogRootIncompletePending compares the half-open observation
// range with the former substr predicate against 20,000 unrelated pending rows.
func BenchmarkCatalogRootIncompletePending(b *testing.B) {
	db, _ := openCatalogCountingTestDB(b)
	const sourceID, generation = int64(96), int64(906)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(96,'pending-bench','Pending bench','openlist');
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		VALUES(96,'/target',906,1,'/target','/target','complete'),
		      (96,'/noise',906,1,'/noise','/noise','complete');
		INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(96,906,'/target');
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		VALUES(96,'/target','pending.mp3',1,'audio',100,1);
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<20000)
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		SELECT 96,'/noise',printf('%05d.mp3',i),1,'audio',100,1 FROM n`); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	oldQuery := `SELECT EXISTS(
		SELECT 1 FROM source_catalog_observation o
		JOIN source_catalog_directory pd ON pd.file_source_id=o.file_source_id AND pd.path=o.directory_path AND o.revision=pd.revision
		WHERE o.file_source_id=? AND pd.generation=? AND o.publish_pending=1
			AND NOT (pd.status='empty' AND pd.error_code='not_found')
			AND (o.kind='audio' OR EXISTS(SELECT 1 FROM source_catalog_audio_root r
				WHERE r.file_source_id=pd.file_source_id AND r.generation=pd.generation AND r.catalog_root=pd.catalog_root))
			AND (?='/' OR pd.catalog_root=? OR pd.path=? OR substr(pd.path,1,length(?)+1)=? || '/'))`
	run := func(query string, args ...any) {
		var found int
		if err := tx.QueryRow(query, args...).Scan(&found); err != nil {
			b.Fatal(err)
		}
		if found != 1 {
			b.Fatal("target pending row not found")
		}
	}
	query, args := catalogRootIncompleteQuery(sourceID, generation, "/target")
	b.Run("range", func(b *testing.B) {
		for range b.N {
			run(query, args...)
		}
	})
	oldArgs := []any{sourceID, generation, "/target", "/target", "/target", "/target", "/target"}
	b.Run("legacy", func(b *testing.B) {
		for range b.N {
			run(oldQuery, oldArgs...)
		}
	})
}
