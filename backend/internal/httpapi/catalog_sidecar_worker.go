package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

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
	var sourceID int64
	err := s.db.QueryRowContext(ctx, `SELECT file_source_id FROM source_catalog_entry
		WHERE relocated_to_id IS NULL AND sidecar_pending=1 AND sidecar_attempts<?
		ORDER BY id LIMIT 1`, catalogSidecarMaxAttempts).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.refreshCatalogSidecars(ctx, sourceID), nil
}
