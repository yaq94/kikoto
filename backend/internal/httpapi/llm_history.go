package httpapi

import (
	"database/sql"
	"net/http"
)

func (s *Server) listLLMTranslations(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeAPIError(w, 403, "llm_disabled", "LLM translation is unavailable in Demo.", false)
		return
	}
	if _, ok := s.requirePermission(w, r, "library:write"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_work_id", "Invalid work id.", false)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT language,field_name,value_text,status FROM work_llm_translation WHERE work_id=? ORDER BY language,field_name", id)
	if err != nil {
		writeError(w, err)
		return
	}
	current := []map[string]any{}
	for rows.Next() {
		var language, field, value, status string
		if err := rows.Scan(&language, &field, &value, &status); err != nil {
			_ = rows.Close()
			writeError(w, err)
			return
		}
		current = append(current, map[string]any{"language": language, "field": field, "value": value, "status": status})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		writeError(w, err)
		return
	}
	_ = rows.Close()
	rows, err = s.db.QueryContext(r.Context(), "SELECT id,language,field_name,action,created_at FROM work_llm_translation_review WHERE work_id=? ORDER BY id DESC LIMIT 64", id)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	history := []map[string]any{}
	for rows.Next() {
		var reviewID int64
		var language, field, action, created string
		if err := rows.Scan(&reviewID, &language, &field, &action, &created); err != nil {
			writeError(w, err)
			return
		}
		history = append(history, map[string]any{"id": reviewID, "language": language, "field": field, "action": action, "createdAt": created})
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"current": current, "history": history})
}
func (s *Server) restoreLLMTranslation(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermissions(w, r, "library:write", "metadata:sync")
	if !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_work_id", "Invalid work id.", false)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var language, field, value, hash, provider, model string
	err = tx.QueryRowContext(r.Context(), "SELECT language,field_name,value_text,source_hash,provider_id,model FROM work_llm_translation_review WHERE id=? AND work_id=?", r.PathValue("review"), id).Scan(&language, &field, &value, &hash, &provider, &model)
	if err == sql.ErrNoRows {
		writeAPIError(w, 404, "llm_review_not_found", "Translation review not found.", false)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := loadLLMWorkInput(r.Context(), tx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if llmInputHash(input) != hash {
		writeAPIError(w, 409, "work_changed", "Source metadata changed. Translate again.", false)
		return
	}
	if err := archiveLLMTranslation(r.Context(), tx, id, language, field, actor.ID, "superseded"); err != nil {
		writeError(w, err)
		return
	}
	source := input.Text.Title
	if field == "description" {
		source = input.Text.Description
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO work_llm_translation(work_id,language,field_name,value_text,source_hash,provider_id,model,status,created_by_user_id,source_text,source_work_id)
 VALUES(?,?,?,?,?,?,?,'accepted',?,?,?) ON CONFLICT(work_id,language,field_name) DO UPDATE SET value_text=excluded.value_text,source_hash=excluded.source_hash,provider_id=excluded.provider_id,model=excluded.model,status='accepted',created_by_user_id=excluded.created_by_user_id,source_text=excluded.source_text,source_work_id=excluded.source_work_id,updated_at=CURRENT_TIMESTAMP`, id, language, field, value, hash, provider, model, actor.ID, source, input.SourceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := archiveLLMTranslation(r.Context(), tx, id, language, field, actor.ID, "restored"); err != nil {
		writeError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
