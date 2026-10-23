package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/yexca/kikoto/backend/internal/catalogadmission"
)

// catalogAdmissionRootsPerPass bounds how many distinct cards one publish
// transaction evaluates. A card with a huge subtree costs at most the three
// bounded counts below, and the pass keeps the writer for a short time.
const catalogAdmissionRootsPerPass = 24

type catalogRootAdmission struct {
	Files, Audio, NonAudio int
	FullyObserved          bool
	Reason                 string
}

// catalogRootAdmissionTx counts the observations that belong to one card root.
// The counts stop at the first value that can decide a threshold, so a card
// with 87k rows costs the same as one with 5001.
func catalogRootAdmissionTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (catalogRootAdmission, error) {
	var adm catalogRootAdmission
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM (
			SELECT 1 FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
				AND d.work_split<>'duplicate' LIMIT 5001)),
		(SELECT count(*) FROM (
			SELECT 1 FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
				AND d.work_split<>'duplicate' AND o.kind='audio' LIMIT 5)),
		(SELECT count(*) FROM (
			SELECT 1 FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
				AND d.work_split<>'duplicate' AND o.kind<>'audio' LIMIT 2001)),
		NOT EXISTS(SELECT 1 FROM source_catalog_directory d
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
				AND d.status<>'complete' AND NOT (d.status='empty' AND d.error_code='not_found'))`,
		sourceID, generation, root).
		Scan(&adm.Files, &adm.Audio, &adm.NonAudio, &adm.FullyObserved)
	if err != nil {
		return catalogRootAdmission{}, err
	}
	adm.Reason = catalogadmission.Decision(adm.Files, adm.Audio, adm.NonAudio)
	return adm, nil
}

// catalogAdmissionBlocks decides whether a count may be used for a refusal.
// A too-large card is monotonic: more observed rows can only keep it over the
// limit. A missing-audio or audio-light card is not, so a partially observed
// subtree waits for its complete listing before it is refused.
func catalogAdmissionBlocks(adm catalogRootAdmission) bool {
	if adm.Reason == "" {
		return false
	}
	if adm.Reason == catalogadmission.ReasonTooManyFiles {
		return true
	}
	return adm.FullyObserved
}

// blockUnadmittedCatalogRootsTx is the publish-plan layer. It evaluates cards
// that still have queued observations and clears that queue when the card can
// never pass admission. Doing it before the publish query keeps the cursor
// moving: leaving those rows pending would make every later batch select the
// same blocked card.
func (s *Server) blockUnadmittedCatalogRootsTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64) (int, error) {
	// Roots without an audio root are invisible to the publish query, so the
	// prepass must clear them. Putting them first prevents an alphabetically
	// early admissible card from occupying every pass and starving the queue.
	// A partially observed root is considered only when it already exceeds the
	// file limit; otherwise a later page may still add audio.
	rows, err := tx.QueryContext(ctx, `SELECT d.catalog_root
		FROM source_catalog_directory d
		JOIN source_catalog_observation o
			ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
		WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root<>'' AND o.publish_pending=1
			AND (
				NOT EXISTS(SELECT 1 FROM source_catalog_directory d2
					WHERE d2.file_source_id=d.file_source_id AND d2.generation=d.generation
						AND d2.catalog_root=d.catalog_root
						AND d2.status<>'complete' AND NOT (d2.status='empty' AND d2.error_code='not_found'))
				OR (SELECT count(*) FROM (
					SELECT 1 FROM source_catalog_directory d3
					JOIN source_catalog_observation o3
						ON o3.file_source_id=d3.file_source_id AND o3.directory_path=d3.path AND o3.revision=d3.revision
					WHERE d3.file_source_id=d.file_source_id AND d3.generation=d.generation
						AND d3.catalog_root=d.catalog_root LIMIT 5001)) > 5000
			)
		GROUP BY d.catalog_root
		ORDER BY CASE WHEN EXISTS(
			SELECT 1 FROM source_catalog_audio_root r
			WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root
		) THEN 1 ELSE 0 END ASC, d.catalog_root
		LIMIT ?`, sourceID, generation, catalogAdmissionRootsPerPass)
	if err != nil {
		return 0, err
	}
	roots := []string{}
	for rows.Next() {
		var root string
		if err = rows.Scan(&root); err != nil {
			break
		}
		roots = append(roots, root)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	blocked := 0
	for _, root := range roots {
		adm, err := catalogRootAdmissionTx(ctx, tx, sourceID, generation, root)
		if err != nil {
			return blocked, err
		}
		if !catalogAdmissionBlocks(adm) {
			continue
		}
		cleared, err := clearCatalogRootPendingTx(ctx, tx, sourceID, generation, root)
		if err != nil {
			return blocked, err
		}
		blocked++
		slog.Info("catalog admission blocked a card",
			"source_id", sourceID, "generation", generation, "root", root,
			"files", adm.Files, "audio", adm.Audio, "non_audio", adm.NonAudio,
			"fully_observed", adm.FullyObserved, "reason", adm.Reason, "cleared", cleared)
	}
	return blocked, nil
}

// catalogRootBlockedTx is the publish-transaction layer. It re-evaluates one
// card before its first file row is written, so a row that bypassed the plan
// pass (a manual observation or a resumed older batch) still cannot create a
// card that admission would refuse.
func (s *Server) catalogRootBlockedTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (bool, catalogRootAdmission, error) {
	adm, err := catalogRootAdmissionTx(ctx, tx, sourceID, generation, root)
	if err != nil {
		return false, adm, err
	}
	return catalogAdmissionBlocks(adm), adm, nil
}

// catalogRootFullyObservedTx is the cheap gate in front of the enqueue-time
// counts. A root with a pending, listing or failed directory may still gain
// audio, so counting it on every directory completion would both cost more and
// risk an early refusal.
func catalogRootFullyObservedTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (bool, error) {
	var observed int
	err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(
		SELECT 1 FROM source_catalog_directory d
		WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=?
			AND d.status<>'complete' AND NOT (d.status='empty' AND d.error_code='not_found'))`,
		sourceID, generation, root).Scan(&observed)
	return observed != 0, err
}

// blockUnadmittedCatalogRootTx is the enqueue-time check. It runs when a
// directory completes, so a card that can never publish leaves the queue in
// the same transaction instead of waiting for the next publish pass.
func (s *Server) blockUnadmittedCatalogRootTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (bool, error) {
	adm, err := catalogRootAdmissionTx(ctx, tx, sourceID, generation, root)
	if err != nil {
		return false, err
	}
	if !catalogAdmissionBlocks(adm) {
		return false, nil
	}
	cleared, err := clearCatalogRootPendingTx(ctx, tx, sourceID, generation, root)
	if err != nil {
		return false, err
	}
	slog.Info("catalog admission blocked a card at directory completion",
		"source_id", sourceID, "generation", generation, "root", root,
		"files", adm.Files, "audio", adm.Audio, "non_audio", adm.NonAudio,
		"fully_observed", adm.FullyObserved, "reason", adm.Reason, "cleared", cleared)
	return true, nil
}

// catalogEntryAdmittedSQL filters a published card by its file rows. Each
// count stops at the first row that can decide its threshold, and the
// (catalog_entry_id,title) index serves the correlated probes.
func catalogEntryAdmittedSQL(alias string) string {
	return fmt.Sprintf(`(
		EXISTS(SELECT 1 FROM source_catalog_file aqf WHERE aqf.catalog_entry_id=%[1]s.id AND aqf.kind='audio')
		AND (SELECT count(*) FROM (SELECT 1 FROM source_catalog_file tqf WHERE tqf.catalog_entry_id=%[1]s.id LIMIT 5001)) <= 5000
		AND NOT (
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file nqf WHERE nqf.catalog_entry_id=%[1]s.id AND nqf.kind<>'audio' LIMIT 2001)) > 2000
			AND (SELECT count(*) FROM (SELECT 1 FROM source_catalog_file sqf WHERE sqf.catalog_entry_id=%[1]s.id AND sqf.kind='audio' LIMIT 5)) < 5
		)
	)`, alias)
}

// catalogEntryAdmission is the structured answer for one card id. The counts
// stop at the first row that can decide their threshold, matching the SQL
// filter and keeping a diagnostic request bounded.
type catalogEntryAdmission struct {
	Files, Audio, NonAudio int
	Reason                 string
}

func (a catalogEntryAdmission) Admitted() bool { return a.Reason == "" }

// catalogPublishedEntryAdmission answers the admission question for one card
// id and returns the reason, so the API can describe a refusal instead of
// reporting an unexplained 404.
func catalogPublishedEntryAdmission(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64) (catalogEntryAdmission, error) {
	var adm catalogEntryAdmission
	err := q.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=? LIMIT 5001)),
		(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=? AND f.kind='audio' LIMIT 5)),
		(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=? AND f.kind<>'audio' LIMIT 2001))`,
		id, id, id).Scan(&adm.Files, &adm.Audio, &adm.NonAudio)
	if err != nil {
		return catalogEntryAdmission{}, err
	}
	adm.Reason = catalogadmission.Decision(adm.Files, adm.Audio, adm.NonAudio)
	return adm, nil
}

func catalogPublishedEntryAdmitted(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64) (bool, error) {
	adm, err := catalogPublishedEntryAdmission(ctx, q, id)
	if err != nil {
		return false, err
	}
	return adm.Admitted(), nil
}

// clearCatalogRootPendingTx removes one blocked card from the publish queue.
// The projection is adjusted by the exact number of rows cleared so a refused
// card cannot leave the run waiting for work the publisher will never do.
func clearCatalogRootPendingTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (int64, error) {
	// publish_pending_remaining only counts rows the publisher can select:
	// complete directories under a root that already has an audio root. A
	// no-audio root was never added to that projection, so subtracting its
	// cleared rows would understate the publishable backlog of other cards.
	var counted int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_directory d
		JOIN source_catalog_observation o
			ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
		WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=?
			AND d.status='complete' AND o.publish_pending=1
			AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
				WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)`,
		sourceID, generation, root).Scan(&counted); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0
		WHERE file_source_id=? AND publish_pending=1 AND EXISTS(
			SELECT 1 FROM source_catalog_directory d
			WHERE d.file_source_id=source_catalog_observation.file_source_id
				AND d.path=source_catalog_observation.directory_path
				AND d.generation=? AND d.catalog_root=?)`, sourceID, generation, root)
	if err != nil {
		return 0, err
	}
	cleared, err := result.RowsAffected()
	if err != nil || cleared == 0 {
		return 0, err
	}
	if counted > int(cleared) {
		counted = int(cleared)
	}
	return cleared, addCatalogPublishPendingTx(ctx, tx, sourceID, -counted)
}
