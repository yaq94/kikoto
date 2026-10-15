CREATE TABLE shared_association (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 actor_id INTEGER REFERENCES user_account(id) ON DELETE SET NULL,
 operation TEXT NOT NULL DEFAULT 'link' CHECK(operation IN ('link','split')),
 split_of INTEGER REFERENCES shared_association(id) ON DELETE RESTRICT,
 status TEXT NOT NULL DEFAULT 'preview' CHECK(status IN ('preview','pending','completed','stale','undone')),
 version TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 snapshot_json TEXT NOT NULL,
 result_json TEXT NOT NULL DEFAULT '{}',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TRIGGER shared_revision_presence_insert AFTER INSERT ON work_source_presence
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_presence_update AFTER UPDATE OF work_id,file_source_id,presence_type,remote_id,remote_code,availability ON work_source_presence
WHEN OLD.work_id IS NOT NEW.work_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.presence_type IS NOT NEW.presence_type OR OLD.remote_id IS NOT NEW.remote_id OR OLD.remote_code IS NOT NEW.remote_code OR OLD.availability IS NOT NEW.availability
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_presence_delete AFTER DELETE ON work_source_presence
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE INDEX shared_association_status ON shared_association(status,id);
CREATE TABLE shared_association_decision (
 operation_id INTEGER NOT NULL REFERENCES shared_association(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
 rating INTEGER CHECK(rating IS NULL OR rating BETWEEN 0 AND 5),
 note TEXT NOT NULL DEFAULT '',
 allocation TEXT NOT NULL DEFAULT '' CHECK(allocation IN ('','left','right','both')),
 PRIMARY KEY(operation_id,user_id)
);
CREATE TABLE shared_identity_member (
 work_id INTEGER PRIMARY KEY REFERENCES work(id) ON DELETE RESTRICT,
 identity_work_id INTEGER NOT NULL REFERENCES work(id) ON DELETE RESTRICT,
 operation_id INTEGER NOT NULL REFERENCES shared_association(id) ON DELETE RESTRICT
);
CREATE INDEX shared_identity_group ON shared_identity_member(identity_work_id,work_id);
CREATE VIEW shared_work_identity AS
 SELECT w.id AS work_id,COALESCE(m.identity_work_id,w.id) AS identity_work_id
 FROM work w LEFT JOIN shared_identity_member m ON m.work_id=w.id;
CREATE TABLE shared_identity_revision (
 kind TEXT NOT NULL,
 entity_id INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(kind,entity_id)
);
CREATE TABLE shared_review_revision (
 user_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
 work_id INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(user_id,work_id)
);

-- Compatibility projection: only rating and note cross a confirmed identity.
-- The difference guard makes propagation finite with recursive triggers on or off.
CREATE TRIGGER shared_review_insert AFTER INSERT ON user_work_state
WHEN NEW.rating IS NOT NULL OR NEW.note<>''
BEGIN
 INSERT INTO shared_review_revision(user_id,work_id) VALUES(NEW.user_id,NEW.work_id)
 ON CONFLICT(user_id,work_id) DO UPDATE SET revision=revision+1;
 INSERT INTO user_work_state(user_id,work_id,rating,note)
 SELECT NEW.user_id,peer.work_id,NEW.rating,NEW.note FROM shared_identity_member own
 JOIN shared_identity_member peer ON peer.identity_work_id=own.identity_work_id
 WHERE own.work_id=NEW.work_id AND peer.work_id<>NEW.work_id
 ON CONFLICT(user_id,work_id) DO UPDATE SET rating=excluded.rating,note=excluded.note,updated_at=CURRENT_TIMESTAMP
 WHERE user_work_state.rating IS NOT excluded.rating OR user_work_state.note IS NOT excluded.note;
END;
CREATE TRIGGER shared_review_update AFTER UPDATE OF rating,note ON user_work_state
WHEN OLD.rating IS NOT NEW.rating OR OLD.note IS NOT NEW.note
BEGIN
 INSERT INTO shared_review_revision(user_id,work_id) VALUES(NEW.user_id,NEW.work_id)
 ON CONFLICT(user_id,work_id) DO UPDATE SET revision=revision+1;
 INSERT INTO user_work_state(user_id,work_id,rating,note)
 SELECT NEW.user_id,peer.work_id,NEW.rating,NEW.note FROM shared_identity_member own
 JOIN shared_identity_member peer ON peer.identity_work_id=own.identity_work_id
 WHERE own.work_id=NEW.work_id AND peer.work_id<>NEW.work_id
 ON CONFLICT(user_id,work_id) DO UPDATE SET rating=excluded.rating,note=excluded.note,updated_at=CURRENT_TIMESTAMP
 WHERE user_work_state.rating IS NOT excluded.rating OR user_work_state.note IS NOT excluded.note;
END;
CREATE TRIGGER shared_review_delete AFTER DELETE ON user_work_state
WHEN OLD.rating IS NOT NULL OR OLD.note<>''
BEGIN
 INSERT INTO shared_review_revision(user_id,work_id) SELECT OLD.user_id,OLD.work_id
 WHERE EXISTS(SELECT 1 FROM user_account WHERE id=OLD.user_id) AND EXISTS(SELECT 1 FROM work WHERE id=OLD.work_id)
 ON CONFLICT(user_id,work_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_work_insert AFTER INSERT ON work
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_work_update AFTER UPDATE OF primary_code,title ON work
WHEN OLD.primary_code IS NOT NEW.primary_code OR OLD.title IS NOT NEW.title
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_work_delete AFTER DELETE ON work
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.id)
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

CREATE TRIGGER shared_revision_source_catalog_entry_delete AFTER DELETE ON source_catalog_entry
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',OLD.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_catalog_series_insert AFTER INSERT ON catalog_series
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('series',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_catalog_series_update AFTER UPDATE OF title,work_id,source_id,source_key ON catalog_series
WHEN OLD.title IS NOT NEW.title OR OLD.work_id IS NOT NEW.work_id OR OLD.source_id IS NOT NEW.source_id OR OLD.source_key IS NOT NEW.source_key
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('series',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_catalog_series_delete AFTER DELETE ON catalog_series
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('series',OLD.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_catalog_series_member_insert AFTER INSERT ON catalog_series_member
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',NEW.catalog_entry_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_catalog_series_member_update AFTER UPDATE ON catalog_series_member
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',NEW.catalog_entry_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_catalog_series_member_delete AFTER DELETE ON catalog_series_member
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',OLD.catalog_entry_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_source_catalog_file_insert AFTER INSERT ON source_catalog_file
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',NEW.catalog_entry_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_source_catalog_file_update AFTER UPDATE ON source_catalog_file
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',NEW.catalog_entry_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_source_catalog_file_delete AFTER DELETE ON source_catalog_file
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('catalog',OLD.catalog_entry_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_file_source_insert AFTER INSERT ON file_source
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('source',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_file_source_update AFTER UPDATE OF enabled,display_name,source_type,config_json ON file_source
WHEN OLD.enabled IS NOT NEW.enabled OR OLD.display_name IS NOT NEW.display_name OR OLD.source_type IS NOT NEW.source_type OR OLD.config_json IS NOT NEW.config_json
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('source',NEW.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_file_source_delete AFTER DELETE ON file_source
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('source',OLD.id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_shared_identity_member_insert AFTER INSERT ON shared_identity_member
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_shared_identity_member_update AFTER UPDATE ON shared_identity_member
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_shared_identity_member_delete AFTER DELETE ON shared_identity_member
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_work_edition_insert AFTER INSERT ON work_edition
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_work_edition_update AFTER UPDATE ON work_edition
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',NEW.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER shared_revision_work_edition_delete AFTER DELETE ON work_edition
BEGIN
 INSERT INTO shared_identity_revision(kind,entity_id) VALUES('work',OLD.work_id)
 ON CONFLICT(kind,entity_id) DO UPDATE SET revision=revision+1;
END;
