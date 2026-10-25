package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Catalog schedules follow a wall clock in Asia/Shanghai. They do not depend on
// when the previous scan finished, so a long-running or missed scan cannot move
// the daily or weekly boundary.
var catalogScheduleLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}()

const catalogScheduleHour = 3

// catalogTime is the wall clock used by the schedule. Tests replace it so a
// boundary can be exercised without waiting for the real 03:00 tick.
func (s *Server) catalogTime() time.Time {
	if s.catalogNow != nil {
		return s.catalogNow().UTC()
	}
	return time.Now().UTC()
}

// nextCatalogDailyDue returns the next local 03:00 strictly after now.
func nextCatalogDailyDue(now time.Time) time.Time {
	local := now.In(catalogScheduleLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), catalogScheduleHour, 0, 0, 0, catalogScheduleLocation)
	if !local.Before(next) {
		next = next.AddDate(0, 0, 1)
	}
	return next.UTC()
}

// nextCatalogWeeklyDue returns the next local Sunday 03:00 strictly after now.
func nextCatalogWeeklyDue(now time.Time) time.Time {
	local := now.In(catalogScheduleLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), catalogScheduleHour, 0, 0, 0, catalogScheduleLocation)
	if !local.Before(next) {
		next = next.AddDate(0, 0, 1)
	}
	next = next.AddDate(0, 0, (7-int(next.Weekday()))%7)
	return next.UTC()
}

// catalogSchedule is the single persisted scheduling state for one source.
// Due boundaries are wall-clock values in Asia/Shanghai. deferred_until is the
// only field a resource hold moves; the boundaries and pending bits stay as
// they were so a recovery catches up once instead of replaying missed days.
type catalogSchedule struct {
	DailyDueAt       int64
	WeeklyDueAt      int64
	DeferredUntil    int64
	DeferredReason   string
	ManualPending    bool
	ResumeFull       bool
	PendingFullRunID sql.NullInt64
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// ensureCatalogScheduleTx inserts the initial row without touching an existing
// schedule. Source creation uses it for a source that is already enabled.
func ensureCatalogScheduleTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_schedule(
			file_source_id,daily_due_at,weekly_due_at,deferred_until,deferred_reason,manual_pending,resume_full_pending,pending_full_run_id)
		VALUES(?,?,?,0,'',0,0,NULL)
		ON CONFLICT(file_source_id) DO NOTHING`,
		sourceID, nextCatalogDailyDue(now).Unix(), nextCatalogWeeklyDue(now).Unix())
	return err
}

// resetCatalogScheduleTx is the re-enable boundary. A disabled source keeps
// its row, but a source that becomes enabled again starts from the next local
// 03:00 and Sunday 03:00 instead of inheriting an expired or held boundary.
func resetCatalogScheduleTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_schedule(
			file_source_id,daily_due_at,weekly_due_at,deferred_until,deferred_reason,manual_pending,resume_full_pending,pending_full_run_id,updated_at)
		VALUES(?,?,?,0,'',0,0,NULL,CURRENT_TIMESTAMP)
		ON CONFLICT(file_source_id) DO UPDATE SET
			daily_due_at=excluded.daily_due_at,
			weekly_due_at=excluded.weekly_due_at,
			deferred_until=0,
			deferred_reason='',
			manual_pending=0,
			resume_full_pending=0,
			pending_full_run_id=NULL,
			updated_at=CURRENT_TIMESTAMP`,
		sourceID, nextCatalogDailyDue(now).Unix(), nextCatalogWeeklyDue(now).Unix())
	return err
}

func loadCatalogScheduleTx(ctx context.Context, tx *sql.Tx, sourceID int64) (catalogSchedule, error) {
	var schedule catalogSchedule
	var manual, resume int
	err := tx.QueryRowContext(ctx, `SELECT daily_due_at,weekly_due_at,deferred_until,deferred_reason,manual_pending,resume_full_pending,pending_full_run_id
		FROM source_catalog_schedule WHERE file_source_id=?`, sourceID).
		Scan(&schedule.DailyDueAt, &schedule.WeeklyDueAt, &schedule.DeferredUntil, &schedule.DeferredReason, &manual, &resume, &schedule.PendingFullRunID)
	schedule.ManualPending = manual != 0
	schedule.ResumeFull = resume != 0
	return schedule, err
}

func clearCatalogScheduleDeferredTx(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_schedule SET deferred_until=0,deferred_reason='',updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=? AND (deferred_until<>0 OR deferred_reason<>'')`, sourceID)
	return err
}

func setCatalogScheduleDeferredTx(ctx context.Context, exec sqlExecer, sourceID int64, until time.Time, reason string) error {
	_, err := exec.ExecContext(ctx, `UPDATE source_catalog_schedule SET deferred_until=?,deferred_reason=?,updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, until.UTC().Unix(), reason, sourceID)
	return err
}

func setCatalogResumeFullPendingTx(ctx context.Context, exec sqlExecer, sourceID int64) error {
	_, err := exec.ExecContext(ctx, `UPDATE source_catalog_schedule SET resume_full_pending=1,updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, sourceID)
	return err
}

func setCatalogPendingFullRunTx(ctx context.Context, exec sqlExecer, sourceID, runID int64) error {
	_, err := exec.ExecContext(ctx, `UPDATE source_catalog_schedule SET pending_full_run_id=?,updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, runID, sourceID)
	return err
}

// clearCatalogPendingFullRunTx forgets a failed or cancelled resume full. The
// resume bit stays set, so the next dispatch creates a replacement.
func clearCatalogPendingFullRunTx(ctx context.Context, exec sqlExecer, sourceID int64) error {
	_, err := exec.ExecContext(ctx, `UPDATE source_catalog_schedule SET pending_full_run_id=NULL,updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, sourceID)
	return err
}

func setCatalogManualPendingTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_schedule(
			file_source_id,daily_due_at,weekly_due_at,deferred_until,deferred_reason,manual_pending,resume_full_pending,pending_full_run_id,updated_at)
		VALUES(?,?,?,0,'',1,0,NULL,CURRENT_TIMESTAMP)
		ON CONFLICT(file_source_id) DO UPDATE SET manual_pending=1,updated_at=CURRENT_TIMESTAMP`,
		sourceID, nextCatalogDailyDue(now).Unix(), nextCatalogWeeklyDue(now).Unix())
	return err
}

func clearCatalogManualPendingTx(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_schedule SET manual_pending=0,updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, sourceID)
	return err
}

// advanceCatalogScheduleTx moves the boundaries in the same transaction as a
// successfully created task. A daily task only moves the daily boundary. A
// weekly task moves both, so the Sunday full is not followed by a duplicate
// daily update on the same day. Manual work never moves either boundary.
func advanceCatalogScheduleTx(ctx context.Context, tx *sql.Tx, sourceID int64, kind string, now time.Time) error {
	switch kind {
	case "daily":
		_, err := tx.ExecContext(ctx, `UPDATE source_catalog_schedule SET daily_due_at=?,updated_at=CURRENT_TIMESTAMP
			WHERE file_source_id=?`, nextCatalogDailyDue(now).Unix(), sourceID)
		return err
	case "weekly":
		_, err := tx.ExecContext(ctx, `UPDATE source_catalog_schedule SET daily_due_at=?,weekly_due_at=?,
				resume_full_pending=0,pending_full_run_id=NULL,updated_at=CURRENT_TIMESTAMP
			WHERE file_source_id=?`, nextCatalogDailyDue(now).Unix(), nextCatalogWeeklyDue(now).Unix(), sourceID)
		return err
	case "manual":
		return clearCatalogManualPendingTx(ctx, tx, sourceID)
	default:
		return errors.New("unknown catalog schedule kind")
	}
}

// catalogScheduleDueTimes returns the persisted boundaries formatted in the
// schedule's own time zone, so an operator can verify the wall clock instead
// of inferring it from an epoch value.
func (s *Server) catalogScheduleDueTimes(ctx context.Context, sourceID int64) (string, string, error) {
	var daily, weekly sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT daily_due_at,weekly_due_at
		FROM source_catalog_schedule WHERE file_source_id=?`, sourceID).Scan(&daily, &weekly)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	format := func(value sql.NullInt64) string {
		if !value.Valid {
			return ""
		}
		return time.Unix(value.Int64, 0).In(catalogScheduleLocation).Format(time.RFC3339)
	}
	return format(daily), format(weekly), nil
}
