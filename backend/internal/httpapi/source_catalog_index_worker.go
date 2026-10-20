package httpapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// catalogIndexNoPacingKey lets the contract suite exercise the real worker,
// HTTP client and database without waiting on the upstream pacing delay. The
// delay is a source-protection policy, not the subject of that suite; the
// configured-rate tests own it.
type catalogIndexNoPacingKey struct{}

func (s *Server) executeSourceCatalogIndexJob(ctx context.Context, job workflowJobRecord) error {
	var input catalogIndexInput
	if err := json.Unmarshal([]byte(job.PayloadJSON), &input); err != nil {
		return err
	}
	source, err := s.catalogIndexSource(ctx, input.SourceID)
	if err != nil {
		return s.settleCatalogBatch(ctx, job, input.SourceID, "failed", "source_unavailable")
	}
	progress, err := s.sourceCatalogProgress(ctx, input.SourceID, 1)
	if err != nil {
		return err
	}
	options := progress.Options
	if err = options.validate(); err != nil {
		return err
	}
	httpClient := s.sourceClient(source, 30*time.Second, sourceRequestCatalogIndex)
	if transport, ok := httpClient.Transport.(*sourceGateTransport); ok {
		transport.minimumInterval = time.Duration(options.IntervalMS) * time.Millisecond
		if ctx.Value(catalogIndexNoPacingKey{}) != nil {
			transport.minimumInterval = 0
		}
		transport.concurrency = options.Concurrency
	}
	client := configuredOpenListClient(source, httpClient).WithResponseLimit(options.MaxResponseBytes)
	// The publish queue is drained before the next page batch, not on every
	// step. A working tree always has another directory to expand or page, so a
	// publish call after each write spends its transaction on rows the next
	// batch would publish anyway. Checkpoint data remains durable in the same
	// page transactions as before. A bounded pass leaves the next workflow
	// invocation to fetch pages instead of publishing an unbounded backlog.
	if _, _, err = s.publishCatalogQueue(ctx, job, input.SourceID); err != nil {
		return err
	}
	for count := 0; count < options.PagesPerBatch; {
		if err = ctx.Err(); err != nil {
			return err
		}
		if reason := s.catalogIndexDiskPause(options); reason != "" {
			return s.settleCatalogBatch(ctx, job, input.SourceID, "partial", reason)
		}
		expanded, err := s.expandCatalogDirectory(ctx, job, input.SourceID)
		if err != nil {
			return err
		}
		if expanded {
			count++
			continue
		}
		dirs, err := s.pendingCatalogDirectories(ctx, input.SourceID, job.RunID, min(options.Concurrency, options.PagesPerBatch-count))
		if err != nil {
			return err
		}
		if len(dirs) == 0 {
			_, more, err := s.publishCatalogQueue(ctx, job, input.SourceID)
			if err != nil {
				return err
			}
			if more {
				return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
			}
			// Warm the credit reader once for this completed pass. The call
			// is bounded by its own request budget; remaining cards are
			// drained by the independent sidecar worker, so this never
			// extends the index job for a whole release tree.
			s.refreshCatalogSidecars(ctx, input.SourceID)
			// No directory is left to observe and the publish queue is empty,
			// so the set of entries this generation republished is final.
			// Retire the rows an older generation left on those entries now;
			// the cursor must not advance before a later page can republish
			// the entry. This runs for a partial run too: it only touches
			// entries this generation published, never the whole source.
			//
			// The cursor only advances in this final pass and is reset when a
			// new generation starts, so it is safe to resume across steps. Keep
			// each invocation bounded; a source with more cards than one pass
			// can retire continues on the next step instead of leaving the tail
			// permanently uncleaned.
			scanned := int64(0)
			for pass := 0; pass < catalogPublishBatchesPerPass; pass++ {
				scanned, _, err = s.cleanupCatalogPublishBatch(ctx, job, input.SourceID)
				if err != nil {
					return err
				}
				if scanned < 100 {
					break
				}
			}
			if scanned == 100 {
				return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
			}
			p, err := s.sourceCatalogProgress(ctx, input.SourceID, 1)
			if err != nil {
				return err
			}
			status := "succeeded"
			// An unconfirmed-empty directory is a completed observation of zero
			// files, not an unfinished range. It must not block retiring rows in
			// ranges that were fully observed; settleCatalogLifecycleBatch still
			// skips any entry whose own range contains an empty directory. A
			// source root that itself came back empty means the whole listing is
			// suspect, so no lifecycle pass runs in that case.
			rootEmpty := false
			if p.Empty > 0 {
				if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_directory
					WHERE file_source_id=? AND generation=? AND status='empty' AND error_code<>'not_found' AND path='/')`,
					input.SourceID, job.RunID).Scan(&rootEmpty); err != nil {
					return err
				}
			}
			if p.Failed+p.Restricted > 0 || rootEmpty {
				status = "partial"
			} else {
				// Retire the split cards of roots that the current generation
				// no longer splits. A batch keeps the write lock short; a full
				// batch asks the workflow for another invocation.
				retired, err := s.retireOrphanSplitRootsBatch(ctx, job, input.SourceID)
				if err != nil {
					return err
				}
				if retired == 100 {
					return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
				}
				pending, err := s.settleCatalogLifecycleBatch(ctx, job, input.SourceID)
				if err != nil {
					return err
				}
				if pending {
					count++
					continue
				}
				n, err := s.cleanupCatalogStagingBatch(ctx, job, input.SourceID)
				if err != nil {
					return err
				}
				if n > 0 {
					count++
					continue
				}
			}
			return s.settleCatalogBatch(ctx, job, input.SourceID, status, "")
		}
		type pageResult struct {
			dir  catalogScanDirectory
			data openlist.Page
			err  error
		}
		results := make(chan pageResult, len(dirs))
		for _, d := range dirs {
			go func() {
				data, readErr := client.ListPage(ctx, d.Path, d.Page, options.PageSize)
				results <- pageResult{d, data, readErr}
			}()
		}
		// Drain every bounded request before returning, including after a write
		// failure. Shutdown cannot close the database beneath a page publisher.
		var writeErr error
		pauseReason := ""
		for range dirs {
			result := <-results
			if ctx.Err() != nil {
				writeErr = ctx.Err()
			}
			if writeErr == nil {
				if reason := s.catalogIndexDiskPause(options); reason != "" {
					pauseReason = reason
				}
				if pauseReason == "" {
					writeErr = s.persistCatalogPage(ctx, job, input.SourceID, result.dir, result.data, result.err, options)
				}
			}
		}
		if writeErr != nil {
			return writeErr
		}
		if pauseReason != "" {
			return s.settleCatalogBatch(ctx, job, input.SourceID, "partial", pauseReason)
		}
		count += len(dirs)
	}
	if _, _, err = s.publishCatalogQueue(ctx, job, input.SourceID); err != nil {
		return err
	}
	return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
}

// publishCatalogQueue drains at most catalogPublishBatchesPerPass batches. It
// reports whether a full batch remained, so the caller can defer the backlog
// to another invocation instead of monopolizing the worker. An exhausted queue
// no longer costs a scan of every completed directory and cannot spin.
func (s *Server) publishCatalogQueue(ctx context.Context, job workflowJobRecord, sourceID int64) (int, bool, error) {
	published := 0
	for pass := 0; pass < catalogPublishBatchesPerPass; pass++ {
		n, err := s.publishCatalogBatch(ctx, job, sourceID)
		if err != nil {
			return published, false, err
		}
		published += n
		if n < 100 {
			return published, false, nil
		}
	}
	return published, true, nil
}

func (s *Server) catalogIndexDiskPause(options catalogIndexOptions) string {
	root := filepath.Dir(s.cfg.DatabasePath)
	if s.cfg.DatabasePath == "" {
		root = s.cfg.CacheRoot
	}
	measure := s.catalogDiskSpace
	if measure == nil {
		measure = queryDiskSpace
	}
	snapshot, err := measure(root)
	if err != nil {
		return "disk_space_unknown"
	}
	// Reserve room for the metadata page and SQLite indexes/WAL as well as the
	// configured floor. The actual filesystem remains the enforcement boundary.
	if snapshot.Available < uint64(options.MinFreeBytes)+uint64(options.MaxResponseBytes)*4 {
		return "low_disk_space"
	}
	return ""
}

// catalogScanSchedulesDaily decides whether a settled generation owns the
// first daily update. A paused low-disk run is not finished coverage and must
// not start the schedule; an upstream failure does not either. A generation
// that reached the end of the tree with only unconfirmed empty or unreadable
// directories does schedule, otherwise new content added later would never be
// observed because a partial run is only ever resumed.
func catalogScanSchedulesDaily(status, reason string, progress catalogIndexProgress) bool {
	if reason != "" || progress.Pending > 0 || progress.Completed == 0 {
		return false
	}
	return status == "succeeded" || status == "partial"
}

func (s *Server) settleCatalogBatch(ctx context.Context, job workflowJobRecord, sourceID int64, status, reason string) error {
	progress, err := s.sourceCatalogProgress(ctx, sourceID, 1)
	if err != nil {
		return err
	}
	progress.Status = status
	progress.PauseReason = reason
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return err
	}
	if catalogScanSchedulesDaily(status, reason, progress) {
		if err = setCatalogNextUpdateTx(ctx, tx.Tx, sourceID, s.catalogTime()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET pause_reason=?,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, reason, sourceID); err != nil {
		return err
	}
	checkpoint := mustJSON(map[string]any{"phase": status, "detail": map[string]any{"sourceId": sourceID, "generation": job.RunID}, "progressCurrent": progress.Completed, "progressTotal": 0})
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_job SET status=?,checkpoint_json=?,progress_current=?,progress_total=0,error_message=?,locked_by='',locked_at=NULL,heartbeat_at=NULL,available_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND locked_by=?`, status, checkpoint, progress.Completed, reason, job.ID, job.LockedBy); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_node_run SET status=?,output_json=?,error_message=?,finished_at=CASE WHEN ?='queued' THEN NULL ELSE CURRENT_TIMESTAMP END WHERE id=?`, status, mustJSON(progress), reason, status, job.NodeRunID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_run SET status=?,summary_json=?,finished_at=CASE WHEN ?='queued' THEN NULL ELSE CURRENT_TIMESTAMP END WHERE id=?`, status, mustJSON(progress), status, job.RunID); err != nil {
		return err
	}
	return tx.Commit()
}
