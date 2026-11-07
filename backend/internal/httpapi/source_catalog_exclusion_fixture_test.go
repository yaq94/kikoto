package httpapi

import (
	"context"
	"database/sql"
)

// Existing rule fixtures supply their own transaction. Exercise the same
// bounded steps as the HTTP worker; production always commits each step with
// its durable cursor and never uses an all-subtree transaction.
func applyCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64, path string) error {
	return runCatalogExclusionFixture(ctx, tx, sourceID, path, true)
}

func revokeCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64, path string) error {
	return runCatalogExclusionFixture(ctx, tx, sourceID, path, false)
}

func runCatalogExclusionFixture(ctx context.Context, tx *sql.Tx, sourceID int64, path string, apply bool) error {
	input := catalogExclusionInput{SourceID: sourceID, Rule: catalogExclusion{Path: path}, Apply: apply}
	cursor := catalogExclusionCursor{Phase: "entries"}
	for cursor.Phase != "counters" {
		var err error
		switch cursor.Phase {
		case "entries":
			err = catalogExclusionEntryBatch(ctx, tx, input, &cursor)
		case "files":
			err = catalogExclusionFileBatch(ctx, tx, input, &cursor)
		case "observations":
			err = catalogExclusionObservationBatch(ctx, tx, input, &cursor)
		case "directories", "bridge":
			err = catalogExclusionDirectoryBatch(ctx, tx, input, &cursor)
		case "roots":
			err = catalogExclusionRootBatch(ctx, tx, input, &cursor)
		}
		if err != nil {
			return err
		}
	}
	return seedCatalogCountersTx(ctx, tx, sourceID)
}
