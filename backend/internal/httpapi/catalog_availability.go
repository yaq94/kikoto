package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	catalogAvailabilityQueueLimit = 64
	catalogAvailabilityCheckTTL   = 24 * time.Hour
	catalogAvailabilityTimeout    = 5 * time.Minute
)

var errCatalogNoPlayableFiles = errors.New("catalog directory has no playable files")

// catalogDirectoryNeedsCheck limits background checks to a card that was never
// observed or whose last observation is old. A disabled source is never probed.
func catalogDirectoryNeedsCheck(status, checkedAt string, now time.Time) bool {
	if status == "disabled" {
		return false
	}
	if status == "unknown" || status == "" {
		return true
	}
	checked, err := time.Parse(time.RFC3339, checkedAt)
	if err != nil {
		return true
	}
	return now.Sub(checked) >= catalogAvailabilityCheckTTL
}

// readCatalogEntryAvailability uses the same published snapshot as the saved
// directory. Only a reader with remote:track can request a background check.
func (s *Server) readCatalogEntryAvailability(ctx context.Context, entry catalogEntry, source remoteSourceForUse) (string, bool, error) {
	var checkedAt string
	if err := s.catalogRead(ctx).QueryRowContext(ctx, `SELECT COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',status_checked_at),'')
		FROM source_catalog_entry WHERE id=?`, entry.ID).Scan(&checkedAt); err != nil {
		return "", false, err
	}
	viewer, known := userFromContext(ctx)
	if source.Enabled && source.SourceType == sourceTypeOpenList && known && userHasPermission(viewer, "remote:track") && catalogDirectoryNeedsCheck(entry.Status, checkedAt, time.Now()) {
		s.enqueueCatalogDirectoryCheck(entry.ID)
	}
	s.catalogAvailabilityMu.Lock()
	_, pending := s.catalogAvailabilityQueued[entry.ID]
	s.catalogAvailabilityMu.Unlock()
	return checkedAt, pending, nil
}

// enqueueCatalogDirectoryCheck coalesces requests for the same card. The queue
// is deliberately small: a work page has only a few catalog tabs, and a full
// queue must not delay the page response.
func (s *Server) enqueueCatalogDirectoryCheck(id int64) {
	if id <= 0 || s.catalogAvailabilityQueue == nil {
		return
	}
	s.catalogAvailabilityMu.Lock()
	if _, exists := s.catalogAvailabilityQueued[id]; exists {
		s.catalogAvailabilityMu.Unlock()
		return
	}
	s.catalogAvailabilityQueued[id] = struct{}{}
	s.catalogAvailabilityMu.Unlock()
	select {
	case s.catalogAvailabilityQueue <- id:
	default:
		s.catalogAvailabilityMu.Lock()
		delete(s.catalogAvailabilityQueued, id)
		s.catalogAvailabilityMu.Unlock()
	}
}

// RunCatalogAvailabilityWorker checks the catalog tabs that a reader just
// opened, in the background. Existing directory data stays available while the
// probe runs, and failures only update that card's status.
func (s *Server) RunCatalogAvailabilityWorker(ctx context.Context) {
	if s.cfg.IsDemo() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.catalogAvailabilityQueue:
			s.runCatalogAvailabilityCheck(ctx, id)
		}
	}
}

func (s *Server) runCatalogAvailabilityCheck(ctx context.Context, id int64) {
	// The marker owns both queued and active work. Releasing it when dequeued
	// lets another reader schedule the same stale entry while this probe runs.
	defer func() {
		s.catalogAvailabilityMu.Lock()
		delete(s.catalogAvailabilityQueued, id)
		s.catalogAvailabilityMu.Unlock()
	}()
	checkCtx, cancel := context.WithTimeout(ctx, catalogAvailabilityTimeout)
	defer cancel()
	status, err := s.refreshCatalogEntry(checkCtx, id)
	if err != nil && ctx.Err() == nil {
		slog.Warn("catalog availability check failed", "catalog_entry_id", id, "status", status, "error", err)
	}
}
