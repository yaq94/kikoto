package httpapi

import (
	"context"
	"log/slog"
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
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT entry.file_source_id FROM source_catalog_entry entry
		JOIN file_source source ON source.id=entry.file_source_id
		WHERE entry.relocated_to_id IS NULL AND entry.sidecar_pending=1 AND entry.sidecar_attempts<?
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
