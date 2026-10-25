-- The prefill/corpus contract separates the output-shaping configuration from
-- credentials and resource limits. Rows without a current key are legacy or
-- superseded and must not answer strict display-language search.
ALTER TABLE content_translation
  ADD COLUMN config_key TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_content_translation_effective
  ON content_translation(language, entity_kind, field, config_key, entity_id);

-- Short queries walk the complete effective language/field set by id. Keeping
-- translated_text in the index avoids a table lookup for each candidate.
CREATE INDEX idx_content_translation_short
  ON content_translation(language, entity_kind, field, id, translated_text);

-- The current-row predicate also needs origin and config_key. Including both
-- in a second covering index avoids a table lookup for every short-query
-- candidate; the required short index above remains the stable shape.
CREATE INDEX idx_content_translation_short_current
  ON content_translation(language, entity_kind, field, origin, config_key, id, translated_text);

ALTER TABLE translation_prefill_state
  ADD COLUMN config_key TEXT NOT NULL DEFAULT '';
-- The prefill queue is ordered by language, entity kind, field and id. The
-- language is part of the cursor so a restart resumes inside the same
-- language/field group instead of replaying every earlier language.
ALTER TABLE translation_prefill_state
  ADD COLUMN cursor_language TEXT NOT NULL DEFAULT '';
ALTER TABLE translation_prefill_state
  ADD COLUMN cursor_kind TEXT NOT NULL DEFAULT 'entry';
ALTER TABLE translation_prefill_state
  ADD COLUMN cursor_field TEXT NOT NULL DEFAULT 'title';
ALTER TABLE translation_prefill_state
  ADD COLUMN cursor_id INTEGER NOT NULL DEFAULT 0;

-- Pre-099 cursors only knew an entry id. Keep it as the starting point; the
-- coverage pass moves it back to the first genuinely missing item.
UPDATE translation_prefill_state
SET cursor_id = cursor_entry_id
WHERE id = 1 AND cursor_id = 0;

CREATE TABLE translation_prefill_failure (
  entity_kind TEXT NOT NULL,
  entity_id INTEGER NOT NULL,
  field TEXT NOT NULL,
  language TEXT NOT NULL,
  config_key TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  -- NULL means the row is not scheduled. A failed attempt always sets a time;
  -- the column stays nullable so a future queue can park a row explicitly.
  next_retry_at TEXT,
  last_error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(entity_kind, entity_id, field, language, config_key, source_hash)
);

CREATE INDEX translation_prefill_failure_due
  ON translation_prefill_failure(config_key, attempts, next_retry_at, entity_kind, field, language);

-- A changed title invalidates the current row for that entity.
-- Failure rows for the old text are removed so a new source hash starts with a
-- fresh retry budget. Restarting the cursor is conservative: coverage skips
-- every row that is still current, so the pass only pays for the changed
-- entity and anything else that drifted while it was stopped.
CREATE TRIGGER translation_prefill_reset_entry_title
AFTER UPDATE OF title ON source_catalog_entry
WHEN OLD.title IS NOT NEW.title
BEGIN
  DELETE FROM translation_prefill_failure
  WHERE entity_kind='entry' AND entity_id=NEW.id AND field='title';
  UPDATE translation_prefill_state
  SET status=CASE WHEN status='complete' THEN 'running' ELSE status END,
      cursor_language='', cursor_kind='entry', cursor_field='title', cursor_id=0,
      updated_at=CURRENT_TIMESTAMP
  WHERE id=1;
END;

CREATE TRIGGER translation_prefill_reset_series_title
AFTER UPDATE OF title ON catalog_series
WHEN OLD.title IS NOT NEW.title
BEGIN
  DELETE FROM translation_prefill_failure
  WHERE entity_kind='series' AND entity_id=NEW.id AND field='title';
  UPDATE translation_prefill_state
  SET status=CASE WHEN status='complete' THEN 'running' ELSE status END,
      cursor_language='', cursor_kind='entry', cursor_field='title', cursor_id=0,
      updated_at=CURRENT_TIMESTAMP
  WHERE id=1;
END;

CREATE TRIGGER translation_prefill_reset_work_title
AFTER UPDATE OF title ON work
WHEN OLD.title IS NOT NEW.title
BEGIN
  DELETE FROM translation_prefill_failure
  WHERE entity_kind='work' AND entity_id=NEW.id AND field='title';
  UPDATE translation_prefill_state
  SET status=CASE WHEN status='complete' THEN 'running' ELSE status END,
      cursor_language='', cursor_kind='entry', cursor_field='title', cursor_id=0,
      updated_at=CURRENT_TIMESTAMP
  WHERE id=1;
END;

CREATE TRIGGER translation_prefill_reset_work_description
AFTER UPDATE OF description ON work
WHEN OLD.description IS NOT NEW.description
BEGIN
  DELETE FROM translation_prefill_failure
  WHERE entity_kind='work' AND entity_id=NEW.id AND field='description';
  UPDATE translation_prefill_state
  SET status=CASE WHEN status='complete' THEN 'running' ELSE status END,
      cursor_language='', cursor_kind='entry', cursor_field='title', cursor_id=0,
      updated_at=CURRENT_TIMESTAMP
  WHERE id=1;
END;
