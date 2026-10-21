package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

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
	started := time.Now()
	written := 0
	defer func() { observeCatalogWrite(ctx, "persist_page", started, written) }()
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
	// A backoff refusal happened before any request reached the source. It must
	// not be counted as an attempt and must not mark the directory failed: the
	// next pass retries the same page once the origin is allowed again.
	var backoff sourceBackoffError
	if errors.As(requestErr, &backoff) {
		return backoff
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET requests=requests+1,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, sourceID); err != nil {
		return err
	}
	status, errorCode := "pending", ""
	if requestErr != nil {
		status, errorCode = "failed", "upstream_unavailable"
		if errors.Is(requestErr, openlist.ErrNotFound) {
			// A 404/410 is a definitive answer: the directory no longer exists.
			// Recording it as a failed range made the run permanently partial
			// and blocked lifecycle for the whole source.
			status, errorCode = "empty", "not_found"
		}
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
			_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash,modified_at,publish_pending) VALUES(?,?,?,?,?,?,?,?,1) ON CONFLICT(file_source_id,directory_path,name) DO UPDATE SET revision=excluded.revision,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,modified_at=excluded.modified_at,published_generation=0,publish_pending=1 WHERE source_catalog_observation.revision<>excluded.revision`, sourceID, d.Path, obj.Name, d.Revision, openlist.Kind(obj.Name, obj.IsDir), obj.Size, obj.ContentHash(), obj.Modified)
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
		// The card that owns this directory reports the failure. The match is
		// still path-bounded: a failed /Work must not mark /Work2, and a split
		// card is reached through its real source_root.
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
	written = len(data.Content)
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
	// "Episode 1 (MP3)" sits beside "Episode 1" under the same series. The
	// format copy is the same episode, so its files join that episode's card
	// instead of creating a second member or a separate work. The sibling must
	// exist as a directory in this generation; otherwise the copy keeps its own
	// identity rather than pointing at a path that was never observed.
	if episode, ok := catalogEpisodeFormatCopy(path.Base(d.Path)); ok {
		sibling := path.Join(path.Dir(d.Path), episode)
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_directory
			WHERE file_source_id=? AND path=?)`, sourceID, sibling).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			d.Root = sibling
			return decideCatalogWorkSplitTx(ctx, tx, sourceID, generation, d.Path, d.Revision)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,kind FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=?`, sourceID, d.Path, d.Revision)
	if err != nil {
		return err
	}
	// Only one page of children is held at a time by SQL enqueue below. The
	// root decision needs counts, not an in-memory copy of the directory.
	audio, formats, auxiliary, tracks, otherDirs := 0, 0, 0, 0, 0
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			break
		}
		if kind == "audio" {
			audio++
		}
		if kind == "folder" {
			switch {
			case catalogFormatDirectory(name):
				formats++
			case catalogAuxiliaryDirectory(name):
				auxiliary++
			case catalogTrackContainerDirectory(name):
				tracks++
			default:
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
	finish := func() error {
		if d.Root == d.Path {
			return decideCatalogWorkSplitTx(ctx, tx, sourceID, generation, d.Path, d.Revision)
		}
		return nil
	}
	// A published container keeps its identity even if a later scan discovers
	// episode markers below it; splitting that needs explicit review. An exact
	// entry for this directory always wins, and a coded child never inherits a
	// parent entry: otherwise a rescan keeps folding several coded works into
	// the uploader card it created before this rule existed.
	var exact int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry WHERE file_source_id=? AND root_path=?)`,
		sourceID, d.Path).Scan(&exact); err != nil {
		return err
	}
	if exact != 0 {
		// An exact entry is this directory's own identity, even when it is
		// uncoded and lives below a published uploader card. A container that
		// already has its own entry still keeps its children separate, and a
		// grouping folder that now lists coded works stops owning them.
		if catalogContainerDirectory(path.Base(d.Path)) {
			d.Root = catalogAdmissionRoot(d.Path, "", catalogDirectorySummary{audio, formats, auxiliary, tracks, otherDirs})
			return finish()
		}
		var codedChild int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_observation o
			JOIN source_catalog_directory pd
				ON pd.file_source_id=o.file_source_id AND pd.path=o.directory_path
			WHERE o.file_source_id=? AND o.directory_path=? AND o.revision=pd.revision
				AND o.kind='folder'
				AND (UPPER(o.name) LIKE 'RJ%' OR UPPER(o.name) LIKE 'BJ%'
					OR UPPER(o.name) LIKE 'VJ%' OR UPPER(o.name) LIKE 'CC%'))`,
			sourceID, d.Path).Scan(&codedChild); err != nil {
			return err
		}
		if codedChild == 0 {
			// A format copy, auxiliary folder or track container keeps the root
			// it inherits. An entry published by an older generation must not
			// pin the directory to itself: that made every later admission
			// fix ineffective and left a second card for one release.
			if catalogWorkPartDirectory(path.Base(d.Path)) {
				d.Root = catalogAdmissionRoot(d.Path, d.Root, catalogDirectorySummary{audio, formats, auxiliary, tracks, otherDirs})
				return finish()
			}
			d.Root = d.Path
			return finish()
		}
		// The exact entry belongs to the grouping folder, not to the coded
		// works below it. Keep this directory's own loose audio in its card and
		// let each coded child decide from its own evidence.
		d.Root = catalogAdmissionRoot(d.Path, "", catalogDirectorySummary{audio, formats, auxiliary, tracks, otherDirs})
		if d.Root == "" && audio == 0 {
			d.Root = d.Path
		}
	}
	if catalogDirectoryCode(d.Path) != "" {
		d.Root = catalogAdmissionRoot(d.Path, "", catalogDirectorySummary{audio, formats, auxiliary, tracks, otherDirs})
		return finish()
	}
	if catalogContainerDirectory(path.Base(d.Path)) {
		d.Root = catalogAdmissionRoot(d.Path, "", catalogDirectorySummary{audio, formats, auxiliary, tracks, otherDirs})
		return finish()
	}
	// A published parent is a work container only while it has no coded child
	// directly below it. An uploader or voice-actor folder that lists coded works
	// is a grouping directory, so those works must not all inherit its card.
	if d.Root != "" && d.Root != d.Path {
		var codedSibling int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_observation o
			JOIN source_catalog_directory pd
				ON pd.file_source_id=o.file_source_id AND pd.path=o.directory_path
			WHERE o.file_source_id=? AND o.directory_path=? AND o.revision=pd.revision
				AND o.kind='folder'
				AND (UPPER(o.name) LIKE 'RJ%' OR UPPER(o.name) LIKE 'BJ%'
					OR UPPER(o.name) LIKE 'VJ%' OR UPPER(o.name) LIKE 'CC%'))`,
			sourceID, d.Root).Scan(&codedSibling); err != nil {
			return err
		}
		if codedSibling != 0 {
			// The parent was published before its coded children were seen.
			// This directory decides from its own evidence instead.
			d.Root = ""
		}
	}
	if d.Root != "" && catalogContainerDirectory(path.Base(d.Root)) {
		// A category parent never supplies the identity of an album below it.
		d.Root = ""
	}
	// A parent that already holds a reviewed work or a confirmed series is an
	// intentional container. Its uncoded episode and format children stay in
	// it, and splitting that needs explicit review. A merely published uploader
	// card has neither, so its children are admitted individually.
	var reviewedParent int
	if d.Root != "" {
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_entry e
			WHERE e.file_source_id=? AND e.root_path=?
				AND (e.work_id IS NOT NULL OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)))`,
			sourceID, d.Root).Scan(&reviewedParent); err != nil {
			return err
		}
	}
	if reviewedParent != 0 {
		// A reviewed card is not automatically a container. An uncoded child
		// that is a new self-contained work must receive its own card; only
		// episodes, format copies, auxiliary folders and track parts stay in
		// the reviewed object. Otherwise a creator card reviewed once absorbs
		// every later numberless work below it.
		if catalogContainerDirectory(path.Base(d.Root)) || catalogWorkPartDirectory(path.Base(d.Path)) {
			return nil
		}
		if _, episode := catalogEpisodeEvidence(d.Path); episode {
			return nil
		}
	}
	d.Root = catalogAdmissionRoot(d.Path, d.Root, catalogDirectorySummary{audio, formats, auxiliary, tracks, otherDirs})
	return finish()
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
	rows, err := tx.QueryContext(ctx, `SELECT name FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='folder' AND name>? ORDER BY name LIMIT ?`, sourceID, p, revision, cursor, catalogExpandRows)
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
	if len(names) < catalogExpandRows {
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

// catalogPublishQuery drives one publication batch from this generation's
// directories, not from the pending index: a materialized roots CTE made every
// publish batch scan the whole audio-root set (88ms per batch on a
// production-sized copy with zero rows to publish), and an observation-first
// plan would walk any rows a purge has not reached. The directory primary key
// orders the scan and the observation primary key orders each directory's rows,
// so no temporary sort is needed. The LIMIT placeholder is
// catalogPublishRows.
const catalogPublishQuery = `SELECT d.path,d.catalog_root,o.name,o.kind,o.size_bytes,o.content_hash,o.modified_at,
	-- The split decision belongs to the catalog root, so every row below
	-- it must see the plan. A format child that read an empty mode used to
	-- publish its own folder row into the creator card, which then could
	-- never be retired.
	COALESCE(rd.work_split,'')
FROM source_catalog_directory d
-- CROSS JOIN fixes the join order. Without it SQLite may start from the
-- pending index and sort every queued row, which turned a 50-row batch into a
-- multi-second statement on a source with a large backlog from an earlier
-- generation. The directory primary key already provides the ORDER BY d.path
-- prefix, so the batch stops as soon as it has enough rows.
CROSS JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
LEFT JOIN source_catalog_directory rd ON rd.file_source_id=d.file_source_id AND rd.path=d.catalog_root AND rd.generation=d.generation
WHERE d.file_source_id=? AND d.generation=? AND d.status='complete' AND d.catalog_root<>''
	AND o.publish_pending=1
	AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
		WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)
ORDER BY d.path,o.name LIMIT ?`

// Publish a bounded number of validated observations. The first playable
// observation creates a catalog identity; it never registers a library work.
func (s *Server) publishCatalogBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (int, error) {
	started := time.Now()
	published := 0
	defer func() { observeCatalogWrite(ctx, "publish_batch", started, published) }()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, catalogPublishQuery, sourceID, job.RunID, catalogPublishRows)
	if err != nil {
		return 0, err
	}
	type publication struct {
		dir, root, name, kind, hash, modified, split string
		size                                         int64
	}
	items := []publication{}
	for rows.Next() {
		var p publication
		if err = rows.Scan(&p.dir, &p.root, &p.name, &p.kind, &p.size, &p.hash, &p.modified, &p.split); err != nil {
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
	splitPlans := map[string]catalogSplitPlan{}
	parentNeeded := map[string]bool{}
	splitRoots := map[string]bool{}
	for _, p := range items {
		if p.split == catalogWorkSplitPrefix || p.split == catalogWorkSplitFile {
			if _, ok := splitPlans[p.root]; ok {
				continue
			}
			plan, needed, planErr := s.catalogSplitPlanForRoot(ctx, tx.Tx, sourceID, job.RunID, p.root)
			if planErr != nil {
				return 0, planErr
			}
			splitPlans[p.root] = plan
			parentNeeded[p.root] = needed
			splitRoots[p.root] = true
		}
	}
	firstSplit := map[string]int64{}
	publishedIDs := []int64{}
	// The configured source root has no directory name of its own. A card for
	// files placed directly there is titled after the source, not "/".
	rootTitle := ""
	for _, p := range items {
		if p.root == "/" {
			if rootTitle == "" {
				if err = tx.QueryRowContext(ctx, `SELECT display_name FROM file_source WHERE id=?`, sourceID).Scan(&rootTitle); err != nil {
					return 0, err
				}
			}
			break
		}
	}
	for _, p := range items {
		root, sourceRoot, title := p.root, "", path.Base(p.root)
		if p.root == "/" {
			title = rootTitle
		}
		if plan, ok := splitPlans[p.root]; ok && p.kind == "audio" {
			// Direct audio and its format copy carry the same work prefix, so
			// both belong to the split card. The shared plan keeps a nested
			// file on the parent unless its directory is a format or track
			// container, so a disc folder with an unrelated name is not
			// silently dropped.
			rel := strings.TrimPrefix(path.Join(p.dir, p.name), strings.TrimRight(p.root, "/")+"/")
			if target, ok := plan.targetFor(rel); ok {
				root, sourceRoot, title = target.Root, target.SourceRoot, target.Title
			}
		}
		if _, split := splitPlans[p.root]; split && !parentNeeded[p.root] && root == p.root && p.kind == "folder" {
			// The creator card was retired in favour of split cards. A folder
			// row still points at it; mark it published without recreating the
			// card (its file_source_id is now NULL, so the unique key would not
			// match the retired row). Readmes, covers and subtitles are kept
			// below by publishing them on the metadata-only parent card: they
			// describe the whole release and carry its credit evidence.
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation SET published_generation=?,publish_pending=0 WHERE file_source_id=? AND directory_path=? AND name=?`, job.RunID, sourceID, p.dir, p.name)
			if err != nil {
				return 0, err
			}
			continue
		}
		var id int64
		// The code is derived from the root name on every generation. An
		// entry published before code parsing understood a prefix keeps its
		// empty code unless the rescan rewrites it, and then search never
		// finds the work by its published number.
		err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_entry(file_source_id,root_path,source_root,primary_code,title,last_seen_generation,discovered_generation) VALUES(?,?,?,?,?,?,?) ON CONFLICT(file_source_id,root_path) DO UPDATE SET source_root=excluded.source_root,primary_code=excluded.primary_code,title=excluded.title,last_seen_generation=excluded.last_seen_generation,availability_status='available',status_checked_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP RETURNING id`, sourceID, root, sourceRoot, catalogEntryCode(root), title, job.RunID, job.RunID).Scan(&id)
		if err != nil {
			return 0, err
		}
		if root != p.root && firstSplit[p.root] == 0 {
			firstSplit[p.root] = id
		}
		if sourceRoot == "" {
			// A split card is already one work. Its title or file name is not
			// episode evidence, so it must not create or join a series.
			if err = attachCatalogSeriesTx(ctx, tx.Tx, id); err != nil {
				return 0, err
			}
		}
		publishedIDs = append(publishedIDs, id)
		// relative_path is relative to the real directory (source_root). The
		// card identity may carry a ::split suffix, so rebase on source_root.
		baseRoot := p.root
		if sourceRoot != "" {
			baseRoot = sourceRoot
		}
		rel := strings.TrimPrefix(path.Join(p.dir, p.name), strings.TrimRight(baseRoot, "/")+"/")
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,modified_at,observed_generation) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,modified_at=excluded.modified_at,observed_generation=excluded.observed_generation`, id, rel, p.name, p.kind, p.size, p.hash, p.modified, job.RunID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation SET published_generation=?,publish_pending=0 WHERE file_source_id=? AND directory_path=? AND name=?`, job.RunID, sourceID, p.dir, p.name)
		if err != nil {
			return 0, err
		}
	}
	// Retirement is a property of a finished generation, not of one publish
	// batch. Running it while other rows are still pending could delete the only
	// playable copy of a group that a later batch is about to publish, and it
	// made every publish batch scan the whole source. The final batch (or a
	// final call with nothing left to publish) performs it once.
	var stillPending int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path AND d.revision=o.revision
		WHERE o.file_source_id=? AND o.publish_pending=1 AND d.generation=? AND d.status='complete' AND d.catalog_root<>''
			AND (o.kind='audio' OR EXISTS(SELECT 1 FROM source_catalog_audio_root r
				WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)))`,
		sourceID, job.RunID).Scan(&stillPending); err != nil {
		return 0, err
	}
	publishIdle := stillPending == 0
	for root, plan := range splitPlans {
		if parentNeeded[root] || len(plan.direct) == 0 {
			continue
		}
		// Retire the creator card only after every observation below this
		// root has been published. Otherwise a later batch could add nested
		// audio to a row that is already an alias.
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_observation o
			JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
			WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=? AND o.publish_pending=1)`,
			sourceID, job.RunID, root).Scan(&pending); err != nil {
			return 0, err
		}
		if pending != 0 {
			continue
		}
		var incomplete int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_directory
			WHERE file_source_id=? AND generation=? AND catalog_root=? AND status<>'complete')`,
			sourceID, job.RunID, root).Scan(&incomplete); err != nil {
			return 0, err
		}
		if incomplete != 0 {
			continue
		}
		targetID := firstSplit[root]
		if targetID == 0 {
			if err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry WHERE file_source_id=? AND root_path=? AND source_root<>'' ORDER BY id LIMIT 1`, sourceID, root).Scan(&targetID); errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return 0, err
			}
		}
		if err = retireCatalogSplitParentTx(ctx, tx.Tx, sourceID, root, targetID); err != nil {
			return 0, err
		}
	}
	for root := range splitRoots {
		if !publishIdle {
			break
		}
		// A root that no longer splits (or now publishes fewer cards) must not
		// leave the cards of an earlier generation visible. Cards that still
		// belong to this generation, or carry a work/series/relocation, stay.
		// The root must also be fully observed: an older card is the only
		// playable copy while a child is still failed or unconfirmed.
		if incomplete, err := catalogRootIncomplete(ctx, tx.Tx, sourceID, job.RunID, root); err != nil {
			return 0, err
		} else if incomplete {
			continue
		}
		keep := map[int64]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry
			WHERE file_source_id=? AND source_root=? AND last_seen_generation=?`,
			sourceID, root, job.RunID)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			keep[id] = true
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return 0, err
		}
		if err = retireStaleSplitCardsTx(ctx, tx.Tx, sourceID, job.RunID, root, keep); err != nil {
			return 0, err
		}
	}
	// A root that is no longer split has a single card at parentRoot. Retire
	// split cards from older generations that pointed at the same source root.
	if !publishIdle {
		// Format and split identities stay untouched until every row of this
		// generation is published.
		if err = markCatalogSidecarCandidatesTx(ctx, tx.Tx, publishedIDs, job.RunID); err != nil {
			return 0, err
		}
		if err = tx.Commit(); err != nil {
			return 0, err
		}
		published = len(items)
		return published, nil
	}
	for _, p := range items {
		if p.split != "" && p.split != catalogWorkSplitSingle {
			continue
		}
		if incomplete, err := catalogRootIncomplete(ctx, tx.Tx, sourceID, job.RunID, p.root); err != nil {
			return 0, err
		} else if incomplete {
			continue
		}
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry WHERE file_source_id=? AND source_root=? AND last_seen_generation<>?)`, sourceID, p.root, job.RunID).Scan(&exists); err != nil {
			return 0, err
		}
		if exists != 0 {
			if err = retireStaleSplitCardsTx(ctx, tx.Tx, sourceID, job.RunID, p.root, map[int64]bool{}); err != nil {
				return 0, err
			}
		}
	}
	// A format copy that an older generation published as its own card is an
	// alias once the release card owns the audio. The scan below is independent
	// of the split plans: a format directory is `single`, so it never enters the
	// split loops, and its stale card would otherwise keep winning the
	// admission check in every later generation. Its own subtree must first be
	// fully observed, or deleting its files would hide unrepublished audio.
	// Retire stale format cards once per final publish batch, not on every
	// cleanup-only call: the scan below walks every stale card of the source and
	// repeated calls would multiply that cost for no new evidence.
	// A directory card whose audio now belongs to one deeper current card is
	// the same work seen twice. Like the format pass this walks every stale
	// card of the source, so it belongs to the final batch only.
	if len(items) > 0 {
		if err = retireCoveredContainerCardsTx(ctx, tx.Tx, sourceID, job.RunID); err != nil {
			return 0, err
		}
	}
	if len(items) > 0 {
		if err = retireStaleFormatCardsTx(ctx, tx.Tx, sourceID, job.RunID); err != nil {
			return 0, err
		}
	}
	// Queue credit files in the same transaction that publishes the card, so a
	// committed card is never forgotten between publication and the reader. The
	// reads run after commit and never hold this write lock.
	if err = markCatalogSidecarCandidatesTx(ctx, tx.Tx, publishedIDs, job.RunID); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	published = len(items)
	return published, nil
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
	// A confirmed 404/410 is a completed observation: the directory is gone,
	// which is exactly what the scan learned. Counting it in neither completed
	// nor unconfirmed made the partition total disagree with its parts.
	err = s.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(status='complete' OR (status='empty' AND error_code='not_found')),0),coalesce(sum(status IN ('pending','listing_complete')),0),coalesce(sum(status='failed'),0),coalesce(sum(status='restricted'),0),coalesce(sum(status='empty' AND error_code<>'not_found'),0),count(DISTINCT partition_path) FROM source_catalog_directory WHERE file_source_id=? AND generation=?`, sourceID, p.RunID).Scan(&p.Directories, &p.Completed, &p.Pending, &p.Failed, &p.Restricted, &p.Empty, &p.PartitionTotal)
	if err != nil {
		return p, err
	}
	// Indexed counts published cards, not distinct catalog roots. A creator
	// folder split into many works shares one root, so counting roots would
	// report one entry for a folder that published hundreds.
	err = s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM source_catalog_directory d JOIN source_catalog_observation o
			ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=? AND d.generation=?),
		(SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND last_seen_generation=?)`,
		sourceID, p.RunID, sourceID, p.RunID).Scan(&p.Observed, &p.Indexed)
	if err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT partition_path,count(*),sum(status='complete' OR (status='empty' AND error_code='not_found')),sum(status='failed'),sum(status='restricted'),sum(status='empty' AND error_code<>'not_found') FROM source_catalog_directory WHERE file_source_id=? AND generation=? GROUP BY partition_path ORDER BY partition_path LIMIT 50 OFFSET ?`, sourceID, p.RunID, (page-1)*50)
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

// sourceCatalogSidecarFailures counts the cards the worker can no longer pick
// up on its own. Eligibility is exactly the worker's predicate: pending, under
// the permanent budget, and not waiting on a transient backoff that already
// reached its stop threshold. A rescan leaves an exhausted card pending so it
// keeps its identity, so "pending" alone cannot be read as "will be retried".
func (s *Server) sourceCatalogSidecarFailures(ctx context.Context, sourceID int64) (catalogSidecarFailureCounts, error) {
	var counts catalogSidecarFailureCounts
	err := s.db.QueryRowContext(ctx, `SELECT count(*),
			coalesce(sum(sidecar_failure='temporarily_unavailable'),0),
			coalesce(sum(sidecar_failure='unreadable'),0),
			coalesce(sum(sidecar_failure='request_budget'),0)
		FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL
			AND sidecar_failure<>''
			AND (sidecar_pending=0 OR sidecar_attempts>=?
				OR (sidecar_transient_attempts>=? AND sidecar_failure='temporarily_unavailable'))`,
		sourceID, catalogSidecarMaxAttempts, catalogSidecarTransientTries).Scan(&counts.Stopped, &counts.TemporarilyUnavailable, &counts.Unreadable, &counts.RequestBudget)
	return counts, err
}
