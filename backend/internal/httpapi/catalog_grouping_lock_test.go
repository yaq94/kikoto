package httpapi

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
	"modernc.org/sqlite"
)

var groupingProbeDriver atomic.Int64

// A second independent SQLite connection observes when BEGIN IMMEDIATE can
// acquire the writer during Commit. WAL auto-checkpoint can continue after that
// lock has been released, so Commit's wall time alone is not lock-hold time.
func newCatalogGroupingLockProbe(t *testing.T, db *sql.DB) func() <-chan time.Time {
	t.Helper()
	var seq int
	var name, file string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	driver := fmt.Sprintf("grouping-lock-probe-%d", groupingProbeDriver.Add(1))
	sql.Register(driver, &sqlite.Driver{})
	probe, err := sql.Open(driver, file+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	probe.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = probe.Close() })
	return func() <-chan time.Time {
		done := make(chan time.Time, 1)
		go func() {
			for {
				_, err := probe.Exec(`BEGIN IMMEDIATE`)
				if err == nil {
					unlocked := time.Now()
					_, _ = probe.Exec(`ROLLBACK`)
					done <- unlocked
					return
				}
				if !sqlutil.IsBusy(err) {
					done <- time.Time{}
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		return done
	}
}
