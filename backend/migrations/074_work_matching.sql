-- Matching is derived from shared metadata. No private review enters this index.
CREATE TABLE work_matching_result (
 kind TEXT NOT NULL CHECK(kind IN ('work','catalog','series')),
 entity_id INTEGER NOT NULL,
 result_json TEXT NOT NULL,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(kind,entity_id)
);
CREATE TABLE work_matching_operation (
 operation_id INTEGER PRIMARY KEY REFERENCES shared_association(id) ON DELETE CASCADE,
 subject_kind TEXT NOT NULL,
 subject_id INTEGER NOT NULL,
 evidence_fingerprint TEXT NOT NULL,
 rule TEXT NOT NULL
);
CREATE INDEX work_matching_operation_subject ON work_matching_operation(subject_kind,subject_id,operation_id);
CREATE INDEX work_matching_result_revision ON work_matching_result(updated_at);
CREATE INDEX work_matching_work_title ON work(lower(trim(title)),id);
CREATE INDEX work_matching_catalog_title ON source_catalog_entry(lower(trim(title)),id);
CREATE INDEX work_matching_series_title ON catalog_series(lower(trim(title)),id);
CREATE VIEW work_matching_subject AS
 SELECT 'work' AS kind,id,title,primary_code,id AS work_id FROM work
 UNION ALL SELECT 'catalog',id,title,primary_code,NULL FROM source_catalog_entry WHERE work_id IS NULL AND relocated_to_id IS NULL
 UNION ALL SELECT 'series',id,title,'',NULL FROM catalog_series WHERE work_id IS NULL;
CREATE TABLE work_matching_control (
 id INTEGER PRIMARY KEY CHECK(id=1),
 revision INTEGER NOT NULL DEFAULT 1,
 scan_revision INTEGER NOT NULL DEFAULT 0,
 cursor_kind TEXT NOT NULL DEFAULT '',
 cursor_id INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'queued',
 attempts INTEGER NOT NULL DEFAULT 0,
 retry_after INTEGER NOT NULL DEFAULT 0,
 last_request INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT ''
);
INSERT INTO work_matching_control(id) VALUES(1);
CREATE TRIGGER matching_identity_insert AFTER INSERT ON shared_identity_member
BEGIN UPDATE work_matching_control SET revision=revision+1,status='queued'; END;
CREATE TRIGGER matching_identity_update AFTER UPDATE ON shared_identity_member
BEGIN UPDATE work_matching_control SET revision=revision+1,status='queued'; END;
CREATE TRIGGER matching_identity_delete AFTER DELETE ON shared_identity_member
BEGIN UPDATE work_matching_control SET revision=revision+1,status='queued'; END;
CREATE TRIGGER matching_work_metadata_revision AFTER UPDATE OF release_date,work_type ON work
WHEN OLD.release_date IS NOT NEW.release_date OR OLD.work_type IS NOT NEW.work_type
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;
ALTER TABLE work_matching_result ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;

CREATE TRIGGER matching_revision_work_insert AFTER INSERT ON work
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_work_update AFTER UPDATE ON work WHEN OLD.title IS NOT NEW.title OR OLD.primary_code IS NOT NEW.primary_code OR OLD.release_date IS NOT NEW.release_date OR OLD.work_type IS NOT NEW.work_type
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_work_delete AFTER DELETE ON work
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

CREATE TRIGGER matching_revision_source_catalog_entry_delete AFTER DELETE ON source_catalog_entry
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_catalog_series_insert AFTER INSERT ON catalog_series
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_catalog_series_update AFTER UPDATE ON catalog_series
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_catalog_series_delete AFTER DELETE ON catalog_series
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_catalog_series_member_insert AFTER INSERT ON catalog_series_member
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_catalog_series_member_update AFTER UPDATE ON catalog_series_member
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_catalog_series_member_delete AFTER DELETE ON catalog_series_member
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_work_credit_insert AFTER INSERT ON work_credit
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_credit_update AFTER UPDATE ON work_credit
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_credit_delete AFTER DELETE ON work_credit
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_party_insert AFTER INSERT ON work_party
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_party_update AFTER UPDATE ON work_party
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_party_delete AFTER DELETE ON work_party
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_edition_insert AFTER INSERT ON work_edition
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_edition_update AFTER UPDATE ON work_edition
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_edition_delete AFTER DELETE ON work_edition
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id) ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER matching_revision_work_code_alias_insert AFTER INSERT ON work_code_alias
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_work_code_alias_update AFTER UPDATE ON work_code_alias
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;

CREATE TRIGGER matching_revision_work_code_alias_delete AFTER DELETE ON work_code_alias
BEGIN
 UPDATE work_matching_control SET revision=revision+1,status='queued';
END;
