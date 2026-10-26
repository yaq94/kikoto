-- A 1-2 character search must walk its complete effective range, and the
-- current-source check is what makes the range expensive: every candidate row
-- joined a wide entity row to compare source text. At the size of a large
-- source that join costs several times the scan itself.
--
-- These two columns carry that conclusion on the translation row. `source_id`
-- is the entry's source, and `effective` is 1 while `source_text` still equals
-- the entity's current title or description. The short-scan index then answers
-- language, kind, field, source, current-row and text from one index range.
--
-- Both columns are advisory: a stale value can only mis-filter a row that a
-- writer already changed, so the triggers below clear them in the same
-- transaction as the change, and a reader treats a NULL source as unknown.
ALTER TABLE content_translation ADD COLUMN source_id INTEGER;
ALTER TABLE content_translation ADD COLUMN effective INTEGER NOT NULL DEFAULT 1;

UPDATE content_translation SET source_id=(
  SELECT e.file_source_id FROM source_catalog_entry e WHERE e.id=content_translation.entity_id)
WHERE entity_kind='entry';

UPDATE content_translation SET source_id=(
  SELECT s.source_id FROM catalog_series s WHERE s.id=content_translation.entity_id)
WHERE entity_kind='series';

UPDATE content_translation SET effective=0 WHERE entity_kind='entry' AND NOT EXISTS(
  SELECT 1 FROM source_catalog_entry e WHERE e.id=content_translation.entity_id
    AND CASE WHEN content_translation.field='title' THEN e.title=content_translation.source_text
             ELSE content_translation.source_text='' END);

-- Superseded by the scoped index: the 103 index cannot separate one source's
-- rows from another's, so a per-source search still walked every source.
DROP INDEX idx_content_translation_short_scan;

CREATE INDEX idx_content_translation_short_scan
  ON content_translation(language,entity_kind,field,source_id,effective,id,origin,config_key,translated_text);

-- A title edit changes what is current in both directions: the old text stops
-- being current and a reverted text becomes current again. The trigger
-- recomputes the flag instead of only clearing it, and it is bounded to the
-- rows of the entity that changed.
CREATE TRIGGER content_translation_scope_work_update
AFTER UPDATE OF title,description ON work
BEGIN
  UPDATE content_translation SET effective=CASE
      WHEN field='title' THEN source_text=COALESCE(NEW.title,'')
      ELSE source_text=COALESCE(NEW.description,'') END
   WHERE entity_kind='work' AND entity_id=NEW.id;
END;

CREATE TRIGGER content_translation_scope_entry_update
AFTER UPDATE OF title,file_source_id ON source_catalog_entry
BEGIN
  UPDATE content_translation SET source_id=NEW.file_source_id
   WHERE entity_kind='entry' AND entity_id=NEW.id;
  UPDATE content_translation SET effective=source_text=COALESCE(NEW.title,'')
   WHERE entity_kind='entry' AND entity_id=NEW.id AND field='title';
END;

CREATE TRIGGER content_translation_scope_entry_delete
AFTER DELETE ON source_catalog_entry
BEGIN
  UPDATE content_translation SET effective=0
   WHERE entity_kind='entry' AND entity_id=OLD.id;
END;
