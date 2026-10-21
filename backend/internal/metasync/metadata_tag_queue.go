package metasync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
	"github.com/yexca/kikoto/backend/internal/storage"
)

// ProcessMetadataTagQueue commits at most limit works atomically. Selection,
// projection and acknowledgement share the write transaction, so concurrent
// edits cannot lose a wakeup. A failed or cancelled batch remains durable.
func ProcessMetadataTagQueue(ctx context.Context, db *sql.DB, limit int, priorities []string) (count int, err error) {
	return processMetadataTagQueue(ctx, db, limit, priorities, time.Now(), 5*time.Second)
}

type metadataTagQueueWorkError struct {
	workID int64
	err    error
}

func (e *metadataTagQueueWorkError) Error() string {
	return fmt.Sprintf("project metadata tags for work %d: %v", e.workID, e.err)
}
func (e *metadataTagQueueWorkError) Unwrap() error { return e.err }

// Busy lock attempts back off instead of polling every two seconds with
// another five-second write wait. The sequence is 5s, 10s, 20s, 40s, then 60s.
const metadataTagBusyBaseDelay = 5 * time.Second

func metadataTagBusyDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	delay := metadataTagBusyBaseDelay
	for step := 1; step < attempt && delay < time.Minute; step++ {
		delay *= 2
	}
	return min(delay, time.Minute)
}

func waitMetadataTagBusyBackoff(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func processMetadataTagQueue(ctx context.Context, db *sql.DB, limit int, priorities []string, now time.Time, timeout time.Duration) (count int, err error) {
	if limit <= 0 {
		return 0, nil
	}
	// Most polls find an empty queue. Check before taking the writer lock so a
	// contended database does not turn an idle worker into a five-second wait
	// followed by a context deadline error.
	var pending bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_metadata_tag_dirty WHERE retry_after<=?)`, now.Unix()).Scan(&pending); err != nil || !pending {
		return 0, err
	}
	limit = min(limit, 64)
	var tx *sqlutil.BoundedTx
	var release func()
	for attempt := 1; ; attempt++ {
		lockCtx := ctx
		var lockCancel context.CancelFunc
		if timeout > 0 && timeout < metadataTagBusyBaseDelay {
			// A caller with a per-batch budget shorter than the first backoff
			// cannot wait for it. Bound its lock attempt by that budget so a
			// library timeout still returns instead of sleeping past its deadline.
			lockCtx, lockCancel = context.WithTimeout(ctx, timeout)
		}
		tx, release, err = storage.BeginBoundedTx(lockCtx, db)
		if err == nil {
			if lockCancel != nil {
				defer lockCancel()
			}
			break
		}
		if lockCancel != nil {
			lockCancel()
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if !sqlutil.IsBusy(err) && !errors.Is(err, context.DeadlineExceeded) {
			return 0, err
		}
		if timeout > 0 && timeout < metadataTagBusyBaseDelay {
			return 0, err
		}
		if waitErr := waitMetadataTagBusyBackoff(ctx, metadataTagBusyDelay(attempt)); waitErr != nil {
			return 0, waitErr
		}
	}
	defer release()
	batchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if err != nil && batchCtx.Err() != nil {
			var workErr *metadataTagQueueWorkError
			if errors.As(err, &workErr) {
				workErr.err = batchCtx.Err()
			} else {
				err = batchCtx.Err()
			}
		}
	}()
	rows, err := tx.QueryContext(batchCtx, "SELECT work_id FROM work_metadata_tag_dirty WHERE retry_after<=? ORDER BY work_id LIMIT ?", now.Unix(), limit)
	if err != nil {
		return 0, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	for _, id := range ids {
		if err := ProjectWorkMetadataTagsTx(batchCtx, tx.Tx, id, priorities); err != nil {
			return 0, &metadataTagQueueWorkError{workID: id, err: err}
		}
	}
	if err := tx.Commit(); err != nil {
		if batchCtx.Err() != nil && len(ids) > 0 {
			return 0, &metadataTagQueueWorkError{workID: ids[len(ids)-1], err: batchCtx.Err()}
		}
		return 0, err
	}
	return len(ids), nil
}
