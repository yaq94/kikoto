package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// sourceFieldTranslationRow is a rebuildable model result for a remote field
// that has no work or catalogue-entry identity. The table is deliberately not
// searchable corpus; see migration 095.
type sourceFieldTranslationRow struct {
	ID              int64
	CacheKey        string
	SourceID        int64
	WorkCode        string
	Field           string
	Path            string
	Language        string
	SourceText      string
	TranslatedText  string
	Model           string
	TemplateVersion string
}

// sourceFieldTranslationCacheKey separates source text, target language, model,
// endpoint, template and reasoning switch. The work code and real directory
// path are part of the identity: the same file name in two folders, or the same
// title on two source works, must not share a row.
func sourceFieldTranslationCacheKey(sourceID int64, workCode, field, path, language, sourceText, model, baseURL, templateVersion string, disableThinking bool) string {
	payload, err := json.Marshal([]any{
		"source-field-v1",
		sourceID,
		normalizeSourceFieldWorkCode(workCode),
		field,
		path,
		language,
		sourceText,
		model,
		baseURL,
		templateVersion,
		disableThinking,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func normalizeSourceFieldWorkCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func (s *Server) lookupSourceFieldTranslations(ctx context.Context, keys []string) (map[string]sourceFieldTranslationRow, error) {
	found := make(map[string]sourceFieldTranslationRow, len(keys))
	if len(keys) == 0 {
		return found, nil
	}
	seen := make(map[string]struct{}, len(keys))
	unique := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	for start := 0; start < len(unique); start += contentTranslationLookupChunk {
		end := start + contentTranslationLookupChunk
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk))
		for _, key := range chunk {
			args = append(args, key)
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT id, cache_key, source_id, work_code, field, path, language,
			       source_text, translated_text, model, template_version
			FROM source_field_translation
			WHERE cache_key IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("lookup source field translations: %w", err)
		}
		for rows.Next() {
			var row sourceFieldTranslationRow
			if err := rows.Scan(&row.ID, &row.CacheKey, &row.SourceID, &row.WorkCode,
				&row.Field, &row.Path, &row.Language, &row.SourceText,
				&row.TranslatedText, &row.Model, &row.TemplateVersion); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan source field translation: %w", err)
			}
			found[row.CacheKey] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lookup source field translations: %w", err)
		}
		rows.Close()
	}
	return found, nil
}

// saveSourceFieldTranslations stores only successful model output. Callers
// invoke it after the model returned a nonempty translation, never for a failed
// or empty result. Every row is rebuildable and therefore participates in the
// shared translation-cache budget.
func (s *Server) saveSourceFieldTranslations(ctx context.Context, rows []sourceFieldTranslationRow) (string, error) {
	if len(rows) == 0 {
		return contentTranslationStored, nil
	}
	cfg, cfgErr := translation.NewStore(s.db).Load(ctx)
	if cfgErr == nil {
		if pause := s.translationDiskPause(cfg); pause != "" {
			return contentTranslationPausedLowDisk, nil
		}
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return "", fmt.Errorf("save source field translations: %w", err)
	}
	defer release()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO source_field_translation(
		  cache_key, source_id, work_code, field, path, language,
		  source_text, translated_text, model, template_version)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(cache_key) DO UPDATE SET
		  translated_text = excluded.translated_text,
		  model = excluded.model,
		  template_version = excluded.template_version,
		  updated_at = CURRENT_TIMESTAMP`)
	if err != nil {
		return "", fmt.Errorf("save source field translations: %w", err)
	}
	defer stmt.Close()
	for _, row := range rows {
		if row.CacheKey == "" {
			return "", errors.New("save source field translations: empty cache key")
		}
		if strings.TrimSpace(row.TranslatedText) == "" {
			return "", errors.New("save source field translations: empty translation")
		}
		if _, err := stmt.ExecContext(ctx, row.CacheKey, row.SourceID, row.WorkCode,
			row.Field, row.Path, row.Language, row.SourceText, row.TranslatedText,
			row.Model, row.TemplateVersion); err != nil {
			return "", fmt.Errorf("save source field translation %q: %w", row.Field, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("save source field translations: %w", err)
	}
	if cfgErr == nil {
		s.pruneTranslationCacheAfterWrite(ctx, cfg, len(rows))
	}
	return contentTranslationStored, nil
}
