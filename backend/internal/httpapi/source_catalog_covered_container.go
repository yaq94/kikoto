package httpapi

import (
	"context"
	"database/sql"
	"strings"
)

// coveredContainerTargetTx reports the one current card that owns every audio
// path of a stale container card. Paths are compared after removing the deeper
// card's directory prefix, because the container stored them relative to
// itself. Two candidates or a missing path leave the card alone.
func coveredContainerTargetTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string, staleID int64) (int64, error) {
	parent := []string{}
	rows, err := tx.QueryContext(ctx, `SELECT relative_path FROM source_catalog_file
		WHERE catalog_entry_id=? AND kind='audio'`, staleID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var relativePath string
		if err = rows.Scan(&relativePath); err != nil {
			break
		}
		parent = append(parent, relativePath)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	if len(parent) == 0 {
		return 0, nil
	}

	rows, err = tx.QueryContext(ctx, `SELECT id,root_path FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL AND last_seen_generation=?
			AND substr(root_path,1,length(?)+1)=? || '/'`,
		sourceID, generation, root, root)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id   int64
		root string
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.root); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}

	matches := []int64{}
	for _, c := range candidates {
		prefix := strings.TrimPrefix(c.root, root+"/") + "/"
		owned := map[string]bool{}
		fileRows, err := tx.QueryContext(ctx, `SELECT relative_path FROM source_catalog_file
			WHERE catalog_entry_id=? AND kind='audio'`, c.id)
		if err != nil {
			return 0, err
		}
		for fileRows.Next() {
			var relativePath string
			if err = fileRows.Scan(&relativePath); err != nil {
				break
			}
			owned[relativePath] = true
		}
		if err == nil {
			err = fileRows.Err()
		}
		_ = fileRows.Close()
		if err != nil {
			return 0, err
		}
		covered := 0
		for _, relativePath := range parent {
			if strings.HasPrefix(relativePath, prefix) && owned[strings.TrimPrefix(relativePath, prefix)] {
				covered++
			}
		}
		if covered == len(parent) {
			matches = append(matches, c.id)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return 0, nil
}
