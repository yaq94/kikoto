package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type workMatchingOptions struct {
	IntervalMS     int   `json:"intervalMs"`
	CandidateLimit int   `json:"candidateLimit"`
	MinFreeBytes   int64 `json:"minFreeBytes"`
}

func readWorkMatchingOptions(ctx context.Context, q catalogEntryQueryer) (workMatchingOptions, error) {
	o := workMatchingOptions{IntervalMS: 1000, CandidateLimit: 100, MinFreeBytes: 2 << 30}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value_json FROM app_setting WHERE key='work_matching_options'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return o, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &o)
	}
	return o, err
}

func (s *Server) workMatchingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	o, err := readWorkMatchingOptions(r.Context(), s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	if r.Method == http.MethodPut {
		if !decodeAssociationBody(w, r, &o) {
			return
		}
		if o.IntervalMS < 100 || o.IntervalMS > 60000 || o.CandidateLimit < 1 || o.CandidateLimit > 1000 || o.MinFreeBytes < 0 || o.MinFreeBytes > 1<<40 {
			writeAPIError(w, 400, "invalid_request", "invalid matching resource limits", false)
			return
		}
		raw, marshalErr := json.Marshal(o)
		if marshalErr != nil {
			writeError(w, marshalErr)
			return
		}
		tx, release, e := beginTxWithDatabaseBusyRetry(r.Context(), s.db)
		if e != nil {
			writeError(w, e)
			return
		}
		defer release()
		_, e = tx.ExecContext(r.Context(), `INSERT INTO app_setting(key,value_json) VALUES('work_matching_options',?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, string(raw))
		if e == nil {
			_, e = tx.ExecContext(r.Context(), `UPDATE work_matching_control SET revision=revision+1,status='queued',retry_after=0,attempts=0`)
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			writeError(w, e)
			return
		}
	}
	writeJSON(w, 200, o)
}

func (s *Server) workMatchingStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	var status, reason, kind string
	var revision, scanRevision, cursor, retry int64
	err := s.db.QueryRowContext(r.Context(), `SELECT status,error_code,revision,scan_revision,cursor_kind,cursor_id,retry_after FROM work_matching_control WHERE id=1`).Scan(&status, &reason, &revision, &scanRevision, &kind, &cursor, &retry)
	if err != nil {
		writeError(w, err)
		return
	}
	var remaining int
	err = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM work_matching_subject WHERE ?>? OR kind>? OR (kind=? AND id>?)`, revision, scanRevision, kind, kind, cursor).Scan(&remaining)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": status, "reason": reason, "remaining": remaining, "retryAfter": retry})
}

// One durable cursor covers all admitted identities. Each transaction processes
// one subject, then releases SQLite and yields; no request or catalog is dropped
// from the remaining range when a batch or process ends.
func (s *Server) RunWorkMatchingWorker(ctx context.Context) {
	if s.cfg.IsDemo() {
		return
	}
	for ctx.Err() == nil {
		o, err := readWorkMatchingOptions(ctx, s.db)
		if err == nil {
			err = s.advanceWorkMatching(ctx, o)
		}
		delay := time.Second
		if err == nil {
			delay = time.Duration(o.IntervalMS) * time.Millisecond
		}
		if err != nil {
			delay = 30 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Server) advanceWorkMatching(ctx context.Context, o workMatchingOptions) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if reason := s.catalogIndexDiskPause(catalogIndexOptions{MinFreeBytes: o.MinFreeBytes, MaxResponseBytes: 64 << 10}); reason != "" {
		_, err := s.db.ExecContext(ctx, `UPDATE work_matching_control SET status='paused',error_code=?`, reason)
		return err
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if _, err = tx.ExecContext(ctx, `UPDATE work_matching_control SET id=id WHERE id=1`); err != nil {
		return err
	}
	var revision, scanRevision, cursor, retry int64
	var kind, status string
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT revision,scan_revision,cursor_kind,cursor_id,retry_after,status,attempts FROM work_matching_control WHERE id=1`).Scan(&revision, &scanRevision, &kind, &cursor, &retry, &status, &attempts)
	if err != nil {
		return err
	}
	if retry > s.catalogTime().Unix() {
		return nil
	}
	if revision != scanRevision {
		kind = ""
		cursor = 0
		attempts = 0
	}
	if revision == scanRevision && status == "complete" {
		return nil
	}
	if revision == scanRevision && status == "partial" {
		kind = ""
		cursor = 0
	}
	var subject associationSubject
	err = tx.QueryRowContext(ctx, `SELECT kind,id FROM work_matching_subject WHERE kind>? OR (kind=? AND id>?) ORDER BY kind,id LIMIT 1`, kind, kind, cursor).Scan(&subject.Kind, &subject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `UPDATE work_matching_control SET status=CASE WHEN EXISTS(SELECT 1 FROM work_matching_result WHERE json_extract(result_json,'$.status') IN ('paused','failed')) THEN 'partial' ELSE 'complete' END,scan_revision=revision,error_code='',retry_after=?`, s.catalogTime().Add(time.Minute).Unix())
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_matching_control SET status='running',scan_revision=?,cursor_kind=?,cursor_id=?`, revision, kind, cursor); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	result, err := s.matchWork(ctx, subject, nil)
	if errors.Is(err, errWorkMatchingBusy) {
		return nil
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && attempts < 2 {
		_, saveErr := s.db.ExecContext(ctx, `UPDATE work_matching_control SET status='retrying',attempts=?,retry_after=?,error_code='matching_failed' WHERE revision=?`, attempts+1, s.catalogTime().Add(time.Duration(30*(1<<attempts))*time.Second).Unix(), revision)
		return saveErr
	}
	if err != nil {
		raw := `{"status":"failed","rule":"matching_failed","candidates":[],"candidateCount":0}`
		if _, saveErr := s.db.ExecContext(ctx, `INSERT INTO work_matching_result(kind,entity_id,result_json,revision) VALUES(?,?,?,?) ON CONFLICT(kind,entity_id) DO UPDATE SET result_json=excluded.result_json,revision=excluded.revision`, subject.Kind, subject.ID, raw, revision); saveErr != nil {
			return saveErr
		}
		result.Revision = revision
	}
	if result.Revision != revision {
		subject = associationSubject{}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE work_matching_control SET cursor_kind=?,cursor_id=?,scan_revision=?,attempts=0,retry_after=0,error_code='' WHERE revision=?`, subject.Kind, subject.ID, result.Revision, result.Revision)
	return err
}
