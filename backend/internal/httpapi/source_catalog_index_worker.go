package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// catalogIndexNoPacingKey lets the contract suite exercise the real worker,
// HTTP client and database without waiting on the upstream pacing delay. The
// delay is a source-protection policy, not the subject of that suite; the
// configured-rate tests own it.
type catalogIndexNoPacingKey struct{}

const catalogWritePause = 8 * time.Millisecond

// pauseCatalogWriter 在写事务之间让出 SQLite 写锁。等待者每 10ms 重试一次，
// 8ms 的窗口足以让它拿到锁。契约套件通过 catalogIndexNoPacingKey 关闭该暂停。
func pauseCatalogWriter(ctx context.Context) error {
	if ctx.Value(catalogIndexNoPacingKey{}) != nil {
		return ctx.Err()
	}
	timer := time.NewTimer(catalogWritePause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

const catalogSlowWriteThreshold = 500 * time.Millisecond

// observeCatalogWrite reports a slow write once. The contract suite runs the
// real worker inside -race, where every transaction is several times slower
// than production; a warning there measures the race detector, not the
// database, so the same no-pacing switch that removes artificial delays also
// silences this report.
func observeCatalogWrite(ctx context.Context, kind string, started time.Time, rows int) {
	if ctx.Value(catalogIndexNoPacingKey{}) != nil {
		return
	}
	if elapsed := time.Since(started); elapsed > catalogSlowWriteThreshold {
		slog.Warn("catalog write transaction is slow", "kind", kind, "rows", rows, "elapsed_ms", elapsed.Milliseconds())
	}
}

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
	// Rows left pending by earlier generations can no longer be published in
	// this run; clearing the flag keeps the publish query from scanning them on
	// every batch. Only the flag changes, and a remaining backlog continues on
	// the next invocation.
	if err = s.purgeStaleCatalogPending(ctx, job, input.SourceID); err != nil {
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
	// A low-space job must not publish even its first batch. The floor applies
	// before any write, not only after the queue has been drained once.
	if reason := s.catalogIndexDiskPause(options); reason != "" {
		return s.settleCatalogBatch(ctx, job, input.SourceID, "partial", reason)
	}
	// The publish queue is drained before the next page batch, not on every
	// step. A working tree always has another directory to expand or page, so a
	// publish call after each write spends its transaction on rows the next
	// batch would publish anyway. Checkpoint data remains durable in the same
	// page transactions as before. A bounded pass leaves the next workflow
	// invocation to fetch pages instead of publishing an unbounded backlog.
	if _, _, err = s.publishCatalogQueue(ctx, job, input.SourceID); err != nil {
		return err
	}
	scanStarted := time.Now()
	for count := 0; count < options.PagesPerBatch; {
		if err = ctx.Err(); err != nil {
			return err
		}
		// A deployment hold stops at a directory boundary, where the committed
		// checkpoint already describes everything this generation observed. The
		// job becomes available again when the hold ends, so a held scan cannot
		// spin on the queue.
		if held, until, holdErr := s.catalogMaintenanceActive(ctx); holdErr != nil {
			return holdErr
		} else if held {
			s.recordCatalogPhase(ctx, job.RunID, "scan", scanStarted)
			return s.settleCatalogBatchAt(ctx, job, input.SourceID, "queued", "maintenance", until)
		}
		if reason := s.catalogIndexDiskPause(options); reason != "" {
			s.recordCatalogPhase(ctx, job.RunID, "scan", scanStarted)
			return s.settleCatalogBatch(ctx, job, input.SourceID, "partial", reason)
		}
		expanded, err := s.expandCatalogDirectory(ctx, job, input.SourceID)
		if err != nil {
			return err
		}
		if expanded {
			if err = pauseCatalogWriter(ctx); err != nil {
				return err
			}
			count++
			continue
		}
		dirs, err := s.pendingCatalogDirectories(ctx, input.SourceID, job.RunID, min(options.Concurrency, options.PagesPerBatch-count))
		if err != nil {
			return err
		}
		if len(dirs) == 0 {
			s.recordCatalogPhase(ctx, job.RunID, "scan", scanStarted)
			publishStarted := time.Now()
			_, more, err := s.publishCatalogQueue(ctx, job, input.SourceID)
			s.recordCatalogPhase(ctx, job.RunID, "publish", publishStarted)
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
			p, err := s.sourceCatalogProgress(ctx, input.SourceID, 1)
			if err != nil {
				return err
			}
			// A root-level unconfirmed or failed listing makes the whole tree
			// suspect, so a deletion decision waits for a later generation.
			rootEmpty := false
			if p.Empty > 0 {
				if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_directory
					WHERE file_source_id=? AND generation=? AND status='empty' AND error_code<>'not_found' AND path='/')`,
					input.SourceID, job.RunID).Scan(&rootEmpty); err != nil {
					return err
				}
			}
			// Retire stale split cards before the row cleanup. The cleanup tail
			// walks every published card and can span many invocations on a large
			// source; the visible duplicate is a correctness problem, while the old
			// file rows are storage hygiene. Both are resumable and independent.
			if !rootEmpty {
				retired, err := s.retireOrphanSplitRootsBatch(ctx, job, input.SourceID)
				if err != nil {
					return err
				}
				if err = pauseCatalogWriter(ctx); err != nil {
					return err
				}
				if retired == catalogRetireRoots {
					return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
				}
			}
			// Retire stale container and format cards as its own bounded step.
			// It used to run inside the final publish transaction, where one
			// root-completeness query per card held the writer for seconds.
			if !rootEmpty {
				retiredCards, done, err := s.retireCatalogCardsBatch(ctx, job, input.SourceID)
				if err != nil {
					return err
				}
				if retiredCards > 0 {
					if err = pauseCatalogWriter(ctx); err != nil {
						return err
					}
				}
				if !done {
					return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
				}
			}
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
			cleanupStarted := time.Now()
			scanned := int64(0)
			for pass := 0; pass < catalogCleanupBatchesPerPass; pass++ {
				scanned, _, err = s.cleanupCatalogPublishBatch(ctx, job, input.SourceID)
				if err != nil {
					return err
				}
				if err = pauseCatalogWriter(ctx); err != nil {
					return err
				}
				if scanned < catalogCleanupRows {
					break
				}
			}
			s.recordCatalogPhase(ctx, job.RunID, "cleanup", cleanupStarted)
			if scanned == catalogCleanupRows {
				return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
			}
			// An unconfirmed-empty directory is not proof that its range is
			// complete, so it keeps the run partial. It does not block the
			// lifecycle pass for ranges that were fully observed: other cards
			// still need their own available/missing state. A failed,
			// restricted or empty root is different: the whole listing is
			// suspect and no lifecycle pass runs.
			status := "succeeded"
			unconfirmedEmpty := 0
			if p.Empty > 0 && !rootEmpty {
				if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_directory
					WHERE file_source_id=? AND generation=? AND status='empty' AND error_code<>'not_found'`,
					input.SourceID, job.RunID).Scan(&unconfirmedEmpty); err != nil {
					return err
				}
			}
			if p.Failed+p.Restricted > 0 || rootEmpty || unconfirmedEmpty > 0 {
				status = "partial"
			}
			// Per-range uncertainty keeps a failed subtree, so a failure
			// elsewhere must not freeze ranges this generation observed fully.
			// Only an unlistable configured root makes every range suspect.
			if !rootEmpty {
				lifecycleStarted := time.Now()
				pending, err := s.settleCatalogLifecycleBatch(ctx, job, input.SourceID)
				s.recordCatalogPhase(ctx, job.RunID, "lifecycle", lifecycleStarted)
				if err != nil {
					return err
				}
				if err = pauseCatalogWriter(ctx); err != nil {
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
				if err = pauseCatalogWriter(ctx); err != nil {
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
					if writeErr = s.persistCatalogPage(ctx, job, input.SourceID, result.dir, result.data, result.err, options); writeErr == nil {
						writeErr = pauseCatalogWriter(ctx)
					}
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
		if err = pauseCatalogWriter(ctx); err != nil {
			return published, false, err
		}
		if !catalogPublishBatchHasMore(n) {
			return published, false, nil
		}
	}
	return published, true, nil
}

// catalogPublishBatchHasMore is the single sentinel for "the batch was full".
// It must use the same constant as the publish query's LIMIT: a smaller row
// count would silently strand the rest of the queue.
func catalogPublishBatchHasMore(rows int) bool {
	return rows >= catalogPublishRows
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
	return s.settleCatalogBatchAt(ctx, job, sourceID, status, reason, time.Time{})
}

// settleCatalogBatchAt is settleCatalogBatch with an explicit resume time. A
// maintenance hold uses it so the job wakes when the hold ends instead of
// immediately re-queuing and pausing again.
func (s *Server) settleCatalogBatchAt(ctx context.Context, job workflowJobRecord, sourceID int64, status, reason string, resumeAt time.Time) error {
	settleStarted := time.Now()
	defer func() { s.recordCatalogPhase(context.WithoutCancel(ctx), job.RunID, "settle", settleStarted) }()
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
	// A queued job normally resumes immediately. A hold defers it to the hold
	// end so the worker does not re-acquire the lease only to pause again.
	var availableAt any
	if status == "queued" {
		next := time.Now()
		if resumeAt.After(next) {
			next = resumeAt
		}
		availableAt = next.UTC().Format("2006-01-02 15:04:05")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_job SET status=?,checkpoint_json=?,progress_current=?,progress_total=0,error_message=?,locked_by='',locked_at=NULL,heartbeat_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND locked_by=?`, status, checkpoint, progress.Completed, reason, availableAt, job.ID, job.LockedBy); err != nil {
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
