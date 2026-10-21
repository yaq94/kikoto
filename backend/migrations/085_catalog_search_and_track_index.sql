-- Browsing and search used instr(lower(...)) over the entries, every file of
-- every card, and each series. On a full index that scanned 41,824 entries and
-- 224,395 file rows for every page, and track listing loaded a whole card.
--
-- The search documents keep one row per searchable title. FTS5's trigram
-- tokenizer answers substring queries (including CJK) from an index instead of
-- scanning source_catalog_file. The track parent column lets the first level of
-- a card be read through an index rather than the card's entire file list.
CREATE TABLE source_catalog_search_doc (
  id INTEGER PRIMARY KEY,
  entity_kind TEXT NOT NULL CHECK(entity_kind IN ('entry','file','series')),
  entity_key TEXT NOT NULL,
  catalog_entry_id INTEGER NOT NULL DEFAULT 0,
  series_id INTEGER NOT NULL DEFAULT 0,
  file_source_id INTEGER NOT NULL DEFAULT 0,
  body TEXT NOT NULL,
  UNIQUE(entity_kind, entity_key)
);

CREATE VIRTUAL TABLE source_catalog_search USING fts5(
  body,
  content='source_catalog_search_doc',
  content_rowid='id',
  tokenize='trigram'
);

INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
SELECT 'entry', CAST(id AS TEXT), id, 0, COALESCE(file_source_id, 0), title || ' ' || primary_code
FROM source_catalog_entry;

INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
SELECT 'file', catalog_entry_id || ':' || relative_path, catalog_entry_id, 0,
       COALESCE((SELECT file_source_id FROM source_catalog_entry e WHERE e.id = f.catalog_entry_id), 0), title
FROM source_catalog_file f;

INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
SELECT 'series', CAST(id AS TEXT), 0, id, COALESCE(source_id, 0), title
FROM catalog_series;

INSERT INTO source_catalog_search(source_catalog_search) VALUES('rebuild');

CREATE TRIGGER source_catalog_search_doc_insert AFTER INSERT ON source_catalog_search_doc BEGIN
  INSERT INTO source_catalog_search(rowid, body) VALUES (NEW.id, NEW.body);
END;

CREATE TRIGGER source_catalog_search_doc_delete AFTER DELETE ON source_catalog_search_doc BEGIN
  INSERT INTO source_catalog_search(source_catalog_search, rowid, body) VALUES ('delete', OLD.id, OLD.body);
END;

CREATE TRIGGER source_catalog_search_doc_update AFTER UPDATE ON source_catalog_search_doc BEGIN
  INSERT INTO source_catalog_search(source_catalog_search, rowid, body) VALUES ('delete', OLD.id, OLD.body);
  INSERT INTO source_catalog_search(rowid, body) VALUES (NEW.id, NEW.body);
END;

CREATE TRIGGER source_catalog_search_entry_insert AFTER INSERT ON source_catalog_entry BEGIN
  INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
  VALUES ('entry', CAST(NEW.id AS TEXT), NEW.id, 0, COALESCE(NEW.file_source_id, 0), NEW.title || ' ' || NEW.primary_code);
END;

CREATE TRIGGER source_catalog_search_entry_update
AFTER UPDATE OF title, primary_code, file_source_id ON source_catalog_entry
WHEN OLD.title IS NOT NEW.title OR OLD.primary_code IS NOT NEW.primary_code OR OLD.file_source_id IS NOT NEW.file_source_id
BEGIN
  INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
  VALUES ('entry', CAST(NEW.id AS TEXT), NEW.id, 0, COALESCE(NEW.file_source_id, 0), NEW.title || ' ' || NEW.primary_code)
  ON CONFLICT(entity_kind, entity_key) DO UPDATE SET
    catalog_entry_id = excluded.catalog_entry_id,
    file_source_id = excluded.file_source_id,
    body = excluded.body;
  UPDATE source_catalog_search_doc SET file_source_id = COALESCE(NEW.file_source_id, 0)
  WHERE entity_kind = 'file' AND catalog_entry_id = NEW.id AND OLD.file_source_id IS NOT NEW.file_source_id;
END;

CREATE TRIGGER source_catalog_search_entry_delete AFTER DELETE ON source_catalog_entry BEGIN
  DELETE FROM source_catalog_search_doc WHERE entity_kind = 'entry' AND entity_key = CAST(OLD.id AS TEXT);
END;

CREATE TRIGGER source_catalog_search_file_insert AFTER INSERT ON source_catalog_file BEGIN
  INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
  VALUES ('file', NEW.catalog_entry_id || ':' || NEW.relative_path, NEW.catalog_entry_id, 0,
          COALESCE((SELECT file_source_id FROM source_catalog_entry WHERE id = NEW.catalog_entry_id), 0), NEW.title);
END;

CREATE TRIGGER source_catalog_search_file_update
AFTER UPDATE OF title, relative_path, catalog_entry_id ON source_catalog_file
WHEN OLD.title IS NOT NEW.title OR OLD.relative_path IS NOT NEW.relative_path OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id
BEGIN
  DELETE FROM source_catalog_search_doc
  WHERE entity_kind = 'file' AND entity_key = OLD.catalog_entry_id || ':' || OLD.relative_path;
  INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
  VALUES ('file', NEW.catalog_entry_id || ':' || NEW.relative_path, NEW.catalog_entry_id, 0,
          COALESCE((SELECT file_source_id FROM source_catalog_entry WHERE id = NEW.catalog_entry_id), 0), NEW.title);
END;

CREATE TRIGGER source_catalog_search_file_delete AFTER DELETE ON source_catalog_file BEGIN
  DELETE FROM source_catalog_search_doc
  WHERE entity_kind = 'file' AND entity_key = OLD.catalog_entry_id || ':' || OLD.relative_path;
END;

CREATE TRIGGER source_catalog_search_series_insert AFTER INSERT ON catalog_series BEGIN
  INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
  VALUES ('series', CAST(NEW.id AS TEXT), 0, NEW.id, COALESCE(NEW.source_id, 0), NEW.title);
END;

CREATE TRIGGER source_catalog_search_series_update
AFTER UPDATE OF title, source_id ON catalog_series
WHEN OLD.title IS NOT NEW.title OR OLD.source_id IS NOT NEW.source_id
BEGIN
  INSERT INTO source_catalog_search_doc(entity_kind, entity_key, catalog_entry_id, series_id, file_source_id, body)
  VALUES ('series', CAST(NEW.id AS TEXT), 0, NEW.id, COALESCE(NEW.source_id, 0), NEW.title)
  ON CONFLICT(entity_kind, entity_key) DO UPDATE SET
    series_id = excluded.series_id,
    file_source_id = excluded.file_source_id,
    body = excluded.body;
END;

CREATE TRIGGER source_catalog_search_series_delete AFTER DELETE ON catalog_series BEGIN
  DELETE FROM source_catalog_search_doc WHERE entity_kind = 'series' AND entity_key = CAST(OLD.id AS TEXT);
END;

ALTER TABLE source_catalog_file ADD COLUMN parent_path TEXT GENERATED ALWAYS AS (
  CASE
    WHEN instr(relative_path, '/') = 0 THEN ''
    ELSE substr(relative_path, 1, length(rtrim(relative_path, replace(relative_path, '/', ''))) - 1)
  END
) VIRTUAL;

CREATE INDEX source_catalog_file_parent ON source_catalog_file(catalog_entry_id, parent_path, relative_path);
