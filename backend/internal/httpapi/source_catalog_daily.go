package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

// catalogMaintenanceActiveTx reads the deployment hold through the dispatch
// transaction. Reading it through s.db while the single writer is held would
// need a second pooled connection and can deadlock a one-connection test or a
// small production pool.
func catalogMaintenanceActiveTx(ctx context.Context, tx *sql.Tx, now time.Time) (bool, time.Time, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value_json FROM app_setting WHERE key=?`, catalogMaintenanceSetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	var state catalogMaintenanceState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		// A malformed hold must not block indexing forever.
		return false, time.Time{}, nil
	}
	if state.Until.IsZero() || !state.Until.After(now) {
		return false, time.Time{}, nil
	}
	return true, state.Until, nil
}

// dispatchDueCatalogUpdates selects at most 16 sources whose persisted
// schedule is actionable. Each source is then decided in its own transaction,
// so one busy or broken source cannot hold the others' write lock.
func (s *Server) dispatchDueCatalogUpdates(ctx context.Context) error {
	now := s.catalogTime()
	rows, err := s.db.QueryContext(ctx, `SELECT schedule.file_source_id
		FROM source_catalog_schedule schedule
		JOIN file_source source ON source.id=schedule.file_source_id
		JOIN source_catalog_scan scan ON scan.file_source_id=schedule.file_source_id
		WHERE source.enabled=1 AND source.source_type=?
			AND (schedule.deferred_until=0 OR schedule.deferred_until<=?)
			AND (schedule.manual_pending=1 OR schedule.resume_full_pending=1
				OR schedule.daily_due_at<=? OR schedule.weekly_due_at<=?)
		ORDER BY schedule.file_source_id LIMIT 16`,
		sourceTypeOpenList, now.Unix(), now.Unix(), now.Unix())
	if err != nil {
		return err
	}
	ids := []int64{}
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

// dispatchCatalogUpdate is the single-transaction scheduling state machine.
// It reads the source, schedule, generation and sync state once, decides at
// most one action, and advances the due boundary in the same transaction as a
// successful enqueue. A rollback therefore leaves the schedule untouched.
func (s *Server) dispatchCatalogUpdate(ctx context.Context, sourceID int64, now time.Time) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()

	var enabled bool
	var sourceType string
	err = tx.QueryRowContext(ctx, `SELECT enabled,source_type FROM file_source WHERE id=?`, sourceID).
		Scan(&enabled, &sourceType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !enabled || sourceType != sourceTypeOpenList {
		return nil
	}
	schedule, err := loadCatalogScheduleTx(ctx, tx.Tx, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		if err = ensureCatalogScheduleTx(ctx, tx.Tx, sourceID, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if schedule.DeferredUntil > now.Unix() {
		return nil
	}

	var optionsJSON string
	var generation int64
	var runStatus string
	var directories int
	err = tx.QueryRowContext(ctx, `SELECT scan.options_json,scan.workflow_run_id,run.status,scan.directories
		FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id
		WHERE scan.file_source_id=?`, sourceID).Scan(&optionsJSON, &generation, &runStatus, &directories)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	options := defaultCatalogIndexOptions()
	if err = jsonUnmarshalCatalogOptions(optionsJSON, &options); err != nil {
		return err
	}
	if err = options.validate(); err != nil {
		return err
	}
	fullRunning := runStatus == "queued" || runStatus == "running"
	syncRunning, err := catalogIncrementalSyncRunningTx(ctx, tx.Tx, sourceID)
	if err != nil {
		return err
	}
	if fullRunning || syncRunning {
		// A running sync cannot create a full generation, so a weekly boundary
		// that came due during it becomes one pending full. A running full is
		// registered as that pending run: its success advances the weekly
		// boundary, and a failure leaves one retryable bit instead of a second
		// full generation on the next tick. Manual requests stay queued.
		if schedule.WeeklyDueAt <= now.Unix() && !schedule.ResumeFull {
			if err = setCatalogResumeFullPendingTx(ctx, tx.Tx, sourceID); err != nil {
				return err
			}
			if fullRunning {
				if err = setCatalogPendingFullRunTx(ctx, tx.Tx, sourceID, generation); err != nil {
					return err
				}
			}
		}
		return tx.Commit()
	}

	// A deferred source re-checks the actual condition when the deadline
	// expires. A hold or a disk shortfall that is still present re-arms the
	// same window; the due boundaries, pending bits and checkpoints stay.
	if schedule.DeferredUntil != 0 {
		reason, until, blocked, holdErr := s.catalogResourceBlock(ctx, tx.Tx, options, now)
		if holdErr != nil {
			return holdErr
		}
		if blocked {
			if err = setCatalogScheduleDeferredTx(ctx, tx.Tx, sourceID, until, reason); err != nil {
				return err
			}
			return tx.Commit()
		}
		if err = clearCatalogScheduleDeferredTx(ctx, tx.Tx, sourceID); err != nil {
			return err
		}
		schedule.DeferredUntil = 0
		schedule.DeferredReason = ""
	}

	effectiveDue := schedule.DailyDueAt
	if schedule.WeeklyDueAt < effectiveDue {
		effectiveDue = schedule.WeeklyDueAt
	}
	actionable := schedule.ResumeFull || schedule.ManualPending || effectiveDue <= now.Unix()
	if !actionable {
		return tx.Commit()
	}

	// A hold or a disk shortfall defers the whole schedule by one hour. The
	// original boundaries and pending bits stay in place, so recovery catches
	// up once instead of replaying every missed boundary. The write happens
	// only when there is actionable work; a deferred source returns above until
	// the deadline, so repeated coordinator ticks cannot extend the window.
	reason, until, blocked, holdErr := s.catalogResourceBlock(ctx, tx.Tx, options, now)
	if holdErr != nil {
		return holdErr
	}
	if blocked {
		if err = setCatalogScheduleDeferredTx(ctx, tx.Tx, sourceID, until, reason); err != nil {
			return err
		}
		return tx.Commit()
	}

	// A resume full is complete only when the run it created succeeds. A
	// queued or running run keeps the marker; a failed one is forgotten so the
	// next tick creates a replacement; success advances the weekly boundary.
	if schedule.ResumeFull {
		if schedule.PendingFullRunID.Valid {
			var pendingStatus string
			err = tx.QueryRowContext(ctx, `SELECT status FROM workflow_run WHERE id=?`, schedule.PendingFullRunID.Int64).Scan(&pendingStatus)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if err = clearCatalogPendingFullRunTx(ctx, tx.Tx, sourceID); err != nil {
					return err
				}
				return tx.Commit()
			case err != nil:
				return err
			case pendingStatus == "queued" || pendingStatus == "running":
				return tx.Commit()
			case pendingStatus == "succeeded":
				if err = advanceCatalogScheduleTx(ctx, tx.Tx, sourceID, "weekly", now); err != nil {
					return err
				}
				return tx.Commit()
			default:
				if err = clearCatalogPendingFullRunTx(ctx, tx.Tx, sourceID); err != nil {
					return err
				}
				return tx.Commit()
			}
		}
		queued, queueErr := s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "interval", true)
		if queueErr != nil {
			return queueErr
		}
		if queued.Existing {
			return tx.Commit()
		}
		if err = setCatalogPendingFullRunTx(ctx, tx.Tx, sourceID, queued.RunID); err != nil {
			return err
		}
		// The full crawl covers a manual refresh requested in the same round;
		// consuming it here prevents a second writer after the full finishes.
		if schedule.ManualPending {
			if err = clearCatalogManualPendingTx(ctx, tx.Tx, sourceID); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	dailyDue := schedule.DailyDueAt <= now.Unix()
	weeklyDue := schedule.WeeklyDueAt <= now.Unix()
	if !dailyDue && !weeklyDue {
		if !schedule.ManualPending {
			return tx.Commit()
		}
		return s.dispatchCatalogManual(ctx, tx, sourceID, directories, options)
	}
	// Both boundaries due on the same tick create one task. The weekly
	// boundary is the earlier or equal one and wins.
	weeklySelected := weeklyDue && schedule.WeeklyDueAt <= schedule.DailyDueAt
	// A failed, partial or cancelled generation is first repaired by a
	// directory-first sync. A due weekly boundary is remembered, not turned into
	// a second writer: the full generation starts after this sync released its
	// lease. Only a generation with no reusable directories needs a full.
	if weeklySelected && directories > 0 && runStatus != "succeeded" {
		if err = setCatalogResumeFullPendingTx(ctx, tx.Tx, sourceID); err != nil {
			return err
		}
		if schedule.ManualPending {
			if err = clearCatalogManualPendingTx(ctx, tx.Tx, sourceID); err != nil {
				return err
			}
		}
		return s.dispatchCatalogSync(ctx, tx, sourceID)
	}
	if weeklySelected {
		queued, queueErr := s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "interval", true)
		if queueErr != nil {
			return queueErr
		}
		if queued.Existing {
			return tx.Commit()
		}
		if err = advanceCatalogScheduleTx(ctx, tx.Tx, sourceID, "weekly", now); err != nil {
			return err
		}
		if schedule.ManualPending {
			if err = clearCatalogManualPendingTx(ctx, tx.Tx, sourceID); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if directories == 0 {
		// No reusable generation exists, so the only useful work is a new full
		// observation. The daily boundary advances with that enqueue.
		queued, queueErr := s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "interval", false)
		if queueErr != nil {
			return queueErr
		}
		if queued.Existing {
			return tx.Commit()
		}
		if err = advanceCatalogScheduleTx(ctx, tx.Tx, sourceID, "daily", now); err != nil {
			return err
		}
		if schedule.ManualPending {
			if err = clearCatalogManualPendingTx(ctx, tx.Tx, sourceID); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	// A complete generation is probed directory first. Partial, failed and
	// cancelled generations use the same maintenance sync, which probes the
	// completed branches and retries the unresolved ranges without creating a
	// second full generation.
	//
	// The due boundary moves only after the sync row exists. If another writer
	// still holds the lease, enqueue returns errCatalogIndexRunning and the
	// whole transaction commits nothing, so the next tick retries the same
	// boundary instead of losing the day.
	return s.dispatchCatalogSyncWithSchedule(ctx, tx, sourceID, now, weeklyDue, schedule.ManualPending)
}

// dispatchCatalogSyncWithSchedule creates the sync row first and advances the
// schedule only after the row exists. A lost race to another full generation
// commits nothing, so the daily boundary stays due for the next tick.
func (s *Server) dispatchCatalogSyncWithSchedule(ctx context.Context, tx *sqlutil.BoundedTx, sourceID int64, now time.Time, weeklyDue, manualPending bool) error {
	syncID, err := enqueueCatalogSyncTx(ctx, tx.Tx, sourceID, false)
	if err != nil {
		if errors.Is(err, errCatalogIndexRunning) {
			return tx.Commit()
		}
		return err
	}
	if syncID == 0 {
		// An existing sync owns the source; keep the boundaries for its finish.
		return tx.Commit()
	}
	if err = advanceCatalogScheduleTx(ctx, tx.Tx, sourceID, "daily", now); err != nil {
		return err
	}
	if manualPending {
		if err = clearCatalogManualPendingTx(ctx, tx.Tx, sourceID); err != nil {
			return err
		}
	}
	if weeklyDue {
		if err = setCatalogResumeFullPendingTx(ctx, tx.Tx, sourceID); err != nil {
			return err
		}
	}
	return s.commitCatalogSyncDispatch(ctx, tx, syncID, sourceID)
}

// dispatchCatalogSync creates the directory-first writer for an existing
// generation. A lost race to another sync leaves the due boundary untouched.
func (s *Server) dispatchCatalogSync(ctx context.Context, tx *sqlutil.BoundedTx, sourceID int64) error {
	syncID, err := enqueueCatalogSyncTx(ctx, tx.Tx, sourceID, false)
	if err != nil {
		if errors.Is(err, errCatalogIndexRunning) {
			return tx.Commit()
		}
		return err
	}
	if syncID == 0 {
		return tx.Commit()
	}
	return s.commitCatalogSyncDispatch(ctx, tx, syncID, sourceID)
}

// dispatchCatalogManual consumes one manual request without moving the daily or
// weekly boundary. A source with no reusable generation needs a full crawl.
func (s *Server) dispatchCatalogManual(ctx context.Context, tx *sqlutil.BoundedTx, sourceID int64, directories int, options catalogIndexOptions) error {
	if directories == 0 {
		queued, err := s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "manual", true)
		if err != nil {
			return err
		}
		if queued.Existing {
			return tx.Commit()
		}
		if err = clearCatalogManualPendingTx(ctx, tx.Tx, sourceID); err != nil {
			return err
		}
		return tx.Commit()
	}
	syncID, err := enqueueCatalogSyncTx(ctx, tx.Tx, sourceID, false)
	if err != nil {
		if errors.Is(err, errCatalogIndexRunning) {
			return tx.Commit()
		}
		return err
	}
	if syncID == 0 {
		return tx.Commit()
	}
	if err = clearCatalogManualPendingTx(ctx, tx.Tx, sourceID); err != nil {
		return err
	}
	return s.commitCatalogSyncDispatch(ctx, tx, syncID, sourceID)
}

// catalogResourceBlock evaluates the shared resource policy inside the
// dispatch transaction. The returned deadline is always in the future.
func (s *Server) catalogResourceBlock(ctx context.Context, tx *sql.Tx, options catalogIndexOptions, now time.Time) (string, time.Time, bool, error) {
	held, _, err := catalogMaintenanceActiveTx(ctx, tx, now)
	if err != nil {
		return "", time.Time{}, false, err
	}
	if held {
		return "maintenance_hold", now.Add(time.Hour), true, nil
	}
	switch reason := s.catalogIndexDiskPause(options); reason {
	case "low_disk_space":
		return "low_disk", now.Add(time.Hour), true, nil
	case "disk_space_unknown":
		return "disk_unknown", now.Add(time.Hour), true, nil
	}
	return "", time.Time{}, false, nil
}

// commitCatalogSyncDispatch commits the schedule advance and then starts the
// worker. The sync row is already durable, so a process stop between the two
// steps is settled as an interrupted run on the next startup.
func (s *Server) commitCatalogSyncDispatch(ctx context.Context, tx interface {
	Commit() error
}, syncID, sourceID int64) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	if !s.Go(func(background context.Context) {
		_ = s.executeCatalogIncrementalSync(background, syncID, sourceID)
	}) {
		_ = s.finishCatalogSyncRun(context.WithoutCancel(ctx), syncID, "cancelled", catalogSyncRunState{ID: syncID, SourceID: sourceID, Status: "cancelled"})
	}
	return nil
}
