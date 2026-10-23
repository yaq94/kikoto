-- Remote source fields such as a work title, tag or directory display name
-- have no durable work or catalogue-entry identity. They used to call the model
-- again on every browse because content_translation only accepts entity kinds
-- that are part of the searchable corpus.
--
-- Keep their model output in a separate table. These rows are rebuildable
-- display cache, never search corpus: no FTS table or trigger refers to them,
-- and content_translation's published CHECK constraint stays unchanged.
CREATE TABLE source_field_translation (
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

CREATE INDEX idx_source_field_translation_owner
  ON source_field_translation(source_id, work_code, field, path, language);
CREATE INDEX idx_source_field_translation_updated
  ON source_field_translation(updated_at, id);
