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
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		(SELECT count(*) FROM (
			SELECT 1 FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
						AND d.work_split<>'duplicate' AND o.kind<>'folder' LIMIT %d)),
		(SELECT count(*) FROM (
			SELECT 1 FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
					AND d.work_split<>'duplicate' AND o.kind='audio' LIMIT %d)),
		(SELECT count(*) FROM (
			SELECT 1 FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
						AND d.work_split<>'duplicate' AND o.kind<>'audio' AND o.kind<>'folder' LIMIT %d)),
		NOT EXISTS(SELECT 1 FROM source_catalog_directory d
			WHERE d.file_source_id=?1 AND d.generation=?2 AND d.catalog_root=?3
					AND d.status<>'complete' AND NOT (d.status='empty' AND d.error_code='not_found'))`,
		catalogadmission.ProbeFiles+1, catalogadmission.ProbeAudio, catalogadmission.ProbeNonAudio+1),
		sourceID, generation, root).
		Scan(&adm.Files, &adm.Audio, &adm.NonAudio, &adm.FullyObserved)
	if err != nil {
		return catalogRootAdmission{}, err
	}
	adm.Reason = catalogadmission.Decision(adm.Files, adm.Audio, adm.NonAudio)
	return adm, nil
}

// catalogAdmissionBlocks decides whether a count may be used for a refusal.
// Missing audio is not monotonic, so a partially observed subtree waits for its
// complete listing before it is refused.
func catalogAdmissionBlocks(adm catalogRootAdmission) bool {
	if adm.Reason == "" {
		return false
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
	// A partially observed root waits for its full listing: a later page may
	// still add the audio that makes it playable.
	rows, err := tx.QueryContext(ctx, `SELECT d.catalog_root
		FROM source_catalog_directory d
		JOIN source_catalog_observation o
			ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root<>'' AND o.publish_pending=1
				AND NOT EXISTS(SELECT 1 FROM source_catalog_directory d2
					WHERE d2.file_source_id=d.file_source_id AND d2.generation=d.generation
						AND d2.catalog_root=d.catalog_root
							AND d2.status<>'complete' AND NOT (d2.status='empty' AND d2.error_code='not_found'))
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

// catalogEntryAdmittedSQL filters a published card by the same thresholds as
// the publisher and single-card API. The limits and probe bounds come from the
// catalogadmission constants, so a rule change cannot leave this SQL on the old
// numbers.
//
// The stored admission row answers the question with one primary-key lookup.
// Its generation must match the entry, otherwise the counts describe an older
// file set; a file write invalidates the row in the same transaction, so a
// mismatch falls back to counting the live rows. Both arms call
// catalogadmission.Decision, so the fast and slow paths cannot disagree.
func catalogEntryAdmittedSQL(alias string) string {
	return fmt.Sprintf(`(
			%[2]s AND COALESCE(
				(SELECT admission.audio>0 AND admission.files<=%[3]d
					AND NOT (admission.non_audio>%[4]d AND admission.audio<%[5]d)
				 FROM source_catalog_entry_admission admission
				 WHERE admission.catalog_entry_id=%[1]s.id AND admission.generation=%[1]s.last_seen_generation),
				(SELECT live.audio>0 AND live.files<=%[3]d
					AND NOT (live.non_audio>%[4]d AND live.audio<%[5]d)
				 FROM (SELECT
					(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file af
						WHERE af.catalog_entry_id=%[1]s.id AND af.kind<>'folder'
						  AND af.observed_generation=%[1]s.last_seen_generation LIMIT %[6]d)) AS files,
					(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file aaf
						WHERE aaf.catalog_entry_id=%[1]s.id AND aaf.kind='audio'
						  AND aaf.observed_generation=%[1]s.last_seen_generation LIMIT %[5]d)) AS audio,
					(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file anf
						WHERE anf.catalog_entry_id=%[1]s.id AND anf.kind<>'audio' AND anf.kind<>'folder'
						  AND anf.observed_generation=%[1]s.last_seen_generation LIMIT %[7]d)) AS non_audio) AS live)
			))`, alias, catalogEntryPendingSQL(alias),
		catalogadmission.ProbeFiles, catalogadmission.ProbeNonAudio, catalogadmission.ProbeAudio,
		catalogadmission.ProbeFiles+1, catalogadmission.ProbeNonAudio+1)
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
	// Read the stored counts when they describe the entry's current generation.
	// A file write marks the row with an impossible generation, so a miss means
	// the live rows are the only trustworthy source; both branches use the same
	// caps as the SQL filter above.
	err := q.QueryRowContext(ctx, fmt.Sprintf(`SELECT
			COALESCE((SELECT admission.files FROM source_catalog_entry_admission admission
				WHERE admission.catalog_entry_id=e.id AND admission.generation=e.last_seen_generation),
				(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind<>'folder' AND f.observed_generation=e.last_seen_generation LIMIT %d))),
			COALESCE((SELECT admission.audio FROM source_catalog_entry_admission admission
				WHERE admission.catalog_entry_id=e.id AND admission.generation=e.last_seen_generation),
				(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio' AND f.observed_generation=e.last_seen_generation LIMIT %d))),
			COALESCE((SELECT admission.non_audio FROM source_catalog_entry_admission admission
				WHERE admission.catalog_entry_id=e.id AND admission.generation=e.last_seen_generation),
				(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind<>'audio' AND f.kind<>'folder' AND f.observed_generation=e.last_seen_generation LIMIT %d)))
		FROM source_catalog_entry e WHERE e.id=?`,
		catalogadmission.ProbeFiles+1, catalogadmission.ProbeAudio, catalogadmission.ProbeNonAudio+1),
		id).Scan(&adm.Files, &adm.Audio, &adm.NonAudio)
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

// catalogEntryAdmissionRequired reports whether a source-backed card must pass
// the published-file bounds before it can be read. A retained missing card is
// provenance with personal state: its source confirmed the absence, so it has
// no current published file set to judge. Every other source-backed card,
// including a zero-file or folder-only row, is judged by the same rule; there
// is no implicit zero-file bypass.
func catalogEntryAdmissionRequired(entry catalogEntry) bool {
	return entry.SourceID != 0 && entry.Status != "missing"
}

// catalogVerdictPlayable reports whether a card whose admission passed can
// still be opened. A failed, restricted, maintenance or unconfirmed range
// proves nothing about its files, and the detail, track and media endpoints
// keep serving the last published set; only a confirmed absence or an operator
// exclusion removes playback. A disabled source never reaches this rule: its
// cards are not listed at all.
func catalogVerdictPlayable(status string) bool {
	switch status {
	case "missing", "excluded":
		return false
	default:
		return true
	}
}

// catalogVerdictPlayableSQL is the SQL form of catalogVerdictPlayable. A series
// and its member cards read the same rule, so a series of failed-but-playable
// episodes cannot report itself unplayable while each episode stays playable.
func catalogVerdictPlayableSQL(status string) string {
	return `(` + status + ` NOT IN ('missing','excluded'))`
}

// catalogEntryHasAudioSQL answers whether a card can play anything, using the
// same stored counts the admission filter reads. A browse page evaluates this
// for every candidate, so the live file probe would add one index lookup per
// card; the stored row answers it from the primary key. A card without a
// current stored row falls back to the file set.
func catalogEntryHasAudioSQL(alias string) string {
	return fmt.Sprintf(`COALESCE(
		(SELECT admission.audio>0 FROM source_catalog_entry_admission admission
		 WHERE admission.catalog_entry_id=%[1]s.id AND admission.generation=%[1]s.last_seen_generation),
		EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=%[1]s.id
			AND f.kind='audio' AND f.observed_generation=%[1]s.last_seen_generation))`, alias)
}

// refreshCatalogEntryAdmissionTx rewrites the stored counts for one card from
// its current published file set. It runs inside the publish transaction, after
// the card's files are written, so a committed card always carries counts that
// describe the rows a reader can see.
func refreshCatalogEntryAdmissionTx(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
		SELECT e.id,e.last_seen_generation,
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind<>'folder'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d)),
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind='audio'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d)),
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind<>'audio' AND f.kind<>'folder'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d))
		FROM source_catalog_entry e WHERE e.id=?
		ON CONFLICT(catalog_entry_id) DO UPDATE SET
			generation=excluded.generation,files=excluded.files,
			audio=excluded.audio,non_audio=excluded.non_audio,updated_at=CURRENT_TIMESTAMP`,
		catalogadmission.ProbeFiles+1, catalogadmission.ProbeAudio, catalogadmission.ProbeNonAudio+1), id)
	return err
}

// refreshCatalogEntryAdmissionsTx is the batch form used by a publish batch.
// The ids come from the same transaction that wrote their files, so one
// statement refreshes the whole batch instead of one round trip per card.
func refreshCatalogEntryAdmissionsTx(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
		SELECT e.id,e.last_seen_generation,
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind<>'folder'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d)),
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind='audio'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d)),
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind<>'audio' AND f.kind<>'folder'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d))
		FROM source_catalog_entry e WHERE e.id IN (`+sqlPlaceholders(len(ids))+`)
		ON CONFLICT(catalog_entry_id) DO UPDATE SET
			generation=excluded.generation,files=excluded.files,
			audio=excluded.audio,non_audio=excluded.non_audio,updated_at=CURRENT_TIMESTAMP`,
		catalogadmission.ProbeFiles+1, catalogadmission.ProbeAudio, catalogadmission.ProbeNonAudio+1),
		int64Args(ids)...)
	return err
}

// refreshSourceCatalogEntryAdmissionsTx recomputes every card of one source.
// It serves a repair pass and the synthetic performance fixture; the publish
// path uses the batch form because it already knows the cards it wrote.
func refreshSourceCatalogEntryAdmissionsTx(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
		SELECT e.id,e.last_seen_generation,
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind<>'folder'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d)),
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind='audio'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d)),
			(SELECT count(*) FROM (SELECT 1 FROM source_catalog_file f
				WHERE f.catalog_entry_id=e.id AND f.kind<>'audio' AND f.kind<>'folder'
				  AND f.observed_generation=e.last_seen_generation LIMIT %d))
		FROM source_catalog_entry e WHERE e.file_source_id=?
		ON CONFLICT(catalog_entry_id) DO UPDATE SET
			generation=excluded.generation,files=excluded.files,
			audio=excluded.audio,non_audio=excluded.non_audio,updated_at=CURRENT_TIMESTAMP`,
		catalogadmission.ProbeFiles+1, catalogadmission.ProbeAudio, catalogadmission.ProbeNonAudio+1),
		sourceID)
	if err != nil {
		return err
	}
	return refreshSourceContentTranslationBrowsableTx(ctx, tx, sourceID)
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
