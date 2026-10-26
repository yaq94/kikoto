-- A 1-2 character search walks its complete effective range. The 103 index
-- answered language, kind, field and text from one range, but the current-row
-- and admission predicates still joined a wide entity row for every candidate.
-- At the size of a large source that join costs several times the scan itself.
--
-- `source_id` names the source an entry or series belongs to. `browsable` says
-- the row still describes a live, playable entity: the stored source text
-- equals the entity's current text, the owning source is enabled, and the card
-- passes the 104 admission bounds. Both columns sit in the index prefix, so a
-- per-source search answers its range without leaving the index.
--
-- Version 105 was used by an unpublished draft of this feature and reverted
-- before any database received it, so the number is reused for this shape.
ALTER TABLE content_translation ADD COLUMN source_id INTEGER;
ALTER TABLE content_translation ADD COLUMN browsable INTEGER NOT NULL DEFAULT 0;

-- One definition of "this card can be opened", shared by the entry arm and by
-- every series member probe. The stored 104 counts answer it with one primary
-- key lookup. A missing or stale row falls back to counting the live published
-- files, exactly like catalogEntryAdmittedSQL, so a writer that forgets to
-- refresh admission hides nothing that a browse would still show.
--
-- This view is read when a translation row's scope is computed, never by a
-- search: a search reads the stored `browsable` column from the index.
CREATE VIEW catalog_entry_searchable AS
SELECT e.id AS catalog_entry_id, e.title AS title,
  CASE WHEN e.relocated_to_id IS NULL AND e.file_source_id IS NOT NULL
    AND trim(e.title)<>''
    AND EXISTS(SELECT 1 FROM file_source src
               WHERE src.id=e.file_source_id
                 AND src.enabled=1 AND src.source_type='openlist')
    AND COALESCE(
      (SELECT adm.audio>0 AND adm.files<=5000
              AND NOT (adm.non_audio>2000 AND adm.audio<5)
       FROM source_catalog_entry_admission adm
       WHERE adm.catalog_entry_id=e.id AND adm.generation=e.last_seen_generation),
      (SELECT live.audio>0 AND live.files<=5000
              AND NOT (live.non_audio>2000 AND live.audio<5)
       FROM (SELECT
         (SELECT count(*) FROM (SELECT 1 FROM source_catalog_file af
           WHERE af.catalog_entry_id=e.id AND af.kind<>'folder'
             AND af.observed_generation=e.last_seen_generation LIMIT 5001)) AS files,
         (SELECT count(*) FROM (SELECT 1 FROM source_catalog_file aaf
           WHERE aaf.catalog_entry_id=e.id AND aaf.kind='audio'
             AND aaf.observed_generation=e.last_seen_generation LIMIT 5)) AS audio,
         (SELECT count(*) FROM (SELECT 1 FROM source_catalog_file anf
           WHERE anf.catalog_entry_id=e.id AND anf.kind<>'audio' AND anf.kind<>'folder'
             AND anf.observed_generation=e.last_seen_generation LIMIT 2001)) AS non_audio) AS live))
  THEN 1 ELSE 0 END AS searchable
FROM source_catalog_entry e;

-- One definition of "this translation row still describes a searchable
-- entity". The triggers below and the publish refresh both read it, so a rule
-- change cannot leave the write path and the reader disagreeing.
CREATE VIEW content_translation_scope AS
SELECT ct.id AS translation_id,
  CASE ct.entity_kind
    WHEN 'entry' THEN (SELECT e.file_source_id FROM source_catalog_entry e WHERE e.id=ct.entity_id)
    WHEN 'series' THEN (SELECT s.source_id FROM catalog_series s WHERE s.id=ct.entity_id)
    ELSE NULL END AS source_id,
  CASE ct.entity_kind
    WHEN 'entry' THEN (CASE WHEN ct.field='title' AND EXISTS(
      SELECT 1 FROM catalog_entry_searchable entry_scope
      WHERE entry_scope.catalog_entry_id=ct.entity_id
        AND entry_scope.searchable=1 AND entry_scope.title=ct.source_text)
      THEN 1 ELSE 0 END)
    WHEN 'series' THEN (CASE WHEN ct.field='title' AND EXISTS(
      SELECT 1 FROM catalog_series s
      JOIN file_source src ON src.id=s.source_id
      WHERE s.id=ct.entity_id AND s.source_id IS NOT NULL
        AND src.enabled=1 AND src.source_type='openlist'
        AND trim(s.title)<>'' AND s.title=ct.source_text
        AND (SELECT COUNT(*) FROM catalog_series_member m WHERE m.series_id=s.id)>=2
        AND EXISTS(
          SELECT 1 FROM catalog_series_member m
          JOIN catalog_entry_searchable member_scope ON member_scope.catalog_entry_id=m.catalog_entry_id
          WHERE m.series_id=s.id AND member_scope.searchable=1))
      THEN 1 ELSE 0 END)
    ELSE (CASE WHEN EXISTS(
      SELECT 1 FROM work w
      WHERE w.id=ct.entity_id
        AND NOT EXISTS(SELECT 1 FROM catalog_series s WHERE s.work_id=w.id)
        AND ((ct.field='title' AND trim(w.title)<>'' AND w.title=ct.source_text)
          OR (ct.field='description' AND trim(w.description)<>'' AND w.description=ct.source_text)))
      THEN 1 ELSE 0 END)
  END AS browsable
FROM content_translation ct;

-- Backfill from the same view. A work row keeps a NULL source, which the
-- short-scan index stores as its own key.
UPDATE content_translation SET (source_id,browsable)=(
  SELECT scope.source_id,scope.browsable
  FROM content_translation_scope scope
  WHERE scope.translation_id=content_translation.id);

DROP INDEX idx_content_translation_short_scan;
CREATE INDEX idx_content_translation_short_scan
  ON content_translation(language,entity_kind,field,source_id,browsable,id,origin,config_key,translated_text,entity_id);

-- A new translation row gets its scope immediately, so prefill never inserts a
-- row that the next search would miss.
CREATE TRIGGER content_translation_scope_insert
AFTER INSERT ON content_translation
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=NEW.id)
  WHERE id=NEW.id;
END;

-- A title edit changes what is current in both directions: the old text stops
-- being current and a reverted text becomes current again. The trigger
-- recomputes instead of only clearing, and it is bounded to the entity's rows.
CREATE TRIGGER content_translation_scope_entry_update
AFTER UPDATE OF title,file_source_id,relocated_to_id,last_seen_generation ON source_catalog_entry
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='entry' AND entity_id=NEW.id;
END;

CREATE TRIGGER content_translation_scope_entry_delete
AFTER DELETE ON source_catalog_entry
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=OLD.id;
END;

-- A file write changes the admitted set, and a publish batch writes up to
-- thousands of files per card. Clearing is the cheap fail-closed answer; the
-- publish transaction restores the row once the card's file set is complete.
CREATE TRIGGER content_translation_scope_file_insert
AFTER INSERT ON source_catalog_file
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=NEW.catalog_entry_id;
END;

CREATE TRIGGER content_translation_scope_file_delete
AFTER DELETE ON source_catalog_file
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=OLD.catalog_entry_id;
END;

CREATE TRIGGER content_translation_scope_file_update
AFTER UPDATE OF catalog_entry_id,kind,observed_generation ON source_catalog_file
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=OLD.catalog_entry_id;
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=NEW.catalog_entry_id;
END;

CREATE TRIGGER content_translation_scope_work_update
AFTER UPDATE OF title,description ON work
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='work' AND entity_id=NEW.id;
END;

CREATE TRIGGER content_translation_scope_series_update
AFTER UPDATE OF title,source_id ON catalog_series
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='series' AND entity_id=NEW.id;
END;

-- Membership decides whether a series has two episodes and a playable member.
CREATE TRIGGER content_translation_scope_series_member_insert
AFTER INSERT ON catalog_series_member
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='series' AND entity_id=NEW.series_id;
END;

CREATE TRIGGER content_translation_scope_series_member_delete
AFTER DELETE ON catalog_series_member
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='series' AND entity_id=OLD.series_id;
END;

-- A work claimed by a series stops being a second card for the same episodes,
-- and the series card takes over its search row.
CREATE TRIGGER content_translation_scope_series_work_insert
AFTER INSERT ON catalog_series
WHEN NEW.work_id IS NOT NULL
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='work' AND entity_id=NEW.work_id;
END;

CREATE TRIGGER content_translation_scope_series_work_update
AFTER UPDATE OF work_id ON catalog_series
WHEN OLD.work_id IS NOT NEW.work_id
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='work' AND entity_id IN (COALESCE(OLD.work_id,-1),COALESCE(NEW.work_id,-1));
END;

-- Enabling or disabling a source changes every row of that source at once.
-- This is an administrative action; the WHEN guard keeps an unrelated settings
-- save from rewriting the corpus.
CREATE TRIGGER content_translation_scope_source_update
AFTER UPDATE OF enabled,source_type ON file_source
WHEN OLD.enabled IS NOT NEW.enabled OR OLD.source_type IS NOT NEW.source_type
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope scope
    WHERE scope.translation_id=content_translation.id)
  WHERE source_id=NEW.id;
END;
