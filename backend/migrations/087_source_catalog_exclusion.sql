-- kikoto:foreign_keys=off
-- A source-level directory rule. The path is absolute, normalized by the API,
-- and applies to the path itself plus every descendant.
CREATE TABLE source_catalog_exclusion (
  id INTEGER PRIMARY KEY,
  file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'whole',
  action TEXT NOT NULL DEFAULT 'exclude',
  note TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(file_source_id,path)
);

-- SQLite cannot widen a CHECK constraint in place. Rebuild the entry table so
-- availability_status can retain an excluded row without pretending it is
-- missing, then recreate every index and trigger that the old table owned.
CREATE TABLE source_catalog_entry_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_source_id INTEGER REFERENCES file_source(id) ON DELETE SET NULL,
  root_path TEXT NOT NULL,
  primary_code TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  work_id INTEGER REFERENCES work(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  availability_status TEXT NOT NULL DEFAULT 'available'
    CHECK(availability_status IN ('available','unavailable','maintenance','restricted','unconfirmed_empty','missing','excluded')),
  status_checked_at TEXT,
  last_seen_generation INTEGER NOT NULL DEFAULT 0,
  discovered_generation INTEGER NOT NULL DEFAULT 0,
  content_fingerprint TEXT NOT NULL DEFAULT '',
  layout_fingerprint TEXT NOT NULL DEFAULT '',
  relocated_to_id INTEGER REFERENCES source_catalog_entry(id) ON DELETE RESTRICT,
  source_root TEXT NOT NULL DEFAULT '',
  sidecar_json TEXT NOT NULL DEFAULT '',
  sidecar_pending INTEGER NOT NULL DEFAULT 0 CHECK(sidecar_pending IN (0,1)),
  sidecar_attempts INTEGER NOT NULL DEFAULT 0,
  sidecar_revision TEXT NOT NULL DEFAULT '',
  sidecar_transient_attempts INTEGER NOT NULL DEFAULT 0,
  sidecar_retry_at TEXT NOT NULL DEFAULT '',
  sidecar_failure TEXT NOT NULL DEFAULT '',
  UNIQUE(file_source_id, root_path)
);

INSERT INTO source_catalog_entry_new (
  id, file_source_id, root_path, primary_code, title, work_id, created_at,
  updated_at, availability_status, status_checked_at, last_seen_generation,
  discovered_generation, content_fingerprint, layout_fingerprint,
  relocated_to_id, source_root, sidecar_json, sidecar_pending,
  sidecar_attempts, sidecar_revision, sidecar_transient_attempts,
  sidecar_retry_at, sidecar_failure
)
SELECT
  id, file_source_id, root_path, primary_code, title, work_id, created_at,
  updated_at, availability_status, status_checked_at, last_seen_generation,
  discovered_generation, content_fingerprint, layout_fingerprint,
  relocated_to_id, source_root, sidecar_json, sidecar_pending,
  sidecar_attempts, sidecar_revision, sidecar_transient_attempts,
  sidecar_retry_at, sidecar_failure
FROM source_catalog_entry;

DROP VIEW work_matching_subject;
DROP TRIGGER IF EXISTS source_catalog_search_entry_insert;
DROP TRIGGER IF EXISTS source_catalog_search_entry_update;
DROP TRIGGER IF EXISTS source_catalog_search_entry_delete;
DROP TRIGGER IF EXISTS source_catalog_search_file_insert;
DROP TRIGGER IF EXISTS source_catalog_search_file_update;
DROP TRIGGER IF EXISTS source_catalog_search_file_delete;
DROP TABLE source_catalog_entry;
ALTER TABLE source_catalog_entry_new RENAME TO source_catalog_entry;
CREATE VIEW work_matching_subject AS
 SELECT 'work' AS kind,id,title,primary_code,id AS work_id FROM work
 UNION ALL SELECT 'catalog',id,title,primary_code,NULL FROM source_catalog_entry
   WHERE work_id IS NULL AND relocated_to_id IS NULL AND file_source_id IS NOT NULL
 UNION ALL SELECT 'series',id,title,'',NULL FROM catalog_series WHERE work_id IS NULL;

CREATE INDEX idx_source_catalog_work ON source_catalog_entry(work_id);
CREATE INDEX source_catalog_entry_content ON source_catalog_entry(file_source_id,content_fingerprint);
CREATE INDEX source_catalog_entry_layout ON source_catalog_entry(file_source_id,layout_fingerprint);
CREATE INDEX source_catalog_entry_lifecycle ON source_catalog_entry(file_source_id,last_seen_generation,id);
CREATE INDEX source_catalog_entry_live_root
    ON source_catalog_entry(file_source_id, root_path)
    WHERE relocated_to_id IS NULL;
CREATE INDEX source_catalog_entry_relocated ON source_catalog_entry(relocated_to_id);
CREATE INDEX source_catalog_entry_sidecar_due
    ON source_catalog_entry(file_source_id, sidecar_retry_at)
    WHERE sidecar_pending=1;
CREATE INDEX source_catalog_entry_sidecar_pending
    ON source_catalog_entry(id)
    WHERE sidecar_pending=1;
CREATE INDEX source_catalog_entry_source_root
    ON source_catalog_entry(file_source_id, source_root)
    WHERE source_root<>'';
CREATE INDEX work_matching_catalog_title ON source_catalog_entry(lower(trim(title)),id);

CREATE TRIGGER matching_revision_source_catalog_entry_delete AFTER DELETE ON source_catalog_entry
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_source_catalog_entry_insert AFTER INSERT ON source_catalog_entry
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_source_catalog_entry_update AFTER UPDATE ON source_catalog_entry WHEN OLD.title IS NOT NEW.title OR OLD.primary_code IS NOT NEW.primary_code OR OLD.work_id IS NOT NEW.work_id OR OLD.relocated_to_id IS NOT NEW.relocated_to_id OR OLD.content_fingerprint IS NOT NEW.content_fingerprint
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER personal_transfer_catalog_insert AFTER INSERT ON source_catalog_entry
BEGIN
 INSERT INTO personal_transfer_ref(ref,catalog_entry_id) VALUES('c:'||lower(hex(randomblob(16))),NEW.id);
END;

CREATE TRIGGER shared_revision_source_catalog_entry_delete AFTER DELETE ON source_catalog_entry
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',OLD.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_source_catalog_entry_insert AFTER INSERT ON source_catalog_entry
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_source_catalog_entry_update AFTER UPDATE OF file_source_id,root_path,primary_code,title,work_id ON source_catalog_entry
WHEN OLD.file_source_id IS NOT NEW.file_source_id OR OLD.root_path IS NOT NEW.root_path OR OLD.primary_code IS NOT NEW.primary_code OR OLD.title IS NOT NEW.title OR OLD.work_id IS NOT NEW.work_id
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
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
