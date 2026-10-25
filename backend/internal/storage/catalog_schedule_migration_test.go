package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The schedule migration is the only place the old source_catalog_daily: and
// source_catalog_full: settings are read. A valid future boundary is retained,
// an expired or malformed value falls back to the next local boundary, and
// only enabled OpenList sources receive a row.
func TestCatalogScheduleMigrationCarriesValidBoundariesAndFallsBack(t *testing.T) {
	sourceDir := filepath.Join("..", "..", "migrations")
	previous := copyNumberedMigrationsThrough(t, sourceDir, 84)
	db := openMigrationManagerDB(t)
	if err := Migrate(db, previous); err != nil {
		t.Fatalf("create 084 database: %v", err)
	}
	validDaily := time.Now().Add(30 * 24 * time.Hour).Unix()
	validWeekly := time.Now().Add(60 * 24 * time.Hour).Unix()
	expired := time.Now().Add(-48 * time.Hour).Unix()
	seeds := []struct {
		id                      int64
		sourceType              string
		enabled                 int
		daily, weekly, dailyRun string
	}{
		{id: 1, sourceType: "openlist", enabled: 1, daily: fmt.Sprint(validDaily), weekly: fmt.Sprint(validWeekly), dailyRun: fmt.Sprint(expired)},
		{id: 2, sourceType: "openlist", enabled: 1, daily: fmt.Sprint(expired), weekly: "not-a-time"},
		{id: 3, sourceType: "openlist", enabled: 0, daily: fmt.Sprint(validDaily), weekly: fmt.Sprint(validWeekly)},
		{id: 4, sourceType: "kikoeru_compatible", enabled: 1, daily: fmt.Sprint(validDaily), weekly: fmt.Sprint(validWeekly)},
	}
	for _, seed := range seeds {
		if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(?,?,?,?,?)`,
			seed.id, fmt.Sprintf("source_%d", seed.id), "Source", seed.sourceType, seed.enabled); err != nil {
			t.Fatal(err)
		}
		if seed.daily != "" {
			if _, err := db.Exec(`INSERT INTO app_setting(key,value_json) VALUES(?,?)`,
				fmt.Sprintf("source_catalog_daily:%d", seed.id), `"`+seed.daily+`"`); err != nil {
				t.Fatal(err)
			}
		}
		if seed.weekly != "" {
			if _, err := db.Exec(`INSERT INTO app_setting(key,value_json) VALUES(?,?)`,
				fmt.Sprintf("source_catalog_full:%d", seed.id), `"`+seed.weekly+`"`); err != nil {
				t.Fatal(err)
			}
		}
		if seed.dailyRun != "" {
			if _, err := db.Exec(`INSERT INTO app_setting(key,value_json) VALUES(?,?)`,
				fmt.Sprintf("source_catalog_daily_run:%d", seed.id), `"`+seed.dailyRun+`"`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Migrate(db, sourceDir); err != nil {
		t.Fatalf("upgrade to schedule schema: %v", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_schedule`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("schedule rows=%d, want the two enabled OpenList sources", rows)
	}
	var daily, weekly, deferred int64
	var reason string
	var manual, resume int
	var pendingFull sql.NullInt64
	if err := db.QueryRow(`SELECT daily_due_at,weekly_due_at,deferred_until,deferred_reason,manual_pending,resume_full_pending,pending_full_run_id
		FROM source_catalog_schedule WHERE file_source_id=1`).
		Scan(&daily, &weekly, &deferred, &reason, &manual, &resume, &pendingFull); err != nil {
		t.Fatal(err)
	}
	if daily != validDaily || weekly != validWeekly {
		t.Fatalf("valid boundaries changed: daily=%d weekly=%d, want %d/%d", daily, weekly, validDaily, validWeekly)
	}
	if deferred != 0 || reason != "" || manual != 0 || resume != 0 || pendingFull.Valid {
		t.Fatalf("migrated row is not idle: deferred=%d reason=%q manual=%d resume=%d pending=%v", deferred, reason, manual, resume, pendingFull)
	}
	if err := db.QueryRow(`SELECT daily_due_at,weekly_due_at FROM source_catalog_schedule WHERE file_source_id=2`).
		Scan(&daily, &weekly); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	dailyLocal := time.Unix(daily, 0).In(location)
	weeklyLocal := time.Unix(weekly, 0).In(location)
	if !time.Unix(daily, 0).After(now) || dailyLocal.Hour() != 3 || dailyLocal.Minute() != 0 {
		t.Fatalf("fallback daily=%s is not the next local 03:00", dailyLocal.Format(time.RFC3339))
	}
	if !time.Unix(weekly, 0).After(now) || weeklyLocal.Hour() != 3 || weeklyLocal.Weekday() != time.Sunday {
		t.Fatalf("fallback weekly=%s is not the next local Sunday 03:00", weeklyLocal.Format(time.RFC3339))
	}

	// A second startup validates the ledger without rewriting the schedule.
	beforeDaily, beforeWeekly := daily, weekly
	if err := Migrate(db, sourceDir); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	if err := db.QueryRow(`SELECT daily_due_at,weekly_due_at FROM source_catalog_schedule WHERE file_source_id=2`).
		Scan(&daily, &weekly); err != nil {
		t.Fatal(err)
	}
	if daily != beforeDaily || weekly != beforeWeekly {
		t.Fatalf("restart rewrote the schedule: %d/%d -> %d/%d", beforeDaily, beforeWeekly, daily, weekly)
	}
}
