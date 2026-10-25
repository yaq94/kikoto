-- Translated content used to live only in a JSON file per cache key: search
-- could not read it, and every hit listed the whole directory. Store the same
-- results in SQLite with an indexed exact lookup and a trigram index so a card
-- can be found by the text in the reader's display language.
--
-- This table is durable corpus, not an LRU cache. There is no last_used_at and
-- no eviction: one row per entity, field, language and content hash. When the
-- source text changes the cache key changes and the old row stays until a
-- low-frequency cleanup task removes it.
CREATE TABLE content_translation (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  cache_key TEXT NOT NULL UNIQUE,
  entity_kind TEXT NOT NULL CHECK(entity_kind IN ('entry','series','work')),
  entity_id INTEGER NOT NULL,
  entity_key TEXT NOT NULL DEFAULT '',
  field TEXT NOT NULL CHECK(field IN ('title','description','tag','directory')),
  language TEXT NOT NULL,
  source_text TEXT NOT NULL DEFAULT '',
  translated_text TEXT NOT NULL,
  origin TEXT NOT NULL CHECK(origin IN ('native','model')),
  model TEXT NOT NULL DEFAULT '',
  template_version TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_content_translation_entity
  ON content_translation(entity_kind, entity_id, field, language);
CREATE INDEX idx_content_translation_language
  ON content_translation(language, entity_kind, entity_id);
-- Language-ordered walk over one kind. The 1-2 character path reads
-- idx_content_translation_short_scan from 103 instead; this index stays part
-- of the frozen 092 shape and is kept for range reads by language.
CREATE INDEX idx_content_translation_language_id
  ON content_translation(language, entity_kind, id);

CREATE VIRTUAL TABLE content_translation_search USING fts5(
  translated_text,
  content='content_translation',
  content_rowid='id',
  tokenize='trigram'
);

CREATE TRIGGER content_translation_ai AFTER INSERT ON content_translation BEGIN
  INSERT INTO content_translation_search(rowid, translated_text)
  VALUES (new.id, new.translated_text);
END;
CREATE TRIGGER content_translation_ad AFTER DELETE ON content_translation BEGIN
  INSERT INTO content_translation_search(content_translation_search, rowid, translated_text)
  VALUES ('delete', old.id, old.translated_text);
END;
CREATE TRIGGER content_translation_au AFTER UPDATE OF translated_text ON content_translation BEGIN
  INSERT INTO content_translation_search(content_translation_search, rowid, translated_text)
  VALUES ('delete', old.id, old.translated_text);
  INSERT INTO content_translation_search(rowid, translated_text)
  VALUES (new.id, new.translated_text);
END;

-- One row records the background prefill cursor so a restart resumes the scan
-- instead of translating the same entries again.
CREATE TABLE translation_prefill_state (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  cursor_entry_id INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'idle'
    CHECK(status IN ('idle','running','paused','complete')),
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO translation_prefill_state(id, cursor_entry_id, status) VALUES (1, 0, 'idle');
