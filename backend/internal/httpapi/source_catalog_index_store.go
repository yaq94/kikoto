package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

type catalogScanDirectory struct {
	Path, Partition, Root              string
	Revision, Page, Expected, Attempts int
}

func enqueueCatalogDirectoryTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, p, partition, root string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root) VALUES(?,?,?,?,?)
	ON CONFLICT(file_source_id,path) DO UPDATE SET generation=excluded.generation,partition_path=excluded.partition_path,catalog_root=excluded.catalog_root,revision=source_catalog_directory.revision+1,next_page=1,expected_total=-1,children_cursor='',status='pending',attempts=0,error_code='',completed_at=NULL WHERE source_catalog_directory.generation<>excluded.generation`, sourceID, p, generation, partition, root)
	return err
}

func (s *Server) pendingCatalogDirectories(ctx context.Context, sourceID, generation int64, limit int) ([]catalogScanDirectory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,partition_path,catalog_root,revision,next_page,expected_total,attempts FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status='pending' ORDER BY path LIMIT ?`, sourceID, generation, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	dirs := []catalogScanDirectory{}
	for rows.Next() {
		var d catalogScanDirectory
		if err = rows.Scan(&d.Path, &d.Partition, &d.Root, &d.Revision, &d.Page, &d.Expected, &d.Attempts); err != nil {
			return nil, err
		}
		dirs = append(dirs, d)
	}
	return dirs, rows.Err()
}

// Every page and its next cursor commit together; no pooled connection remains
// checked out while an HTTP response is pending.
func (s *Server) persistCatalogPage(ctx context.Context, job workflowJobRecord, sourceID int64, d catalogScanDirectory, data openlist.Page, requestErr error, options catalogIndexOptions) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return err
	}
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=? AND status='pending'`, sourceID, job.RunID, d.Path).Scan(&revision); err != nil {
		return err
	}
	if revision != d.Revision {
		return context.Canceled
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET requests=requests+1,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, sourceID); err != nil {
		return err
	}
	status, errorCode := "pending", ""
	if requestErr != nil {
		status, errorCode = "failed", "upstream_unavailable"
		if errors.Is(requestErr, openlist.ErrChallenged) {
			// A bot challenge is a transport-level refusal: never retried
			// tightly, and never reported as an anonymous permission denial.
			status, errorCode = "restricted", "challenge_detected"
		}
		if errors.Is(requestErr, openlist.ErrRestricted) {
			status, errorCode = "restricted", "access_restricted"
		}
		if errors.Is(requestErr, openlist.ErrLimit) {
			errorCode = "response_limit"
		}
		if errors.Is(requestErr, openlist.ErrInvalid) {
			errorCode = "invalid_page"
		}
	} else if d.Expected >= 0 && data.Total != d.Expected {
		if d.Attempts >= 2 {
			status, errorCode = "failed", "pagination_changed"
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET next_page=1,expected_total=-1,revision=revision+1,attempts=attempts+1,error_code='pagination_changed' WHERE file_source_id=? AND path=?`, sourceID, d.Path)
			if err != nil {
				return err
			}
			return tx.Commit()
		}
	} else {
		var before int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=?`, sourceID, d.Path, d.Revision).Scan(&before); err != nil {
			return err
		}
		for _, obj := range data.Content {
			if len(path.Join(d.Path, obj.Name)) > 4096 {
				status, errorCode = "failed", "path_limit"
				break
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash,publish_pending) VALUES(?,?,?,?,?,?,?,1) ON CONFLICT(file_source_id,directory_path,name) DO UPDATE SET revision=excluded.revision,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,published_generation=0,publish_pending=1 WHERE source_catalog_observation.revision<>excluded.revision`, sourceID, d.Path, obj.Name, d.Revision, openlist.Kind(obj.Name, obj.IsDir), obj.Size, obj.ContentHash())
			if err != nil {
				return err
			}
		}
		// A rescan can remove names or replace a page revision. Rows that no
		// longer belong to the directory revision must not stay in the pending
		// queue until a later successful cleanup.
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0 WHERE file_source_id=? AND directory_path=? AND revision<>? AND publish_pending=1`, sourceID, d.Path, d.Revision); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=?`, sourceID, d.Path, d.Revision).Scan(&count); err != nil {
			return err
		}
		if status == "pending" {
			switch {
			case count == 0 && data.Total == 0:
				status, errorCode = "empty", "unconfirmed_empty"
			case count > data.Total:
				status, errorCode = "failed", "invalid_total"
			case count == data.Total:
				status = "listing_complete"
			case count == before:
				status, errorCode = "failed", "repeated_page"
			case d.Page >= options.MaxPagesPerDirectory:
				status, errorCode = "failed", "page_limit"
			}
		}
	}
	if status == "listing_complete" {
		if err = completeCatalogDirectoryTx(ctx, tx.Tx, sourceID, job.RunID, &d); err != nil {
			return err
		}
	}
	if status == "failed" || status == "restricted" || status == "empty" {
		availability := catalogFailureStatus(requestErr)
		if status == "empty" {
			availability = "unconfirmed_empty"
		}
		root := d.Path
		if d.Root != "" {
			root = d.Root
		}
		if err = markCatalogRangeStatusTx(ctx, tx.Tx, sourceID, root, availability); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET next_page=?,expected_total=?,status=?,error_code=?,catalog_root=?,completed_at=CASE WHEN ?='complete' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE file_source_id=? AND path=?`, d.Page+1, data.Total, status, errorCode, d.Root, status, sourceID, d.Path)
	if err != nil {
		return err
	}
	if err = refreshCatalogAudioRootTx(ctx, tx.Tx, sourceID, job.RunID, d.Path); err != nil {
		return err
	}
	return tx.Commit()
}

func verifyCatalogLease(ctx context.Context, tx *sql.Tx, job workflowJobRecord) error {
	var active int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_job WHERE id=? AND status='running' AND locked_by=?`, job.ID, job.LockedBy).Scan(&active)
	if err == nil && active != 1 {
		return context.Canceled
	}
	return err
}

func completeCatalogDirectoryTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, d *catalogScanDirectory) error {
	rows, err := tx.QueryContext(ctx, `SELECT name,kind FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=?`, sourceID, d.Path, d.Revision)
	if err != nil {
		return err
	}
	// Only one page of children is held at a time by SQL enqueue below. The
	// root decision needs counts, not an in-memory copy of the directory.
	audio, formats, otherDirs := 0, 0, 0
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			break
		}
		if kind == "audio" {
			audio++
		}
		if kind == "folder" {
			if catalogFormatDirectory(name) {
				formats++
			} else {
				otherDirs++
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	// An already published container keeps its identity even if a later scan
	// discovers episode markers below it. Splitting it needs explicit review.
	var existingRoot string
	err = tx.QueryRowContext(ctx, `SELECT root_path FROM source_catalog_entry WHERE file_source_id=? AND root_path IN (?,?) ORDER BY length(root_path) LIMIT 1`, sourceID, d.Path, d.Root).Scan(&existingRoot)
	if err == nil {
		d.Root = existingRoot
	} else if errors.Is(err, sql.ErrNoRows) {
		d.Root = catalogAdmissionRoot(d.Path, d.Root, catalogDirectorySummary{audio, formats, otherDirs})
	} else {
		return err
	}
	return nil
}

// expandCatalogDirectory releases the writer after at most 100 child enqueues.
// A large directory is not marked complete until its child cursor is exhausted.
func (s *Server) expandCatalogDirectory(ctx context.Context, job workflowJobRecord, sourceID int64) (bool, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return false, err
	}
	var p, partition, root, cursor string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT path,partition_path,catalog_root,children_cursor,revision FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status='listing_complete' ORDER BY path LIMIT 1`, sourceID, job.RunID).Scan(&p, &partition, &root, &cursor, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='folder' AND name>? ORDER BY name LIMIT 100`, sourceID, p, revision, cursor)
	if err != nil {
		return false, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	for _, name := range names {
		child := path.Join(p, name)
		part := partition
		if p == "/" {
			part = child
		}
		if err = enqueueCatalogDirectoryTx(ctx, tx.Tx, sourceID, job.RunID, child, part, root); err != nil {
			return false, err
		}
		cursor = name
	}
	status := "listing_complete"
	if len(names) < 100 {
		status = "complete"
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET children_cursor=?,status=?,completed_at=CASE WHEN ?='complete' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE file_source_id=? AND path=?`, cursor, status, status, sourceID, p)
	if err != nil {
		return false, err
	}
	if err = refreshCatalogAudioRootTx(ctx, tx.Tx, sourceID, job.RunID, p); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// refreshCatalogAudioRootTx keeps the publisher's join set exactly equal to
// "a complete directory with audio exists for this root". Call it after the
// directory status changes, so a rescan that removes the last audio row also
// removes the root from the publish queue.
func refreshCatalogAudioRootTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string) error {
	var root string
	err := tx.QueryRowContext(ctx, `SELECT catalog_root FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, directoryPath).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) || root == "" {
		return nil
	}
	if err != nil {
		return err
	}
	var present int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_directory a
		JOIN source_catalog_observation b ON b.file_source_id=a.file_source_id AND b.directory_path=a.path AND b.revision=a.revision
		WHERE a.file_source_id=? AND a.generation=? AND a.catalog_root=? AND a.status='complete' AND b.kind='audio')`, sourceID, generation, root).Scan(&present); err != nil {
		return err
	}
	if present == 1 {
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?,?,?) ON CONFLICT DO NOTHING`, sourceID, generation, root)
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_audio_root WHERE file_source_id=? AND generation=? AND catalog_root=?`, sourceID, generation, root)
	return err
}

// Publish a bounded number of validated observations. The first playable
// observation creates a catalog identity; it never registers a library work.
func (s *Server) publishCatalogBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (int, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `WITH roots AS MATERIALIZED (
	SELECT catalog_root FROM source_catalog_audio_root WHERE file_source_id=? AND generation=?
)
SELECT d.path,d.catalog_root,o.name,o.kind,o.size_bytes,o.content_hash
FROM source_catalog_observation o
JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path AND d.revision=o.revision
JOIN roots r ON r.catalog_root=d.catalog_root
WHERE o.file_source_id=? AND o.publish_pending=1 AND d.generation=? AND d.status='complete' AND d.catalog_root<>''
ORDER BY o.directory_path,o.name LIMIT 100`, sourceID, job.RunID, sourceID, job.RunID)
	if err != nil {
		return 0, err
	}
	type publication struct {
		dir, root, name, kind, hash string
		size                        int64
	}
	items := []publication{}
	for rows.Next() {
		var p publication
		if err = rows.Scan(&p.dir, &p.root, &p.name, &p.kind, &p.size, &p.hash); err != nil {
			break
		}
		items = append(items, p)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	for _, p := range items {
		var id int64
		err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_entry(file_source_id,root_path,primary_code,title,last_seen_generation,discovered_generation) VALUES(?,?,?,?,?,?) ON CONFLICT(file_source_id,root_path) DO UPDATE SET title=excluded.title,last_seen_generation=excluded.last_seen_generation,updated_at=CURRENT_TIMESTAMP RETURNING id`, sourceID, p.root, catalogDirectoryCode(p.root), path.Base(p.root), job.RunID, job.RunID).Scan(&id)
		if err != nil {
			return 0, err
		}
		if err = attachCatalogSeriesTx(ctx, tx.Tx, id); err != nil {
			return 0, err
		}
		rel := strings.TrimPrefix(path.Join(p.dir, p.name), strings.TrimRight(p.root, "/")+"/")
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation) VALUES(?,?,?,?,?,?,?) ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,observed_generation=excluded.observed_generation`, id, rel, p.name, p.kind, p.size, p.hash, job.RunID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation SET published_generation=?,publish_pending=0 WHERE file_source_id=? AND directory_path=? AND name=?`, job.RunID, sourceID, p.dir, p.name)
		if err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit()
}

func (s *Server) sourceCatalogProgress(ctx context.Context, sourceID int64, page int) (catalogIndexProgress, error) {
	p := catalogIndexProgress{Partitions: []catalogPartitionProgress{}, Details: []catalogDirectoryProgress{}}
	var options string
	err := s.db.QueryRowContext(ctx, `SELECT scan.workflow_run_id,run.status,scan.pause_reason,scan.options_json,scan.requests,scan.started_at,scan.updated_at FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id WHERE scan.file_source_id=?`, sourceID).Scan(&p.RunID, &p.Status, &p.PauseReason, &options, &p.Requests, &p.StartedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(options), &p.Options); err != nil {
		return p, err
	}
	if p.Status == "succeeded" {
		p.Status = "complete"
	}
	err = s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM source_catalog_directory WHERE file_source_id=?),(SELECT count(*) FROM source_catalog_observation WHERE file_source_id=?)`, sourceID, sourceID).Scan(&p.StagingDirectories, &p.StagingObservations)
	if err != nil {
		return p, err
	}
	if p.PauseReason != "" && p.Status == "partial" {
		p.Status = "paused"
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(status='complete'),0),coalesce(sum(status IN ('pending','listing_complete')),0),coalesce(sum(status='failed'),0),coalesce(sum(status='restricted'),0),coalesce(sum(status='empty'),0),count(DISTINCT partition_path) FROM source_catalog_directory WHERE file_source_id=? AND generation=?`, sourceID, p.RunID).Scan(&p.Directories, &p.Completed, &p.Pending, &p.Failed, &p.Restricted, &p.Empty, &p.PartitionTotal)
	if err != nil {
		return p, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT CASE WHEN o.published_generation=? THEN d.catalog_root END) FROM source_catalog_directory d JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision WHERE d.file_source_id=? AND d.generation=?`, p.RunID, sourceID, p.RunID).Scan(&p.Observed, &p.Indexed)
	if err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT partition_path,count(*),sum(status='complete'),sum(status='failed'),sum(status='restricted'),sum(status='empty') FROM source_catalog_directory WHERE file_source_id=? AND generation=? GROUP BY partition_path ORDER BY partition_path LIMIT 50 OFFSET ?`, sourceID, p.RunID, (page-1)*50)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var x catalogPartitionProgress
		if err = rows.Scan(&x.Path, &x.Directories, &x.Completed, &x.Failed, &x.Restricted, &x.Empty); err != nil {
			break
		}
		p.Partitions = append(p.Partitions, x)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return p, err
	}
	p.DetailsTotal = p.Directories - p.Completed
	rows, err = s.db.QueryContext(ctx, `SELECT path,status,next_page,error_code FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status<>'complete' ORDER BY path LIMIT 50 OFFSET ?`, sourceID, p.RunID, (page-1)*50)
	if err != nil {
		return p, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var x catalogDirectoryProgress
		if err = rows.Scan(&x.Path, &x.Status, &x.NextPage, &x.ErrorCode); err != nil {
			return p, err
		}
		p.Details = append(p.Details, x)
	}
	return p, rows.Err()
}
