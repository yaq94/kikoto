package httpapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

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
		transport.concurrency = options.Concurrency
	}
	client := openlist.NewClient(source.Endpoint.APIURL, httpClient).WithResponseLimit(options.MaxResponseBytes)
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
			n, err := s.publishCatalogBatch(ctx, job, input.SourceID)
			if err != nil {
				return err
			}
			if n > 0 {
				count++
				continue
			}
			p, err := s.sourceCatalogProgress(ctx, input.SourceID, 1)
			if err != nil {
				return err
			}
			status := "succeeded"
			if p.Failed+p.Restricted+p.Empty > 0 {
				status = "partial"
			} else {
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
		if _, err = s.publishCatalogBatch(ctx, job, input.SourceID); err != nil {
			return err
		}
	}
	return s.settleCatalogBatch(ctx, job, input.SourceID, "queued", "")
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

func (s *Server) settleCatalogBatch(ctx context.Context, job workflowJobRecord, sourceID int64, status, reason string) error {
	progress, err := s.sourceCatalogProgress(ctx, sourceID, 1)
	if err != nil {
		return err
	}
	progress.Status = status
	progress.PauseReason = reason
	tx, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifyCatalogLease(ctx, tx, job); err != nil {
		return err
	}
	if status == "succeeded" {
		if err = setCatalogNextUpdateTx(ctx, tx, sourceID, s.catalogTime()); err != nil {
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
