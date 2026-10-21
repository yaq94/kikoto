package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// A page of cards asks for at most 240 titles. The endpoint accepts 200 so a
// client keeps a deterministic bound while still fitting a full prefetch batch.
const contentTranslationBatchLimit = 200

// A full page of production titles (about 20 characters each) is roughly
// 20 KiB of JSON; long titles and optional paths need headroom. The batch gets
// its own budget so the single-field limit stays tight.
const contentTranslationBatchBodyLimit = 512 << 10

type contentTranslationBatchItem struct {
	Key string `json:"key"`
	contentTranslationRequest
}

type contentTranslationBatchRequest struct {
	Language string                        `json:"language"`
	Items    []contentTranslationBatchItem `json:"items"`
}

type contentTranslationBatchResponse struct {
	Items []contentTranslationBatchResult `json:"items"`
}

type contentTranslationBatchResult struct {
	Key      string `json:"key"`
	Text     string `json:"text"`
	Original string `json:"original"`
	Language string `json:"language"`
	Status   string `json:"status"`
	Code     string `json:"code,omitempty"`
	Version  string `json:"version,omitempty"`
}

// translateContentBatch answers a page without making the caller issue one
// request per card. A refused item is reported beside the successes; it never
// cancels its neighbours.
func (s *Server) translateContentBatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "library:read"); !ok {
		return
	}
	var in contentTranslationBatchRequest
	if decodeTranslationBodyLimit(w, r, &in, contentTranslationBatchBodyLimit) != nil {
		writeTranslationTrialError(w, &translation.Error{Code: "translation_invalid_input", Message: "Send a language and a list of fields."})
		return
	}
	if len(in.Items) == 0 || len(in.Items) > contentTranslationBatchLimit {
		writeAPIError(w, http.StatusBadRequest, "translation_invalid_input", "A batch accepts between 1 and 200 items.", false)
		return
	}
	// The response keeps request order so a client can zip results back into
	// its cards even though the items run concurrently.
	results := make([]contentTranslationBatchResult, len(in.Items))
	workers := 8
	if workers > len(in.Items) {
		workers = len(in.Items)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index] = s.translateBatchItem(r.Context(), in.Language, in.Items[index])
			}
		}()
	}
	for index := range in.Items {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	version := s.contentTranslationVersion(r.Context())
	for index := range results {
		results[index].Version = version
	}
	writeJSON(w, http.StatusOK, contentTranslationBatchResponse{Items: results})
}

func (s *Server) translateBatchItem(ctx context.Context, language string, item contentTranslationBatchItem) contentTranslationBatchResult {
	request := item.contentTranslationRequest
	if request.Language == "" {
		request.Language = language
	}
	result := contentTranslationBatchResult{Key: item.Key, Language: request.Language, Original: request.Text}
	if !slices.Contains([]string{"title", "description", "tag", "directory"}, request.Field) || (request.Language != "origin" && translationTargets[request.Language].modelLanguage == "") {
		result.Status = "failed"
		result.Code = "translation_invalid_input"
		return result
	}
	request, err := s.resolveTranslationResource(ctx, request)
	if err != nil {
		// Keep the single-item distinction: an unknown code is a 404 and a
		// malformed identity is a rejected request, not a source outage.
		result.Status = "failed"
		if errors.Is(err, sql.ErrNoRows) {
			result.Code = "not_found"
		} else {
			result.Code = "translation_invalid_input"
		}
		return result
	}
	if request.SeriesID <= 0 && request.CatalogEntryID <= 0 && request.SourceID <= 0 {
		if ok, demoErr := s.translationWorkReadable(ctx, request.WorkID); demoErr != nil {
			result.Status = "failed"
			result.Code = "translation_content_unavailable"
			return result
		} else if !ok {
			result.Status = "failed"
			result.Code = "not_found"
			return result
		}
	}
	translated, status, err := s.translateContentField(ctx, request)
	if err != nil {
		result.Status = "failed"
		var public *translation.Error
		if errors.As(err, &public) {
			result.Code = public.Code
		} else {
			result.Code = "translation_content_unavailable"
		}
		return result
	}
	result.Text = translated.Text
	result.Original = translated.Original
	result.Status = status
	return result
}
