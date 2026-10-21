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

	"modernc.org/sqlite"
)

type playbackStatementStats struct {
	mu      sync.Mutex
	begins  int
	execs   int
	queries int
}

func (s *playbackStatementStats) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.begins, s.execs, s.queries = 0, 0, 0
}

func (s *playbackStatementStats) snapshot() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.begins, s.execs, s.queries
}

type playbackCountingDriver struct {
	inner *sqlite.Driver
	stats *playbackStatementStats
	dsn   string
}

func (d *playbackCountingDriver) Connect(context.Context) (driver.Conn, error) {
	conn, err := d.inner.Open(d.dsn)
	if err != nil {
		return nil, err
	}
	return &playbackCountingConn{Conn: conn, stats: d.stats}, nil
}

func (d *playbackCountingDriver) Driver() driver.Driver { return d.inner }

type playbackCountingConn struct {
	driver.Conn
	stats *playbackStatementStats
}

func (c *playbackCountingConn) Begin() (driver.Tx, error) {
	c.stats.mu.Lock()
	c.stats.begins++
	c.stats.mu.Unlock()
	return c.Conn.Begin()
}

func (c *playbackCountingConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.stats.mu.Lock()
	c.stats.begins++
	c.stats.mu.Unlock()
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		return begin.BeginTx(ctx, options)
	}
	return c.Conn.Begin()
}

func (c *playbackCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.stats.mu.Lock()
	c.stats.execs++
	c.stats.mu.Unlock()
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *playbackCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.stats.mu.Lock()
	c.stats.queries++
	c.stats.mu.Unlock()
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func openCountingPlaybackDB(t *testing.T) (*sql.DB, *playbackStatementStats) {
	t.Helper()
	image, err := migratedTestDatabaseImage()
	if err != nil {
		t.Fatalf("prepare schema image: %v", err)
	}
	path := filepath.Join(t.TempDir(), "playback.db")
	if err = os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	stats := &playbackStatementStats{}
	db := sql.OpenDB(&playbackCountingDriver{
		inner: &sqlite.Driver{},
		stats: stats,
		dsn:   path + "?_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)",
	})
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Ping(); err != nil {
		t.Fatalf("open counting database: %v", err)
	}
	return db, stats
}

func seedPlaybackCatalog(t *testing.T, db *sql.DB, entryID int64, paths []string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'playback_source','Playback Source','openlist')`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid')`); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(?, '', 'Playback Work')`, entryID); err != nil {
		t.Fatalf("seed work: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,primary_code,work_id) VALUES(?,7,?,'Playback Work','',?)`,
		entryID, fmt.Sprintf("/work/%d", entryID), entryID); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	for _, relative := range paths {
		if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(?,?,?,'audio',100)`, entryID, relative, filepath.Base(relative)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCatalogPlaybackReadPathSkipsWriteTransaction(t *testing.T) {
	db, stats := openCountingPlaybackDB(t)
	seedPlaybackCatalog(t, db, 1, []string{"01.mp3"})
	workID := int64(1)
	server := &Server{db: db}
	entry := catalogEntry{ID: 1, SourceID: 7, WorkID: &workID}
	if _, err := server.prepareCatalogPlaybackTargets(context.Background(), entry, []string{"01.mp3"}); err != nil {
		t.Fatal(err)
	}

	stats.reset()
	targets, err := server.prepareCatalogPlaybackTargets(context.Background(), entry, []string{"01.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	begins, execs, queries := stats.snapshot()
	if begins != 0 {
		t.Fatalf("read-only playback opened %d write transactions", begins)
	}
	if execs != 0 {
		t.Fatalf("read-only playback executed %d write statements", execs)
	}
	if queries == 0 {
		t.Fatal("read-only playback did not read the saved media rows")
	}
	if len(targets) != 1 || targets[0]["streamUrl"] != "/api/source-catalog/1/media?path=01.mp3" {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestCatalogPlaybackBatchesMissingRows(t *testing.T) {
	db, stats := openCountingPlaybackDB(t)
	paths := make([]string, 0, 256)
	for index := 0; index < 256; index++ {
		paths = append(paths, fmt.Sprintf("disc/track-%03d.mp3", index))
	}
	seedPlaybackCatalog(t, db, 80, paths)
	workID := int64(80)
	server := &Server{db: db}
	entry := catalogEntry{ID: 80, SourceID: 7, WorkID: &workID}

	stats.reset()
	targets, err := server.prepareCatalogPlaybackTargets(context.Background(), entry, paths)
	if err != nil {
		t.Fatal(err)
	}
	begins, execs, queries := stats.snapshot()
	if len(targets) != 256 {
		t.Fatalf("targets = %d, want 256", len(targets))
	}
	if begins != 1 {
		t.Fatalf("write path opened %d transactions, want 1", begins)
	}
	if execs > 2 {
		t.Fatalf("write path ran %d exec statements, want media + locations batched into 2", execs)
	}
	if queries > 6 {
		t.Fatalf("write path ran %d read statements, want at most 6", queries)
	}
	for index, target := range targets {
		want := "/api/source-catalog/80/media?path=" + strings.ReplaceAll(paths[index], "/", "%2F")
		if target["streamUrl"] != want {
			t.Fatalf("target %d stream = %v, want %s", index, target["streamUrl"], want)
		}
	}
	var media, locations int
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_item WHERE work_id=80`).Scan(&media); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_file_location WHERE file_source_id=7 AND location_type='catalog_stream'`).Scan(&locations); err != nil {
		t.Fatal(err)
	}
	if media != 256 || locations != 256 {
		t.Fatalf("persisted media/locations = %d/%d, want 256/256", media, locations)
	}
}
