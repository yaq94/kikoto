package httpapi

import (
	"context"
	"database/sql"
)

// catalogSeriesMembersSQL is the detail projection and admission boundary for
// published, relocated and retained missing members of a shared series.
func catalogSeriesMembersSQL() string {
	pendingMember := catalogEntryPendingSQL("e")
	admittedMember := catalogEntryAdmittedSQL("e")
	pendingSuccessor := catalogEntryPendingSQL("successor")
	admittedSuccessor := catalogEntryAdmittedSQL("successor")
	return `WITH grouped AS (
		SELECT COALESCE(successor.id,e.id) AS id,
			COALESCE(NULLIF(successor.title,''),e.title) AS title,
			COALESCE(NULLIF(successor.file_source_id,0),NULLIF(e.file_source_id,0),0) AS source_id,
			COALESCE(successor_fs.display_name,fs.display_name,'') AS display_name,
			COALESCE(successor.primary_code,e.primary_code) AS primary_code,
				COALESCE(successor.work_id,e.work_id) AS work_id,
				COALESCE(successor.availability_status,e.availability_status) AS status,m.position,
				ROW_NUMBER() OVER(
					PARTITION BY COALESCE(identity.identity_work_id,-e.id)
					ORDER BY CASE WHEN COALESCE(successor.availability_status,e.availability_status)='available' THEN 0 ELSE 1 END,m.position,e.id
				) AS identity_rank
			FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
			LEFT JOIN source_catalog_entry successor ON successor.id=e.relocated_to_id
			LEFT JOIN file_source fs ON fs.id=e.file_source_id
			LEFT JOIN file_source successor_fs ON successor_fs.id=successor.file_source_id
			LEFT JOIN shared_work_identity identity ON identity.work_id=COALESCE(successor.work_id,e.work_id)
			WHERE ((e.relocated_to_id IS NULL AND e.file_source_id IS NOT NULL)
				OR (e.relocated_to_id IS NOT NULL AND successor.file_source_id IS NOT NULL))
				AND ` + pendingMember + `
				AND (
					(e.relocated_to_id IS NULL AND (e.availability_status='missing' OR ` + admittedMember + `))
					OR (e.relocated_to_id IS NOT NULL AND (COALESCE(successor.availability_status,'')='missing'
						OR (` + pendingSuccessor + ` AND ` + admittedSuccessor + `)))
				)
				AND m.series_id IN (SELECT related.id FROM catalog_series selected JOIN catalog_series related ON related.id=selected.id OR related.work_id IN (SELECT peer.work_id FROM shared_work_identity own JOIN shared_work_identity peer ON peer.identity_work_id=own.identity_work_id WHERE own.work_id=selected.work_id) WHERE selected.id=?))
			SELECT id,title,source_id,display_name,primary_code,work_id,status,position FROM grouped WHERE identity_rank=1 ORDER BY position,id`
}

// requireCatalogSeriesReadable protects review and translation with the same
// visible membership as the detail route. Personal imports restore episodes
// through their parent work without creating source membership or media.
func requireCatalogSeriesReadable(ctx context.Context, q catalogEntryQueryer, id int64) error {
	var readable bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(`+catalogSeriesMembersSQL()+`) OR EXISTS(
  SELECT 1 FROM catalog_series s JOIN work child ON child.parent_work_id=s.work_id WHERE s.id=?)`, id, id).Scan(&readable)
	if err != nil {
		return err
	}
	if !readable {
		return sql.ErrNoRows
	}
	return nil
}
