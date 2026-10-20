package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

func catalogFingerprintsTx(ctx context.Context, tx *sql.Tx, id, generation int64) (string, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT relative_path,size_bytes,content_hash FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation=? AND kind='audio' ORDER BY relative_path LIMIT 10001`, id, generation)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rows.Close() }()
	content, layout := sha256.New(), sha256.New()
	count := 0
	complete := true
	for rows.Next() {
		var path, hash string
		var size int64
		if err = rows.Scan(&path, &size, &hash); err != nil {
			return "", "", err
		}
		count++
		_, _ = fmt.Fprintf(layout, "%s\x00%d\n", path, size)
		_, _ = fmt.Fprintf(content, "%s\x00%d\x00%s\n", path, size, hash)
		if hash == "" {
			complete = false
		}
	}
	if err = rows.Err(); err != nil {
		return "", "", err
	}
	if count == 0 || count > 10000 {
		return "", "", nil
	}
	strong := ""
	if complete {
		strong = hex.EncodeToString(content.Sum(nil))
	}
	return strong, hex.EncodeToString(layout.Sum(nil)), nil
}

// catalogSeriesMembershipsCompatibleTx reports whether two cards can be one
// episode after a rename. A successor created by the same scan is attached to
// the series before reconciliation, so requiring an empty membership would
// reject every renamed episode. The series is the identity here: the renamed
// directory takes the next free position, so comparing positions would turn
// every rename into a conflict. Two different series stay a review conflict.
func catalogSeriesMembershipsCompatibleTx(ctx context.Context, tx *sql.Tx, oldID, targetID int64) (bool, error) {
	// Memberships may sit on an alias left by an older relocation, so both
	// sides are read through their alias closure. A card claimed by two
	// different series cannot be merged without an explicit review.
	oldSeries, err := catalogSeriesMembersOfEntryTx(ctx, tx, oldID)
	if err != nil {
		return false, err
	}
	if len(oldSeries) == 0 {
		return true, nil
	}
	if len(oldSeries) > 1 {
		return false, nil
	}
	targetSeries, err := catalogSeriesMembersOfEntryTx(ctx, tx, targetID)
	if err != nil {
		return false, err
	}
	if len(targetSeries) == 0 {
		return true, nil
	}
	if len(targetSeries) > 1 {
		return false, nil
	}
	return catalogSeriesRenameEvidenceTx(ctx, tx, oldSeries[0], targetSeries[0])
}

// catalogSeriesRenameMatchesEpisodeTx reports a single-file episode inside a
// series directory that moved as a group: both cards belong to different
// series that describe the same episode set, and the directory name is
// unchanged. That group correspondence is the evidence a lone file lacks.
func catalogSeriesRenameMatchesEpisodeTx(ctx context.Context, tx *sql.Tx, old, target catalogRelocationEntry) (bool, error) {
	oldSeries, err := catalogSeriesMembersOfEntryTx(ctx, tx, old.id)
	if err != nil || len(oldSeries) != 1 {
		return false, err
	}
	targetSeries, err := catalogSeriesMembersOfEntryTx(ctx, tx, target.id)
	if err != nil || len(targetSeries) != 1 || targetSeries[0] == oldSeries[0] {
		return false, err
	}
	renamed, err := catalogSeriesRenameEvidenceTx(ctx, tx, oldSeries[0], targetSeries[0])
	if err != nil || !renamed {
		return false, err
	}
	return strings.EqualFold(path.Base(catalogEffectiveRoot(old.root, old.sourceRoot)), path.Base(catalogEffectiveRoot(target.root, target.sourceRoot))), nil
}

func catalogEffectiveRoot(root, sourceRoot string) string {
	if sourceRoot != "" {
		return sourceRoot
	}
	return root
}

type catalogRelocationEntry struct {
	id                                            int64
	root, sourceRoot, title, code, strong, layout string
	workID                                        sql.NullInt64
	discovered, lastSeen                          int64
}

func reconcileCatalogEntryTx(ctx context.Context, tx *sql.Tx, sourceID, generation, id int64) error {
	var old catalogRelocationEntry
	err := tx.QueryRowContext(ctx, `SELECT id,root_path,source_root,title,primary_code,content_fingerprint,layout_fingerprint,work_id,discovered_generation,last_seen_generation FROM source_catalog_entry WHERE id=?`, id).Scan(&old.id, &old.root, &old.sourceRoot, &old.title, &old.code, &old.strong, &old.layout, &old.workID, &old.discovered, &old.lastSeen)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,root_path,source_root,title,primary_code,content_fingerprint,layout_fingerprint,work_id,discovered_generation,last_seen_generation FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL AND last_seen_generation=? AND id<>?
		AND ((?<>'' AND content_fingerprint=?) OR (?<>'' AND layout_fingerprint=?) OR title=?) ORDER BY id LIMIT 9`, sourceID, generation, id, old.strong, old.strong, old.layout, old.layout, old.title)
	if err != nil {
		return err
	}
	var candidates []catalogRelocationEntry
	for rows.Next() {
		var item catalogRelocationEntry
		if err = rows.Scan(&item.id, &item.root, &item.sourceRoot, &item.title, &item.code, &item.strong, &item.layout, &item.workID, &item.discovered, &item.lastSeen); err != nil {
			break
		}
		candidates = append(candidates, item)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	// The lifecycle cursor visits cards in id order, so a newly published
	// successor is often reconciled before its own fingerprint pass stored the
	// manifest. Compute the missing side from this generation's files instead
	// of treating a real rename as insufficient evidence.
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.strong != "" && candidate.layout != "" {
			continue
		}
		strong, layout, fingerprintErr := catalogFingerprintsTx(ctx, tx, candidate.id, generation)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if candidate.strong == "" {
			candidate.strong = strong
		}
		if candidate.layout == "" {
			candidate.layout = layout
		}
	}
	strongCount := 0
	layoutCount := 0
	var layoutTarget catalogRelocationEntry
	var target catalogRelocationEntry
	for _, candidate := range candidates {
		if old.strong != "" && candidate.strong == old.strong {
			strongCount++
			target = candidate
		}
		// A provider hash on both sides is the stronger evidence: a differing
		// hash contradicts a matching manifest. When the renamed location has
		// no hash, the manifest remains the only comparable evidence.
		if old.layout != "" && candidate.layout == old.layout && (candidate.strong == "" || candidate.strong == old.strong) {
			layoutCount++
			layoutTarget = candidate
		}
	}
	reason := "insufficient_evidence"
	if strongCount > 0 {
		var oldCount int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL AND last_seen_generation<>? AND content_fingerprint=?`, sourceID, generation, old.strong).Scan(&oldCount); err != nil {
			return err
		}
		reason = "ambiguous_evidence"
		if strongCount == 1 && oldCount == 1 && len(candidates) < 9 {
			compatible, err := catalogSeriesMembershipsCompatibleTx(ctx, tx, old.id, target.id)
			if err != nil {
				return err
			}
			reason = "conflicting_identity"
			// The provisional card may have been published by an interrupted
			// generation, so it is enough that it is newer than the card being
			// replaced. Requiring this exact generation would lose a rename
			// whenever the resume reuses an earlier published row.
			if !target.workID.Valid && compatible && target.discovered >= old.lastSeen && old.code == target.code {
				return relocateCatalogEntryTx(ctx, tx, old, target, generation)
			}
		}
	} else if old.layout != "" && layoutCount > 0 {
		// OpenList often returns no provider hash. A renamed directory still
		// has one stable manifest: the same relative paths and sizes. Accept
		// it only for a multi-file release, unique in this scan, with no
		// older entry carrying the same manifest, so a copied release or a
		// one-track recording cannot be mistaken for a rename. Provider
		// hashes remain the stronger evidence above.
		var files int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation=? AND kind='audio'`, layoutTarget.id, generation).Scan(&files); err != nil {
			return err
		}
		var oldCount int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL AND last_seen_generation<>? AND layout_fingerprint=?`, sourceID, generation, old.layout).Scan(&oldCount); err != nil {
			return err
		}
		// A single same-sized file is not enough on its own to call this a
		// rename: a replacement recording looks identical. Inside a series
		// directory that moved as one group, the matching episode name is the
		// additional evidence that this is the same episode.
		singleFile := files < 2
		if singleFile {
			renamed, renameErr := catalogSeriesRenameMatchesEpisodeTx(ctx, tx, old, layoutTarget)
			if renameErr != nil {
				return renameErr
			}
			singleFile = !renamed
		}
		if layoutCount > 1 || oldCount > 1 {
			reason = "ambiguous_evidence"
		} else if singleFile {
			reason = "insufficient_evidence"
		} else if len(candidates) >= 9 {
			reason = "ambiguous_evidence"
		} else {
			compatible, err := catalogSeriesMembershipsCompatibleTx(ctx, tx, old.id, layoutTarget.id)
			if err != nil {
				return err
			}
			reason = "conflicting_identity"
			if !layoutTarget.workID.Valid && compatible && layoutTarget.discovered >= old.lastSeen && old.code == layoutTarget.code {
				return relocateCatalogEntryTx(ctx, tx, old, layoutTarget, generation)
			}
		}
	}
	for _, candidate := range candidates[:min(len(candidates), 8)] {
		if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_relocation_candidate(catalog_entry_id,candidate_entry_id,reason,generation) VALUES(?,?,?,?)`, old.id, candidate.id, reason, generation); err != nil {
			return err
		}
	}
	return nil
}

func relocateCatalogEntryTx(ctx context.Context, tx *sql.Tx, old, target catalogRelocationEntry, generation int64) error {
	// The established row keeps its id, work link, series membership and any
	// personal record. The freshly discovered location is the provisional one,
	// so it becomes the alias. Copying the new location onto the established
	// row also keeps every old /catalog/<id> link stable.
	if err := mergeCatalogSeriesMembershipTx(ctx, tx, target.id, old.id); err != nil {
		return err
	}
	// Release the unique (source, root) key before the survivor takes the new
	// path, and make the provisional row an alias in the same statement.
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=? WHERE id=?`, old.id, target.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET root_path=?,source_root=?,title=?,last_seen_generation=?,content_fingerprint=?,layout_fingerprint=? WHERE id=?`, target.root, target.sourceRoot, target.title, generation, target.strong, target.layout, old.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,modified_at,observed_generation)
		SELECT ?,relative_path,title,kind,size_bytes,content_hash,modified_at,observed_generation FROM source_catalog_file WHERE catalog_entry_id=?
		ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,modified_at=excluded.modified_at,observed_generation=excluded.observed_generation`, old.id, target.id); err != nil {
		return err
	}
	// The survivor keeps its own credit evidence. Only the alias's rows and the
	// survivor's own stale rows are removed.
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? OR (catalog_entry_id=? AND observed_generation<>?)`, target.id, old.id, generation); err != nil {
		return err
	}
	return setCatalogEntryStatusTx(ctx, tx, old.id, "available")
}
