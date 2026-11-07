package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/workflow"
)

const sourceCatalogExclusionWorkerType = "source_catalog_exclusion"
const catalogExclusionBatchesPerInvocation = 16

type catalogExclusionLeaseKey struct{}
type catalogPublicationCommitKey struct{}

var errCatalogExclusionBusy = errors.New("another catalog operation owns this source")

func rejectPendingCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_publication WHERE file_source_id=? AND state='preparing' AND owner LIKE 'exclusion:%')`, sourceID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errCatalogExclusionBusy
	}
	return nil
}

type catalogExclusionInput struct {
	SourceID   int64            `json:"sourceId"`
	Rule       catalogExclusion `json:"rule"`
	Delete     bool             `json:"delete"`
	Apply      bool             `json:"apply"`
	Operation  int64            `json:"operation"`
	Generation int64            `json:"generation"`
}

type catalogExclusionCursor struct {
	Phase       string                 `json:"phase"`
	Entry       int64                  `json:"entry"`
	ActiveEntry int64                  `json:"activeEntry"`
	File        string                 `json:"file"`
	Directory   string                 `json:"directory"`
	Name        string                 `json:"name"`
	Generation  int64                  `json:"generation"`
	Visited     int64                  `json:"visited"`
	Counts      catalogExclusionCounts `json:"counts"`
}

// Rule configuration, publication ownership and the recoverable job are one
// durable decision. Readers continue using the publication's original catalog.
func queueCatalogExclusionTx(ctx context.Context, tx *sql.Tx, input catalogExclusionInput) (workflowJobRecord, error) {
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_publication WHERE file_source_id=? AND state='preparing')`, input.SourceID).Scan(&busy)
	if err != nil {
		return workflowJobRecord{}, err
	}
	if busy {
		return workflowJobRecord{}, errCatalogExclusionBusy
	}
	if busy, err = catalogFullIndexRunningTx(ctx, tx, input.SourceID); err != nil {
		return workflowJobRecord{}, err
	}
	if busy {
		return workflowJobRecord{}, errCatalogExclusionBusy
	}
	if busy, err = catalogIncrementalSyncRunningTx(ctx, tx, input.SourceID); err != nil {
		return workflowJobRecord{}, err
	}
	if busy {
		return workflowJobRecord{}, errCatalogExclusionBusy
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?),0)`, input.SourceID).Scan(&input.Generation); err != nil {
		return workflowJobRecord{}, err
	}
	definition, err := workflow.EnsureDefinition(ctx, tx, sourceCatalogExclusionWorkerType, "Reconcile catalog exclusion", "Apply a catalog rule in recoverable batches.", map[string]any{"nodes": []map[string]string{{"id": "reconcile", "type": sourceCatalogExclusionWorkerType, "displayName": "Reconcile catalog exclusion"}}})
	if err != nil {
		return workflowJobRecord{}, err
	}
	run, err := workflow.InsertRun(ctx, tx, definition, sourceCatalogExclusionWorkerType, "Reconcile catalog exclusion", "queued", "manual", "manual", map[string]any{"sourceId": input.SourceID, "requested_by_user_id": optionalUserID(ctx)}, map[string]any{})
	if err != nil {
		return workflowJobRecord{}, err
	}
	if err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_publication(file_source_id,owner) VALUES(?,?) RETURNING id`, input.SourceID, catalogPublicationOwner("exclusion", run)).Scan(&input.Operation); err != nil {
		return workflowJobRecord{}, err
	}
	node, err := workflow.InsertNodeRun(ctx, tx, run, workflow.NodeRunSpec{NodeID: "reconcile", NodeType: sourceCatalogExclusionWorkerType, DisplayName: "Reconcile catalog exclusion", Position: 1, Status: "queued", Input: input})
	if err != nil {
		return workflowJobRecord{}, err
	}
	job, err := workflow.InsertJob(ctx, tx, run, workflow.JobSpec{NodeRunID: node, WorkerType: sourceCatalogExclusionWorkerType, Status: "queued", Priority: workflow.JobPriorityBackground, ResourceKey: "remote:catalog", Payload: input, Checkpoint: map[string]any{"exclusion": catalogExclusionCursor{Phase: "entries"}}, Recoverable: true, MaxRetries: 3})
	return workflowJobRecord{ID: job, RunID: run, NodeRunID: node, WorkerType: sourceCatalogExclusionWorkerType, PayloadJSON: mustJSON(input)}, err
}

func (s *Server) runInlineCatalogExclusion(ctx context.Context, job workflowJobRecord) error {
	jobCtx, release, err := s.leaseInlineWorkflowJob(ctx, job)
	if errors.Is(err, errWorkflowJobQueued) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	if err = s.db.QueryRowContext(jobCtx, `SELECT locked_by,checkpoint_json FROM workflow_job WHERE id=?`, job.ID).Scan(&job.LockedBy, &job.CheckpointJSON); err != nil {
		return err
	}
	err = s.executeSourceCatalogExclusionJob(jobCtx, job)
	return s.handleWorkflowJobResult(ctx, jobCtx, job, err)
}

func (s *Server) executeSourceCatalogExclusionJob(ctx context.Context, job workflowJobRecord) (resultErr error) {
	baseCtx := ctx
	var input catalogExclusionInput
	if err := json.Unmarshal([]byte(job.PayloadJSON), &input); err != nil {
		return err
	}
	if input.SourceID <= 0 || input.Operation <= 0 {
		return errors.New("invalid catalog exclusion operation")
	}
	// Configuration remains live. A disabled source pauses the saved decision;
	// it does not restore access through its readable before-image.
	var enabled bool
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM file_source WHERE id=? AND source_type='openlist'`, input.SourceID).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return s.settleCatalogExclusionJob(ctx, job, "queued", "source_disabled", time.Now().Add(time.Hour))
	}
	options, _, err := s.catalogSyncOptions(ctx, input.SourceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	ctx = s.catalogPublicationContext(ctx, options)
	ctx = context.WithValue(ctx, catalogPublicationKey{}, input.Operation)
	ctx = context.WithValue(ctx, catalogExclusionLeaseKey{}, func(tx *sql.Tx) error {
		// The same transaction orders cancellation against the publication marker.
		// After success only publication cleanup remains, under the same context.
		var completed bool
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT status='succeeded',COALESCE((SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?),0) FROM workflow_job WHERE id=?`, input.SourceID, job.ID).Scan(&completed, &generation); err != nil {
			return err
		}
		if completed {
			return nil
		}
		if generation != input.Generation {
			return errors.New("catalog exclusion generation changed")
		}
		return verifyCatalogLease(ctx, tx, job)
	})
	ctx = context.WithValue(ctx, catalogPublicationCommitKey{}, func(tx *sql.Tx) error {
		return settleCatalogExclusionJobTx(ctx, tx, job, "succeeded", "", time.Time{})
	})
	defer func() {
		var paused catalogSyncPaused
		if errors.As(resultErr, &paused) {
			settle := context.WithValue(context.WithoutCancel(ctx), catalogPublicationResourceGuardKey{}, (func() error)(nil))
			settle = context.WithValue(settle, catalogPublicationKey{}, int64(0))
			resultErr = s.settleCatalogExclusionJob(settle, job, "queued", paused.reason, time.Now().Add(time.Hour))
		}
	}()
	for batches := 0; ; batches++ {
		if batches == catalogExclusionBatchesPerInvocation {
			return s.settleCatalogExclusionJob(ctx, job, "queued", "", time.Time{})
		}
		done, err := s.catalogExclusionBatch(ctx, job, input)
		if err != nil {
			return err
		}
		if done {
			break
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
	if err = s.commitCatalogPublication(ctx, input.Operation); err != nil {
		var completed bool
		if readErr := s.db.QueryRowContext(context.WithoutCancel(baseCtx), `SELECT status='succeeded' FROM workflow_job WHERE id=?`, job.ID).Scan(&completed); readErr != nil || !completed {
			return err
		}
		// Cleanup is reclaimable maintenance. Failure after the atomic marker
		// cannot turn the successful operation into a failed or cancelled job.
		slog.Warn("catalog exclusion publication cleanup deferred", "run", job.RunID, "error", err)
	}
	s.invalidateRemoteWorkCache(input.SourceID)
	if !input.Delete && input.Rule.Mode == catalogExclusionModeWhitelist {
		_, _ = s.startCatalogIncrementalSync(baseCtx, input.SourceID, false)
	}
	return nil
}

func (s *Server) settleCatalogExclusionJob(ctx context.Context, job workflowJobRecord, status, reason string, available time.Time) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return err
	}
	if err = settleCatalogExclusionJobTx(ctx, tx.Tx, job, status, reason, available); err != nil {
		return err
	}
	return tx.Commit()
}

func settleCatalogExclusionJobTx(ctx context.Context, tx *sql.Tx, job workflowJobRecord, status, reason string, available time.Time) error {
	var err error
	var at any
	if !available.IsZero() {
		at = available.UTC().Format("2006-01-02 15:04:05")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_job SET status=?,error_message=?,available_at=?,locked_by='',locked_at=NULL,heartbeat_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, reason, at, job.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_node_run SET status=?,error_message=?,finished_at=CASE WHEN ?='queued' THEN NULL ELSE CURRENT_TIMESTAMP END WHERE id=?`, status, reason, status, job.NodeRunID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_run SET status=?,summary_json=?,finished_at=CASE WHEN ?='queued' THEN NULL ELSE CURRENT_TIMESTAMP END WHERE id=?`, status, mustJSON(map[string]any{"reason": reason}), status, job.RunID); err != nil {
		return err
	}
	return nil
}

func (s *Server) retryCatalogExclusion(ctx context.Context, run int64) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_job WHERE workflow_run_id=? AND worker_type=? AND recoverable=1 AND status IN ('failed','cancelled','partial')`, run, sourceCatalogExclusionWorkerType).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errWorkflowRetryNoRecoverableJob
	}
	if err = resumeCatalogWorkflowTx(ctx, tx.Tx, run); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) catalogExclusionBatch(ctx context.Context, job workflowJobRecord, input catalogExclusionInput) (bool, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return false, err
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(checkpoint_json,'$.exclusion'),'{}') FROM workflow_job WHERE id=?`, job.ID).Scan(&raw); err != nil {
		return false, err
	}
	var cursor catalogExclusionCursor
	if err = json.Unmarshal([]byte(raw), &cursor); err != nil {
		return false, err
	}
	if cursor.Phase == "publish" {
		return true, nil
	}
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT owner FROM source_catalog_publication WHERE id=? AND file_source_id=? AND state='preparing'`, input.Operation, input.SourceID).Scan(&owner); err != nil {
		return false, err
	}
	if owner != catalogPublicationOwner("exclusion", job.RunID) {
		return false, errCatalogExclusionBusy
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?),0)`, input.SourceID).Scan(&generation); err != nil {
		return false, err
	}
	if generation != input.Generation {
		return false, errors.New("catalog exclusion generation changed")
	}
	switch cursor.Phase {
	case "entries":
		err = catalogExclusionEntryBatch(ctx, tx.Tx, input, &cursor)
	case "files":
		err = catalogExclusionFileBatch(ctx, tx.Tx, input, &cursor)
	case "observations":
		err = catalogExclusionObservationBatch(ctx, tx.Tx, input, &cursor)
	case "directories", "bridge":
		err = catalogExclusionDirectoryBatch(ctx, tx.Tx, input, &cursor)
	case "roots":
		err = catalogExclusionRootBatch(ctx, tx.Tx, input, &cursor)
	case "counters", "count_directories", "count_observations", "count_publish":
		err = catalogExclusionCounterBatch(ctx, tx.Tx, input, &cursor)
	default:
		return false, fmt.Errorf("invalid catalog exclusion phase %q", cursor.Phase)
	}
	if err != nil {
		return false, err
	}
	// Retain other checkpoint fields used by the durable runner and diagnostics.
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_job SET checkpoint_json=json_set(checkpoint_json,'$.exclusion',json(?)),progress_current=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, mustJSON(cursor), cursor.Visited, job.ID); err != nil {
		return false, err
	}
	return cursor.Phase == "publish", tx.Commit()
}

func catalogExclusionContains(root, p string) bool {
	return root == p || strings.HasPrefix(p, root+"/")
}

// Primary-key order deliberately avoids the source/root index: filtering that
// index then sorting by id would revisit the completed prefix on every page.
func catalogExclusionEntryBatch(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,"+catalogExclusionBaseRootSQL+" FROM source_catalog_entry NOT INDEXED WHERE file_source_id=? AND id>? ORDER BY id LIMIT 64", input.SourceID, cursor.Entry)
	if err != nil {
		return err
	}
	type entry struct {
		id   int64
		base string
	}
	batch := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.base); err != nil {
			break
		}
		batch = append(batch, e)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, e := range batch {
		if catalogExclusionContains(input.Rule.Path, e.base) {
			excluded, queryErr := catalogPathExcludedTx(ctx, tx, input.SourceID, e.base)
			if queryErr != nil {
				return queryErr
			}
			if input.Apply && excluded {
				_, err = tx.ExecContext(ctx, "UPDATE source_catalog_entry SET availability_status='excluded',updated_at=CURRENT_TIMESTAMP WHERE id=? AND availability_status<>'excluded'", e.id)
			} else if !input.Apply && !excluded {
				_, err = tx.ExecContext(ctx, "UPDATE source_catalog_entry SET availability_status=CASE WHEN status_checked_at IS NULL THEN 'available' ELSE 'unavailable' END,updated_at=CURRENT_TIMESTAMP WHERE id=? AND availability_status='excluded'", e.id)
			}
			if err != nil {
				return err
			}
		}
		cursor.Entry = e.id
		cursor.Visited++
	}
	if len(batch) < 64 {
		cursor.Phase = "files"
		cursor.Entry = 0
		if !input.Apply {
			cursor.Phase = "bridge"
		}
	}
	return nil
}

func catalogExclusionFileBatch(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	if cursor.ActiveEntry == 0 {
		var base string
		root := catalogExclusionBaseRootSQL
		err := tx.QueryRowContext(ctx, "SELECT id,"+root+" FROM source_catalog_entry NOT INDEXED WHERE file_source_id=? AND id>? AND (?="+root+" OR substr(?,1,length("+root+")+1)="+root+"||'/' OR substr("+root+",1,length(?)+1)=?||'/') ORDER BY id LIMIT 1", input.SourceID, cursor.Entry, input.Rule.Path, input.Rule.Path, input.Rule.Path, input.Rule.Path).Scan(&cursor.ActiveEntry, &base)
		if errors.Is(err, sql.ErrNoRows) {
			cursor.Phase = "observations"
			return nil
		}
		if err != nil {
			return err
		}
		if !catalogExclusionContains(input.Rule.Path, base) && !catalogExclusionContains(base, input.Rule.Path) {
			cursor.Entry, cursor.ActiveEntry = cursor.ActiveEntry, 0
			return nil
		}
	}
	var base string
	if err := tx.QueryRowContext(ctx, "SELECT "+catalogExclusionBaseRootSQL+" FROM source_catalog_entry WHERE id=?", cursor.ActiveEntry).Scan(&base); err != nil {
		return err
	}
	excluded, err := catalogPathExcludedTx(ctx, tx, input.SourceID, base)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT relative_path,COALESCE(NULLIF(source_root,''),?) FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path>? ORDER BY relative_path LIMIT 64`, base, cursor.ActiveEntry, cursor.File)
	if err != nil {
		return err
	}
	type file struct{ name, root string }
	files := []file{}
	for rows.Next() {
		var f file
		if err = rows.Scan(&f.name, &f.root); err != nil {
			break
		}
		files = append(files, f)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, f := range files {
		remove := excluded && catalogExclusionContains(input.Rule.Path, base)
		if !remove {
			remove, err = catalogPathExcludedTx(ctx, tx, input.SourceID, strings.TrimRight(f.root, "/")+"/"+f.name)
			if err != nil {
				return err
			}
		}
		if remove {
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path=?`, cursor.ActiveEntry, f.name); err != nil {
				return err
			}
		}
		cursor.File = f.name
		cursor.Visited++
	}
	if len(files) < 64 {
		// Presence owns source state only; no personal or stable identity row is removed.
		if _, err = tx.ExecContext(ctx, `UPDATE work_source_presence SET availability=CASE WHEN EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.work_id=work_source_presence.work_id AND e.file_source_id=work_source_presence.file_source_id AND e.relocated_to_id IS NULL AND e.availability_status='available') THEN 'available' ELSE 'unavailable' END WHERE file_source_id=? AND presence_type='source' AND work_id=(SELECT work_id FROM source_catalog_entry WHERE id=?)`, input.SourceID, cursor.ActiveEntry); err != nil {
			return err
		}
		cursor.Entry, cursor.ActiveEntry, cursor.File = cursor.ActiveEntry, 0, ""
	}
	return nil
}

func catalogExclusionObservationBatch(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	rows, err := tx.QueryContext(ctx, `SELECT directory_path,name FROM source_catalog_observation WHERE file_source_id=? AND (directory_path,name)>(?,?) ORDER BY directory_path,name LIMIT 64`, input.SourceID, cursor.Directory, cursor.Name)
	if err != nil {
		return err
	}
	type item struct{ directory, name string }
	batch := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.directory, &i.name); err != nil {
			break
		}
		batch = append(batch, i)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, i := range batch {
		full := strings.TrimRight(i.directory, "/") + "/" + i.name
		if catalogExclusionContains(input.Rule.Path, i.directory) || catalogExclusionContains(input.Rule.Path, full) {
			hit, err := catalogPathExcludedTx(ctx, tx, input.SourceID, full)
			if err != nil {
				return err
			}
			if hit {
				if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND name=?`, input.SourceID, i.directory, i.name); err != nil {
					return err
				}
			}
		}
		cursor.Directory, cursor.Name = i.directory, i.name
		cursor.Visited++
	}
	if len(batch) < 64 {
		cursor.Phase, cursor.Directory, cursor.Name = "directories", "", ""
	}
	return nil
}

func catalogExclusionDirectoryBatch(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	rows, err := tx.QueryContext(ctx, `SELECT path FROM source_catalog_directory WHERE file_source_id=? AND path>? ORDER BY path LIMIT 64`, input.SourceID, cursor.Directory)
	if err != nil {
		return err
	}
	batch := []string{}
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			break
		}
		batch = append(batch, p)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range batch {
		if cursor.Phase == "bridge" {
			if !input.Delete && input.Rule.Mode == catalogExclusionModeWhitelist && catalogExclusionContains(p, input.Rule.Path) {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET sync_state='changed' WHERE file_source_id=? AND path=?`, input.SourceID, p)
			}
		} else if catalogExclusionContains(input.Rule.Path, p) {
			traversable, e := catalogPathTraversableTx(ctx, tx, input.SourceID, p)
			if e != nil {
				return e
			}
			if !traversable {
				_, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_directory WHERE file_source_id=? AND path=?`, input.SourceID, p)
			}
		}
		if err != nil {
			return err
		}
		cursor.Directory = p
		cursor.Visited++
	}
	if len(batch) < 64 {
		if cursor.Phase == "bridge" {
			cursor.Phase = "counters"
		} else {
			cursor.Phase = "roots"
		}
		cursor.Directory = ""
	}
	return nil
}

func catalogExclusionRootBatch(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	rows, err := tx.QueryContext(ctx, `SELECT generation,catalog_root FROM source_catalog_audio_root WHERE file_source_id=? AND (generation,catalog_root)>(?,?) ORDER BY generation,catalog_root LIMIT 64`, input.SourceID, cursor.Generation, cursor.Directory)
	if err != nil {
		return err
	}
	type root struct {
		generation int64
		path       string
	}
	batch := []root{}
	for rows.Next() {
		var r root
		if err = rows.Scan(&r.generation, &r.path); err != nil {
			break
		}
		batch = append(batch, r)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, r := range batch {
		if catalogExclusionContains(input.Rule.Path, r.path) {
			traversable, e := catalogPathTraversableTx(ctx, tx, input.SourceID, r.path)
			if e != nil {
				return e
			}
			if !traversable {
				if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_audio_root WHERE file_source_id=? AND generation=? AND catalog_root=?`, input.SourceID, r.generation, r.path); err != nil {
					return err
				}
			}
		}
		cursor.Generation, cursor.Directory = r.generation, r.path
		cursor.Visited++
	}
	if len(batch) < 64 {
		cursor.Phase = "counters"
	}
	return nil
}
