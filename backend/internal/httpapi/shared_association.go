package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

type associationInput struct {
	Left  associationSubject `json:"left"`
	Right associationSubject `json:"right"`
}
type associationDecisionInput struct {
	Version string          `json:"version"`
	Rating  json.RawMessage `json:"rating"`
	Note    json.RawMessage `json:"note"`
}
type storedAssociation struct {
	ID                           int64
	ActorID                      sql.NullInt64
	Status, Version, Fingerprint string
	Snapshot                     associationSnapshot
}

func decodeAssociationBody(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		writeAPIError(w, 400, "invalid_request", "invalid association request", false)
		return false
	}
	return true
}

func writeAssociationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeAPIError(w, 404, "not_found", "association or object not found", false)
	case errors.Is(err, errAssociationInvalid):
		writeAPIError(w, 409, "association_invalid", errAssociationInvalid.Error(), false)
	case errors.Is(err, errAssociationStale):
		writeAPIError(w, 409, "association_stale", "refresh the association preview", false)
	case errors.Is(err, errAssociationLimit):
		writeAPIError(w, 429, "limit_exceeded", errAssociationLimit.Error(), false)
	default:
		writeError(w, err)
	}
}

func (s *Server) createSharedAssociationPreview(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:read")
	if !ok {
		return
	}
	var input associationInput
	if !decodeAssociationBody(w, r, &input) {
		return
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(r.Context(), s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	// Lock before snapshotting: concurrent reviews cannot be captured half-written.
	if _, err = tx.ExecContext(r.Context(), `UPDATE shared_association SET id=id WHERE id=-1`); err != nil {
		writeError(w, err)
		return
	}
	snapshot, fingerprint, err := loadAssociationSnapshot(r.Context(), tx.Tx, input.Left, input.Right)
	if err != nil {
		writeAssociationError(w, err)
		return
	}
	var count int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM shared_association WHERE actor_id=? AND status IN ('preview','pending') AND created_at>datetime('now','-1 day')`, actor.ID).Scan(&count); err != nil {
		writeError(w, err)
		return
	}
	if count >= 32 {
		writeAssociationError(w, errAssociationLimit)
		return
	}
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		writeError(w, err)
		return
	}
	a := storedAssociation{ActorID: sql.NullInt64{Int64: actor.ID, Valid: true}, Status: "preview", Version: hex.EncodeToString(nonce[:]), Fingerprint: fingerprint, Snapshot: snapshot}
	raw, _ := json.Marshal(snapshot)
	err = tx.QueryRowContext(r.Context(), `INSERT INTO shared_association(actor_id,version,fingerprint,snapshot_json) VALUES(?,?,?,?) RETURNING id`, actor.ID, a.Version, fingerprint, string(raw)).Scan(&a.ID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	view, err := s.associationView(r.Context(), a, actor.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, view)
}

func loadStoredAssociation(ctx context.Context, tx *sql.Tx, id int64) (storedAssociation, error) {
	a := storedAssociation{ID: id}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT actor_id,status,version,fingerprint,snapshot_json FROM shared_association WHERE id=? AND operation='link'`, id).Scan(&a.ActorID, &a.Status, &a.Version, &a.Fingerprint, &raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &a.Snapshot)
	}
	return a, err
}

func publicAssociationObject(o associationObject) map[string]any {
	locations := []map[string]any{}
	for _, l := range o.Locations {
		locations = append(locations, map[string]any{"catalogEntryId": l.CatalogEntryID, "sourceId": l.SourceID, "sourceName": l.SourceName, "title": l.Title, "workId": l.WorkID, "seriesId": l.SeriesID, "position": l.Position})
	}
	return map[string]any{"subject": o.Subject, "title": o.Title, "primaryCode": o.Code, "level": o.Level, "workId": o.WorkID, "identityWorkId": o.IdentityWorkID, "members": o.Members, "sources": locations}
}

func (s *Server) associationView(ctx context.Context, a storedAssociation, user int64) (map[string]any, error) {
	left, right := a.Snapshot.Left.Reviews[user], a.Snapshot.Right.Reviews[user]
	view := map[string]any{"id": a.ID, "status": a.Status, "version": a.Version, "left": publicAssociationObject(a.Snapshot.Left), "right": publicAssociationObject(a.Snapshot.Right), "myConflictFields": associationConflicts(left, right), "myLeftReview": map[string]any{"rating": left.Rating, "note": left.Note}, "myRightReview": map[string]any{"rating": right.Rating, "note": right.Note}, "myDecision": nil, "waitingForOwners": false}
	if user > 0 && a.Status != "stale" {
		var rating *int64
		var note string
		err := s.db.QueryRowContext(ctx, `SELECT rating,note FROM shared_association_decision WHERE operation_id=? AND user_id=?`, a.ID, user).Scan(&rating, &note)
		if err == nil {
			view["myDecision"] = map[string]any{"rating": rating, "note": note}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	view["waitingForOwners"] = a.Status == "pending"
	return view, nil
}

func (s *Server) getSharedAssociation(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeAssociationError(w, sql.ErrNoRows)
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAssociationError(w, sql.ErrNoRows)
		return
	}
	// Reading one stored association never writes, so a read transaction avoids
	// taking SQLite's single write lock.
	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, err)
		return
	}
	a, err := loadStoredAssociation(r.Context(), tx, id)
	if err == nil && (a.Status == "preview" || a.Status == "pending") {
		_, fingerprint, e := loadAssociationSnapshot(r.Context(), tx, a.Snapshot.Left.Subject, a.Snapshot.Right.Subject)
		if e != nil || fingerprint != a.Fingerprint || validateWorkMatchingTx(r.Context(), tx, a.ID) != nil {
			a.Status = "stale"
		}
	}
	_ = tx.Rollback()
	if err != nil {
		writeAssociationError(w, err)
		return
	}
	view, err := s.associationView(r.Context(), a, optionalUserID(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

// startSharedAssociationTx is the single submission boundary for manual and
// automatic association. Callers must authorize the shared operation first.
// It always revalidates the captured objects and private reviews under a write lock.
func startSharedAssociationTx(ctx context.Context, tx *sql.Tx, a *storedAssociation) error {
	if a.Status == "completed" {
		return nil
	}
	if a.Status != "preview" && a.Status != "pending" {
		return errAssociationStale
	}
	_, fingerprint, err := loadAssociationSnapshot(ctx, tx, a.Snapshot.Left.Subject, a.Snapshot.Right.Subject)
	if err != nil || fingerprint != a.Fingerprint {
		return errAssociationStale
	}
	a.Status = "pending"
	if _, err = tx.ExecContext(ctx, `UPDATE shared_association SET status='pending',updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.ID); err != nil {
		return err
	}
	return commitReadyAssociationTx(ctx, tx, a)
}

func (s *Server) startSharedAssociation(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:write")
	if !ok {
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if !decodeAssociationBody(w, r, &input) {
		return
	}
	s.mutateSharedAssociation(w, r, actor.ID, input.Version, func(ctx context.Context, tx *sql.Tx, a *storedAssociation) error {
		if !a.ActorID.Valid || a.ActorID.Int64 != actor.ID {
			return errAssociationStale
		}
		return startSharedAssociationTx(ctx, tx, a)
	})
}

func (s *Server) decideSharedAssociation(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:read")
	if !ok {
		return
	}
	var input associationDecisionInput
	if !decodeAssociationBody(w, r, &input) {
		return
	}
	patch := workUserStatePatch{Rating: input.Rating, Note: input.Note}
	if patch.validate() != nil {
		writeAPIError(w, 400, "invalid_request", "invalid review decision", false)
		return
	}
	s.mutateSharedAssociation(w, r, actor.ID, input.Version, func(ctx context.Context, tx *sql.Tx, a *storedAssociation) error {
		if a.Status != "pending" {
			return errAssociationStale
		}
		left, right := a.Snapshot.Left.Reviews[actor.ID], a.Snapshot.Right.Reviews[actor.ID]
		fields := associationConflicts(left, right)
		if len(fields) == 0 {
			return errAssociationNotOwner
		}
		for _, field := range fields {
			if field == "rating" && len(input.Rating) == 0 || field == "note" && len(input.Note) == 0 {
				return errAssociationInvalid
			}
		}
		_, fingerprint, err := loadAssociationSnapshot(ctx, tx, a.Snapshot.Left.Subject, a.Snapshot.Right.Subject)
		if err != nil || fingerprint != a.Fingerprint {
			return errAssociationStale
		}
		merged := mergeAssociationReview(left, right)
		if len(input.Rating) > 0 {
			_ = json.Unmarshal(input.Rating, &merged.Rating)
		}
		if len(input.Note) > 0 {
			_ = json.Unmarshal(input.Note, &merged.Note)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO shared_association_decision(operation_id,user_id,rating,note) VALUES(?,?,?,?) ON CONFLICT(operation_id,user_id) DO UPDATE SET rating=excluded.rating,note=excluded.note`, a.ID, actor.ID, merged.Rating, merged.Note); err != nil {
			return err
		}
		return commitReadyAssociationTx(ctx, tx, a)
	})
}

var errAssociationNotOwner = errors.New("only the record owner can decide")

func (s *Server) mutateSharedAssociation(w http.ResponseWriter, r *http.Request, user int64, version string, change func(context.Context, *sql.Tx, *storedAssociation) error) {
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAssociationError(w, sql.ErrNoRows)
		return
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(r.Context(), s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	if _, err = tx.ExecContext(r.Context(), `UPDATE shared_association SET id=id WHERE id=?`, id); err != nil {
		writeError(w, err)
		return
	}
	a, err := loadStoredAssociation(r.Context(), tx.Tx, id)
	if err == nil && a.Version != version {
		err = errAssociationStale
	}
	if err == nil {
		err = change(r.Context(), tx.Tx, &a)
	}
	if errors.Is(err, errAssociationNotOwner) {
		writeAPIError(w, 403, "forbidden", "only the record owner can decide", false)
		return
	}
	if err != nil {
		writeAssociationError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	view, err := s.associationView(r.Context(), a, user)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

func mergeAssociationReview(left, right associationReview) associationReview {
	if left.Rating == nil {
		left.Rating = right.Rating
	}
	if left.Note == "" {
		left.Note = right.Note
	}
	return left
}

func registerAssociationObject(ctx context.Context, tx *sql.Tx, o *associationObject) error {
	if o.WorkID != nil {
		return nil
	}
	var id int64
	if o.Code != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work(primary_code,title) VALUES(?,?) ON CONFLICT(primary_code) WHERE primary_code<>'' DO NOTHING`, o.Code, o.Title); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM work WHERE primary_code=?`, o.Code).Scan(&id); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRowContext(ctx, `INSERT INTO work(primary_code,title) VALUES('',?) RETURNING id`, o.Title).Scan(&id); err != nil {
			return err
		}
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_edition WHERE work_id=?)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		var logical int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO logical_work(canonical_work_id,canonical_code) VALUES(?,?) RETURNING id`, id, o.Code).Scan(&logical); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(?,?,?,1)`, id, logical, o.Code); err != nil {
			return err
		}
	}
	switch o.Subject.Kind {
	case "catalog":
		if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET work_id=? WHERE id=?`, id, o.Subject.ID); err != nil {
			return err
		}
	case "series":
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_series SET work_id=? WHERE id=?`, id, o.Subject.ID); err != nil {
			return err
		}
	}
	o.WorkID = &id
	o.IdentityWorkID = id
	o.Members = []int64{id}
	return nil
}

func commitReadyAssociationTx(ctx context.Context, tx *sql.Tx, a *storedAssociation) error {
	if err := validateWorkMatchingTx(ctx, tx, a.ID); err != nil {
		return err
	}
	users := associationUsers(a.Snapshot)
	merged := map[int64]associationReview{}
	for _, user := range users {
		left, right := a.Snapshot.Left.Reviews[user], a.Snapshot.Right.Reviews[user]
		value := mergeAssociationReview(left, right)
		if len(associationConflicts(left, right)) > 0 {
			err := tx.QueryRowContext(ctx, `SELECT rating,note FROM shared_association_decision WHERE operation_id=? AND user_id=?`, a.ID, user).Scan(&value.Rating, &value.Note)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		merged[user] = value
	}
	left, right := a.Snapshot.Left, a.Snapshot.Right
	if err := registerAssociationObject(ctx, tx, &left); err != nil {
		return err
	}
	if err := registerAssociationObject(ctx, tx, &right); err != nil {
		return err
	}
	if left.IdentityWorkID == right.IdentityWorkID {
		return errAssociationStale
	}
	members := append(append([]int64{}, left.Members...), right.Members...)
	// Resolve every existing group while separate, then publish the complete mapping.
	for _, id := range members {
		for _, user := range users {
			v := merged[user]
			if _, err := tx.ExecContext(ctx, `INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(?,?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET rating=excluded.rating,note=excluded.note,updated_at=CURRENT_TIMESTAMP`, user, id, v.Rating, v.Note); err != nil {
				return err
			}
		}
	}
	for _, id := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO shared_identity_member(work_id,identity_work_id,operation_id) VALUES(?,?,?) ON CONFLICT(work_id) DO UPDATE SET identity_work_id=excluded.identity_work_id,operation_id=excluded.operation_id`, id, left.IdentityWorkID, a.ID); err != nil {
			return err
		}
	}
	applied, err := loadAssociationObject(ctx, tx, associationSubject{Kind: "work", ID: left.IdentityWorkID})
	if err != nil {
		return err
	}
	result := associationResult{associationSnapshot: associationSnapshot{Left: left, Right: right}, AppliedReviews: applied.Reviews}
	raw, _ := json.Marshal(result)
	_, err = tx.ExecContext(ctx, `UPDATE shared_association SET status='completed',result_json=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(raw), a.ID)
	if err == nil {
		a.Status = "completed"
	}
	return err
}

func (s *Server) listSharedAssociations(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeJSON(w, 200, []any{})
		return
	}
	kind := r.URL.Query().Get("kind")
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 || (kind != "work" && kind != "catalog" && kind != "series") {
		writeAPIError(w, 400, "invalid_request", "invalid object", false)
		return
	}
	// Listing reads only. A read transaction avoids taking SQLite's single
	// write lock for a request that never writes.
	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	object, err := loadAssociationObject(r.Context(), tx, associationSubject{Kind: kind, ID: id})
	if err != nil {
		writeAssociationError(w, err)
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,status FROM shared_association WHERE operation='link' AND (status<>'preview' OR actor_id=?) AND ((json_extract(snapshot_json,'$.left.subject.kind')=? AND json_extract(snapshot_json,'$.left.subject.id')=?) OR (json_extract(snapshot_json,'$.right.subject.kind')=? AND json_extract(snapshot_json,'$.right.subject.id')=?)
	 OR EXISTS(SELECT 1 FROM json_each(snapshot_json,'$.left.members') member WHERE member.value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?))
	 OR EXISTS(SELECT 1 FROM json_each(snapshot_json,'$.right.members') member WHERE member.value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?))
	 OR EXISTS(SELECT 1 FROM json_each(result_json,'$.left.members') member WHERE member.value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?))
	 OR EXISTS(SELECT 1 FROM json_each(result_json,'$.right.members') member WHERE member.value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?))) ORDER BY id DESC LIMIT 32`, optionalUserID(r.Context()), kind, id, kind, id, object.IdentityWorkID, object.IdentityWorkID, object.IdentityWorkID, object.IdentityWorkID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := []map[string]any{}
	for rows.Next() {
		var op int64
		var status string
		if err = rows.Scan(&op, &status); err != nil {
			writeError(w, err)
			return
		}
		items = append(items, map[string]any{"id": op, "status": status})
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
