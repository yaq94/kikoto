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

func (s *Server) catalogTime() time.Time {
	if s.catalogNow != nil {
		return s.catalogNow().UTC()
	}
	return time.Now().UTC()
}

// setCatalogDailyNextUpdateTx advances only the daily tick. It is used for
// every dispatch and for scan settlement: neither a resume nor an incremental
// sync starts a new full generation, so neither may restart the weekly clock.
func setCatalogDailyNextUpdateTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO app_setting(key,value_json) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogDailySettingPrefix+strconv.FormatInt(sourceID, 10), now.Add(24*time.Hour).Unix())
	return err
}

// setCatalogDailyDue overrides the next daily tick. A maintenance hold or a
// low-disk pause uses it so the source is retried when the condition ends
// instead of waiting a full day.
func (s *Server) setCatalogDailyDue(ctx context.Context, sourceID int64, due time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_setting(key,value_json) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogDailySettingPrefix+strconv.FormatInt(sourceID, 10), due.UTC().Unix())
	return err
}

// markCatalogFullGenerationTx records when a new full generation was actually
// enqueued. The marker uses catalogTime, not CURRENT_TIMESTAMP, so a test or an
// operator clock and the weekly interval agree.
func markCatalogFullGenerationTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO app_setting(key,value_json) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogFullSettingPrefix+strconv.FormatInt(sourceID, 10), now.Unix())
	return err
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
		WHERE source.enabled=1 AND source.source_type=? AND
			-- A failed or cancelled run also owns a finished_at and a pause
			-- reason. It must still retry: its incomplete range otherwise
			-- keeps matching paused forever. A low-disk pause is different:
			-- it is not finished coverage and waits for the operator.
			(CASE WHEN scan.pause_reason='low_disk_space' THEN NULL ELSE COALESCE(CAST(setting.value_json AS INTEGER),CASE
				WHEN scan.pause_reason='' AND run.status IN ('succeeded','partial') THEN unixepoch(run.finished_at)+86400
				WHEN run.status IN ('failed','cancelled') THEN unixepoch(run.finished_at)+86400
			END) END)<=?
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

// dispatchCatalogUpdate only replaces a completed generation with an
// incremental sync. A cancelled, failed, partial or unconfirmed-empty
// generation is unfinished coverage: it keeps its own checkpoints, failed and
// restricted ranges must be re-observed, and a directory-first sync cannot
// fill a range the full crawl never reached. A generation older than a week, a
// generation with no completed directory, or an explicit operator force starts
// a new full generation.
func (s *Server) dispatchCatalogUpdate(ctx context.Context, sourceID int64, now time.Time) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var saved string
	var due, lastFull sql.NullInt64
	var generation int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT scan.options_json,
			(CASE WHEN scan.pause_reason='low_disk_space' THEN NULL ELSE COALESCE(CAST(setting.value_json AS INTEGER),CASE
				WHEN scan.pause_reason='' AND run.status IN ('succeeded','partial') THEN unixepoch(run.finished_at)+86400
				WHEN run.status IN ('failed','cancelled') THEN unixepoch(run.finished_at)+86400
			END) END),
			scan.workflow_run_id,run.status,
			CAST(full_setting.value_json AS INTEGER)
		FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id
		JOIN file_source source ON source.id=scan.file_source_id
		LEFT JOIN app_setting setting ON setting.key=? || scan.file_source_id
		LEFT JOIN app_setting full_setting ON full_setting.key=? || scan.file_source_id
		WHERE scan.file_source_id=? AND source.enabled=1 AND source.source_type=?`, catalogDailySettingPrefix, catalogFullSettingPrefix, sourceID, sourceTypeOpenList).Scan(&saved, &due, &generation, &status, &lastFull)
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
	options := defaultCatalogIndexOptions()
	if err = json.Unmarshal([]byte(saved), &options); err != nil {
		return err
	}
	if err = options.validate(); err != nil {
		return err
	}
	var directories int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=?", sourceID, generation).Scan(&directories); err != nil {
		return err
	}
	// A cancelled run may point at a generation with no directory rows because
	// it never started. That is a run to resume, not a reason to replace its
	// id. A succeeded generation with no rows, or an expired weekly marker,
	// does need a fresh full generation.
	expired := lastFull.Valid && now.Sub(time.Unix(lastFull.Int64, 0).UTC()) >= catalogSyncFullInterval
	forceFull := expired || (status == "succeeded" && (!lastFull.Valid || directories == 0))
	// The daily update is an incremental probe only for a generation that
	// finished the whole tree. Everything else is unfinished: a partial run
	// with failed or restricted ranges, a cancelled run, or a run whose only
	// gap is an unconfirmed empty directory. Those resume/restart their own
	// generation; the unconfirmed empty root is re-observed by that resume.
	incremental := !forceFull && status == "succeeded"
	if !incremental {
		if _, err = s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "interval", forceFull); err != nil {
			return err
		}
		if err = setCatalogDailyNextUpdateTx(ctx, tx.Tx, sourceID, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	syncID, err := enqueueCatalogSyncTx(ctx, tx.Tx, sourceID, false)
	if err != nil {
		return err
	}
	if err = setCatalogDailyNextUpdateTx(ctx, tx.Tx, sourceID, now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if syncID != 0 {
		if !s.Go(func(background context.Context) {
			_ = s.executeCatalogIncrementalSync(background, syncID, sourceID)
		}) {
			_ = s.finishCatalogSyncRun(context.WithoutCancel(ctx), syncID, "cancelled", catalogSyncRunState{ID: syncID, SourceID: sourceID, Status: "cancelled"})
		}
	}
	return nil
}
