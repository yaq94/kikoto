-- The prefill status is an operator-facing summary, not just a cursor flag: a
-- scan that exhausted its retry budget must be distinguishable from one that
-- is still working. SQLite cannot alter a CHECK constraint in place, so the
-- table is rebuilt with the same columns plus `failed`.
--
-- Triggers on other tables reference this one, so they are dropped first and
-- recreated with their original bodies after the rebuild.
DROP TRIGGER translation_prefill_reset_entry_title;
DROP TRIGGER translation_prefill_reset_series_title;
DROP TRIGGER translation_prefill_reset_work_title;
DROP TRIGGER translation_prefill_reset_work_description;

CREATE TABLE translation_prefill_state_rebuilt (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  cursor_entry_id INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'idle'
    CHECK(status IN ('idle','running','paused','complete','failed')),
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  config_key TEXT NOT NULL DEFAULT '',
  cursor_language TEXT NOT NULL DEFAULT '',
  cursor_kind TEXT NOT NULL DEFAULT 'entry',
  cursor_field TEXT NOT NULL DEFAULT 'title',
  cursor_id INTEGER NOT NULL DEFAULT 0
);

INSERT INTO translation_prefill_state_rebuilt(
  id, cursor_entry_id, status, last_error, updated_at,
  config_key, cursor_language, cursor_kind, cursor_field, cursor_id)
SELECT id, cursor_entry_id, status, last_error, updated_at,
  config_key, cursor_language, cursor_kind, cursor_field, cursor_id
FROM translation_prefill_state;

DROP TABLE translation_prefill_state;
ALTER TABLE translation_prefill_state_rebuilt RENAME TO translation_prefill_state;

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

-- The retry cleanup filters by config key and source hash; without this prefix
-- the bounded delete has to scan the whole table.
CREATE INDEX translation_prefill_failure_source
  ON translation_prefill_failure(config_key, source_hash);