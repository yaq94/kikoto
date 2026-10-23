package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
)

type splitSnapshot struct {
	AssociationID int64               `json:"associationId"`
	Before        associationSnapshot `json:"before"`
	Applied       associationResult   `json:"applied"`
	Current       associationObject   `json:"current"`
}
type storedSplit struct {
	ID                           int64
	ActorID                      sql.NullInt64
	Status, Version, Fingerprint string
	Snapshot                     splitSnapshot
}

func loadSplitSnapshot(ctx context.Context, tx *sql.Tx, associationID int64) (splitSnapshot, string, error) {
	snapshot := splitSnapshot{AssociationID: associationID}
	var status, before, result string
	err := tx.QueryRowContext(ctx, `SELECT status,snapshot_json,result_json FROM shared_association WHERE id=? AND operation='link'`, associationID).Scan(&status, &before, &result)
	if err != nil {
		return snapshot, "", err
	}
	if status != "completed" {
		return snapshot, "", errAssociationStale
	}
	if err = json.Unmarshal([]byte(before), &snapshot.Before); err != nil {
		return snapshot, "", err
	}
	if err = json.Unmarshal([]byte(result), &snapshot.Applied); err != nil {
		return snapshot, "", err
	}
	snapshot.Current, err = loadAssociationObject(ctx, tx, associationSubject{Kind: "work", ID: snapshot.Applied.Left.IdentityWorkID})
	if err != nil {
		return snapshot, "", err
	}
	members := append(append([]int64{}, snapshot.Applied.Left.Members...), snapshot.Applied.Right.Members...)
	slices.Sort(members)
	if !slices.Equal(members, snapshot.Current.Members) {
		return snapshot, "", errAssociationStale
	}
	for _, id := range snapshot.Current.OperationIDs {
		if id != associationID {
			return snapshot, "", errAssociationStale
		}
	}
	fingerprint, err := associationFingerprint(snapshot)
	if err != nil {
		return snapshot, "", err
	}
	return snapshot, fingerprint, nil
}

func loadStoredSplit(ctx context.Context, tx *sql.Tx, id int64) (storedSplit, error) {
	s := storedSplit{ID: id}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT actor_id,status,version,fingerprint,snapshot_json FROM shared_association WHERE id=? AND operation='split'`, id).Scan(&s.ActorID, &s.Status, &s.Version, &s.Fingerprint, &raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &s.Snapshot)
	}
	return s, err
}

func splitRecordChange(snapshot splitSnapshot, user int64) string {
	current, hasCurrent := snapshot.Current.Reviews[user]
	applied, hadApplied := snapshot.Applied.AppliedReviews[user]
	if !hadApplied && hasCurrent {
		return "added"
	}
	currentPrint, currentErr := associationFingerprint(current)
	appliedPrint, appliedErr := associationFingerprint(applied)
	if currentErr != nil || appliedErr != nil {
		// A record that cannot be serialized is treated as changed: keeping it
		// visible for the owner is safer than silently calling it unchanged.
		return "modified"
	}
	if hasCurrent != hadApplied || currentPrint != appliedPrint {
		return "modified"
	}
	return "unchanged"
}

func splitUsers(snapshot splitSnapshot) []int64 {
	seen := map[int64]bool{}
	for _, id := range associationUsers(snapshot.Before) {
		seen[id] = true
	}
	for id := range snapshot.Current.Reviews {
		seen[id] = true
	}
	for id := range snapshot.Applied.AppliedReviews {
		seen[id] = true
	}
	users := make([]int64, 0, len(seen))
	for id := range seen {
		users = append(users, id)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	return users
}

func (s *Server) splitView(ctx context.Context, split storedSplit, user int64) (map[string]any, error) {
	beforeLeft, beforeRight, current := split.Snapshot.Before.Left.Reviews[user], split.Snapshot.Before.Right.Reviews[user], split.Snapshot.Current.Reviews[user]
	publicReview := func(value associationReview) map[string]any {
		return map[string]any{"rating": value.Rating, "note": value.Note}
	}
	view := map[string]any{"id": split.ID, "associationId": split.Snapshot.AssociationID, "status": split.Status, "version": split.Version,
		"left": publicAssociationObject(split.Snapshot.Applied.Left), "right": publicAssociationObject(split.Snapshot.Applied.Right),
		"myBeforeLeft": publicReview(beforeLeft), "myBeforeRight": publicReview(beforeRight), "myCurrentReview": publicReview(current),
		"myRecordChange": splitRecordChange(split.Snapshot, user), "myAllocation": ""}
	if user > 0 && split.Status != "stale" {
		var allocation string
		err := s.db.QueryRowContext(ctx, `SELECT allocation FROM shared_association_decision WHERE operation_id=? AND user_id=?`, split.ID, user).Scan(&allocation)
		if err == nil {
			view["myAllocation"] = allocation
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return view, nil
}

func (s *Server) createSharedSplitPreview(w http.ResponseWriter, r *http.Request) {
	// Splitting a shared association is a data-editor operation, like creating
	// the association it reverses. Affected owners only choose their records.
	actor, ok := s.requirePermission(w, r, "library:write")
	if !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAssociationError(w, sql.ErrNoRows)
		return
	}
	var body struct{}
	if !decodeAssociationBody(w, r, &body) {
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
	snapshot, fingerprint, err := loadSplitSnapshot(r.Context(), tx.Tx, id)
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
	split := storedSplit{ActorID: sql.NullInt64{Int64: actor.ID, Valid: true}, Status: "preview", Version: hex.EncodeToString(nonce[:]), Fingerprint: fingerprint, Snapshot: snapshot}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		writeError(w, err)
		return
	}
	err = tx.QueryRowContext(r.Context(), `INSERT INTO shared_association(actor_id,operation,split_of,version,fingerprint,snapshot_json) VALUES(?,'split',?,?,?,?) RETURNING id`, actor.ID, id, split.Version, fingerprint, string(raw)).Scan(&split.ID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	view, err := s.splitView(r.Context(), split, actor.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, view)
}

func (s *Server) getSharedSplit(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeAssociationError(w, sql.ErrNoRows)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAssociationError(w, sql.ErrNoRows)
		return
	}
	// An immediate transaction takes SQLite's write lock even for this read,
	// so the GET must declare itself read-only and leave writers unblocked.
	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, err)
		return
	}
	split, err := loadStoredSplit(r.Context(), tx, id)
	if err == nil && (split.Status == "preview" || split.Status == "pending") {
		_, fingerprint, e := loadSplitSnapshot(r.Context(), tx, split.Snapshot.AssociationID)
		if e != nil || fingerprint != split.Fingerprint {
			split.Status = "stale"
		}
	}
	_ = tx.Rollback()
	if err != nil {
		writeAssociationError(w, err)
		return
	}
	view, err := s.splitView(r.Context(), split, optionalUserID(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

func (s *Server) mutateSharedSplit(w http.ResponseWriter, r *http.Request, user int64, version string, change func(context.Context, *sql.Tx, *storedSplit) error) {
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
	split, err := loadStoredSplit(r.Context(), tx.Tx, id)
	if err == nil && split.Version != version {
		err = errAssociationStale
	}
	if err == nil && split.Status != "completed" {
		_, fingerprint, e := loadSplitSnapshot(r.Context(), tx.Tx, split.Snapshot.AssociationID)
		if e != nil || fingerprint != split.Fingerprint {
			err = errAssociationStale
		}
	}
	if err == nil {
		err = change(r.Context(), tx.Tx, &split)
	}
	if errors.Is(err, errAssociationNotOwner) {
		writeAPIError(w, 403, "forbidden", "only the record owner can choose allocation", false)
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
	view, err := s.splitView(r.Context(), split, user)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

func (s *Server) startSharedSplit(w http.ResponseWriter, r *http.Request) {
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
	s.mutateSharedSplit(w, r, actor.ID, input.Version, func(ctx context.Context, tx *sql.Tx, split *storedSplit) error {
		if !split.ActorID.Valid || split.ActorID.Int64 != actor.ID {
			return errAssociationNotOwner
		}
		if split.Status == "completed" {
			return nil
		}
		if split.Status != "preview" && split.Status != "pending" {
			return errAssociationStale
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shared_association SET status='pending' WHERE id=?`, split.ID); err != nil {
			return err
		}
		split.Status = "pending"
		return commitReadySplitTx(ctx, tx, split)
	})
}

func (s *Server) decideSharedSplit(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:read")
	if !ok {
		return
	}
	var input struct {
		Version    string `json:"version"`
		Allocation string `json:"allocation"`
	}
	if !decodeAssociationBody(w, r, &input) {
		return
	}
	if input.Allocation != "left" && input.Allocation != "right" && input.Allocation != "both" {
		writeAPIError(w, 400, "invalid_request", "choose left, right or both", false)
		return
	}
	s.mutateSharedSplit(w, r, actor.ID, input.Version, func(ctx context.Context, tx *sql.Tx, split *storedSplit) error {
		if split.Status != "pending" {
			return errAssociationStale
		}
		if splitRecordChange(split.Snapshot, actor.ID) == "unchanged" {
			return errAssociationNotOwner
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO shared_association_decision(operation_id,user_id,allocation) VALUES(?,?,?) ON CONFLICT(operation_id,user_id) DO UPDATE SET allocation=excluded.allocation`, split.ID, actor.ID, input.Allocation); err != nil {
			return err
		}
		return commitReadySplitTx(ctx, tx, split)
	})
}

func commitReadySplitTx(ctx context.Context, tx *sql.Tx, split *storedSplit) error {
	users := splitUsers(split.Snapshot)
	allocations := map[int64]string{}
	for _, user := range users {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_account WHERE id=?)`, user).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		allocation := "unchanged"
		if splitRecordChange(split.Snapshot, user) != "unchanged" {
			err := tx.QueryRowContext(ctx, `SELECT allocation FROM shared_association_decision WHERE operation_id=? AND user_id=?`, split.ID, user).Scan(&allocation)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		allocations[user] = allocation
	}
	// Remove the active association before restoring review ownership, so the
	// compatibility triggers cannot copy a selected side into the other side.
	for _, id := range split.Snapshot.Current.Members {
		if _, err := tx.ExecContext(ctx, `DELETE FROM shared_identity_member WHERE work_id=? AND operation_id=?`, id, split.Snapshot.AssociationID); err != nil {
			return err
		}
	}
	for side, object := range []associationObject{split.Snapshot.Applied.Left, split.Snapshot.Applied.Right} {
		for index, id := range object.Members {
			if index < len(object.OperationIDs) && object.OperationIDs[index] > 0 {
				if _, err := tx.ExecContext(ctx, `INSERT INTO shared_identity_member(work_id,identity_work_id,operation_id) VALUES(?,?,?)`, id, object.IdentityWorkID, object.OperationIDs[index]); err != nil {
					return err
				}
			}
		}
		for _, id := range object.Members {
			for user, allocation := range allocations {
				var review associationReview
				if side == 0 {
					review = split.Snapshot.Before.Left.Reviews[user]
				} else {
					review = split.Snapshot.Before.Right.Reviews[user]
				}
				if allocation == "both" || allocation == "left" && side == 0 || allocation == "right" && side == 1 {
					review = split.Snapshot.Current.Reviews[user]
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(?,?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET rating=excluded.rating,note=excluded.note,updated_at=CURRENT_TIMESTAMP`, user, id, review.Rating, review.Note); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shared_association SET status='undone',updated_at=CURRENT_TIMESTAMP WHERE id=?`, split.Snapshot.AssociationID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE shared_association SET status='completed',updated_at=CURRENT_TIMESTAMP WHERE id=?`, split.ID)
	if err == nil {
		split.Status = "completed"
	}
	return err
}

func (s *Server) listSharedSplits(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeJSON(w, 200, []any{})
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAssociationError(w, sql.ErrNoRows)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,status FROM shared_association WHERE operation='split' AND split_of=? AND (status<>'preview' OR actor_id=?) ORDER BY id DESC LIMIT 32`, id, optionalUserID(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var status string
		if err = rows.Scan(&id, &status); err != nil {
			writeError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "status": status})
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
