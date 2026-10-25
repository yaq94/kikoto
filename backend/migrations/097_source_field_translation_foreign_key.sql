-- Rebuild the display cache with the foreign key that the frozen 095 should
-- have shipped. Rows whose source no longer exists are unreachable cache and
-- are intentionally discarded. The migration runner performs the final
-- PRAGMA foreign_key_check before committing this file.
CREATE TABLE source_field_translation_rebuilt (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  cache_key TEXT NOT NULL UNIQUE,
  source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
  work_code TEXT NOT NULL DEFAULT '',
  field TEXT NOT NULL CHECK(field IN ('title','tag','directory')),
  path TEXT NOT NULL DEFAULT '',
  language TEXT NOT NULL,
  source_text TEXT NOT NULL,
  translated_text TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  template_version TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO source_field_translation_rebuilt (
  id, cache_key, source_id, work_code, field, path, language,
  source_text, translated_text, model, template_version, created_at, updated_at
)
SELECT
  cache.id, cache.cache_key, cache.source_id, cache.work_code, cache.field,
  cache.path, cache.language, cache.source_text, cache.translated_text,
  cache.model, cache.template_version, cache.created_at, cache.updated_at
FROM source_field_translation AS cache
WHERE EXISTS (
  SELECT 1 FROM file_source AS source WHERE source.id = cache.source_id
);

DROP TABLE source_field_translation;
ALTER TABLE source_field_translation_rebuilt RENAME TO source_field_translation;

CREATE INDEX idx_source_field_translation_owner
  ON source_field_translation(source_id, work_code, field, path, language);
CREATE INDEX idx_source_field_translation_updated
  ON source_field_translation(updated_at, id);
