package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// A source that cannot be loaded, for example because its endpoint was
// removed, would otherwise stay at the head of the queue forever. Remember a
// short local cooldown; the durable rows are untouched and a restart retries.
type catalogSidecarSourceCooldown struct {
	mu      sync.Mutex
	until   map[int64]time.Time
	clock   func() time.Time
	backoff time.Duration
}

func newCatalogSidecarSourceCooldown() *catalogSidecarSourceCooldown {
	return &catalogSidecarSourceCooldown{until: map[int64]time.Time{}, clock: time.Now, backoff: 30 * time.Second}
}

func (c *catalogSidecarSourceCooldown) ready(sourceID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, found := c.until[sourceID]
	if !found || !c.clock().Before(until) {
		delete(c.until, sourceID)
		return true
	}
	return false
}

func (c *catalogSidecarSourceCooldown) fail(sourceID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[sourceID] = c.clock().Add(c.backoff)
}

func (c *catalogSidecarSourceCooldown) succeed(sourceID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.until, sourceID)
}

// retrySourceCatalogSidecars reopens the credit queue for one source. It is
// the operator's recovery path after a solved credential or network problem:
// neither a permanent attempt budget nor a long transient backoff should
// require editing the database. Personal records and published evidence are
// untouched; only the retry bookkeeping is cleared.
func (s *Server) retrySourceCatalogSidecars(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	sourceID, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid source id", false)
		return
	}
	if _, err = s.catalogIndexSource(r.Context(), sourceID); err != nil {
		writeUpstreamError(w, err)
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE source_catalog_entry
		SET sidecar_pending=1,sidecar_attempts=0,sidecar_transient_attempts=0,sidecar_retry_at='',sidecar_failure=''
		WHERE file_source_id=? AND relocated_to_id IS NULL
			AND (sidecar_pending=1 OR sidecar_attempts>0 OR sidecar_failure<>'')
			AND EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=source_catalog_entry.id AND f.kind<>'folder')`,
		sourceID)
	if err != nil {
		writeError(w, err)
		return
	}
	reopened, _ := result.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"sourceId": sourceID, "reopened": reopened})
}

// RunCatalogSidecarWorker drains queued release-credit files outside the index
// workflow. The index owns directory coverage; credit files are optional
// matching evidence and must never extend a full scan by the number of
// releases. The queue is durable, so a restart resumes it without another
// listing.
func (s *Server) RunCatalogSidecarWorker(ctx context.Context) {
	if s.cfg.IsDemo() {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		worked, err := s.refreshPendingCatalogSidecars(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("refresh catalog sidecars", "error", err)
		}
		if worked && ctx.Err() == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refreshPendingCatalogSidecars advances at most one source per call. Sources
// are independent, so a blocked origin cannot hold every other queue. The
// boolean is the reader's continuation signal: it is true only after progress
// with queued work left, never after a pass that read nothing.
func (s *Server) refreshPendingCatalogSidecars(ctx context.Context) (bool, error) {
	if reason := s.catalogIndexDiskPause(defaultCatalogIndexOptions()); reason != "" {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT entry.file_source_id FROM source_catalog_entry entry INDEXED BY source_catalog_entry_sidecar_pending
		JOIN file_source source ON source.id=entry.file_source_id
		WHERE entry.relocated_to_id IS NULL AND entry.sidecar_pending=1 AND entry.sidecar_attempts<?
			AND (entry.sidecar_retry_at='' OR entry.sidecar_retry_at<=CURRENT_TIMESTAMP)
			AND source.enabled=1 AND source.source_type=?
		ORDER BY entry.file_source_id`, catalogSidecarMaxAttempts, sourceTypeOpenList)
	if err != nil {
		return false, err
	}
	sourceIDs := []int64{}
	for rows.Next() {
		var sourceID int64
		if err = rows.Scan(&sourceID); err != nil {
			_ = rows.Close()
			return false, err
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if len(sourceIDs) == 0 {
		return false, nil
	}
	if readErr != nil {
		return false, readErr
	}
	for _, sourceID := range sourceIDs {
		if !s.catalogSidecarCooldown.ready(sourceID) {
			continue
		}
		worked := s.refreshCatalogSidecars(ctx, sourceID)
		if _, err := s.catalogIndexSource(ctx, sourceID); err != nil {
			s.catalogSidecarCooldown.fail(sourceID)
		} else {
			s.catalogSidecarCooldown.succeed(sourceID)
		}
		return worked, nil
	}
	return false, nil
}
