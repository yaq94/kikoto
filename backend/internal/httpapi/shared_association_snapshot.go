package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

var errAssociationStale = errors.New("association requires a fresh preview")
var errAssociationInvalid = errors.New("association requires different objects at the same evaluation level")
var errAssociationLimit = errors.New("association exceeds the bounded review size")

type associationSubject struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

type associationReview struct {
	Rating   *int64 `json:"rating"`
	Note     string `json:"note"`
	Revision int64  `json:"revision"`
}

type associationLocation struct {
	CatalogEntryID int64  `json:"catalogEntryId"`
	SourceID       int64  `json:"sourceId"`
	SourceName     string `json:"sourceName"`
	Title          string `json:"title"`
	Path           string `json:"path"`
	WorkID         *int64 `json:"workId"`
	SeriesID       *int64 `json:"seriesId"`
	Position       int    `json:"position"`
	Revision       int64  `json:"revision"`
	SourceRevision int64  `json:"sourceRevision"`
}

type associationObject struct {
	Subject        associationSubject          `json:"subject"`
	Level          string                      `json:"level"`
	Title          string                      `json:"title"`
	Code           string                      `json:"code"`
	WorkID         *int64                      `json:"workId"`
	Revision       int64                       `json:"revision"`
	IdentityWorkID int64                       `json:"identityWorkId"`
	Members        []int64                     `json:"members"`
	WorkRevisions  []int64                     `json:"workRevisions"`
	OperationIDs   []int64                     `json:"operationIds"`
	Locations      []associationLocation       `json:"locations"`
	Reviews        map[int64]associationReview `json:"reviews"`
}

type associationSnapshot struct {
	Left  associationObject `json:"left"`
	Right associationObject `json:"right"`
}

type associationResult struct {
	associationSnapshot
	AppliedReviews map[int64]associationReview `json:"appliedReviews"`
}

func associationRevision(ctx context.Context, tx *sql.Tx, kind string, id int64) (int64, error) {
	var value int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM shared_identity_revision WHERE kind=? AND entity_id=?),0)`, kind, id).Scan(&value)
	return value, err
}

func loadAssociationObject(ctx context.Context, tx *sql.Tx, subject associationSubject) (associationObject, error) {
	o := associationObject{Subject: subject, Level: "work", Members: []int64{}, WorkRevisions: []int64{}, Locations: []associationLocation{}, Reviews: map[int64]associationReview{}}
	if subject.ID <= 0 {
		return o, errAssociationInvalid
	}
	var err error
	switch subject.Kind {
	case "work":
		err = tx.QueryRowContext(ctx, `SELECT title,primary_code,id FROM work WHERE id=?`, subject.ID).Scan(&o.Title, &o.Code, &o.WorkID)
	case "catalog":
		err = tx.QueryRowContext(ctx, `SELECT title,primary_code,work_id FROM source_catalog_entry WHERE id=?`, subject.ID).Scan(&o.Title, &o.Code, &o.WorkID)
		if err == nil && o.WorkID == nil && o.Code != "" {
			var id int64
			err = tx.QueryRowContext(ctx, `SELECT COALESCE(l.canonical_work_id,w.id) FROM work w LEFT JOIN work_edition e ON e.work_id=w.id LEFT JOIN logical_work l ON l.id=e.logical_work_id WHERE w.primary_code=?`, o.Code).Scan(&id)
			if err == nil {
				o.WorkID = &id
			} else if errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
		}
	case "series":
		o.Level = "series"
		err = tx.QueryRowContext(ctx, `SELECT title,work_id FROM catalog_series WHERE id=?`, subject.ID).Scan(&o.Title, &o.WorkID)
	default:
		return o, errAssociationInvalid
	}
	if err != nil {
		return o, err
	}
	if o.Revision, err = associationRevision(ctx, tx, subject.Kind, subject.ID); err != nil {
		return o, err
	}
	if o.WorkID != nil {
		if err = tx.QueryRowContext(ctx, `SELECT identity_work_id FROM shared_work_identity WHERE work_id=?`, *o.WorkID).Scan(&o.IdentityWorkID); err != nil {
			return o, err
		}
		rows, e := tx.QueryContext(ctx, `SELECT i.work_id,COALESCE(r.revision,0),COALESCE(m.operation_id,0) FROM shared_work_identity i LEFT JOIN shared_identity_revision r ON r.kind='work' AND r.entity_id=i.work_id LEFT JOIN shared_identity_member m ON m.work_id=i.work_id WHERE i.identity_work_id=? ORDER BY i.work_id LIMIT 101`, o.IdentityWorkID)
		if e != nil {
			return o, e
		}
		for rows.Next() {
			var id, revision, operation int64
			if err = rows.Scan(&id, &revision, &operation); err != nil {
				break
			}
			o.Members = append(o.Members, id)
			o.WorkRevisions = append(o.WorkRevisions, revision)
			o.OperationIDs = append(o.OperationIDs, operation)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return o, err
		}
		if len(o.Members) > 100 {
			return o, errAssociationLimit
		}
		var series, episode int
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_series WHERE work_id=?),EXISTS(SELECT 1 FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id WHERE e.work_id=?)`, *o.WorkID, *o.WorkID).Scan(&series, &episode)
		if err != nil {
			return o, err
		}
		if series != 0 {
			o.Level = "series"
		} else if episode != 0 {
			o.Level = "episode"
		}
	} else if subject.Kind == "catalog" {
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_series_member WHERE catalog_entry_id=?)`, subject.ID).Scan(&exists)
		if err != nil {
			return o, err
		}
		if exists != 0 {
			o.Level = "episode"
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,COALESCE(e.file_source_id,0),COALESCE(fs.display_name,''),e.title,e.root_path,e.work_id,m.series_id,COALESCE(m.position,0),COALESCE(r.revision,0),COALESCE(sr.revision,0)
	 FROM source_catalog_entry e LEFT JOIN file_source fs ON fs.id=e.file_source_id LEFT JOIN catalog_series_member m ON m.catalog_entry_id=e.id
	 LEFT JOIN shared_identity_revision r ON r.kind='catalog' AND r.entity_id=e.id LEFT JOIN shared_identity_revision sr ON sr.kind='source' AND sr.entity_id=e.file_source_id
	 WHERE (?='catalog' AND e.id=?) OR e.work_id IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=? AND ?>0)
	 OR m.series_id IN (SELECT id FROM catalog_series WHERE (?='series' AND id=?) OR (work_id IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=? AND ?>0)))
	 ORDER BY e.id LIMIT 101`, subject.Kind, subject.ID, o.IdentityWorkID, o.IdentityWorkID, subject.Kind, subject.ID, o.IdentityWorkID, o.IdentityWorkID)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var l associationLocation
		if err = rows.Scan(&l.CatalogEntryID, &l.SourceID, &l.SourceName, &l.Title, &l.Path, &l.WorkID, &l.SeriesID, &l.Position, &l.Revision, &l.SourceRevision); err != nil {
			break
		}
		o.Locations = append(o.Locations, l)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return o, err
	}
	if len(o.Locations) > 100 {
		return o, errAssociationLimit
	}
	if o.IdentityWorkID == 0 {
		return o, nil
	}
	// Numbered/local sources also belong to the evidence, without publishing
	// endpoints, remote paths or protected source configuration in the preview.
	rows, err = tx.QueryContext(ctx, `SELECT p.work_id,p.file_source_id,fs.display_name,p.remote_id,COALESCE(r.revision,0)
	 FROM work_source_presence p JOIN file_source fs ON fs.id=p.file_source_id
	 LEFT JOIN shared_identity_revision r ON r.kind='source' AND r.entity_id=fs.id
	 WHERE p.work_id IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?)
	 AND NOT EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.work_id=p.work_id AND e.file_source_id=p.file_source_id)
	 ORDER BY p.work_id,p.file_source_id,p.presence_type LIMIT 101`, o.IdentityWorkID)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		l := associationLocation{Title: o.Title}
		if err = rows.Scan(&l.WorkID, &l.SourceID, &l.SourceName, &l.Path, &l.SourceRevision); err != nil {
			break
		}
		o.Locations = append(o.Locations, l)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return o, err
	}
	if len(o.Locations) > 100 {
		return o, errAssociationLimit
	}
	rows, err = tx.QueryContext(ctx, `SELECT s.user_id,s.rating,s.note,COALESCE(r.revision,0) FROM user_work_state s LEFT JOIN shared_review_revision r ON r.user_id=s.user_id AND r.work_id=s.work_id WHERE s.work_id=? AND (s.rating IS NOT NULL OR s.note<>'' OR r.revision>0) ORDER BY s.user_id LIMIT 1001`, o.IdentityWorkID)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var user int64
		var review associationReview
		if err = rows.Scan(&user, &review.Rating, &review.Note, &review.Revision); err != nil {
			break
		}
		o.Reviews[user] = review
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return o, err
	}
	if len(o.Reviews) > 1000 {
		return o, errAssociationLimit
	}
	return o, nil
}

func loadAssociationSnapshot(ctx context.Context, tx *sql.Tx, left, right associationSubject) (associationSnapshot, string, error) {
	var snapshot associationSnapshot
	var err error
	if left == right {
		return snapshot, "", errAssociationInvalid
	}
	if snapshot.Left, err = loadAssociationObject(ctx, tx, left); err != nil {
		return snapshot, "", err
	}
	if snapshot.Right, err = loadAssociationObject(ctx, tx, right); err != nil {
		return snapshot, "", err
	}
	if snapshot.Left.Level != snapshot.Right.Level || (snapshot.Left.IdentityWorkID > 0 && snapshot.Left.IdentityWorkID == snapshot.Right.IdentityWorkID) {
		return snapshot, "", errAssociationInvalid
	}
	if len(snapshot.Left.Members)+len(snapshot.Right.Members) > 100 {
		return snapshot, "", errAssociationLimit
	}
	return snapshot, associationFingerprint(snapshot), nil
}

func associationFingerprint(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func associationConflicts(a, b associationReview) []string {
	fields := []string{}
	if a.Rating != nil && b.Rating != nil && *a.Rating != *b.Rating {
		fields = append(fields, "rating")
	}
	if a.Note != "" && b.Note != "" && a.Note != b.Note {
		fields = append(fields, "note")
	}
	return fields
}

func associationUsers(snapshot associationSnapshot) []int64 {
	seen := map[int64]bool{}
	for id := range snapshot.Left.Reviews {
		seen[id] = true
	}
	for id := range snapshot.Right.Reviews {
		seen[id] = true
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
