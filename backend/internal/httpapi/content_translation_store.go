package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// contentTranslationRow is one durable translation of a card field.
//
// The table is corpus, not a cache: a row is identified by the hash of the
// source text and of everything that shaped the model output, so a changed
// source or template produces another row instead of overwriting history.
type contentTranslationRow struct {
	ID              int64
	CacheKey        string
	EntityKind      string
	EntityID        int64
	EntityKey       string
	Field           string
	Language        string
	SourceText      string
	TranslatedText  string
	Origin          string
	Model           string
	TemplateVersion string
}

const (
	contentTranslationLookupChunk   = 500
	contentTranslationStored        = "stored"
	contentTranslationPausedLowDisk = "paused_low_disk"
)

var errTranslationCachePaused = errors.New("translation cache paused: low disk space")

// contentTranslationCacheKey hashes every input that changes the stored text.
// The field order is fixed: changing it would orphan every existing row.
func contentTranslationCacheKey(entityKind string, entityID int64, entityKey, field, language, sourceText, origin, model, baseURL, templateVersion string, disableThinking bool) string {
	payload := strings.Join([]string{
		entityKind,
		strconv.FormatInt(entityID, 10),
		entityKey,
		field,
		language,
		sourceText,
		origin,
		model,
		baseURL,
		templateVersion,
		strconv.FormatBool(disableThinking),
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// lookupContentTranslations reads exact matches in chunks so a batch request
// never builds a statement with thousands of bind parameters.
func (s *Server) lookupContentTranslations(ctx context.Context, keys []string) (map[string]contentTranslationRow, error) {
	found := make(map[string]contentTranslationRow, len(keys))
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
			SELECT id, cache_key, entity_kind, entity_id, entity_key, field, language,
			       source_text, translated_text, origin, model, template_version
			FROM content_translation
			WHERE cache_key IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("lookup content translations: %w", err)
		}
		for rows.Next() {
			var row contentTranslationRow
			if err := rows.Scan(&row.ID, &row.CacheKey, &row.EntityKind, &row.EntityID, &row.EntityKey,
				&row.Field, &row.Language, &row.SourceText, &row.TranslatedText,
				&row.Origin, &row.Model, &row.TemplateVersion); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan content translation: %w", err)
			}
			found[row.CacheKey] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lookup content translations: %w", err)
		}
		rows.Close()
	}
	return found, nil
}

// saveContentTranslations upserts rows by cache key. Re-saving the same key
// keeps one row and refreshes the text and the model metadata. The returned
// status distinguishes a durable write from a low-disk skip so callers do not
// report an uncached result as stored.
func (s *Server) saveContentTranslations(ctx context.Context, rows []contentTranslationRow) (string, error) {
	if len(rows) == 0 {
		return contentTranslationStored, nil
	}
	cfg, cfgErr := translation.NewStore(s.db).Load(ctx)
	// A low-disk pause stops new cache rows. The caller still returns the
	// translation it already has; only the durable copy waits for space.
	if cfgErr == nil && s.translationDiskPause(cfg) != "" {
		return contentTranslationPausedLowDisk, nil
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return "", fmt.Errorf("save content translations: %w", err)
	}
	defer release()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO content_translation(
		  cache_key, entity_kind, entity_id, entity_key, field, language,
		  source_text, translated_text, origin, model, template_version)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(cache_key) DO UPDATE SET
		  translated_text = excluded.translated_text,
		  model = excluded.model,
		  template_version = excluded.template_version,
		  updated_at = CURRENT_TIMESTAMP`)
	if err != nil {
		return "", fmt.Errorf("save content translations: %w", err)
	}
	defer stmt.Close()
	for _, row := range rows {
		if row.CacheKey == "" {
			return "", errors.New("save content translations: empty cache key")
		}
		if _, err := stmt.ExecContext(ctx, row.CacheKey, row.EntityKind, row.EntityID, row.EntityKey,
			row.Field, row.Language, row.SourceText, row.TranslatedText,
			row.Origin, row.Model, row.TemplateVersion); err != nil {
			return "", fmt.Errorf("save content translation %q: %w", row.Field, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("save content translations: %w", err)
	}
	if cfgErr == nil {
		hasModel := false
		for _, row := range rows {
			if row.Origin == "model" {
				hasModel = true
				break
			}
		}
		if hasModel {
			s.pruneTranslationCacheAfterWrite(ctx, cfg, len(rows))
		}
	}
	return contentTranslationStored, nil
}

// translatedEntityIDs returns entities whose translated text contains the
// query. Three or more characters go through the trigram index; shorter
// queries walk bounded index ranges over the complete language corpus.
func (s *Server) translatedEntityIDs(ctx context.Context, language, entityKind, query string, limit int) ([]int64, error) {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil, nil
	}
	var (
		rows *sql.Rows
		err  error
	)
	if utf8.RuneCountInString(query) >= 3 {
		// A quoted phrase keeps punctuation and operators in the user's text
		// from being parsed as FTS5 syntax.
		phrase := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
		rows, err = s.db.QueryContext(ctx, `
			SELECT t.entity_id
			FROM content_translation_search
			JOIN content_translation AS t ON t.id = content_translation_search.rowid
			WHERE content_translation_search MATCH ?
			  AND t.language = ?
			  AND t.entity_kind = ?
			-- No ORDER BY: FTS5 yields rowids in index order, so LIMIT stops
			-- early. The caller sorts cards; sorting here would read every match.
			LIMIT ?`, phrase, language, entityKind, limit)
	} else {
		return s.scanShortTranslationEntityIDs(ctx, language, entityKind, query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("search translated content: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("search translated content: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search translated content: %w", err)
	}
	return ids, nil
}

// scanShortTranslationEntityIDs walks the language's id index in bounded
// segments. Each statement reads at most contentTranslationShortSegment rows;
// the loop continues until the complete corpus has been inspected or the
// caller's result limit is reached. Old and new rows are therefore equally
// visible, without a fixed newest-window truncation.
func (s *Server) scanShortTranslationEntityIDs(ctx context.Context, language, entityKind, query string, limit int) ([]int64, error) {
	ids := make([]int64, 0, limit)
	seen := make(map[int64]struct{}, limit)
	after := int64(0)
	for {
		var batchMax sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `
			SELECT MAX(id) FROM (
				SELECT id FROM content_translation
				WHERE language=? AND entity_kind=? AND id>?
				ORDER BY id LIMIT ?
			)`, language, entityKind, after, contentTranslationShortSegment).Scan(&batchMax); err != nil {
			return nil, fmt.Errorf("scan translated content boundary: %w", err)
		}
		if !batchMax.Valid {
			return ids, nil
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT entity_id FROM content_translation
			WHERE language=? AND entity_kind=? AND id>? AND id<=?
			  AND instr(lower(translated_text),lower(?))>0
			ORDER BY id`, language, entityKind, after, batchMax.Int64, query)
		if err != nil {
			return nil, fmt.Errorf("search translated content: %w", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("search translated content: %w", err)
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			if len(ids) >= limit {
				rows.Close()
				return ids, nil
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("search translated content: %w", err)
		}
		rows.Close()
		after = batchMax.Int64
	}
}

// countContentTranslations reports how many rows exist for a language; the
// prefill progress endpoint uses it without touching the FTS index.
func (s *Server) countContentTranslations(ctx context.Context, language string) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM content_translation WHERE language = ?`, language).Scan(&count); err != nil {
		return 0, fmt.Errorf("count content translations: %w", err)
	}
	return count, nil
}
