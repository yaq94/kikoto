package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

type catalogScanDirectory struct {
	Path, Partition, Root              string
	Revision, Page, Expected, Attempts int
}

// catalogDirectoryCounterClass is the single classification shared by the
// incremental counters and the exact reconciliation query. A confirmed
// 404/410 is completed coverage; an unconfirmed empty directory is not.
func catalogDirectoryCounterClass(status, errorCode string) string {
	switch {
	case status == "complete" || (status == "empty" && errorCode == "not_found"):
		return "completed"
	case status == "failed":
		return "failed"
	case status == "restricted":
		return "restricted"
	case status == "empty":
		return "empty"
	default:
		return "pending"
	}
}

// moveCatalogDirectoryCountersTx records one status transition on the scan
// row inside the same transaction that changed the directory. Rows counted by
// the projection always belong to the scan's current generation; a caller that
// moves a row out of the current generation must not use this helper.
func moveCatalogDirectoryCountersTx(ctx context.Context, tx *sql.Tx, sourceID int64, fromStatus, fromCode, toStatus, toCode string) error {
	from, to := catalogDirectoryCounterClass(fromStatus, fromCode), catalogDirectoryCounterClass(toStatus, toCode)
	if from == to {
		return nil
	}
	updates := []string{from + "=" + from + "-1", to + "=" + to + "+1"}
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET `+strings.Join(updates, ",")+`,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, sourceID)
	return err
}

func enqueueCatalogDirectoryTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, p, partition, root string) (bool, error) {
	// A whitelist below this path still needs its ancestors listed, so the
	// traversal check is looser than the publish/cleanup rule.
	traversable, err := catalogPathTraversableTx(ctx, tx, sourceID, p)
	if err != nil {
		return false, err
	}
	if !traversable {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root) VALUES(?,?,?,?,?)
	ON CONFLICT(file_source_id,path) DO UPDATE SET generation=excluded.generation,partition_path=excluded.partition_path,catalog_root=excluded.catalog_root,revision=source_catalog_directory.revision+1,next_page=1,expected_total=-1,children_cursor='',status='pending',attempts=0,error_code='',completed_at=NULL WHERE source_catalog_directory.generation<>excluded.generation`, sourceID, p, generation, partition, root)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func addCatalogDirectoryCountersTx(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET directories=directories+1,pending=pending+1,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, sourceID)
	return err
}

func addCatalogObservationCountersTx(ctx context.Context, tx *sql.Tx, sourceID int64, observed, indexed int) error {
	if observed == 0 && indexed == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET observed=max(0,observed+?),indexed=max(0,indexed+?),updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, observed, indexed, sourceID)
	return err
}

// reconcileCatalogIndexedTx restores the one counter a retirement can lower.
// Retiring a card clears file_source_id, so only this column needs a recount
// after such a pass; observed and the directory classes are unchanged.
func reconcileCatalogIndexedTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET indexed=(SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND last_seen_generation=?) WHERE file_source_id=?`, sourceID, generation, sourceID)
	return err
}

func addCatalogPublishPendingTx(ctx context.Context, tx *sql.Tx, sourceID int64, delta int) error {
	if delta == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_pending_remaining=max(0,publish_pending_remaining+?),updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, delta, sourceID)
	return err
}

// seedCatalogCountersTx recomputes the projection once for a generation. It is
// used when a generation starts or resumes: both events rewrite statuses or
// revisions, and a full recount there is cheaper and safer than tracking every
// row the rewrite touches.
func seedCatalogCountersTx(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET
		directories=(SELECT count(*) FROM source_catalog_directory d
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id),
		completed=(SELECT count(*) FROM source_catalog_directory d
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
				AND (d.status='complete' OR (d.status='empty' AND d.error_code='not_found'))),
		pending=(SELECT count(*) FROM source_catalog_directory d
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
				AND d.status IN ('pending','listing_complete')),
		failed=(SELECT count(*) FROM source_catalog_directory d
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id AND d.status='failed'),
		restricted=(SELECT count(*) FROM source_catalog_directory d
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id AND d.status='restricted'),
		empty=(SELECT count(*) FROM source_catalog_directory d
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
				AND d.status='empty' AND d.error_code<>'not_found'),
		observed=(SELECT count(*) FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id),
		indexed=(SELECT count(*) FROM source_catalog_entry e
			WHERE e.file_source_id=source_catalog_scan.file_source_id AND e.last_seen_generation=source_catalog_scan.workflow_run_id),
		publish_pending_remaining=(SELECT count(*) FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
				AND d.status='complete' AND d.catalog_root<>'' AND o.publish_pending=1
				AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
					WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)),
		updated_at=CURRENT_TIMESTAMP
	WHERE source_catalog_scan.file_source_id=?`, sourceID)
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
	observedDelta := 0
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
			result, writeErr := tx.ExecContext(ctx, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash,modified_at,publish_pending) VALUES(?,?,?,?,?,?,?,?,1) ON CONFLICT(file_source_id,directory_path,name) DO UPDATE SET revision=excluded.revision,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,modified_at=excluded.modified_at,published_generation=0,publish_pending=1 WHERE source_catalog_observation.revision<>excluded.revision`, sourceID, d.Path, obj.Name, d.Revision, openlist.Kind(obj.Name, obj.IsDir), obj.Size, obj.ContentHash(), obj.Modified)
			if writeErr != nil {
				return writeErr
			}
			// The upsert only touches a row whose revision differs from this
			// directory revision, and exactly those rows enter the observed
			// projection. An unchanged row reports zero.
			changed, writeErr := result.RowsAffected()
			if writeErr != nil {
				return writeErr
			}
			observedDelta += int(changed)
		}
		// A rescan can remove names or replace a page revision. Rows that no
		// longer belong to the directory revision must not stay in the pending
		// queue until a later successful cleanup.
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0 WHERE file_source_id=? AND directory_path=? AND revision<>? AND publish_pending=1`, sourceID, d.Path, d.Revision); err != nil {
			return err
		}
		// An excluded path must not stay in the pending queue: it would keep
		// catalogRootIncomplete true even though the publisher will never see it.
		if err = suppressExcludedCatalogObservationsTx(ctx, tx.Tx, sourceID, d.Path, d.Revision); err != nil {
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
	// The row was selected as pending, so its previous class is always
	// pending; failed, restricted, empty and listing_complete all leave it.
	if err = moveCatalogDirectoryCountersTx(ctx, tx.Tx, sourceID, "pending", "", status, errorCode); err != nil {
		return err
	}
	if err = addCatalogObservationCountersTx(ctx, tx.Tx, sourceID, observedDelta, 0); err != nil {
		return err
	}
	change, err := refreshCatalogAudioRootStateTx(ctx, tx.Tx, sourceID, job.RunID, d.Path)
	if err != nil {
		return err
	}
	if change.Deleted {
		// Deleting the root makes every pending row below it ineligible.
		if err = addCatalogPublishPendingTx(ctx, tx.Tx, sourceID, -change.Pending); err != nil {
			return err
		}
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

// catalogCodedChildWorkTx reports a coded child directory that names a different
// work than the release it sits in. A child that repeats the parent's code is a
// nested copy (bonus disc, "RJ... MP3"), so it never turns the parent into a
// grouping folder: doing that disowned the parent's own format directories and
// could make the release card disappear when it had no direct audio.
func catalogCodedChildWorkTx(ctx context.Context, tx *sql.Tx, sourceID int64, directory, parentCode string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.name FROM source_catalog_observation o
		JOIN source_catalog_directory pd
			ON pd.file_source_id=o.file_source_id AND pd.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.revision=pd.revision
				AND o.kind='folder'`,
		sourceID, directory)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return 0, err
		}
		code := catalogDirectoryCode(name)
		if code == "" {
			continue
		}
		if parentCode != "" && strings.EqualFold(code, parentCode) {
			continue
		}
		return 1, nil
	}
	return 0, rows.Err()
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
			WHERE file_source_id=? AND path=? AND generation=?
				AND status<>'empty')`, sourceID, sibling, generation).Scan(&exists); err != nil {
			return err
		}
		positions, canonicalCounts, err := catalogEpisodePositionsTx(ctx, tx, sourceID, generation, path.Dir(d.Path))
		if err != nil {
			return err
		}
		// A bare sibling owns the episode. A format-only series has no bare
		// directory, so two format folders for the same canonical episode or a
		// parent with at least two distinct episode positions establish the
		// canonical root. An isolated format copy keeps its own card, which lets
		// a deleted episode stay missing instead of being revived by its copy.
		if exists != 0 || canonicalCounts[episode] >= 2 || positions >= 2 {
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
			// A package copy that repeats its parent's audio is one physical
			// version, not a second work. It must not publish a card of its
			// own; otherwise the parent listing and its copy both appear in
			// search and personal records can be split across two identities.
			// Only an unclaimed directory is eligible, so an intentionally
			// reviewed work or series member keeps its identity.
			var pinned int
			if err := tx.QueryRowContext(ctx, catalogSplitPinnedSQL, sourceID, d.Path).Scan(&pinned); err != nil {
				return err
			}
			if pinned == 0 {
				decision, duplicateErr := catalogDuplicateDirectoryDecisionTx(ctx, tx, sourceID, generation, d.Path)
				if duplicateErr != nil {
					return duplicateErr
				}
				if decision.Duplicate {
					// Keep the copy under the canonical parent root. Files that
					// repeat the canonical recording are consumed, while a
					// unique bonus stays pending and publishes into that card.
					// Only a copy with no unique audio receives the duplicate
					// split marker, which keeps the whole directory out of the
					// publisher as a second card.
					d.Root = decision.Root
					mode := catalogWorkSplitSingle
					if len(decision.Unique) == 0 {
						mode = catalogWorkSplitDuplicate
					}
					if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_directory
						SET work_split=?
						WHERE file_source_id=? AND path=? AND generation=?`,
						mode, sourceID, d.Path, generation); err != nil {
						return err
					}
					for _, file := range decision.Matched {
						if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_observation
							SET publish_pending=0
							WHERE file_source_id=? AND directory_path=? AND revision=? AND name=?`,
							sourceID, file.Directory, d.Revision, file.Name); err != nil {
							return err
						}
					}
					// Readmes and covers of a copy describe the same release and
					// must not create a second card either.
					if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_observation
						SET publish_pending=0
						WHERE file_source_id=? AND directory_path=? AND revision=? AND kind<>'audio'`,
						sourceID, d.Path, d.Revision); err != nil {
						return err
					}
					return nil
				}
			}
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
		codedChild, err := catalogCodedChildWorkTx(ctx, tx, sourceID, d.Path, catalogDirectoryCode(d.Path))
		if err != nil {
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
		d.Root = catalogAdmissionRoot(d.Path, d.Root, catalogDirectorySummary{audio, formats, auxiliary, tracks, otherDirs})
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
		codedSibling, codeErr := catalogCodedChildWorkTx(ctx, tx, sourceID, d.Root, catalogDirectoryCode(d.Root))
		if codeErr != nil {
			return codeErr
		}
		// A coded directory is one release. A nested folder with another code is
		// a separate work, but it does not turn the release into a grouping
		// folder: its own format and bonus directories still belong to it.
		if codedSibling != 0 && catalogDirectoryCode(d.Root) == "" {
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
		traversable, excludeErr := catalogPathTraversableTx(ctx, tx.Tx, sourceID, child)
		if excludeErr != nil {
			return false, excludeErr
		}
		if !traversable {
			cursor = name
			continue
		}
		// A name that arrived as U+FFFD or invalid UTF-8 cannot be requested:
		// the source answers "object not found" for it. Record the name and
		// skip the child; a guessed sibling is not a repair.
		added, addErr := s.expandCatalogChildTx(ctx, tx.Tx, sourceID, job.RunID, p, partition, root, name)
		if addErr != nil {
			return false, addErr
		}
		if added {
			if addErr = addCatalogDirectoryCountersTx(ctx, tx.Tx, sourceID); addErr != nil {
				return false, addErr
			}
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
	if status == "complete" {
		if err = moveCatalogDirectoryCountersTx(ctx, tx.Tx, sourceID, "listing_complete", "", "complete", ""); err != nil {
			return false, err
		}
	}
	change, err := refreshCatalogAudioRootStateTx(ctx, tx.Tx, sourceID, job.RunID, p)
	if err != nil {
		return false, err
	}
	if status == "complete" {
		if err = addCompletedDirectoryPublishPendingTx(ctx, tx.Tx, sourceID, job.RunID, p, change); err != nil {
			return false, err
		}
		// Once the listing is complete the card's thresholds are stable
		// enough to decide. Clearing a refused card here keeps the queue
		// from accumulating rows the publisher can never select. An empty
		// root means the directory has no card yet; it is not a grouping
		// key and must not aggregate unrelated directories.
		if root != "" {
			observed, gateErr := catalogRootFullyObservedTx(ctx, tx.Tx, sourceID, job.RunID, root)
			if gateErr != nil {
				return false, gateErr
			}
			if observed {
				if _, err = s.blockUnadmittedCatalogRootTx(ctx, tx.Tx, sourceID, job.RunID, root); err != nil {
					return false, err
				}
			}
		}
		if err = resetCatalogPublishCursorTx(ctx, tx.Tx, sourceID, p, change.Inserted); err != nil {
			return false, err
		}
	} else if change.Deleted {
		if err = addCatalogPublishPendingTx(ctx, tx.Tx, sourceID, -change.Pending); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// catalogAudioRootChange describes how one directory update changed the
// publishable root set. Pending is the number of rows whose eligibility
// changed: they entered the set on an insert and left it on a delete.
type catalogAudioRootChange struct {
	Root     string
	Present  bool
	Inserted bool
	Deleted  bool
	Pending  int
}

// refreshCatalogAudioRootTx keeps the publisher's join set exactly equal to
// "a complete directory with audio exists for this root". It is a thin wrapper
// for callers that do not need the row delta.
func refreshCatalogAudioRootTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string) error {
	_, err := refreshCatalogAudioRootStateTx(ctx, tx, sourceID, generation, directoryPath)
	return err
}

// refreshCatalogAudioRootStateTx inserts the root directly when the directory
// is complete and holds audio. Only an insert pays for counting the rows that
// become publishable; an already present root costs one primary-key probe.
// A directory that stopped being complete removes the root only when no other
// complete directory of the generation still has audio for it.
func refreshCatalogAudioRootStateTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string) (catalogAudioRootChange, error) {
	var root, status string
	err := tx.QueryRowContext(ctx, `SELECT catalog_root,status FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, directoryPath).Scan(&root, &status)
	if errors.Is(err, sql.ErrNoRows) || root == "" {
		return catalogAudioRootChange{}, nil
	}
	if err != nil {
		return catalogAudioRootChange{}, err
	}
	change := catalogAudioRootChange{Root: root}
	if status == "complete" {
		var audio int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_observation b
			JOIN source_catalog_directory a ON a.file_source_id=b.file_source_id AND a.path=b.directory_path AND a.revision=b.revision
			WHERE a.file_source_id=? AND a.generation=? AND a.path=? AND b.kind='audio')`,
			sourceID, generation, directoryPath).Scan(&audio); err != nil {
			return change, err
		}
		if audio != 0 {
			result, execErr := tx.ExecContext(ctx, `INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?,?,?) ON CONFLICT DO NOTHING`, sourceID, generation, root)
			if execErr != nil {
				return change, execErr
			}
			inserted, execErr := result.RowsAffected()
			if execErr != nil {
				return change, execErr
			}
			change.Present = true
			change.Inserted = inserted > 0
			if change.Inserted {
				// The root was absent, so every pending row below it becomes
				// publishable now, not only the rows of this directory.
				if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_directory d
					JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
					WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=? AND d.status='complete' AND o.publish_pending=1`,
					sourceID, generation, root).Scan(&change.Pending); err != nil {
					return change, err
				}
			}
			return change, nil
		}
		// A complete directory without audio neither adds nor removes a root:
		// another directory may already own it.
		var present int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_audio_root WHERE file_source_id=? AND generation=? AND catalog_root=?)`,
			sourceID, generation, root).Scan(&present); err != nil {
			return change, err
		}
		change.Present = present != 0
		return change, nil
	}
	// The directory is no longer complete (cleared, failed, restricted or
	// unconfirmed empty). Its own audio no longer proves the root.
	var present int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_audio_root WHERE file_source_id=? AND generation=? AND catalog_root=?)`,
		sourceID, generation, root).Scan(&present); err != nil {
		return change, err
	}
	if present == 0 {
		return change, nil
	}
	var used int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_directory a
		JOIN source_catalog_observation b ON b.file_source_id=a.file_source_id AND b.directory_path=a.path AND b.revision=a.revision
		WHERE a.file_source_id=? AND a.generation=? AND a.catalog_root=? AND a.status='complete' AND b.kind='audio')`,
		sourceID, generation, root).Scan(&used); err != nil {
		return change, err
	}
	if used != 0 {
		change.Present = true
		return change, nil
	}
	// Count the rows that stop being eligible before deleting the root; the
	// predicate needs the root row to exist.
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_directory d
		JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
		WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=? AND d.status='complete' AND o.publish_pending=1`,
		sourceID, generation, root).Scan(&change.Pending); err != nil {
		return change, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_audio_root WHERE file_source_id=? AND generation=? AND catalog_root=?`, sourceID, generation, root); err != nil {
		return change, err
	}
	change.Deleted = true
	return change, nil
}

// addCompletedDirectoryPublishPendingTx records the rows that became
// publishable when a directory reached status='complete'. An inserted root
// already counted every row below it, so only its total is added.
func addCompletedDirectoryPublishPendingTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string, change catalogAudioRootChange) error {
	if change.Root == "" {
		return nil
	}
	if change.Inserted {
		return addCatalogPublishPendingTx(ctx, tx, sourceID, change.Pending)
	}
	if !change.Present {
		return nil
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path AND d.revision=o.revision
		WHERE d.file_source_id=? AND d.generation=? AND d.path=? AND o.publish_pending=1`,
		sourceID, generation, directoryPath).Scan(&pending); err != nil {
		return err
	}
	return addCatalogPublishPendingTx(ctx, tx, sourceID, pending)
}

// resetCatalogPublishCursorTx rewinds the keyset cursor when rows behind its
// current position can still become publishable in this generation: a
// directory completed at or before the cursor, or a newly publishable root.
// Rewinding costs a scan over the pending index only; published rows keep
// publish_pending=0 and are not selected again.
func resetCatalogPublishCursorTx(ctx context.Context, tx *sql.Tx, sourceID int64, before string, force bool) error {
	if force {
		_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cursor_path='',publish_cursor_name='' WHERE file_source_id=? AND (publish_cursor_path<>'' OR publish_cursor_name<>'')`, sourceID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cursor_path='',publish_cursor_name='' WHERE file_source_id=? AND publish_cursor_path<>'' AND ?<=publish_cursor_path`, sourceID, before)
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
var catalogPublishQuery = `SELECT d.path,d.catalog_root,o.name,o.kind,o.size_bytes,o.content_hash,o.modified_at,
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
WHERE d.file_source_id=?1 AND d.generation=?2 AND d.status='complete' AND d.catalog_root<>''
	AND d.work_split<>'duplicate'
	-- Keyset pagination. The comparison uses the same BINARY collation pair
	-- as ORDER BY d.path,o.name, so a batch never skips a name inside a
	-- directory or repeats one across a restart. An empty cursor sorts before
	-- every path, which always starts with '/'.
	AND (d.path>(SELECT publish_cursor_path FROM source_catalog_scan WHERE file_source_id=?1)
		OR (d.path=(SELECT publish_cursor_path FROM source_catalog_scan WHERE file_source_id=?1)
			AND o.name>(SELECT publish_cursor_name FROM source_catalog_scan WHERE file_source_id=?1)))
	AND o.publish_pending=1
	AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
		WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)
	AND NOT ` + catalogPathExcludedSQL("d.file_source_id", catalogObservationPathSQL) + `
ORDER BY d.path,o.name LIMIT ?3`

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
	// Refuse cards that can never pass admission before selecting a batch.
	// Without this prepass their rows stay pending forever and every later
	// batch selects the same blocked card.
	if _, err = s.blockUnadmittedCatalogRootsTx(ctx, tx.Tx, sourceID, job.RunID); err != nil {
		return 0, err
	}
	// The cursor is durable on the scan row and the query reads it directly,
	// so a restarted worker resumes at the exact (directory,name) pair the
	// previous batch consumed.
	var remaining int
	if err = tx.QueryRowContext(ctx, `SELECT publish_pending_remaining FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&remaining); err != nil {
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
		if p.split == catalogWorkSplitPrefix || p.split == catalogWorkSplitFile || p.split == catalogWorkSplitEpisode {
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
	// indexed counts entries last seen in this generation. A root enters the
	// count once, on its first row of this batch; the lookup is one primary-key
	// probe per distinct root rather than a count over the whole entry table.
	seenRoots := map[string]int64{}
	indexedDelta := 0
	// Per-batch admission cache. The prepass already refuses fully observed
	// cards; this guard covers a row that reached the batch by another path.
	blockedRoots := map[string]bool{}
	resolvedSourceRoots := map[string]string{}
	for _, p := range items {
		if _, checked := blockedRoots[p.root]; !checked {
			blocked, adm, checkErr := s.catalogRootBlockedTx(ctx, tx.Tx, sourceID, job.RunID, p.root)
			if checkErr != nil {
				return 0, checkErr
			}
			blockedRoots[p.root] = blocked
			if blocked {
				slog.Info("catalog publish refused an unqualified card",
					"source_id", sourceID, "generation", job.RunID, "root", p.root,
					"files", adm.Files, "audio", adm.Audio, "non_audio", adm.NonAudio,
					"reason", adm.Reason)
			}
		}
		root, sourceRoot, title := p.root, "", path.Base(p.root)
		var episodeEvidence catalogSeriesEvidence
		hasEpisode := false
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
				episodeEvidence, hasEpisode = target.Episode, target.HasEpisode
			}
		}
		if sourceRoot == "" && p.root != "/" {
			if cached, ok := resolvedSourceRoots[p.root]; ok {
				sourceRoot = cached
			} else {
				var exists int
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_directory
					WHERE file_source_id=? AND generation=? AND path=?)`, sourceID, job.RunID, p.root).Scan(&exists); err != nil {
					return 0, err
				}
				if exists == 0 {
					// A virtual episode root has no directory of its own. Its
					// files live under the observed parent, which is also the
					// refresh boundary for the card.
					sourceRoot = path.Dir(p.root)
				}
				resolvedSourceRoots[p.root] = sourceRoot
			}
		}
		if !hasEpisode && sourceRoot != "" {
			if evidence, ok := catalogEpisodeEvidence(root); ok {
				episodeEvidence, hasEpisode = evidence, true
			}
		}
		if blockedRoots[p.root] {
			// Consume the row without creating an identity. Publication is
			// refused; the file rows never enter the card.
			if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_observation
				SET published_generation=?,publish_pending=0
				WHERE file_source_id=? AND directory_path=? AND name=?`, job.RunID, sourceID, p.dir, p.name); err != nil {
				return 0, err
			}
			continue
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
		if _, seen := seenRoots[root]; !seen {
			var priorGeneration int64
			if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT last_seen_generation FROM source_catalog_entry WHERE file_source_id=? AND root_path=?),0)`, sourceID, root).Scan(&priorGeneration); err != nil {
				return 0, err
			}
			seenRoots[root] = priorGeneration
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
		if seenRoots[root] != job.RunID {
			indexedDelta++
			seenRoots[root] = job.RunID
		}
		if root != p.root && firstSplit[p.root] == 0 {
			firstSplit[p.root] = id
		}
		if sourceRoot == "" {
			// A split card is already one work. Its title or file name is not
			// episode evidence, so it must not create or join a series.
			if err = attachCatalogSeriesForRootTx(ctx, tx.Tx, id, sourceID, root); err != nil {
				return 0, err
			}
		} else if hasEpisode {
			if err = attachCatalogSeriesEvidenceTx(ctx, tx.Tx, id, sourceID, episodeEvidence); err != nil {
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
		fullPath := path.Join(p.dir, p.name)
		rel := strings.TrimPrefix(fullPath, strings.TrimRight(baseRoot, "/")+"/")
		// A format copy beside its release publishes into the release card but
		// its files stay in the copy's own directory. One card-wide base cannot
		// address both, so the row keeps the directory it really lives in and a
		// path relative to that directory's parent. Without this the path stayed
		// absolute and playback joined it to the card root, returning 404.
		fileRoot := ""
		if strings.HasPrefix(rel, "/") {
			fileRoot = path.Dir(baseRoot)
			rel = strings.TrimPrefix(fullPath, strings.TrimRight(fileRoot, "/")+"/")
			if strings.HasPrefix(rel, "/") {
				fileRoot, rel = p.dir, p.name
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,source_root,title,kind,size_bytes,content_hash,modified_at,observed_generation) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET source_root=excluded.source_root,title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,modified_at=excluded.modified_at,observed_generation=excluded.observed_generation`, id, rel, fileRoot, p.name, p.kind, p.size, p.hash, p.modified, job.RunID)
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
	// Every selected row was publishable and is now consumed, so the stored
	// remainder drops by the batch size. The final-batch decision reads this
	// projection instead of scanning every pending observation before commit.
	if len(items) > 0 {
		if err = addCatalogPublishPendingTx(ctx, tx.Tx, sourceID, -len(items)); err != nil {
			return 0, err
		}
		last := items[len(items)-1]
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cursor_path=?,publish_cursor_name=? WHERE file_source_id=?`, last.dir, last.name, sourceID); err != nil {
			return 0, err
		}
		remaining -= len(items)
	}
	if remaining < 0 {
		remaining = 0
	}
	if err = addCatalogObservationCountersTx(ctx, tx.Tx, sourceID, 0, indexedDelta); err != nil {
		return 0, err
	}
	publishIdle := remaining == 0
	// Retiring a card clears its file_source_id, which removes it from the
	// indexed projection. Recompute that one column when a pass retired
	// anything; all other increments stay incremental.
	retireRan := false
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
		retireRan = true
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
		retireRan = true
		if err = retireStaleSplitCardsTx(ctx, tx.Tx, sourceID, job.RunID, root, keep); err != nil {
			return 0, err
		}
	}
	// A root that is no longer split has a single card at parentRoot. Retire
	// split cards from older generations that pointed at the same source root.
	if !publishIdle {
		// Format and split identities stay untouched until every row of this
		// generation is published.
		if retireRan {
			if err = reconcileCatalogIndexedTx(ctx, tx.Tx, sourceID, job.RunID); err != nil {
				return 0, err
			}
		}
		if err = markCatalogSidecarCandidatesTx(ctx, tx.Tx, publishedIDs, job.RunID); err != nil {
			return 0, err
		}
		// The prefill cursor moves back in the same transaction as the cards it
		// refers to. A committed card can therefore never be invisible to the
		// prefill pass, and a failed update rolls the publication back instead
		// of leaving a silent gap.
		if err = noteCatalogEntriesPublishedTx(ctx, tx.Tx, publishedIDs); err != nil {
			return 0, err
		}
		if err = tx.Commit(); err != nil {
			return 0, err
		}
		published = len(items)
		s.clearTranslationPrefillFailures(publishedIDs)
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
			retireRan = true
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
	// Stale card retirement runs as its own workflow step. It walks every card
	// of the source and decides per card with a root-completeness query, so
	// keeping it here turned the final publish batch into a multi-second write
	// transaction that blocked the writer and every reader behind it.
	// Queue credit files in the same transaction that publishes the card, so a
	// committed card is never forgotten between publication and the reader. The
	// reads run after commit and never hold this write lock.
	if retireRan {
		if err = reconcileCatalogIndexedTx(ctx, tx.Tx, sourceID, job.RunID); err != nil {
			return 0, err
		}
	}
	if err = markCatalogSidecarCandidatesTx(ctx, tx.Tx, publishedIDs, job.RunID); err != nil {
		return 0, err
	}
	if err = noteCatalogEntriesPublishedTx(ctx, tx.Tx, publishedIDs); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	published = len(items)
	s.clearTranslationPrefillFailures(publishedIDs)
	return published, nil
}

// sourceCatalogProgress reads the counters maintained in the same
// transactions as the rows they describe. ?exact=1 stays available through
// sourceCatalogProgressExact for reconciliation after a manual repair.
func (s *Server) sourceCatalogProgress(ctx context.Context, sourceID int64, page int) (catalogIndexProgress, error) {
	p := catalogIndexProgress{Partitions: []catalogPartitionProgress{}, Details: []catalogDirectoryProgress{}}
	var options string
	err := s.db.QueryRowContext(ctx, `SELECT scan.workflow_run_id,run.status,scan.pause_reason,scan.options_json,scan.requests,
		scan.directories,scan.completed,scan.pending,scan.failed,scan.restricted,scan.empty,scan.observed,scan.indexed,
		scan.publish_pending_remaining,scan.started_at,scan.updated_at
		FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id WHERE scan.file_source_id=?`, sourceID).Scan(
		&p.RunID, &p.Status, &p.PauseReason, &options, &p.Requests,
		&p.Directories, &p.Completed, &p.Pending, &p.Failed, &p.Restricted, &p.Empty, &p.Observed, &p.Indexed, &p.PublishPending,
		&p.StartedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if p.EncodingUnresolved, err = s.catalogEncodingUnresolvedCount(ctx, sourceID); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(options), &p.Options); err != nil {
		return p, err
	}
	if p.Status == "succeeded" {
		p.Status = "complete"
	}
	if p.PauseReason != "" && p.Status == "partial" {
		p.Status = "paused"
	}
	p.DetailsTotal = p.Directories - p.Completed
	if err = s.db.QueryRowContext(ctx, `SELECT count(DISTINCT partition_path) FROM source_catalog_directory WHERE file_source_id=? AND generation=?`, sourceID, p.RunID).Scan(&p.PartitionTotal); err != nil {
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
	rows, err = s.db.QueryContext(ctx, `SELECT path,status,next_page,error_code FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status<>'complete' AND NOT(status='empty' AND error_code='not_found') ORDER BY path LIMIT 50 OFFSET ?`, sourceID, p.RunID, (page-1)*50)
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

// sourceCatalogProgressExact is the original aggregate. It is the
// reconciliation path behind ?exact=1 and never writes.
func (s *Server) sourceCatalogProgressExact(ctx context.Context, sourceID int64, page int) (catalogIndexProgress, error) {
	p := catalogIndexProgress{Partitions: []catalogPartitionProgress{}, Details: []catalogDirectoryProgress{}}
	var options string
	err := s.db.QueryRowContext(ctx, `SELECT scan.workflow_run_id,run.status,scan.pause_reason,scan.options_json,scan.requests,scan.started_at,scan.updated_at FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id WHERE scan.file_source_id=?`, sourceID).Scan(&p.RunID, &p.Status, &p.PauseReason, &options, &p.Requests, &p.StartedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if p.EncodingUnresolved, err = s.catalogEncodingUnresolvedCount(ctx, sourceID); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(options), &p.Options); err != nil {
		return p, err
	}
	if p.Status == "succeeded" {
		p.Status = "complete"
	}
	var stagingDirectories, stagingObservations int
	if err = s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM source_catalog_directory WHERE file_source_id=?),(SELECT count(*) FROM source_catalog_observation WHERE file_source_id=?)`, sourceID, sourceID).Scan(&stagingDirectories, &stagingObservations); err != nil {
		return p, err
	}
	p.StagingDirectories, p.StagingObservations = &stagingDirectories, &stagingObservations
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
	rows, err = s.db.QueryContext(ctx, `SELECT path,status,next_page,error_code FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status<>'complete' AND NOT(status='empty' AND error_code='not_found') ORDER BY path LIMIT 50 OFFSET ?`, sourceID, p.RunID, (page-1)*50)
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
