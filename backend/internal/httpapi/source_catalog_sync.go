package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
	"github.com/yexca/kikoto/backend/internal/workflow"
)

// The incremental sync is directory first. Every non-leaf directory is probed
// with /api/fs/dirs so a changed child mtime is seen even when the parent mtime
// did not move. A directory is listed only when its own modified value (seen by
// its parent) changed, or when the directory is new/retrying an earlier gap.
const (
	sourceCatalogSyncWorkerType = "source_catalog_sync"
	catalogSyncBatch            = 32
	catalogSyncFullInterval     = 7 * 24 * time.Hour
	catalogSyncMaxListBatches   = 512
	catalogSyncMaxLifecycle     = 4096
	catalogSyncMaxNote          = 600
)

var (
	errCatalogSyncRunning  = errors.New("catalog incremental sync is already running")
	errCatalogIndexRunning = errors.New("catalog full index is already running")
)

type catalogSyncRunState struct {
	ID               int64
	SourceID         int64
	Status           string
	DirsProbed       int
	DirsChanged      int
	DirsListed       int
	EntriesPublished int
	Requests         int
	Force            bool
	Note             string
}

type catalogSyncDirectoryState struct {
	Path            string
	Partition       string
	Root            string
	Revision        int
	Status          string
	SyncState       string
	ObservedVersion string
	ListingDigest   string
}

type catalogSyncProbeResult struct {
	Dir      catalogSyncDirectoryState
	Children []openlist.Object
	Err      error
}

type catalogSyncObservation struct {
	Name     string
	Kind     string
	Size     int64
	Hash     string
	Modified string
	Pending  int
}

type catalogSyncLease struct {
	Job workflowJobRecord
	// WorkflowRunID is the sync's own workflow_run row. It is not the catalog
	// generation: phase timings must describe this sync, not the full crawl.
	WorkflowRunID int64
	release       func(string)
}

func catalogSyncBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func catalogIncrementalSyncRunningTx(ctx context.Context, tx *sql.Tx, sourceID int64) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_sync_run
		WHERE file_source_id=? AND status='running'`, sourceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func catalogFullIndexRunningTx(ctx context.Context, tx *sql.Tx, sourceID int64) (bool, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT run.status FROM source_catalog_scan scan
		JOIN workflow_run run ON run.id=scan.workflow_run_id
		WHERE scan.file_source_id=?`, sourceID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "queued" || status == "running", nil
}

func enqueueCatalogSyncTx(ctx context.Context, tx *sql.Tx, sourceID int64, force bool) (int64, error) {
	// A process that stopped during a long probe leaves one running row. Six
	// hours is well above a full 3k-directory pass at the observed latency and
	// keeps the partial unique index from blocking every later day.
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_sync_run SET status='failed',finished_at=CURRENT_TIMESTAMP,note=CASE WHEN note='' THEN 'interrupted run' ELSE note END WHERE file_source_id=? AND status='running' AND started_at<datetime('now','-6 hours')`, sourceID); err != nil {
		return 0, err
	}
	var running int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_sync_run WHERE file_source_id=? AND status='running'`, sourceID).Scan(&running)
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	fullRunning, err := catalogFullIndexRunningTx(ctx, tx, sourceID)
	if err != nil {
		return 0, err
	}
	if fullRunning {
		return 0, errCatalogIndexRunning
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_sync_run(file_source_id,status,force) VALUES(?, 'running', ?)`, sourceID, catalogSyncBoolInt(force))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Server) startCatalogIncrementalSync(ctx context.Context, sourceID int64, force bool) (int64, error) {
	runID, err := s.queueCatalogIncrementalSync(ctx, sourceID, force)
	if err != nil || runID == 0 {
		return runID, err
	}
	if !s.Go(func(background context.Context) {
		_ = s.executeCatalogIncrementalSync(background, runID, sourceID)
	}) {
		_ = s.finishCatalogSyncRun(context.WithoutCancel(ctx), runID, "cancelled", catalogSyncRunState{ID: runID, SourceID: sourceID, Status: "cancelled"})
	}
	return runID, nil
}

func (s *Server) runCatalogIncrementalSync(ctx context.Context, sourceID int64, force bool) (int64, error) {
	runID, err := s.queueCatalogIncrementalSync(ctx, sourceID, force)
	if err != nil || runID == 0 {
		return runID, err
	}
	return runID, s.executeCatalogIncrementalSync(ctx, runID, sourceID)
}

func (s *Server) queueCatalogIncrementalSync(ctx context.Context, sourceID int64, force bool) (int64, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	runID, err := enqueueCatalogSyncTx(ctx, tx.Tx, sourceID, force)
	if err != nil || runID == 0 {
		return runID, err
	}
	if err = setCatalogDailyNextUpdateTx(ctx, tx.Tx, sourceID, s.catalogTime()); err != nil {
		return 0, err
	}
	return runID, tx.Commit()
}

func (s *Server) catalogSyncOptions(ctx context.Context, sourceID int64) (catalogIndexOptions, int64, error) {
	options := defaultCatalogIndexOptions()
	var saved string
	var generation int64
	err := s.db.QueryRowContext(ctx, `SELECT scan.options_json,scan.workflow_run_id FROM source_catalog_scan scan WHERE scan.file_source_id=?`, sourceID).Scan(&saved, &generation)
	if err != nil {
		return options, 0, err
	}
	if err = jsonUnmarshalCatalogOptions(saved, &options); err != nil {
		return options, 0, err
	}
	if err = options.validate(); err != nil {
		return options, 0, err
	}
	return options, generation, nil
}

func jsonUnmarshalCatalogOptions(saved string, options *catalogIndexOptions) error {
	if strings.TrimSpace(saved) == "" {
		return nil
	}
	return json.Unmarshal([]byte(saved), options)
}

func (s *Server) executeCatalogIncrementalSync(ctx context.Context, runID, sourceID int64) (runErr error) {
	state := catalogSyncRunState{ID: runID, SourceID: sourceID, Status: "running"}
	// resumeAt delays the next daily attempt when a hold or a low disk stopped
	// this run. It stays zero for an ordinary finish.
	var resumeAt time.Time
	settleStarted := time.Now()
	defer func() {
		s.recordCatalogPhase(context.WithoutCancel(ctx), runID, "settle", settleStarted)
		status := state.Status
		if status == "" || status == "running" {
			status = "succeeded"
		}
		if runErr != nil {
			status = "failed"
			state.Note = appendCatalogSyncNote(state.Note, runErr.Error())
		}
		if state.DirsProbed == 0 && state.DirsListed == 0 && runErr == nil && status == "succeeded" {
			state.Note = appendCatalogSyncNote(state.Note, "no non-leaf directories were probed")
		}
		settle := context.WithoutCancel(ctx)
		_ = s.finishCatalogSyncRun(settle, runID, status, state)
		if !resumeAt.IsZero() {
			_ = s.setCatalogDailyDue(settle, sourceID, resumeAt)
		}
	}()

	options, generation, err := s.catalogSyncOptions(ctx, sourceID)
	if err != nil {
		return err
	}
	// A hold or a low disk is checked before the first request, so a paused
	// sync costs nothing and leaves every checkpoint untouched.
	if reason, until := s.pauseCatalogSync(ctx, options); reason != "" {
		state.Status = "partial"
		state.Note = appendCatalogSyncNote(state.Note, reason)
		resumeAt = until
		return nil
	}
	source, err := s.catalogIndexSource(ctx, sourceID)
	if err != nil {
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

	var force int
	if err = s.db.QueryRowContext(ctx, `SELECT force FROM source_catalog_sync_run WHERE id=?`, runID).Scan(&force); err != nil {
		return err
	}
	state.Force = force != 0

	if err = s.seedCatalogSyncDirectories(ctx, sourceID, generation, state.Force); err != nil {
		return err
	}
	lease, err := s.beginCatalogSyncLease(ctx, runID, generation)
	if err != nil {
		return err
	}
	defer func() {
		status := state.Status
		if runErr != nil {
			status = "failed"
		} else if status == "" || status == "running" {
			status = "succeeded"
		}
		lease.release(status)
	}()

	failed := false
	// listingCursor advances over changed directories in path order. It is not
	// durable: a restart re-lists from the beginning, and the listing digest
	// makes an unchanged directory cheap.
	listingCursor := ""
	listingPasses := 0
	scanStarted := time.Now()
	for {
		if err = ctx.Err(); err != nil {
			s.recordCatalogPhase(context.WithoutCancel(ctx), lease.WorkflowRunID, "scan", scanStarted)
			return err
		}
		// The hold and the disk floor are re-checked between directory batches,
		// so a long probe pass stops where the committed checkpoint ends.
		if reason, until := s.pauseCatalogSync(ctx, options); reason != "" {
			s.recordCatalogPhase(context.WithoutCancel(ctx), lease.WorkflowRunID, "scan", scanStarted)
			state.Status = "partial"
			state.Note = appendCatalogSyncNote(state.Note, reason)
			resumeAt = until
			return nil
		}
		probes, queryErr := s.pendingCatalogSyncProbes(ctx, sourceID, generation, runID, catalogSyncBatch)
		if queryErr != nil {
			return queryErr
		}
		if len(probes) > 0 {
			results := make([]catalogSyncProbeResult, 0, len(probes))
			for _, dir := range probes {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				children, requestErr := client.Dirs(ctx, dir.Path)
				state.Requests++
				state.DirsProbed++
				if requestErr != nil {
					failed = true
					state.Note = appendCatalogSyncNote(state.Note, fmt.Sprintf("dirs %s: %v", dir.Path, requestErr))
				}
				results = append(results, catalogSyncProbeResult{Dir: dir, Children: children, Err: requestErr})
			}
			changed, applyErr := s.applyCatalogSyncProbes(ctx, sourceID, generation, runID, results)
			if applyErr != nil {
				return applyErr
			}
			state.DirsChanged += changed
			_ = s.persistCatalogSyncProgress(ctx, state)
			continue
		}

		// A path cursor walks each changed directory exactly once. A failed
		// listing keeps its row for the next daily run; retrying it inside this
		// run would either spin or force a full-table read per batch. Child
		// directories sort after their parent, so rows inserted by this pass
		// are still ahead of the cursor.
		changedDirs, queryErr := s.pendingCatalogSyncListings(ctx, sourceID, generation, listingCursor, catalogSyncBatch)
		if queryErr != nil {
			return queryErr
		}
		if len(changedDirs) == 0 {
			// A listing can insert a child directory that sorts before the last
			// path already consumed. One extra pass sees those leftovers; a row
			// that fails to list stays 'changed' and must not spin the worker,
			// so the retry count is bounded and the run reports partial.
			var remaining int
			if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_directory
				WHERE file_source_id=? AND generation=? AND sync_state='changed'`, sourceID, generation).Scan(&remaining); err != nil {
				return err
			}
			if remaining == 0 {
				break
			}
			if listingPasses >= 2 {
				state.Note = appendCatalogSyncNote(state.Note, fmt.Sprintf("%d changed directories remained unlisted", remaining))
				failed = true
				break
			}
			listingPasses++
			listingCursor = ""
			continue
		}
		listingCursor = changedDirs[len(changedDirs)-1].Path
		for _, dir := range changedDirs {
			entries, pages, requestErr := s.fetchCatalogSyncListing(ctx, client, dir, options)
			state.Requests += pages
			if requestErr != nil {
				failed = true
				state.Note = appendCatalogSyncNote(state.Note, fmt.Sprintf("list %s: %v", dir.Path, requestErr))
				if markErr := s.markCatalogSyncListFailure(ctx, sourceID, generation, runID, dir, requestErr); markErr != nil {
					return markErr
				}
				continue
			}
			published, changed, applyErr := s.applyCatalogSyncListing(ctx, sourceID, generation, runID, dir, entries, options)
			if applyErr != nil {
				return applyErr
			}
			state.DirsListed++
			state.DirsChanged += changed
			state.EntriesPublished += published
		}
		_ = s.persistCatalogSyncProgress(ctx, state)
	}

	s.recordCatalogPhase(context.WithoutCancel(ctx), lease.WorkflowRunID, "scan", scanStarted)
	if err = s.refreshCatalogSyncCounters(ctx, sourceID); err != nil {
		return err
	}
	if state.DirsListed > 0 || state.DirsChanged > 0 || failed || state.Force {
		publishStarted := time.Now()
		published, publishErr := s.publishCatalogSyncChanges(ctx, lease.Job, sourceID)
		s.recordCatalogPhase(context.WithoutCancel(ctx), lease.WorkflowRunID, "publish", publishStarted)
		if publishErr != nil {
			return publishErr
		}
		state.EntriesPublished += published
		lifecycleStarted := time.Now()
		lifecycleErr := s.settleCatalogSyncLifecycle(ctx, lease.Job, sourceID, generation)
		s.recordCatalogPhase(context.WithoutCancel(ctx), lease.WorkflowRunID, "lifecycle", lifecycleStarted)
		if lifecycleErr != nil {
			return lifecycleErr
		}
		if lifecycleErr = s.reconcileCatalogSyncEmptyEntries(ctx, sourceID, generation); lifecycleErr != nil {
			return lifecycleErr
		}
	}
	if failed {
		state.Status = "partial"
	}
	if state.Status == "" || state.Status == "running" {
		state.Status = "succeeded"
	}
	return nil
}

// pauseCatalogSync reports why an incremental sync must stop and when it may
// try again. A deployment hold ends at a known time; a disk shortfall is
// re-checked after a short delay rather than after the full daily interval.
func (s *Server) pauseCatalogSync(ctx context.Context, options catalogIndexOptions) (string, time.Time) {
	if held, until, err := s.catalogMaintenanceActive(ctx); err != nil {
		return "maintenance_state_unknown", time.Now().Add(time.Hour)
	} else if held {
		return "maintenance", until
	}
	if reason := s.catalogIndexDiskPause(options); reason != "" {
		return reason, time.Now().Add(time.Hour)
	}
	return "", time.Time{}
}

// settleInterruptedCatalogSyncTiming runs once at startup. A process that
// stopped mid-sync leaves a running row and its workflow records behind; both
// are marked failed so the next daily tick can start instead of being skipped
// for six hours by the partial unique index.
func (s *Server) settleInterruptedCatalogSyncTiming(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM source_catalog_sync_run WHERE status='running'`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil || len(ids) == 0 {
		return err
	}
	for _, id := range ids {
		if err = s.finishCatalogSyncRun(ctx, id, "failed", catalogSyncRunState{ID: id, Note: "startup interrupted before completion"}); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_job SET status='failed',error_message=CASE WHEN error_message='' THEN 'startup interrupted before completion' ELSE error_message END,locked_by='',locked_at=NULL,heartbeat_at=NULL,updated_at=CURRENT_TIMESTAMP
		WHERE worker_type=? AND status IN ('running','queued')`, sourceCatalogSyncWorkerType)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_node_run SET status='failed',finished_at=CURRENT_TIMESTAMP
		WHERE id IN (SELECT workflow_node_run_id FROM workflow_job WHERE worker_type=? AND status='failed')`, sourceCatalogSyncWorkerType)
	return err
}

func (s *Server) seedCatalogSyncDirectories(ctx context.Context, sourceID, generation int64, force bool) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if force {
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET sync_state='changed' WHERE file_source_id=? AND generation=? AND sync_state<>'excluded'`, sourceID, generation)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET sync_state='changed'
			WHERE file_source_id=? AND generation=? AND sync_state<>'excluded'
			AND (path='/' OR status IN ('pending','listing_complete','failed','restricted')
				OR (status='empty' AND error_code<>'not_found'))`, sourceID, generation)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) pendingCatalogSyncProbes(ctx context.Context, sourceID, generation, runID int64, limit int) ([]catalogSyncDirectoryState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.path,d.partition_path,d.catalog_root,d.revision,d.status,d.sync_state,d.observed_version,d.listing_digest
		FROM source_catalog_directory d
		WHERE d.file_source_id=? AND d.generation=? AND d.sync_state<>'excluded' AND d.last_verified_run<>?
			AND (d.path='/' OR EXISTS(
				SELECT 1 FROM source_catalog_observation o
				WHERE o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision AND o.kind='folder'))
		ORDER BY d.path LIMIT ?`, sourceID, generation, runID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	dirs := []catalogSyncDirectoryState{}
	for rows.Next() {
		var dir catalogSyncDirectoryState
		if err = rows.Scan(&dir.Path, &dir.Partition, &dir.Root, &dir.Revision, &dir.Status, &dir.SyncState, &dir.ObservedVersion, &dir.ListingDigest); err != nil {
			return nil, err
		}
		dirs = append(dirs, dir)
	}
	return dirs, rows.Err()
}

// pendingCatalogSyncListings returns the next page of changed directories
// after the given path. The caller advances the cursor to the last row, so one
// run reads every row once even on a 14k-directory source.
func (s *Server) pendingCatalogSyncListings(ctx context.Context, sourceID, generation int64, after string, limit int) ([]catalogSyncDirectoryState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,partition_path,catalog_root,revision,status,sync_state,observed_version,listing_digest
		FROM source_catalog_directory
		WHERE file_source_id=? AND generation=? AND sync_state='changed' AND path>?
		ORDER BY path LIMIT ?`, sourceID, generation, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	dirs := []catalogSyncDirectoryState{}
	for rows.Next() {
		var dir catalogSyncDirectoryState
		if err = rows.Scan(&dir.Path, &dir.Partition, &dir.Root, &dir.Revision, &dir.Status, &dir.SyncState, &dir.ObservedVersion, &dir.ListingDigest); err != nil {
			return nil, err
		}
		dirs = append(dirs, dir)
	}
	return dirs, rows.Err()
}

func (s *Server) applyCatalogSyncProbes(ctx context.Context, sourceID, generation, runID int64, results []catalogSyncProbeResult) (int, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	changed := 0
	for _, result := range results {
		if result.Err != nil {
			if _, updateErr := tx.ExecContext(ctx, `UPDATE source_catalog_directory SET last_verified_run=?,last_verified_at=CURRENT_TIMESTAMP WHERE file_source_id=? AND generation=? AND path=?`, runID, sourceID, generation, result.Dir.Path); updateErr != nil {
				return 0, updateErr
			}
			continue
		}
		count, applyErr := s.applyCatalogSyncProbeTx(ctx, tx.Tx, sourceID, generation, runID, result)
		if applyErr != nil {
			return 0, applyErr
		}
		changed += count
	}
	return changed, tx.Commit()
}

func (s *Server) applyCatalogSyncProbeTx(ctx context.Context, tx *sql.Tx, sourceID, generation, runID int64, result catalogSyncProbeResult) (int, error) {
	parent := result.Dir
	byName := map[string]openlist.Object{}
	for _, child := range result.Children {
		if _, exists := byName[child.Name]; exists {
			return 0, openlist.ErrInvalid
		}
		byName[child.Name] = child
	}
	type existingRow struct {
		state, observed, status string
	}
	existing := map[string]existingRow{}
	lower, upper, nameOffset := parent.Path+"/", parent.Path+"0", len(parent.Path)+2
	if parent.Path == "/" {
		lower, upper, nameOffset = "/", "0", 2
	}
	rows, err := tx.QueryContext(ctx, `SELECT path,sync_state,observed_version,status FROM source_catalog_directory
		WHERE file_source_id=? AND generation=? AND path>=? AND path<?
			AND instr(substr(path,?),'/')=0`,
		sourceID, generation, lower, upper, nameOffset)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var childPath, state, observed, status string
		if err = rows.Scan(&childPath, &state, &observed, &status); err != nil {
			break
		}
		if path.Dir(childPath) == parent.Path {
			existing[path.Base(childPath)] = existingRow{state: state, observed: observed, status: status}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	changedCount := 0
	for name, child := range byName {
		childPath := path.Join(parent.Path, name)
		// An excluded subtree is invisible to the index, but a path that leads
		// to a whitelist must still exist so the exception can be reached. The
		// publish path applies the strict rule, so a bridge directory never
		// publishes anything from the excluded part of its tree.
		traversable, excludeErr := catalogPathTraversableTx(ctx, tx, sourceID, childPath)
		if excludeErr != nil {
			return 0, excludeErr
		}
		if !traversable {
			continue
		}
		row, ok := existing[name]
		if !ok {
			// A damaged name cannot be requested. Record it and leave the row out
			// of the generation, exactly as the full index does.
			if catalogNameEncodingDamaged(name) {
				if _, err = recordCatalogEncodingIssueTx(ctx, tx, sourceID, parent.Path, name); err != nil {
					return 0, err
				}
				continue
			}
			partition := parent.Partition
			if parent.Path == "/" {
				partition = childPath
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status,sync_state,observed_version,last_verified_run,last_verified_at)
				VALUES(?,?,?,?,?,'pending','changed',?,0,CURRENT_TIMESTAMP)`, sourceID, childPath, generation, partition, parent.Root, child.Modified); err != nil {
				return 0, err
			}
			if err = addCatalogDirectoryCountersTx(ctx, tx, sourceID); err != nil {
				return 0, err
			}
			continue
		}
		state := row.state
		if row.state == "" && row.observed == "" && row.status == "complete" {
			state = "unchanged"
		} else if child.Modified != row.observed {
			state = "changed"
			changedCount++
		} else if state == "" {
			state = "unchanged"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET observed_version=?,sync_state=? WHERE file_source_id=? AND path=?`, child.Modified, state, sourceID, childPath); err != nil {
			return 0, err
		}
	}

	// A folder that disappeared from dirs is a confirmed deletion. Mark the
	// directory row and its descendants as gone now, so the next lifecycle pass
	// can retire its cards even when the parent listing is deferred.
	rows, err = tx.QueryContext(ctx, `SELECT name FROM source_catalog_observation
		WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='folder'`, sourceID, parent.Path, parent.Revision)
	if err != nil {
		return 0, err
	}
	missing := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		if _, ok := byName[name]; !ok {
			missing = append(missing, name)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	for _, name := range missing {
		childPath := path.Join(parent.Path, name)
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET sync_state='excluded',status='empty',error_code='not_found',completed_at=CURRENT_TIMESTAMP
			WHERE file_source_id=? AND generation=? AND (path=? OR (path>=? AND path<?))`, sourceID, generation, childPath, childPath+"/", childPath+"0"); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND name=?`, sourceID, parent.Path, name); err != nil {
			return 0, err
		}
	}
	state := "unchanged"
	if parent.SyncState == "changed" || len(missing) > 0 {
		state = "changed"
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET sync_state=?,last_verified_run=?,last_verified_at=CURRENT_TIMESTAMP WHERE file_source_id=? AND path=?`, state, runID, sourceID, parent.Path)
	if err != nil {
		return 0, err
	}
	return changedCount, nil
}

func (s *Server) fetchCatalogSyncListing(ctx context.Context, client *openlist.Client, dir catalogSyncDirectoryState, options catalogIndexOptions) ([]openlist.Object, int, error) {
	entries := []openlist.Object{}
	pages := 0
	expected := -1
	for page := 1; ; page++ {
		if page > options.MaxPagesPerDirectory {
			return nil, pages, openlist.ErrLimit
		}
		data, err := client.ListPage(ctx, dir.Path, page, options.PageSize)
		pages++
		if err != nil {
			return nil, pages, err
		}
		if expected < 0 {
			expected = data.Total
		} else if expected != data.Total {
			return nil, pages, openlist.ErrInvalid
		}
		entries = append(entries, data.Content...)
		if len(entries) >= expected {
			break
		}
		if len(data.Content) == 0 {
			return nil, pages, openlist.ErrInvalid
		}
	}
	return entries, pages, nil
}

func (s *Server) markCatalogSyncListFailure(ctx context.Context, sourceID, generation, runID int64, dir catalogSyncDirectoryState, requestErr error) error {
	status, code := "failed", "upstream_unavailable"
	if errors.Is(requestErr, openlist.ErrNotFound) {
		status, code = "empty", "not_found"
	} else if errors.Is(requestErr, openlist.ErrRestricted) {
		status, code = "restricted", "access_restricted"
	} else if errors.Is(requestErr, openlist.ErrChallenged) {
		status, code = "restricted", "challenge_detected"
	} else if errors.Is(requestErr, openlist.ErrLimit) {
		code = "response_limit"
	} else if errors.Is(requestErr, openlist.ErrInvalid) {
		code = "invalid_page"
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET status=?,error_code=?,sync_state='changed',last_verified_run=0
		WHERE file_source_id=? AND generation=? AND path=?`, status, code, sourceID, generation, dir.Path)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) applyCatalogSyncListing(ctx context.Context, sourceID, generation, runID int64, dir catalogSyncDirectoryState, entries []openlist.Object, options catalogIndexOptions) (published, changed int, err error) {
	digest := catalogSyncDigest(entries)
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, 0, err
	}
	defer release()
	var revision int
	var status, state, root, oldDigest string
	if err = tx.QueryRowContext(ctx, `SELECT revision,status,sync_state,catalog_root,listing_digest FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, dir.Path).Scan(&revision, &status, &state, &root, &oldDigest); err != nil {
		return 0, 0, err
	}
	if state != "changed" {
		return 0, 0, tx.Commit()
	}
	if oldDigest == digest && digest != "" {
		newStatus := "complete"
		if len(entries) == 0 {
			newStatus = "empty"
		}
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET status=?,error_code='',sync_state='listed',last_verified_run=?,last_verified_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP WHERE file_source_id=? AND path=?`, newStatus, runID, sourceID, dir.Path)
		if err != nil {
			return 0, 0, err
		}
		return 0, 0, tx.Commit()
	}

	oldRows := map[string]catalogSyncObservation{}
	rows, err := tx.QueryContext(ctx, `SELECT name,kind,size_bytes,content_hash,modified_at,publish_pending FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=?`, sourceID, dir.Path, revision)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var row catalogSyncObservation
		if err = rows.Scan(&row.Name, &row.Kind, &row.Size, &row.Hash, &row.Modified, &row.Pending); err != nil {
			break
		}
		oldRows[row.Name] = row
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, 0, err
	}

	newRevision := revision + 1
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation SET revision=?,publish_pending=0 WHERE file_source_id=? AND directory_path=? AND revision=?`, newRevision, sourceID, dir.Path, revision); err != nil {
		return 0, 0, err
	}
	// An exclusion or a damaged name removes the child from this generation.
	// Filtering here keeps observations, directory rows and the publish queue
	// consistent; the probe path applies the same rule.
	visible := make([]openlist.Object, 0, len(entries))
	for _, obj := range entries {
		childPath := path.Join(dir.Path, obj.Name)
		traversable, excludeErr := catalogPathTraversableTx(ctx, tx.Tx, sourceID, childPath)
		if excludeErr != nil {
			return 0, 0, excludeErr
		}
		if !traversable {
			continue
		}
		if catalogNameEncodingDamaged(obj.Name) {
			if _, err = recordCatalogEncodingIssueTx(ctx, tx.Tx, sourceID, dir.Path, obj.Name); err != nil {
				return 0, 0, err
			}
			continue
		}
		visible = append(visible, obj)
	}
	entries = visible
	seen := map[string]bool{}
	for _, obj := range entries {
		name := obj.Name
		if seen[name] {
			return 0, 0, openlist.ErrInvalid
		}
		seen[name] = true
		kind := openlist.Kind(name, obj.IsDir)
		hash := obj.ContentHash()
		old, exists := oldRows[name]
		rowChanged := !exists || old.Kind != kind || old.Size != obj.Size || old.Hash != hash || old.Modified != obj.Modified
		if rowChanged {
			changed++
		}
		if exists {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation SET revision=?,kind=?,size_bytes=?,content_hash=?,modified_at=?,publish_pending=? WHERE file_source_id=? AND directory_path=? AND name=?`, newRevision, kind, obj.Size, hash, obj.Modified, catalogSyncBoolInt(rowChanged), sourceID, dir.Path, name)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash,modified_at,publish_pending,published_generation) VALUES(?,?,?,?,?,?,?,?,?,0)`, sourceID, dir.Path, name, newRevision, kind, obj.Size, hash, obj.Modified, catalogSyncBoolInt(rowChanged))
		}
		if err != nil {
			return 0, 0, err
		}
	}
	removed := []catalogSyncObservation{}
	for name, row := range oldRows {
		if !seen[name] {
			removed = append(removed, row)
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND name=?`, sourceID, dir.Path, name); err != nil {
				return 0, 0, err
			}
		}
	}
	for _, row := range removed {
		childPath := path.Join(dir.Path, row.Name)
		if row.Kind == "folder" {
			if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET sync_state='excluded',status='empty',error_code='not_found',completed_at=CURRENT_TIMESTAMP
				WHERE file_source_id=? AND generation=? AND (path=? OR (path>=? AND path<?))`, sourceID, generation, childPath, childPath+"/", childPath+"0"); err != nil {
				return 0, 0, err
			}
		} else if root != "" {
			// A split card keeps the directory in source_root and a group
			// suffix in root_path, so matching root_path alone would leave the
			// deleted audio playable through the split card. A file published
			// from a folded format copy also carries its own directory, so the
			// comparison uses the resolved path instead of one card-wide base.
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id IN (
					SELECT id FROM source_catalog_entry WHERE file_source_id=? AND `+catalogExclusionBaseRootSQL+`=?)
				AND (CASE WHEN source_root='' THEN
					(CASE WHEN ?='/' THEN '/' || relative_path ELSE ? || '/' || relative_path END)
					ELSE (CASE WHEN source_root='/' THEN '/' || relative_path ELSE source_root || '/' || relative_path END) END)=?`,
				sourceID, root, root, root, childPath); err != nil {
				return 0, 0, err
			}
		}
	}
	for _, obj := range entries {
		if !obj.IsDir {
			continue
		}
		childPath := path.Join(dir.Path, obj.Name)
		partition := dir.Partition
		if dir.Path == "/" {
			partition = childPath
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status,sync_state,observed_version,last_verified_run,last_verified_at)
			VALUES(?,?,?,?,?,'pending','changed',?,0,CURRENT_TIMESTAMP)
			ON CONFLICT(file_source_id,path) DO NOTHING`, sourceID, childPath, generation, partition, root, obj.Modified)
		if err != nil {
			return 0, 0, err
		}
	}
	newStatus := "complete"
	if len(entries) == 0 {
		newStatus = "empty"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET revision=?,status=?,error_code=?,catalog_root=?,listing_digest=?,sync_state='listed',last_verified_run=?,last_verified_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP WHERE file_source_id=? AND path=?`, newRevision, newStatus, catalogSyncEmptyCode(newStatus), root, digest, runID, sourceID, dir.Path); err != nil {
		return 0, 0, err
	}
	if status != "complete" || root == "" {
		d := catalogScanDirectory{Path: dir.Path, Partition: dir.Partition, Root: root, Revision: newRevision, Page: 1, Expected: -1}
		if err = completeCatalogDirectoryTx(ctx, tx.Tx, sourceID, generation, &d); err != nil {
			return 0, 0, err
		}
		root = d.Root
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET catalog_root=? WHERE file_source_id=? AND path=?`, root, sourceID, dir.Path); err != nil {
			return 0, 0, err
		}
	}
	if newStatus == "empty" {
		// A successful but empty listing remains unconfirmed. The card keeps
		// its identity and personal records, but it must stop advertising the
		// old files as playable while the range is unresolved.
		target := root
		if target == "" {
			target = dir.Path
		}
		if err = markCatalogRangeStatusTx(ctx, tx.Tx, sourceID, target, "unconfirmed_empty"); err != nil {
			return 0, 0, err
		}
	}
	change, err := refreshCatalogAudioRootStateTx(ctx, tx.Tx, sourceID, generation, dir.Path)
	if err != nil {
		return 0, 0, err
	}
	if newStatus == "complete" {
		if err = addCompletedDirectoryPublishPendingTx(ctx, tx.Tx, sourceID, generation, dir.Path, change); err != nil {
			return 0, 0, err
		}
	} else if change.Deleted {
		if err = addCatalogPublishPendingTx(ctx, tx.Tx, sourceID, -change.Pending); err != nil {
			return 0, 0, err
		}
	}
	if err = resetCatalogPublishCursorTx(ctx, tx.Tx, sourceID, dir.Path, true); err != nil {
		return 0, 0, err
	}
	return 0, changed, tx.Commit()
}

func catalogSyncEmptyCode(status string) string {
	if status == "empty" {
		return "unconfirmed_empty"
	}
	return ""
}

func catalogSyncDigest(entries []openlist.Object) string {
	names := make([]string, 0, len(entries))
	byName := make(map[string]openlist.Object, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
		byName[entry.Name] = entry
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		entry := byName[name]
		_, _ = fmt.Fprintf(digest, "%s%c%s%c%d%c%s%c%s%c", name, 0, openlist.Kind(name, entry.IsDir), 0, entry.Size, 0, entry.ContentHash(), 0, entry.Modified, 0)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (s *Server) refreshCatalogSyncCounters(ctx context.Context, sourceID int64) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = seedCatalogCountersTx(ctx, tx.Tx, sourceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) publishCatalogSyncChanges(ctx context.Context, job workflowJobRecord, sourceID int64) (int, error) {
	if err := s.refreshCatalogSyncCounters(ctx, sourceID); err != nil {
		return 0, err
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	if err = resetCatalogPublishCursorTx(ctx, tx.Tx, sourceID, "", true); err != nil {
		release()
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		release()
		return 0, err
	}
	release()
	published := 0
	for pass := 0; pass < catalogSyncMaxListBatches; pass++ {
		n, err := s.publishCatalogBatch(ctx, job, sourceID)
		if err != nil {
			return published, err
		}
		published += n
		if n == 0 {
			break
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return published, err
		}
	}
	return published, nil
}

func (s *Server) settleCatalogSyncLifecycle(ctx context.Context, job workflowJobRecord, sourceID, generation int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE source_catalog_scan SET lifecycle_stage='observe',lifecycle_cursor=0,publish_cleanup_cursor=0 WHERE file_source_id=?`, sourceID); err != nil {
		return err
	}
	lifecycleJob := job
	lifecycleJob.RunID = generation
	for pass := 0; pass < catalogSyncMaxLifecycle; pass++ {
		pending, err := s.settleCatalogLifecycleBatch(ctx, lifecycleJob, sourceID)
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
	}
	return nil
}

// reconcileCatalogSyncEmptyEntries applies the deletion rule after lifecycle.
// The lifecycle pass marks every card seen in the current generation available;
// an incremental sync can remove the last file without republishing the card.
// A card with no playable file is missing only when its range was fully
// observed, otherwise it remains unconfirmed-empty so a transient empty
// listing does not erase an evaluation object.
func (s *Server) reconcileCatalogSyncEmptyEntries(ctx context.Context, sourceID, generation int64) error {
	for {
		pending, err := s.reconcileCatalogSyncEmptyEntriesBatch(ctx, sourceID, generation)
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		if err := pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
}

// catalogReconcileEntriesPerBatch keeps the post-sync status transaction
// bounded even when a source has accumulated many empty cards.
const catalogReconcileEntriesPerBatch = 100

func (s *Server) reconcileCatalogSyncEmptyEntriesBatch(ctx context.Context, sourceID, generation int64) (bool, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, `SELECT e.id,`+catalogEffectiveRootSQL("e")+` FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND e.availability_status='available'
			AND NOT EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio')
		ORDER BY e.id LIMIT ?`,
		sourceID, catalogReconcileEntriesPerBatch)
	if err != nil {
		return false, err
	}
	type candidate struct {
		id   int64
		base string
	}
	entries := []candidate{}
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.id, &item.base); err != nil {
			rows.Close()
			return false, err
		}
		entries = append(entries, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	_ = rows.Close()
	if len(entries) == 0 {
		return false, tx.Commit()
	}
	bases := make([]string, 0, len(entries))
	for _, entry := range entries {
		bases = append(bases, entry.base)
	}
	uncertain, err := catalogRangeUncertainSetTx(ctx, tx.Tx, sourceID, generation, bases)
	if err != nil {
		return false, err
	}
	updates := []catalogEntryStatusUpdate{}
	for _, entry := range entries {
		status := "missing"
		if uncertain[entry.base] {
			status = "unconfirmed_empty"
		}
		updates = append(updates, catalogEntryStatusUpdate{ID: entry.id, Status: status})
	}
	if err = setCatalogEntryStatusesTx(ctx, tx.Tx, updates); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return len(entries) == catalogReconcileEntriesPerBatch, nil
}

func (s *Server) beginCatalogSyncLease(ctx context.Context, syncRunID, generation int64) (*catalogSyncLease, error) {
	token := s.workflowLeases.reserve()
	leaseCtx, cancel := context.WithCancel(ctx)
	s.workflowLeases.activate(token, syncRunID, 0, cancel)
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		s.workflowLeases.release(token)
		cancel()
		return nil, err
	}
	defer release()
	definitionID, err := workflow.EnsureDefinition(ctx, tx.Tx, sourceCatalogSyncWorkerType, "Sync source catalog", "Probe directory mtimes and publish only changed catalog rows.", map[string]any{"nodes": []map[string]string{{"id": "sync", "type": sourceCatalogSyncWorkerType, "displayName": "Sync source catalog"}}})
	if err != nil {
		s.workflowLeases.release(token)
		cancel()
		return nil, err
	}
	runID, err := workflow.InsertRun(ctx, tx.Tx, definitionID, sourceCatalogSyncWorkerType, "Sync source catalog", "running", "interval", "catalog incremental sync", map[string]any{"sourceId": syncRunID}, map[string]any{})
	if err != nil {
		s.workflowLeases.release(token)
		cancel()
		return nil, err
	}
	nodeID, err := workflow.InsertNodeRun(ctx, tx.Tx, runID, workflow.NodeRunSpec{NodeID: "sync", NodeType: sourceCatalogSyncWorkerType, DisplayName: "Sync source catalog", Position: 1, Status: "running", Input: map[string]any{"sourceId": syncRunID}})
	if err != nil {
		s.workflowLeases.release(token)
		cancel()
		return nil, err
	}
	jobID, err := workflow.InsertJob(ctx, tx.Tx, runID, workflow.JobSpec{NodeRunID: nodeID, WorkerType: sourceCatalogSyncWorkerType, Status: "running", Priority: workflow.JobPriorityBackground, ResourceKey: "remote:catalog", Payload: map[string]any{"syncRunId": syncRunID}, Checkpoint: map[string]any{"phase": "sync"}, Recoverable: false, MaxRetries: 1})
	if err != nil {
		s.workflowLeases.release(token)
		cancel()
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_job SET locked_by=?,locked_at=CURRENT_TIMESTAMP,heartbeat_at=CURRENT_TIMESTAMP WHERE id=?`, token, jobID); err != nil {
		s.workflowLeases.release(token)
		cancel()
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		s.workflowLeases.release(token)
		cancel()
		return nil, err
	}
	s.workflowLeases.activate(token, runID, jobID, cancel)
	job := workflowJobRecord{ID: jobID, RunID: generation, NodeRunID: nodeID, WorkerType: sourceCatalogSyncWorkerType, LockedBy: token, PayloadJSON: mustJSON(map[string]any{"syncRunId": syncRunID})}
	released := false
	releaseLease := func(status string) {
		if released {
			return
		}
		released = true
		s.workflowLeases.release(token)
		cancel()
		settleCtx, settleCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer settleCancel()
		_, _ = s.db.ExecContext(settleCtx, `UPDATE workflow_job SET status=?,error_message=CASE WHEN ?='succeeded' THEN '' ELSE error_message END,locked_by='',locked_at=NULL,heartbeat_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, status, jobID)
		_, _ = s.db.ExecContext(settleCtx, `UPDATE workflow_node_run SET status=?,finished_at=CURRENT_TIMESTAMP WHERE id=?`, status, nodeID)
		_, _ = s.db.ExecContext(settleCtx, `UPDATE workflow_run SET status=?,finished_at=CURRENT_TIMESTAMP WHERE id=?`, status, runID)
	}
	_ = leaseCtx
	return &catalogSyncLease{Job: job, WorkflowRunID: runID, release: releaseLease}, nil
}

func (s *Server) persistCatalogSyncProgress(ctx context.Context, state catalogSyncRunState) error {
	_, err := s.db.ExecContext(ctx, `UPDATE source_catalog_sync_run SET dirs_probed=?,dirs_changed=?,dirs_listed=?,entries_published=?,requests=?,note=? WHERE id=?`, state.DirsProbed, state.DirsChanged, state.DirsListed, state.EntriesPublished, state.Requests, state.Note, state.ID)
	return err
}

func (s *Server) finishCatalogSyncRun(ctx context.Context, runID int64, status string, state catalogSyncRunState) error {
	if status == "" {
		status = "succeeded"
	}
	note := strings.TrimSpace(state.Note)
	if len(note) > catalogSyncMaxNote {
		note = note[len(note)-catalogSyncMaxNote:]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE source_catalog_sync_run SET status=?,finished_at=CURRENT_TIMESTAMP,dirs_probed=?,dirs_changed=?,dirs_listed=?,entries_published=?,requests=?,note=? WHERE id=?`, status, state.DirsProbed, state.DirsChanged, state.DirsListed, state.EntriesPublished, state.Requests, note, runID)
	return err
}

func appendCatalogSyncNote(existing, addition string) string {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return existing
	}
	if existing == "" {
		return addition
	}
	combined := existing + "; " + addition
	if len(combined) > catalogSyncMaxNote {
		return combined[len(combined)-catalogSyncMaxNote:]
	}
	return combined
}
