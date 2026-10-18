package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
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

type catalogRelocationEntry struct {
	id                                            int64
	root, sourceRoot, title, code, strong, layout string
	workID                                        sql.NullInt64
	discovered                                    int64
}

func reconcileCatalogEntryTx(ctx context.Context, tx *sql.Tx, sourceID, generation, id int64) error {
	var old catalogRelocationEntry
	err := tx.QueryRowContext(ctx, `SELECT id,root_path,source_root,title,primary_code,content_fingerprint,layout_fingerprint,work_id,discovered_generation FROM source_catalog_entry WHERE id=?`, id).Scan(&old.id, &old.root, &old.sourceRoot, &old.title, &old.code, &old.strong, &old.layout, &old.workID, &old.discovered)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,root_path,source_root,title,primary_code,content_fingerprint,layout_fingerprint,work_id,discovered_generation FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL AND last_seen_generation=? AND id<>?
		AND ((?<>'' AND content_fingerprint=?) OR (?<>'' AND layout_fingerprint=?) OR title=?) ORDER BY id LIMIT 9`, sourceID, generation, id, old.strong, old.strong, old.layout, old.layout, old.title)
	if err != nil {
		return err
	}
	var candidates []catalogRelocationEntry
	for rows.Next() {
		var item catalogRelocationEntry
		if err = rows.Scan(&item.id, &item.root, &item.sourceRoot, &item.title, &item.code, &item.strong, &item.layout, &item.workID, &item.discovered); err != nil {
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
	strongCount := 0
	var target catalogRelocationEntry
	for _, candidate := range candidates {
		if old.strong != "" && candidate.strong == old.strong {
			strongCount++
			target = candidate
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
			var membership int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_series_member WHERE catalog_entry_id=?`, target.id).Scan(&membership); err != nil {
				return err
			}
			reason = "conflicting_identity"
			if !target.workID.Valid && membership == 0 && target.discovered == generation && old.code == target.code {
				return relocateCatalogEntryTx(ctx, tx, old, target, generation)
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
	// The provisional location becomes an alias; old links and the established
	// work/media ids survive. No evaluation object is merged or re-registered.
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=? WHERE id=?`, old.id, target.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET root_path=?,source_root=?,title=?,last_seen_generation=?,content_fingerprint=?,layout_fingerprint=? WHERE id=?`, target.root, target.sourceRoot, target.title, generation, target.strong, target.layout, old.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation)
		SELECT ?,relative_path,title,kind,size_bytes,content_hash,observed_generation FROM source_catalog_file WHERE catalog_entry_id=?
		ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,observed_generation=excluded.observed_generation`, old.id, target.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id IN (?,?) AND (catalog_entry_id=? OR observed_generation<>?)`, old.id, target.id, target.id, generation); err != nil {
		return err
	}
	return setCatalogEntryStatusTx(ctx, tx, old.id, "available")
}
