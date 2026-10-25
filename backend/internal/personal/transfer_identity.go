package personal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// exportIdentity names one evaluation object. An episode's parent is a
// separate minimal series identity: it never carries the episode's aliases,
// review, note, progress or statistics.
func exportIdentity(ctx context.Context, tx *sql.Tx, work int64, includeParent bool, exported map[int64]bool) (*BackupIdentity, error) {
	i := &BackupIdentity{}
	kind, parentWork, err := transferWorkKind(ctx, tx, work)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT r.ref,w.title FROM work w JOIN personal_transfer_ref r ON r.work_id=w.id AND r.is_primary=1 WHERE w.id=?`, work).Scan(&i.Ref, &i.Title); err != nil {
		return nil, err
	}
	i.Kind = kind
	err = scanRows(ctx, tx, `SELECT r.ref FROM personal_transfer_ref r
		LEFT JOIN source_catalog_entry c ON c.id=r.catalog_entry_id
		LEFT JOIN catalog_series s ON s.id=r.series_id
		WHERE (r.work_id=? OR c.work_id=? OR s.work_id=?) AND r.ref<>? ORDER BY r.ref LIMIT 101`, []any{work, work, work, i.Ref}, func(rows *sql.Rows) error {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		i.References = append(i.References, ref)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(i.References) > 100 {
		return nil, ErrLimit
	}
	if kind != "episode" || !includeParent {
		return i, nil
	}
	parent, err := exportEpisodeParent(ctx, tx, work, parentWork)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		// No persisted relation exists to carry. The entry is still exported so
		// the review is not lost; a v3 import reports parent_required rather
		// than inventing a series from the title.
		return i, nil
	}
	i.Parent = parent
	// Parent aliases belong to the parent field, never to the episode object.
	filtered := i.References[:0]
	for _, ref := range i.References {
		if ref != parent.Ref && !parentReference(parent, ref) {
			filtered = append(filtered, ref)
		}
	}
	i.References = filtered
	return i, nil
}

func parentReference(parent *BackupIdentity, ref string) bool {
	for _, candidate := range parent.References {
		if candidate == ref {
			return true
		}
	}
	return false
}

// exportEpisodeParent uses work.parent_work_id as the authoritative relation.
// A library that knows the episode's catalog membership but has no registered
// series work still exports the stable series reference and minimal title, as
// the backup contract requires. Ambiguous membership is a conflict.
func exportEpisodeParent(ctx context.Context, tx *sql.Tx, work, parentWork int64) (*BackupIdentity, error) {
	var seriesID int64
	var title, ref string
	if parentWork != 0 {
		if parentWork == work {
			return nil, ErrConflict
		}
		var parentOfParent sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, parentWork).Scan(&parentOfParent); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrConflict
			}
			return nil, err
		}
		if parentOfParent.Valid {
			return nil, ErrConflict
		}
		if err := transferParentCycleProblemRead(ctx, tx, work, parentWork); err != nil {
			return nil, err
		}
		err := tx.QueryRowContext(ctx, `SELECT s.id,s.title,r.ref FROM catalog_series s
			JOIN personal_transfer_ref r ON r.series_id=s.id
			WHERE s.work_id=? ORDER BY s.id LIMIT 2`, parentWork).Scan(&seriesID, &title, &ref)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConflict
		}
		if err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT title FROM work WHERE id=?`, parentWork).Scan(&title); err != nil {
			return nil, err
		}
	} else {
		// A library can know an episode's exact catalog membership before
		// work.parent_work_id was written. Only an unambiguous membership
		// supplies a stable parent identity; a missing or multiple series is
		// never guessed. The series may have no registered work yet: its own
		// stable reference and title are still the minimal identity the backup
		// must carry, and export never registers an object.
		rows, err := tx.QueryContext(ctx, `WITH episode_entry(id) AS (
				SELECT id FROM source_catalog_entry WHERE work_id=?
				UNION
				SELECT catalog_entry_id FROM personal_transfer_ref
					WHERE work_id=? AND catalog_entry_id IS NOT NULL
			)
			SELECT DISTINCT series.id,COALESCE(series.work_id,0),series.title,ref.ref
			FROM episode_entry entry
			JOIN catalog_series_member member ON member.catalog_entry_id=entry.id
			JOIN catalog_series series ON series.id=member.series_id
			JOIN personal_transfer_ref ref ON ref.series_id=series.id
			LIMIT 2`, work, work)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		found := 0
		for rows.Next() {
			var nextSeries, nextWork int64
			var nextTitle, nextRef string
			if err = rows.Scan(&nextSeries, &nextWork, &nextTitle, &nextRef); err != nil {
				return nil, err
			}
			if found == 0 {
				seriesID, parentWork, title, ref = nextSeries, nextWork, nextTitle, nextRef
			} else if nextSeries != seriesID {
				return nil, ErrConflict
			}
			found++
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
		if found == 0 {
			return nil, nil
		}
		if parentWork != 0 {
			var parentOfParent sql.NullInt64
			if err = tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, parentWork).Scan(&parentOfParent); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, ErrConflict
				}
				return nil, err
			}
			if parentOfParent.Valid {
				return nil, ErrConflict
			}
			if err = transferParentCycleProblemRead(ctx, tx, work, parentWork); err != nil {
				return nil, err
			}
		}
	}
	parent := &BackupIdentity{Ref: ref, Kind: "series", Title: title}
	// A registered parent work may keep aliases as series-kind work references;
	// they resolve to the same series and are safe parent aliases.
	err := scanRows(ctx, tx, `SELECT r.ref FROM personal_transfer_ref r
		WHERE (r.series_id=? OR (r.work_id=? AND r.object_kind='series' AND r.is_primary=0))
			AND r.ref<>? ORDER BY r.ref LIMIT 101`, []any{seriesID, parentWork, ref}, func(rows *sql.Rows) error {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return err
		}
		if strings.HasPrefix(alias, "s:") {
			parent.References = append(parent.References, alias)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(parent.References) > 100 {
		return nil, ErrLimit
	}
	return parent, nil
}

// transferParentCycleProblemRead is the read-only cycle check used by export.
func transferParentCycleProblemRead(ctx context.Context, tx *sql.Tx, episode, parent int64) error {
	problem, err := transferParentCycleProblem(ctx, tx, episode, parent)
	if err != nil {
		return err
	}
	if problem != "" {
		return ErrConflict
	}
	return nil
}

// transferWorkKind infers the evaluation level from persisted relations.
// object_kind remains the fallback for records created before parent_work_id.
func transferWorkKind(ctx context.Context, tx *sql.Tx, work int64) (string, int64, error) {
	var isSeries, hasMembership bool
	var parentWork sql.NullInt64
	var fallback string
	err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM catalog_series s WHERE s.work_id=w.id),
		w.parent_work_id,
		EXISTS(SELECT 1 FROM source_catalog_entry c JOIN catalog_series_member m ON m.catalog_entry_id=c.id WHERE c.work_id=w.id),
		COALESCE(r.object_kind,'work')
		FROM work w LEFT JOIN personal_transfer_ref r ON r.work_id=w.id AND r.is_primary=1
		WHERE w.id=?`, work).Scan(&isSeries, &parentWork, &hasMembership, &fallback)
	if err != nil {
		return "", 0, err
	}
	if isSeries {
		if parentWork.Valid {
			return "", parentWork.Int64, ErrConflict
		}
		return "series", 0, nil
	}
	if parentWork.Valid {
		return "episode", parentWork.Int64, nil
	}
	if hasMembership || fallback == "episode" {
		return "episode", 0, nil
	}
	return fallback, 0, nil
}

type identityMatch struct {
	work    int64
	catalog int64
	series  int64
	problem string
}

func sharedIdentity(ctx context.Context, tx *sql.Tx, work int64) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT identity_work_id FROM shared_work_identity WHERE work_id=?`, work).Scan(&id)
	return id, err
}

// Exact portable references win over a code's canonical edition projection.
// Every known piece of evidence must agree; unknown foreign references never
// cause a title/path match or rewrite an existing source association.
func resolveTransferIdentity(ctx context.Context, tx *sql.Tx, i *BackupIdentity, code string) (identityMatch, error) {
	m := identityMatch{}
	refs := append([]string{i.Ref}, i.References...)
	for _, ref := range refs {
		var work, catalog, series sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(r.work_id,c.work_id,s.work_id),r.catalog_entry_id,r.series_id
			FROM personal_transfer_ref r LEFT JOIN source_catalog_entry c ON c.id=r.catalog_entry_id LEFT JOIN catalog_series s ON s.id=r.series_id WHERE r.ref=?`, ref).
			Scan(&work, &catalog, &series)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return m, err
		}
		if !work.Valid {
			// A known but unregistered source is not permission to replace its identity.
			if m.catalog != 0 && m.catalog != catalog.Int64 || m.series != 0 && m.series != series.Int64 {
				m.problem = "identity_conflict"
				return m, nil
			}
			if catalog.Valid {
				m.catalog = catalog.Int64
			}
			if series.Valid {
				m.series = series.Int64
			}
			continue
		}
		if m.work != 0 && m.work != work.Int64 {
			m.problem = "identity_conflict"
			return m, nil
		}
		m.work = work.Int64
		if series.Valid {
			if m.series != 0 && m.series != series.Int64 {
				m.problem = "identity_conflict"
				return m, nil
			}
			m.series = series.Int64
		}
	}
	if code != "" {
		var exact int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM work WHERE primary_code=?`, code).Scan(&exact)
		if errors.Is(err, sql.ErrNoRows) {
			exact, err = resolveCode(ctx, tx, code)
		}
		if err != nil {
			return m, err
		}
		if m.work == 0 {
			m.work = exact
		} else if exact != 0 && exact != m.work {
			m.problem = "identity_conflict"
			return m, nil
		}
		if m.work != 0 {
			var actual string
			if err := tx.QueryRowContext(ctx, `SELECT primary_code FROM work WHERE id=?`, m.work).Scan(&actual); err != nil {
				return m, err
			}
			if actual != "" && actual != code && exact != m.work {
				m.problem = "identity_conflict"
				return m, nil
			}
		}
	}
	if m.work != 0 {
		kind, _, err := transferWorkKind(ctx, tx, m.work)
		if err != nil {
			return m, err
		}
		if kind != i.Kind {
			m.problem = "identity_conflict"
		}
	}
	return m, nil
}

// transferIdentityKindProblem checks the evaluation level that an unregistered
// source reference implies. A known catalog episode must not be restored as a
// series or standalone work, because those identities keep separate reviews.
func transferIdentityKindProblem(ctx context.Context, tx *sql.Tx, w BackupWork, m identityMatch) (string, error) {
	if m.catalog != 0 {
		expected := "work"
		var episode bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_series_member WHERE catalog_entry_id=?)`, m.catalog).Scan(&episode); err != nil {
			return "", err
		}
		if episode {
			expected = "episode"
		}
		if w.Identity.Kind != expected {
			return "identity_conflict", nil
		}
		var code string
		if err := tx.QueryRowContext(ctx, `SELECT primary_code FROM source_catalog_entry WHERE id=?`, m.catalog).Scan(&code); err != nil {
			return "", err
		}
		if w.PrimaryCode != "" && w.PrimaryCode != code {
			return "identity_conflict", nil
		}
	}
	if m.catalog == 0 && m.series != 0 {
		if w.Identity.Kind != "series" || w.PrimaryCode != "" {
			return "identity_conflict", nil
		}
	}
	if w.Identity.Kind == "series" && w.PrimaryCode != "" {
		return "identity_conflict", nil
	}
	return "", nil
}

func registerTransferIdentityWork(ctx context.Context, tx *sql.Tx, w BackupWork, m identityMatch) (int64, error) {
	if m.catalog != 0 {
		return registerCatalogTransferWork(ctx, tx, w, m.catalog)
	}
	if m.series != 0 {
		return registerSeriesTransferWork(ctx, tx, w, m.series)
	}
	return registerTransferWork(ctx, tx, w)
}

// registerCatalogTransferWork rebuilds the same minimal registration that the
// source-catalog review endpoint performs: one work, its canonical logical row
// and edition, then the entry's work_id. It never moves an existing work.
func registerCatalogTransferWork(ctx context.Context, tx *sql.Tx, w BackupWork, catalogID int64) (int64, error) {
	catalogID, title, code, linked, err := liveTransferCatalogEntryTx(ctx, tx, catalogID)
	if err != nil {
		return 0, err
	}
	if linked.Valid {
		return linked.Int64, nil
	}
	id, err := insertTransferWork(ctx, tx, code, title, w.Identity.Kind)
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET work_id=? WHERE id=? AND work_id IS NULL`, id, catalogID); err != nil {
		return 0, err
	}
	if err = adoptEpisodeParentFromMembership(ctx, tx, id); err != nil {
		return 0, err
	}
	return id, nil
}

// adoptEpisodeParentFromMembership records the one series a catalog episode
// already belongs to. It only fills a missing parent and never overwrites a
// different relation or creates a series.
func adoptEpisodeParentFromMembership(ctx context.Context, tx *sql.Tx, workID int64) error {
	var existing sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, workID).Scan(&existing); err != nil {
		return err
	}
	if existing.Valid {
		return nil
	}
	var seriesWork int64
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT series.work_id),COALESCE(MAX(series.work_id),0)
		FROM source_catalog_entry entry
		JOIN catalog_series_member member ON member.catalog_entry_id=entry.id
		JOIN catalog_series series ON series.id=member.series_id
		WHERE entry.work_id=? AND series.work_id IS NOT NULL AND series.work_id<>?`, workID, workID).Scan(&count, &seriesWork); err != nil {
		return err
	}
	if count != 1 {
		return nil
	}
	problem, err := transferParentCycleProblem(ctx, tx, workID, seriesWork)
	if err != nil || problem != "" {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE work SET parent_work_id=? WHERE id=? AND parent_work_id IS NULL`, seriesWork, workID)
	return err
}

// liveTransferCatalogEntryTx follows retirement aliases to the card that still
// owns the identity. A restore must attach a new work to that successor, never
// resurrect a hidden work on an alias the catalog can no longer expose.
func liveTransferCatalogEntryTx(ctx context.Context, tx *sql.Tx, catalogID int64) (int64, string, string, sql.NullInt64, error) {
	for hop := 0; ; hop++ {
		var title, code string
		var linked, next sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT title,primary_code,work_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, catalogID).
			Scan(&title, &code, &linked, &next); err != nil {
			return 0, "", "", sql.NullInt64{}, err
		}
		if !next.Valid {
			return catalogID, title, code, linked, nil
		}
		if hop == 8 {
			return 0, "", "", sql.NullInt64{}, sql.ErrNoRows
		}
		catalogID = next.Int64
	}
}

func registerSeriesTransferWork(ctx context.Context, tx *sql.Tx, w BackupWork, seriesID int64) (int64, error) {
	var title string
	var linked sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT title,work_id FROM catalog_series WHERE id=?`, seriesID).Scan(&title, &linked); err != nil {
		return 0, err
	}
	if linked.Valid {
		return linked.Int64, nil
	}
	id, err := insertTransferWork(ctx, tx, "", title, "series")
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_series SET work_id=? WHERE id=? AND work_id IS NULL`, id, seriesID); err != nil {
		return 0, err
	}
	if err = adoptSeriesIdentityRefs(ctx, tx, id, seriesID, w.Identity, false); err != nil {
		return 0, err
	}
	return id, nil
}

func insertTransferWork(ctx context.Context, tx *sql.Tx, code, title, kind string) (int64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO work(primary_code,title) VALUES(?,?)`, code, title)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO logical_work(canonical_work_id,canonical_code) VALUES(?,?)`, id, code)
	if err != nil {
		return 0, err
	}
	logical, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(?,?,?,1)`, id, logical, code); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET object_kind=? WHERE work_id=? AND is_primary=1`, kind, id); err != nil {
		return 0, err
	}
	return id, nil
}

func registerTransferWork(ctx context.Context, tx *sql.Tx, w BackupWork) (int64, error) {
	id, err := insertTransferWork(ctx, tx, w.PrimaryCode, w.Identity.Title, w.Identity.Kind)
	if err != nil {
		return 0, err
	}
	if w.Identity.Kind == "series" {
		result, err := tx.ExecContext(ctx, `INSERT INTO catalog_series(title,work_id) VALUES(?,?)`, w.Identity.Title, id)
		if err != nil {
			return 0, err
		}
		seriesID, err := result.LastInsertId()
		if err != nil {
			return 0, err
		}
		if err = adoptSeriesIdentityRefs(ctx, tx, id, seriesID, w.Identity, true); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// adoptSeriesIdentityRefs preserves one series-scoped reference per series.
// A newly created series adopts the backup reference as its series identity;
// an existing series keeps its own reference and records the backup reference
// as a series-kind work alias. Any other owner is contradictory.
func adoptSeriesIdentityRefs(ctx context.Context, tx *sql.Tx, workID, seriesID int64, identity *BackupIdentity, created bool) error {
	candidates := append([]string{identity.Ref}, identity.References...)
	adopted := false
	for _, ref := range candidates {
		if !strings.HasPrefix(ref, "s:") {
			continue
		}
		state, err := loadTransferRefState(ctx, tx, ref)
		if err != nil {
			return err
		}
		if state.exists {
			if state.series.Valid && state.series.Int64 == seriesID {
				adopted = true
				continue
			}
			if state.work.Valid && state.work.Int64 == workID {
				adopted = true
				continue
			}
			return ErrConflict
		}
		if created && !adopted {
			result, err := tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET ref=? WHERE series_id=?`, ref, seriesID)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return ErrConflict
			}
			adopted = true
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO personal_transfer_ref(ref,work_id,object_kind) VALUES(?,?,'series')`, ref, workID); err != nil {
			return err
		}
		adopted = true
	}
	return nil
}

func saveTransferReferences(ctx context.Context, tx *sql.Tx, work int64, i *BackupIdentity) error {
	for _, ref := range append([]string{i.Ref}, i.References...) {
		// Only missing aliases are remembered; known catalog/series locations keep
		// their exact ownership and are never reassigned by a personal import.
		if _, err := tx.ExecContext(ctx, `INSERT INTO personal_transfer_ref(ref,work_id,object_kind) VALUES(?,?,?) ON CONFLICT(ref) DO NOTHING`, ref, work, i.Kind); err != nil {
			return err
		}
	}
	return nil
}
