package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

const (
	catalogDailySettingPrefix = "source_catalog_daily:"
	catalogFullSettingPrefix  = "source_catalog_full:"
)

// The setting holds the next wall-clock boundary as Unix seconds. The fallback
// is only for rows created before the setting existed: it makes the update due
// immediately once, and dispatch writes the next local 03:00. It deliberately
// does not derive a new boundary by adding a rolling interval to finished_at.
const catalogDailyDueSQL = `COALESCE(CAST(setting.value_json AS INTEGER),CASE
	WHEN scan.pause_reason IN ('low_disk_space','disk_space_unknown') THEN unixepoch(scan.updated_at)+3600
	WHEN run.status IN ('succeeded','partial','failed','cancelled') THEN unixepoch(run.finished_at)
END)`

func (s *Server) catalogTime() time.Time {
	if s.catalogNow != nil {
		return s.catalogNow().UTC()
	}
	return time.Now().UTC()
}

// setCatalogDailyNextUpdateTx records the next local 03:00. It is used for every
// dispatch and for scan settlement: neither a resume nor an incremental sync
// starts a new full generation, so neither may restart the weekly clock.
func setCatalogDailyNextUpdateTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO app_setting(key,value_json) VALUES(?,?)
			ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogDailySettingPrefix+strconv.FormatInt(sourceID, 10), nextCatalogDailyDue(now).Unix())
	return err
}

// setCatalogDailyDue overrides the next daily tick. A maintenance hold or a
// low-disk pause uses it so the source is retried when the condition ends
// instead of waiting a full day.
func (s *Server) setCatalogDailyDue(ctx context.Context, sourceID int64, due time.Time) error {
	return setCatalogDailyDueAt(ctx, s.db, sourceID, due)
}

type appSettingExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func setCatalogDailyDueAt(ctx context.Context, exec appSettingExecer, sourceID int64, due time.Time) error {
	_, err := exec.ExecContext(ctx, `INSERT INTO app_setting(key,value_json) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogDailySettingPrefix+strconv.FormatInt(sourceID, 10), due.UTC().Unix())
	return err
}

// catalogScheduleDueTimes returns the persisted boundaries formatted in the
// schedule's own time zone, so an operator can verify the wall clock instead of
// inferring it from an epoch value.
func (s *Server) catalogScheduleDueTimes(ctx context.Context, sourceID int64) (string, string, error) {
	var daily, full sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
			(SELECT CAST(value_json AS INTEGER) FROM app_setting WHERE key=?),
			(SELECT CAST(value_json AS INTEGER) FROM app_setting WHERE key=?)`,
		catalogDailySettingPrefix+strconv.FormatInt(sourceID, 10),
		catalogFullSettingPrefix+strconv.FormatInt(sourceID, 10)).Scan(&daily, &full)
	if err != nil {
		return "", "", err
	}
	format := func(value sql.NullInt64) string {
		if !value.Valid {
			return ""
		}
		return time.Unix(value.Int64, 0).In(catalogScheduleLocation).Format(time.RFC3339)
	}
	return format(daily), format(full), nil
}

func setCatalogFullDueAt(ctx context.Context, exec appSettingExecer, sourceID int64, due time.Time) error {
	_, err := exec.ExecContext(ctx, `INSERT INTO app_setting(key,value_json) VALUES(?,?)
			ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogFullSettingPrefix+strconv.FormatInt(sourceID, 10), due.UTC().Unix())
	return err
}

// markCatalogFullGenerationTx records the next Sunday 03:00 when a new full
// generation is actually enqueued. The marker uses catalogTime, not
// CURRENT_TIMESTAMP, so a test or an operator clock and the weekly boundary
// agree.
func markCatalogFullGenerationTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	return setCatalogFullDueAt(ctx, tx, sourceID, nextCatalogWeeklyDue(now))
}

// The completed scan owns the first due date. Every later dispatch advances it
// in the same transaction as joining/resuming/creating the durable scan. A stop
// between dispatch and execution cannot lose the update or duplicate its run.
func (s *Server) dispatchDueCatalogUpdates(ctx context.Context) error {
	now := s.catalogTime()
	rows, err := s.db.QueryContext(ctx, `SELECT scan.file_source_id FROM source_catalog_scan scan
		JOIN file_source source ON source.id=scan.file_source_id
		JOIN workflow_run run ON run.id=scan.workflow_run_id
		LEFT JOIN app_setting setting ON setting.key=? || scan.file_source_id
			WHERE source.enabled=1 AND source.source_type=? AND `+catalogDailyDueSQL+`<=?
			AND NOT EXISTS(SELECT 1 FROM source_catalog_sync_run sync WHERE sync.file_source_id=scan.file_source_id AND sync.status='running')
		ORDER BY scan.file_source_id LIMIT 16`, catalogDailySettingPrefix, sourceTypeOpenList, now.Unix())
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	var result error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result = errors.Join(result, s.dispatchCatalogUpdate(ctx, id, now))
	}
	return result
}

// dispatchCatalogUpdate separates the trigger from scan completion. A completed
// generation and every partial generation receive a directory-first sync, so
// pending and failed ranges are retried without freezing changes in the
// branches that succeeded. A cancelled or failed run resumes its full
// checkpoint. The weekly due marker starts a new full generation instead of an
// incremental sync.
func (s *Server) dispatchCatalogUpdate(ctx context.Context, sourceID int64, now time.Time) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var saved string
	var due, fullDue sql.NullInt64
	var generation, directories int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT scan.options_json,`+catalogDailyDueSQL+`,
				scan.workflow_run_id,run.status,scan.directories,
			CAST(full_setting.value_json AS INTEGER)
		FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id
		JOIN file_source source ON source.id=scan.file_source_id
		LEFT JOIN app_setting setting ON setting.key=? || scan.file_source_id
		LEFT JOIN app_setting full_setting ON full_setting.key=? || scan.file_source_id
			WHERE scan.file_source_id=? AND source.enabled=1 AND source.source_type=?`, catalogDailySettingPrefix, catalogFullSettingPrefix, sourceID, sourceTypeOpenList).Scan(&saved, &due, &generation, &status, &directories, &fullDue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !due.Valid || due.Int64 > now.Unix() {
		return nil
	}
	if status == "queued" || status == "running" {
		return nil
	}
	var syncRunning int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_sync_run WHERE file_source_id=? AND status='running')`, sourceID).Scan(&syncRunning); err != nil {
		return err
	}
	if syncRunning != 0 {
		return nil
	}
	options := defaultCatalogIndexOptions()
	if err = json.Unmarshal([]byte(saved), &options); err != nil {
		return err
	}
	if err = options.validate(); err != nil {
		return err
	}

	// An upgraded database can have a full generation without the wall-clock
	// marker. Establish the next Sunday boundary once instead of inventing a
	// rolling seven-day interval from finished_at.
	if !fullDue.Valid {
		nextFull := nextCatalogWeeklyDue(now)
		if err = setCatalogFullDueAt(ctx, tx.Tx, sourceID, nextFull); err != nil {
			return err
		}
		fullDue = sql.NullInt64{Int64: nextFull.Unix(), Valid: true}
	}
	if fullDue.Valid && fullDue.Int64 <= now.Unix() {
		queued, queueErr := s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "interval", true)
		if queueErr != nil {
			if errors.Is(queueErr, errCatalogSyncRunning) {
				return nil
			}
			return queueErr
		}
		// Joining queued/running work does not complete the trigger. The
		// settlement path owns the next due date.
		if queued.Existing {
			return nil
		}
		if err = setCatalogDailyNextUpdateTx(ctx, tx.Tx, sourceID, now); err != nil {
			return err
		}
		return tx.Commit()
	}

	// A completed generation and every partial generation are probed directory
	// first. For a partial run, seeding marks its pending, failed, restricted
	// and unconfirmed-empty directories for listing while the same sync probes
	// the completed branches, so neither side waits for the other.
	if directories > 0 && (status == "succeeded" || status == "partial") {
		syncID, queueErr := enqueueCatalogSyncTx(ctx, tx.Tx, sourceID, false)
		if queueErr != nil {
			if errors.Is(queueErr, errCatalogIndexRunning) {
				return nil
			}
			return queueErr
		}
		if syncID == 0 {
			// Another sync won the lease. Leave the due marker untouched so
			// the next coordinator tick can observe its settlement.
			return nil
		}
		if err = setCatalogDailyNextUpdateTx(ctx, tx.Tx, sourceID, now); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if !s.Go(func(background context.Context) {
			_ = s.executeCatalogIncrementalSync(background, syncID, sourceID)
		}) {
			_ = s.finishCatalogSyncRun(context.WithoutCancel(ctx), syncID, "cancelled", catalogSyncRunState{ID: syncID, SourceID: sourceID, Status: "cancelled"})
		}
		return nil
	}

	// A cancelled or failed run resumes its saved full checkpoint. A generation
	// with no directory rows cannot be resumed and starts a fresh full
	// generation instead.
	_, queueErr := s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "interval", directories == 0)
	if queueErr != nil {
		if errors.Is(queueErr, errCatalogSyncRunning) {
			return nil
		}
		return queueErr
	}
	if err = setCatalogDailyNextUpdateTx(ctx, tx.Tx, sourceID, now); err != nil {
		return err
	}
	return tx.Commit()
}
