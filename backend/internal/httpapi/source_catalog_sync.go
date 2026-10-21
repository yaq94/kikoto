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
	Job     workflowJobRecord
	release func(string)
}

func catalogSyncBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
	if err = setCatalogSyncNextUpdateTx(ctx, tx.Tx, sourceID, s.catalogTime()); err != nil {
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
	defer func() {
		status := state.Status
		if status == "" || status == "running" {
			status = "succeeded"
		}
		if runErr != nil {
			status = "failed"
			state.Note = appendCatalogSyncNote(state.Note, runErr.Error())
		}
		if state.DirsProbed == 0 && state.DirsListed == 0 && runErr == nil {
			state.Note = appendCatalogSyncNote(state.Note, "no non-leaf directories were probed")
		}
		_ = s.finishCatalogSyncRun(context.WithoutCancel(ctx), runID, status, state)
	}()

	options, generation, err := s.catalogSyncOptions(ctx, sourceID)
	if err != nil {
		return err
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
	attempted := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return err
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

		changedDirs, queryErr := s.pendingCatalogSyncListings(ctx, sourceID, generation, catalogSyncBatch)
		if queryErr != nil {
			return queryErr
		}
		unattempted := changedDirs[:0]
		for _, dir := range changedDirs {
			if !attempted[dir.Path] {
				unattempted = append(unattempted, dir)
			}
		}
		if len(unattempted) == 0 {
			break
		}
		for _, dir := range unattempted {
			attempted[dir.Path] = true
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

	if err = s.refreshCatalogSyncCounters(ctx, sourceID); err != nil {
		return err
	}
	if state.DirsListed > 0 || state.DirsChanged > 0 || failed || state.Force {
		published, publishErr := s.publishCatalogSyncChanges(ctx, lease.Job, sourceID)
		if publishErr != nil {
			return publishErr
		}
		state.EntriesPublished += published
		if err = s.settleCatalogSyncLifecycle(ctx, lease.Job, sourceID, generation); err != nil {
			return err
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
			AND (status IN ('pending','listing_complete','failed','restricted') OR (status='empty' AND error_code<>'not_found'))`, sourceID, generation)
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

func (s *Server) pendingCatalogSyncListings(ctx context.Context, sourceID, generation int64, limit int) ([]catalogSyncDirectoryState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,partition_path,catalog_root,revision,status,sync_state,observed_version,listing_digest
		FROM source_catalog_directory
		WHERE file_source_id=? AND generation=? AND sync_state='changed'
		ORDER BY path LIMIT 100000`, sourceID, generation)
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
		row, ok := existing[name]
		if !ok {
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
			rel := strings.TrimPrefix(path.Join(dir.Path, row.Name), strings.TrimRight(root, "/")+"/")
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE relative_path=? AND catalog_entry_id IN (SELECT id FROM source_catalog_entry WHERE file_source_id=? AND root_path=?)`, rel, sourceID, root); err != nil {
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
	return &catalogSyncLease{Job: job, release: releaseLease}, nil
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
