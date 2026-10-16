package library

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Opt-in experiment for the shared identity membership predicate. A correlated
// member probe re-runs the complete Library predicate for every outer work row,
// so this prints the plan and the best of three counts at growing fixture sizes.
// Run with KIKOTO_SHARED_IDENTITY_PERF=1.
func TestSharedIdentityListWhereScaling(t *testing.T) {
	if os.Getenv("KIKOTO_SHARED_IDENTITY_PERF") != "1" {
		t.Skip("opt-in performance experiment")
	}
	ctx := context.Background()
	for _, count := range []int{180, 360, 720} {
		store, userID := seedPaginationLibrary(t, count, 0, 0)
		where, args := listWhere("", "", "", userID, false)
		query := "SELECT COUNT(*) FROM work LEFT JOIN user_work_state ON user_work_state.work_id = work.id AND user_work_state.user_id = ? WHERE " + where
		queryArgs := append([]any{userID}, args...)
		rows, err := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, queryArgs...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			t.Logf("works=%d plan: %s", count, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		best := time.Duration(1 << 62)
		var total int
		for sample := 0; sample < 3; sample++ {
			start := time.Now()
			if err := store.db.QueryRowContext(ctx, query, queryArgs...).Scan(&total); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed < best {
				best = elapsed
			}
		}
		t.Logf("works=%d total=%d best=%s", count, total, best)
		fmt.Fprintf(os.Stderr, "works=%d total=%d best=%s\n", count, total, best)
	}
}
