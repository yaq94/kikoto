package httpapi

import (
	"context"
	"database/sql"
	"fmt"
)

// contentTranslationScopeSQL copies the stored scope of one translation row
// from the migration's view. The view is the single definition of "this row is
// still searchable", so every writer below recomputes from it instead of
// repeating the rule.
const contentTranslationScopeSQL = `(source_id,browsable)=(
	SELECT scope.source_id,scope.browsable
	FROM content_translation_scope scope
	WHERE scope.translation_id=content_translation.id)`

// refreshContentTranslationScopeTx recomputes the scope of the given cache
// keys. It runs in the same transaction as the row write, so a committed
// translation is searchable exactly when its entity is.
func refreshContentTranslationScopeTx(ctx context.Context, tx *sql.Tx, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE content_translation SET `+contentTranslationScopeSQL+`
		WHERE cache_key IN (`+sqlPlaceholders(len(keys))+`)`, stringArgs(keys)...)
	if err != nil {
		return fmt.Errorf("refresh translation scope: %w", err)
	}
	return nil
}

// refreshContentTranslationBrowsableTx recomputes the scope of every
// translation row of the given cards and of the series that contain them. A
// publish batch refreshes admission counts after writing files, and both
// change what a short search may return; the two statements keep every answer
// in the same transaction.
func refreshContentTranslationBrowsableTx(ctx context.Context, tx *sql.Tx, entryIDs []int64) error {
	if len(entryIDs) == 0 {
		return nil
	}
	// A series answers a search through its members, so a member's file set
	// decides the series' scope too. One statement covers both kinds, which
	// keeps a publish batch's statement count flat.
	placeholders := sqlPlaceholders(len(entryIDs))
	args := append(int64Args(entryIDs), int64Args(entryIDs)...)
	if _, err := tx.ExecContext(ctx, `UPDATE content_translation SET `+contentTranslationScopeSQL+`
		WHERE (entity_kind='entry' AND entity_id IN (`+placeholders+`))
		   OR (entity_kind='series' AND entity_id IN (
				SELECT series_id FROM catalog_series_member WHERE catalog_entry_id IN (`+placeholders+`)))`,
		args...); err != nil {
		return fmt.Errorf("refresh entry translation scope: %w", err)
	}
	return nil
}

// refreshSourceContentTranslationBrowsableTx is the repair form: it rebuilds
// every translation row of one source after an operator changes that source's
// enabled flag or after a migration backfill.
func refreshSourceContentTranslationBrowsableTx(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE content_translation SET `+contentTranslationScopeSQL+`
		WHERE source_id=?`, sourceID)
	if err != nil {
		return fmt.Errorf("refresh source translation scope: %w", err)
	}
	return nil
}
