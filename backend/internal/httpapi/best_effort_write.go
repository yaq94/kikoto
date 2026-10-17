package httpapi

import (
	"context"
	"errors"
	"log/slog"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

// execBestEffort runs a status or bookkeeping write whose failure must not
// change the caller's outcome. A failed write leaves the stored state at its
// previous value, so it is logged for diagnosis instead of silently dropped.
// Cancellation is expected when a request or job ends and is not logged.
func (s *Server) execBestEffort(ctx context.Context, operation string, query string, args ...any) {
	// A bookkeeping write takes the same bounded lock path as any other writer:
	// losing it to a contended lock is acceptable, parking a pooled connection
	// behind the lock is not.
	if _, err := sqlutil.ExecBounded(ctx, s.db, query, args...); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("best-effort database write failed", "operation", operation, "error", err)
	}
}
