package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/llm"
	"github.com/yexca/kikoto/backend/internal/metadatatitles"
)

type llmTranslatePayload struct {
	ProviderID string   `json:"providerId"`
	Target     string   `json:"targetLanguage"`
	Fields     []string `json:"fields"`
}

type llmWorkInput struct {
	Text     llm.Text
	Age      string
	Tags     []int64
	TagNames string
}

type llmPreview struct {
	WorkID   int64
	UserID   int64
	Expires  time.Time
	Hash     string
	Input    llmWorkInput
	Fields   []string
	Result   llm.Text
	Provider llm.Provider
	Target   string
}

type llmInputQuerier interface {
	metadatatitles.Querier
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// The original edition supplies translation input regardless of the viewer's
// display language. Tag bases retain hidden and removed risk evidence.
func loadLLMWorkInput(ctx context.Context, q llmInputQuerier, workID int64) (llmWorkInput, error) {
	inputs, err := metadatatitles.LoadInputs(ctx, q, []int64{workID}, true)
	if err != nil {
		return llmWorkInput{}, err
	}
	input, ok := inputs[workID]
	if !ok {
		return llmWorkInput{}, sql.ErrNoRows
	}
	origin := metadatatitles.Original(input.Variants, input.Fallback)
	result := llmWorkInput{Text: llm.Text{Title: origin.Title, Description: origin.Description}, Tags: []int64{}}
	var sourceID int64
	if err := q.QueryRowContext(ctx, "SELECT id,age_rating FROM work WHERE primary_code=?", origin.Code).Scan(&sourceID, &result.Age); err != nil {
		return result, err
	}
	rows, err := q.QueryContext(ctx, `WITH risk_tags AS (
 SELECT tag_id FROM work_metadata_tag_base WHERE work_id IN (?,?)
 UNION SELECT tag_id FROM work_tag WHERE work_id IN (?,?)
 UNION SELECT tag_id FROM work_tag_override WHERE work_id IN (?,?) AND action='add'
 ), all_tags AS (
 SELECT tag_id FROM risk_tags UNION SELECT resolution.resolved_tag_id FROM risk_tags JOIN metadata_tag_resolution AS resolution ON resolution.source_tag_id=risk_tags.tag_id
 ) SELECT tag.id,tag.display_name,
 COALESCE((SELECT GROUP_CONCAT(name,' ') FROM metadata_tag_name WHERE tag_id=tag.id),'') || ' ' ||
 COALESCE((SELECT GROUP_CONCAT(name,' ') FROM metadata_tag_provider_name WHERE tag_id=tag.id),'')
 FROM all_tags JOIN tag ON tag.id=all_tags.tag_id ORDER BY tag.id`, workID, sourceID, workID, sourceID, workID, sourceID)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var name, aliases string
		if err := rows.Scan(&id, &name, &aliases); err != nil {
			return result, err
		}
		result.Tags = append(result.Tags, id)
		result.TagNames += name + " " + aliases + "\n"
	}
	return result, rows.Err()
}

func llmInputHash(input llmWorkInput) string {
	data, _ := json.Marshal(input)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func llmFields(input llmWorkInput, fields []string) (llm.Text, map[string]string) {
	text := llm.Text{}
	scan := map[string]string{"tags": input.TagNames}
	for _, field := range fields {
		switch field {
		case "title":
			text.Title = input.Text.Title
			scan[field] = text.Title
		case "description":
			text.Description = input.Text.Description
			scan[field] = text.Description
		}
	}
	return text, scan
}
func llmAllowed(class, boundary string) bool {
	return class == "safe" || class == "sensitive" && boundary == "private"
}

func (s *Server) translateWorkWithLLM(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "metadata:sync")
	if !ok {
		return
	}
	if _, ok := s.requirePermission(w, r, "library:write"); !ok {
		return
	}
	workID, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_work_id", "Invalid work id.", false)
		return
	}
	var payload llmTranslatePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&payload); err != nil {
		writeAPIError(w, 400, "invalid_translation_request", "Invalid translation request.", false)
		return
	}
	if payload.Target == "" || !validTitleLanguage(payload.Target) {
		writeAPIError(w, 400, "invalid_translation_request", "Choose a supported target language.", false)
		return
	}
	fields := []string{}
	seen := map[string]bool{}
	for _, field := range payload.Fields {
		if field != "title" && field != "description" {
			writeAPIError(w, 400, "invalid_translation_request", "Choose title or description.", false)
			return
		}
		if !seen[field] {
			fields = append(fields, field)
			seen[field] = true
		}
	}
	if len(fields) == 0 {
		writeAPIError(w, 400, "invalid_translation_request", "Choose at least one field.", false)
		return
	}
	if !s.acquireLLMSlot(w, r) {
		return
	}
	defer func() { <-s.llmRequestSlots }()
	provider, err := s.llmProvider(r.Context(), payload.ProviderID)
	if err != nil || !provider.Enabled || provider.DefaultModel == "" {
		writeAPIError(w, 422, "llm_not_ready", "Choose an enabled provider and model.", false)
		return
	}
	input, err := loadLLMWorkInput(r.Context(), s.db, workID)
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, 404, "work_not_found", "Work not found.", false)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if len(input.Text.Title) > 4096 || len(input.Text.Description) > 64<<10 || len(input.TagNames) > 64<<10 {
		writeAPIError(w, 422, "llm_input_too_large", "Translation input exceeds the text limit.", false)
		return
	}
	text, scan := llmFields(input, fields)
	if text.Title == "" && text.Description == "" {
		writeAPIError(w, 422, "llm_empty_input", "Selected fields have no text.", false)
		return
	}
	policy, err := s.llmStore.Policy(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	assessment := policy.Assess(input.Age, scan, input.Tags)
	if !llmAllowed(assessment.Class, provider.Boundary) {
		writeAPIError(w, 422, "llm_policy_blocked", "Content policy blocks this translation. Use a private model for sensitive content; unknown or blocked content cannot be translated.", false)
		return
	}
	client, closeClient, err := s.llmClient(r.Context(), provider, 60*time.Second)
	if err != nil {
		writeAPIError(w, 502, "llm_unavailable", "The LLM endpoint is unavailable.", false)
		return
	}
	defer closeClient()
	result, err := client.Translate(r.Context(), provider.DefaultModel, payload.Target, text)
	if err != nil {
		writeAPIError(w, 502, "llm_translation_failed", "Translation failed. Check the configured endpoint and model.", false)
		return
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		writeError(w, err)
		return
	}
	id := hex.EncodeToString(token[:])
	s.llmPreviewMu.Lock()
	if s.llmPreviews == nil {
		s.llmPreviews = map[string]llmPreview{}
	}
	for key, p := range s.llmPreviews {
		if time.Now().After(p.Expires) {
			delete(s.llmPreviews, key)
		}
	}
	if len(s.llmPreviews) >= 64 {
		s.llmPreviewMu.Unlock()
		writeAPIError(w, 429, "llm_preview_limit", "Too many translation previews. Try again later.", false)
		return
	}
	s.llmPreviews[id] = llmPreview{WorkID: workID, UserID: actor.ID, Expires: time.Now().Add(15 * time.Minute), Hash: llmInputHash(input), Input: input, Fields: fields, Result: result, Provider: provider, Target: payload.Target}
	s.llmPreviewMu.Unlock()
	writeJSON(w, 200, map[string]any{"previewId": id, "providerId": provider.ID, "providerName": provider.Name, "model": provider.DefaultModel, "targetLanguage": payload.Target, "assessment": assessment, "source": text, "translation": result})
}

func (s *Server) applyLLMTranslation(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:write")
	if !ok {
		return
	}
	workID, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_work_id", "Invalid work id.", false)
		return
	}
	var payload struct {
		PreviewID string `json:"previewId"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&payload) != nil {
		writeAPIError(w, 400, "invalid_translation", "Invalid translation preview.", false)
		return
	}
	s.llmPreviewMu.Lock()
	defer s.llmPreviewMu.Unlock()
	preview, ok := s.llmPreviews[payload.PreviewID]
	if !ok || preview.UserID != actor.ID || preview.WorkID != workID || time.Now().After(preview.Expires) {
		writeAPIError(w, 404, "llm_preview_expired", "Translation preview expired. Translate again.", false)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	current, err := loadLLMWorkInput(r.Context(), tx, workID)
	if err != nil {
		writeError(w, err)
		return
	}
	if llmInputHash(current) != preview.Hash {
		writeAPIError(w, 409, "work_changed", "Work metadata changed. Translate again.", false)
		return
	}
	for field, value := range map[string]string{"title": preview.Result.Title, "description": preview.Result.Description} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO work_llm_translation(work_id,language,field_name,value_text,source_hash,provider_id,model,created_by_user_id)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(work_id,language,field_name) DO UPDATE SET value_text=excluded.value_text,source_hash=excluded.source_hash,provider_id=excluded.provider_id,model=excluded.model,status='accepted',created_by_user_id=excluded.created_by_user_id,updated_at=CURRENT_TIMESTAMP`, workID, preview.Target, field, value, preview.Hash, preview.Provider.ID, preview.Provider.DefaultModel, actor.ID); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	delete(s.llmPreviews, payload.PreviewID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listLLMTranslationModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "metadata:sync"); !ok {
		return
	}
	providers, err := s.llmStore.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	result := []map[string]any{}
	for _, p := range providers {
		if p.Enabled && p.DefaultModel != "" {
			result = append(result, map[string]any{"id": p.ID, "name": p.Name, "defaultModel": p.DefaultModel, "boundary": p.Boundary})
		}
	}
	writeJSON(w, 200, result)
}
