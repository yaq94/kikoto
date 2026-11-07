ALTER TABLE source_catalog_directory ADD COLUMN last_probe_failed_run INTEGER NOT NULL DEFAULT 0;

-- A listing owns its pages and checkpoints independently of the readable set.
CREATE TABLE source_catalog_listing (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
    directory_path TEXT NOT NULL,
    generation INTEGER NOT NULL,
    base_revision INTEGER NOT NULL,
    observed_version TEXT NOT NULL,
    options_json TEXT NOT NULL,
    next_page INTEGER NOT NULL DEFAULT 1,
    expected_total INTEGER NOT NULL DEFAULT -1,
    received INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'fetching' CHECK(state IN ('fetching','complete','applying','applied','invalid')),
    apply_cursor TEXT NOT NULL DEFAULT '',
    remove_cursor TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX source_catalog_listing_directory ON source_catalog_listing(file_source_id,directory_path,id DESC);
CREATE TABLE source_catalog_listing_item (
    listing_id INTEGER NOT NULL REFERENCES source_catalog_listing(id) ON DELETE CASCADE,
    directory_path TEXT NOT NULL,
    name TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    modified_at TEXT NOT NULL,
    PRIMARY KEY(listing_id,name)
) WITHOUT ROWID;
-- Publication operations hold sparse before-images until one marker commits.
-- Existing rows are snapshot zero; migration does not copy the catalog.
CREATE TABLE source_catalog_publication (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
 owner TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'preparing' CHECK(state IN ('preparing','committed')),
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX source_catalog_publication_pending ON source_catalog_publication(file_source_id) WHERE state='preparing';
CREATE TABLE source_catalog_publication_writer (id INTEGER PRIMARY KEY CHECK(id=1),operation_id INTEGER NOT NULL DEFAULT 0);
INSERT INTO source_catalog_publication_writer(id) VALUES(1);
CREATE INDEX content_translation_publication_entity ON content_translation(entity_kind,entity_id,id);
CREATE TABLE source_catalog_publication_entity (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 entity_kind TEXT NOT NULL,
 entity_id INTEGER NOT NULL,
 after_translation_id INTEGER NOT NULL DEFAULT 0,
 translation_prepared INTEGER NOT NULL DEFAULT 0,
 after_location_id INTEGER NOT NULL DEFAULT 0,
 media_prepared INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(operation_id,entity_kind,entity_id)
) WITHOUT ROWID;
CREATE INDEX source_catalog_publication_entity_translation_pending
 ON source_catalog_publication_entity(operation_id,entity_kind,entity_id) WHERE translation_prepared=0;
CREATE INDEX source_catalog_publication_entity_media_pending
 ON source_catalog_publication_entity(operation_id,entity_kind,entity_id) WHERE media_prepared=0;
CREATE INDEX source_catalog_publication_entity_identity
 ON source_catalog_publication_entity(entity_kind,entity_id,operation_id);
-- Source mutations invalidate existing ownership, including keys prepared
-- before a failed marker. Run before INSERT's conflict handling so ON CONFLICT
-- DO NOTHING cannot discard an invalidation or leave a stale keyset cursor.
CREATE TRIGGER publication_entity_invalidate BEFORE INSERT ON source_catalog_publication_entity
BEGIN
 UPDATE source_catalog_publication_entity SET after_translation_id=0,translation_prepared=0,after_location_id=0,media_prepared=0
 WHERE operation_id=NEW.operation_id AND entity_kind=NEW.entity_kind AND entity_id=NEW.entity_id;
END;
CREATE TRIGGER source_catalog_publication_commit AFTER UPDATE OF state ON source_catalog_publication
WHEN OLD.state='preparing' AND NEW.state='committed'
BEGIN
 UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TABLE source_catalog_entry_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 id INTEGER,
 file_source_id INTEGER,
 root_path TEXT,
 primary_code TEXT,
 title TEXT,
 created_at TEXT,
 updated_at TEXT,
 availability_status TEXT,
 status_checked_at TEXT,
 last_seen_generation INTEGER,
 discovered_generation INTEGER,
 content_fingerprint TEXT,
 layout_fingerprint TEXT,
 relocated_to_id INTEGER,
 source_root TEXT,
 sidecar_json TEXT,
 sidecar_pending INTEGER,
 sidecar_attempts INTEGER,
 sidecar_revision TEXT,
 sidecar_transient_attempts INTEGER,
 sidecar_retry_at TEXT,
 sidecar_failure TEXT,
 PRIMARY KEY(id)
);
CREATE INDEX source_catalog_entry_publication_before_operation ON source_catalog_entry_publication_before(operation_id);

CREATE VIEW published_source_catalog_entry AS
 SELECT live.id,live.file_source_id,live.root_path,live.primary_code,live.title,live.work_id,live.created_at,live.updated_at,live.availability_status,live.status_checked_at,live.last_seen_generation,live.discovered_generation,live.content_fingerprint,live.layout_fingerprint,live.relocated_to_id,live.source_root,live.sidecar_json,live.sidecar_pending,live.sidecar_attempts,live.sidecar_revision,live.sidecar_transient_attempts,live.sidecar_retry_at,live.sidecar_failure,live.id AS rowid FROM source_catalog_entry live
 WHERE NOT EXISTS(SELECT 1 FROM source_catalog_entry_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.id=live.id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.id,b.file_source_id,b.root_path,b.primary_code,b.title,COALESCE(live.work_id,(SELECT work_id FROM source_catalog_entry identity_target WHERE identity_target.id=live.relocated_to_id)),b.created_at,b.updated_at,b.availability_status,b.status_checked_at,b.last_seen_generation,b.discovered_generation,b.content_fingerprint,b.layout_fingerprint,b.relocated_to_id,b.source_root,b.sidecar_json,b.sidecar_pending,b.sidecar_attempts,b.sidecar_revision,b.sidecar_transient_attempts,b.sidecar_retry_at,b.sidecar_failure,b.id AS rowid FROM source_catalog_entry_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN source_catalog_entry live ON b.id=live.id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_source_catalog_entry_insert AFTER INSERT ON source_catalog_entry
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_entry_publication_before(operation_id,existed,id,file_source_id,root_path,primary_code,title,created_at,updated_at,availability_status,status_checked_at,last_seen_generation,discovered_generation,content_fingerprint,layout_fingerprint,relocated_to_id,source_root,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.file_source_id,NEW.root_path,NEW.primary_code,NEW.title,NEW.created_at,NEW.updated_at,NEW.availability_status,NEW.status_checked_at,NEW.last_seen_generation,NEW.discovered_generation,NEW.content_fingerprint,NEW.layout_fingerprint,NEW.relocated_to_id,NEW.source_root,NEW.sidecar_json,NEW.sidecar_pending,NEW.sidecar_attempts,NEW.sidecar_revision,NEW.sidecar_transient_attempts,NEW.sidecar_retry_at,NEW.sidecar_failure)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,file_source_id=excluded.file_source_id,root_path=excluded.root_path,primary_code=excluded.primary_code,title=excluded.title,created_at=excluded.created_at,updated_at=excluded.updated_at,availability_status=excluded.availability_status,status_checked_at=excluded.status_checked_at,last_seen_generation=excluded.last_seen_generation,discovered_generation=excluded.discovered_generation,content_fingerprint=excluded.content_fingerprint,layout_fingerprint=excluded.layout_fingerprint,relocated_to_id=excluded.relocated_to_id,source_root=excluded.source_root,sidecar_json=excluded.sidecar_json,sidecar_pending=excluded.sidecar_pending,sidecar_attempts=excluded.sidecar_attempts,sidecar_revision=excluded.sidecar_revision,sidecar_transient_attempts=excluded.sidecar_transient_attempts,sidecar_retry_at=excluded.sidecar_retry_at,sidecar_failure=excluded.sidecar_failure
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_entry_insert BEFORE INSERT ON source_catalog_entry
 WHEN EXISTS(SELECT 1 FROM source_catalog_entry_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=NEW.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_entry_insert BEFORE INSERT ON source_catalog_entry
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=NEW.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_entry_update BEFORE UPDATE OF id,file_source_id,root_path,primary_code,title,created_at,updated_at,availability_status,status_checked_at,last_seen_generation,discovered_generation,content_fingerprint,layout_fingerprint,relocated_to_id,source_root,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure ON source_catalog_entry
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.root_path IS NOT NEW.root_path OR OLD.primary_code IS NOT NEW.primary_code OR OLD.title IS NOT NEW.title OR OLD.created_at IS NOT NEW.created_at OR OLD.updated_at IS NOT NEW.updated_at OR OLD.availability_status IS NOT NEW.availability_status OR OLD.status_checked_at IS NOT NEW.status_checked_at OR OLD.last_seen_generation IS NOT NEW.last_seen_generation OR OLD.discovered_generation IS NOT NEW.discovered_generation OR OLD.content_fingerprint IS NOT NEW.content_fingerprint OR OLD.layout_fingerprint IS NOT NEW.layout_fingerprint OR OLD.relocated_to_id IS NOT NEW.relocated_to_id OR OLD.source_root IS NOT NEW.source_root OR OLD.sidecar_json IS NOT NEW.sidecar_json OR OLD.sidecar_pending IS NOT NEW.sidecar_pending OR OLD.sidecar_attempts IS NOT NEW.sidecar_attempts OR OLD.sidecar_revision IS NOT NEW.sidecar_revision OR OLD.sidecar_transient_attempts IS NOT NEW.sidecar_transient_attempts OR OLD.sidecar_retry_at IS NOT NEW.sidecar_retry_at OR OLD.sidecar_failure IS NOT NEW.sidecar_failure)
 BEGIN
 INSERT INTO source_catalog_entry_publication_before(operation_id,existed,id,file_source_id,root_path,primary_code,title,created_at,updated_at,availability_status,status_checked_at,last_seen_generation,discovered_generation,content_fingerprint,layout_fingerprint,relocated_to_id,source_root,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.file_source_id,OLD.root_path,OLD.primary_code,OLD.title,OLD.created_at,OLD.updated_at,OLD.availability_status,OLD.status_checked_at,OLD.last_seen_generation,OLD.discovered_generation,OLD.content_fingerprint,OLD.layout_fingerprint,OLD.relocated_to_id,OLD.source_root,OLD.sidecar_json,OLD.sidecar_pending,OLD.sidecar_attempts,OLD.sidecar_revision,OLD.sidecar_transient_attempts,OLD.sidecar_retry_at,OLD.sidecar_failure)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,file_source_id=excluded.file_source_id,root_path=excluded.root_path,primary_code=excluded.primary_code,title=excluded.title,created_at=excluded.created_at,updated_at=excluded.updated_at,availability_status=excluded.availability_status,status_checked_at=excluded.status_checked_at,last_seen_generation=excluded.last_seen_generation,discovered_generation=excluded.discovered_generation,content_fingerprint=excluded.content_fingerprint,layout_fingerprint=excluded.layout_fingerprint,relocated_to_id=excluded.relocated_to_id,source_root=excluded.source_root,sidecar_json=excluded.sidecar_json,sidecar_pending=excluded.sidecar_pending,sidecar_attempts=excluded.sidecar_attempts,sidecar_revision=excluded.sidecar_revision,sidecar_transient_attempts=excluded.sidecar_transient_attempts,sidecar_retry_at=excluded.sidecar_retry_at,sidecar_failure=excluded.sidecar_failure
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.id ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_new_key_source_catalog_entry BEFORE UPDATE ON source_catalog_entry
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id)
 BEGIN
 INSERT INTO source_catalog_entry_publication_before(operation_id,existed,id,file_source_id,root_path,primary_code,title,created_at,updated_at,availability_status,status_checked_at,last_seen_generation,discovered_generation,content_fingerprint,layout_fingerprint,relocated_to_id,source_root,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.file_source_id,NEW.root_path,NEW.primary_code,NEW.title,NEW.created_at,NEW.updated_at,NEW.availability_status,NEW.status_checked_at,NEW.last_seen_generation,NEW.discovered_generation,NEW.content_fingerprint,NEW.layout_fingerprint,NEW.relocated_to_id,NEW.source_root,NEW.sidecar_json,NEW.sidecar_pending,NEW.sidecar_attempts,NEW.sidecar_revision,NEW.sidecar_transient_attempts,NEW.sidecar_retry_at,NEW.sidecar_failure)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,file_source_id=excluded.file_source_id,root_path=excluded.root_path,primary_code=excluded.primary_code,title=excluded.title,created_at=excluded.created_at,updated_at=excluded.updated_at,availability_status=excluded.availability_status,status_checked_at=excluded.status_checked_at,last_seen_generation=excluded.last_seen_generation,discovered_generation=excluded.discovered_generation,content_fingerprint=excluded.content_fingerprint,layout_fingerprint=excluded.layout_fingerprint,relocated_to_id=excluded.relocated_to_id,source_root=excluded.source_root,sidecar_json=excluded.sidecar_json,sidecar_pending=excluded.sidecar_pending,sidecar_attempts=excluded.sidecar_attempts,sidecar_revision=excluded.sidecar_revision,sidecar_transient_attempts=excluded.sidecar_transient_attempts,sidecar_retry_at=excluded.sidecar_retry_at,sidecar_failure=excluded.sidecar_failure WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_source_catalog_entry_update BEFORE UPDATE OF id,file_source_id,root_path,primary_code,title,created_at,updated_at,availability_status,status_checked_at,last_seen_generation,discovered_generation,content_fingerprint,layout_fingerprint,relocated_to_id,source_root,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure ON source_catalog_entry
 WHEN EXISTS(SELECT 1 FROM source_catalog_entry_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.id=OLD.id) OR (b.id=NEW.id)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.id IS NOT NEW.id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.root_path IS NOT NEW.root_path OR OLD.primary_code IS NOT NEW.primary_code OR OLD.title IS NOT NEW.title OR OLD.created_at IS NOT NEW.created_at OR OLD.updated_at IS NOT NEW.updated_at OR OLD.availability_status IS NOT NEW.availability_status OR OLD.status_checked_at IS NOT NEW.status_checked_at OR OLD.last_seen_generation IS NOT NEW.last_seen_generation OR OLD.discovered_generation IS NOT NEW.discovered_generation OR OLD.content_fingerprint IS NOT NEW.content_fingerprint OR OLD.layout_fingerprint IS NOT NEW.layout_fingerprint OR OLD.relocated_to_id IS NOT NEW.relocated_to_id OR OLD.source_root IS NOT NEW.source_root OR OLD.sidecar_json IS NOT NEW.sidecar_json OR OLD.sidecar_pending IS NOT NEW.sidecar_pending OR OLD.sidecar_attempts IS NOT NEW.sidecar_attempts OR OLD.sidecar_revision IS NOT NEW.sidecar_revision OR OLD.sidecar_transient_attempts IS NOT NEW.sidecar_transient_attempts OR OLD.sidecar_retry_at IS NOT NEW.sidecar_retry_at OR OLD.sidecar_failure IS NOT NEW.sidecar_failure)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_entry_update BEFORE UPDATE OF id,file_source_id,root_path,primary_code,title,created_at,updated_at,availability_status,status_checked_at,last_seen_generation,discovered_generation,content_fingerprint,layout_fingerprint,relocated_to_id,source_root,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure ON source_catalog_entry
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.id IS NOT NEW.id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.root_path IS NOT NEW.root_path OR OLD.primary_code IS NOT NEW.primary_code OR OLD.title IS NOT NEW.title OR OLD.created_at IS NOT NEW.created_at OR OLD.updated_at IS NOT NEW.updated_at OR OLD.availability_status IS NOT NEW.availability_status OR OLD.status_checked_at IS NOT NEW.status_checked_at OR OLD.last_seen_generation IS NOT NEW.last_seen_generation OR OLD.discovered_generation IS NOT NEW.discovered_generation OR OLD.content_fingerprint IS NOT NEW.content_fingerprint OR OLD.layout_fingerprint IS NOT NEW.layout_fingerprint OR OLD.relocated_to_id IS NOT NEW.relocated_to_id OR OLD.source_root IS NOT NEW.source_root OR OLD.sidecar_json IS NOT NEW.sidecar_json OR OLD.sidecar_pending IS NOT NEW.sidecar_pending OR OLD.sidecar_attempts IS NOT NEW.sidecar_attempts OR OLD.sidecar_revision IS NOT NEW.sidecar_revision OR OLD.sidecar_transient_attempts IS NOT NEW.sidecar_transient_attempts OR OLD.sidecar_retry_at IS NOT NEW.sidecar_retry_at OR OLD.sidecar_failure IS NOT NEW.sidecar_failure)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_entry_delete BEFORE DELETE ON source_catalog_entry
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_entry_publication_before(operation_id,existed,id,file_source_id,root_path,primary_code,title,created_at,updated_at,availability_status,status_checked_at,last_seen_generation,discovered_generation,content_fingerprint,layout_fingerprint,relocated_to_id,source_root,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.file_source_id,OLD.root_path,OLD.primary_code,OLD.title,OLD.created_at,OLD.updated_at,OLD.availability_status,OLD.status_checked_at,OLD.last_seen_generation,OLD.discovered_generation,OLD.content_fingerprint,OLD.layout_fingerprint,OLD.relocated_to_id,OLD.source_root,OLD.sidecar_json,OLD.sidecar_pending,OLD.sidecar_attempts,OLD.sidecar_revision,OLD.sidecar_transient_attempts,OLD.sidecar_retry_at,OLD.sidecar_failure)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,file_source_id=excluded.file_source_id,root_path=excluded.root_path,primary_code=excluded.primary_code,title=excluded.title,created_at=excluded.created_at,updated_at=excluded.updated_at,availability_status=excluded.availability_status,status_checked_at=excluded.status_checked_at,last_seen_generation=excluded.last_seen_generation,discovered_generation=excluded.discovered_generation,content_fingerprint=excluded.content_fingerprint,layout_fingerprint=excluded.layout_fingerprint,relocated_to_id=excluded.relocated_to_id,source_root=excluded.source_root,sidecar_json=excluded.sidecar_json,sidecar_pending=excluded.sidecar_pending,sidecar_attempts=excluded.sidecar_attempts,sidecar_revision=excluded.sidecar_revision,sidecar_transient_attempts=excluded.sidecar_transient_attempts,sidecar_retry_at=excluded.sidecar_retry_at,sidecar_failure=excluded.sidecar_failure
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_entry_delete BEFORE DELETE ON source_catalog_entry
 WHEN EXISTS(SELECT 1 FROM source_catalog_entry_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=OLD.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_entry_delete BEFORE DELETE ON source_catalog_entry
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE source_catalog_file_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 catalog_entry_id INTEGER,
 relative_path TEXT,
 title TEXT,
 kind TEXT,
 size_bytes INTEGER,
 content_hash TEXT,
 observed_generation INTEGER,
 modified_at TEXT,
 parent_path TEXT,
 source_root TEXT,
 PRIMARY KEY(catalog_entry_id,relative_path)
);
CREATE INDEX source_catalog_file_publication_before_operation ON source_catalog_file_publication_before(operation_id);

CREATE VIEW published_source_catalog_file AS
 SELECT live.catalog_entry_id,live.relative_path,live.title,live.kind,live.size_bytes,live.content_hash,live.observed_generation,live.modified_at,live.parent_path,live.source_root FROM source_catalog_file live
 WHERE NOT EXISTS(SELECT 1 FROM source_catalog_file_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.catalog_entry_id=live.catalog_entry_id AND b.relative_path=live.relative_path AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.catalog_entry_id,b.relative_path,b.title,b.kind,b.size_bytes,b.content_hash,b.observed_generation,b.modified_at,b.parent_path,b.source_root FROM source_catalog_file_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN source_catalog_file live ON b.catalog_entry_id=live.catalog_entry_id AND b.relative_path=live.relative_path
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_source_catalog_file_insert AFTER INSERT ON source_catalog_file
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_file_publication_before(operation_id,existed,catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation,modified_at,parent_path,source_root)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.catalog_entry_id,NEW.relative_path,NEW.title,NEW.kind,NEW.size_bytes,NEW.content_hash,NEW.observed_generation,NEW.modified_at,NEW.parent_path,NEW.source_root)
 ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,observed_generation=excluded.observed_generation,modified_at=excluded.modified_at,parent_path=excluded.parent_path,source_root=excluded.source_root
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_file_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_file_insert BEFORE INSERT ON source_catalog_file
 WHEN EXISTS(SELECT 1 FROM source_catalog_file_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=NEW.catalog_entry_id AND b.relative_path=NEW.relative_path) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_file_insert BEFORE INSERT ON source_catalog_file
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=NEW.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_file_update BEFORE UPDATE OF catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation,modified_at,parent_path,source_root ON source_catalog_file
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.relative_path IS NOT NEW.relative_path OR OLD.title IS NOT NEW.title OR OLD.kind IS NOT NEW.kind OR OLD.size_bytes IS NOT NEW.size_bytes OR OLD.content_hash IS NOT NEW.content_hash OR OLD.observed_generation IS NOT NEW.observed_generation OR OLD.modified_at IS NOT NEW.modified_at OR OLD.parent_path IS NOT NEW.parent_path OR OLD.source_root IS NOT NEW.source_root)
 BEGIN
 INSERT INTO source_catalog_file_publication_before(operation_id,existed,catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation,modified_at,parent_path,source_root)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.catalog_entry_id,OLD.relative_path,OLD.title,OLD.kind,OLD.size_bytes,OLD.content_hash,OLD.observed_generation,OLD.modified_at,OLD.parent_path,OLD.source_root)
 ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,observed_generation=excluded.observed_generation,modified_at=excluded.modified_at,parent_path=excluded.parent_path,source_root=excluded.source_root
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_file_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_new_key_source_catalog_file BEFORE UPDATE ON source_catalog_file
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.relative_path IS NOT NEW.relative_path)
 BEGIN
 INSERT INTO source_catalog_file_publication_before(operation_id,existed,catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation,modified_at,parent_path,source_root) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.catalog_entry_id,NEW.relative_path,NEW.title,NEW.kind,NEW.size_bytes,NEW.content_hash,NEW.observed_generation,NEW.modified_at,NEW.parent_path,NEW.source_root)
 ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,observed_generation=excluded.observed_generation,modified_at=excluded.modified_at,parent_path=excluded.parent_path,source_root=excluded.source_root WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_file_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_source_catalog_file_update BEFORE UPDATE OF catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation,modified_at,parent_path,source_root ON source_catalog_file
 WHEN EXISTS(SELECT 1 FROM source_catalog_file_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.catalog_entry_id=OLD.catalog_entry_id AND b.relative_path=OLD.relative_path) OR (b.catalog_entry_id=NEW.catalog_entry_id AND b.relative_path=NEW.relative_path)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.relative_path IS NOT NEW.relative_path OR OLD.title IS NOT NEW.title OR OLD.kind IS NOT NEW.kind OR OLD.size_bytes IS NOT NEW.size_bytes OR OLD.content_hash IS NOT NEW.content_hash OR OLD.observed_generation IS NOT NEW.observed_generation OR OLD.modified_at IS NOT NEW.modified_at OR OLD.parent_path IS NOT NEW.parent_path OR OLD.source_root IS NOT NEW.source_root)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_file_update BEFORE UPDATE OF catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation,modified_at,parent_path,source_root ON source_catalog_file
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=OLD.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.relative_path IS NOT NEW.relative_path OR OLD.title IS NOT NEW.title OR OLD.kind IS NOT NEW.kind OR OLD.size_bytes IS NOT NEW.size_bytes OR OLD.content_hash IS NOT NEW.content_hash OR OLD.observed_generation IS NOT NEW.observed_generation OR OLD.modified_at IS NOT NEW.modified_at OR OLD.parent_path IS NOT NEW.parent_path OR OLD.source_root IS NOT NEW.source_root)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_file_delete BEFORE DELETE ON source_catalog_file
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_file_publication_before(operation_id,existed,catalog_entry_id,relative_path,title,kind,size_bytes,content_hash,observed_generation,modified_at,parent_path,source_root)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.catalog_entry_id,OLD.relative_path,OLD.title,OLD.kind,OLD.size_bytes,OLD.content_hash,OLD.observed_generation,OLD.modified_at,OLD.parent_path,OLD.source_root)
 ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,observed_generation=excluded.observed_generation,modified_at=excluded.modified_at,parent_path=excluded.parent_path,source_root=excluded.source_root
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_file_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_file_delete BEFORE DELETE ON source_catalog_file
 WHEN EXISTS(SELECT 1 FROM source_catalog_file_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=OLD.catalog_entry_id AND b.relative_path=OLD.relative_path) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_file_delete BEFORE DELETE ON source_catalog_file
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=OLD.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE source_catalog_search_doc_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 id INTEGER,
 entity_kind TEXT,
 entity_key TEXT,
 catalog_entry_id INTEGER,
 series_id INTEGER,
 file_source_id INTEGER,
 body TEXT,
 PRIMARY KEY(id)
);
CREATE INDEX source_catalog_search_doc_publication_before_operation ON source_catalog_search_doc_publication_before(operation_id);

CREATE VIEW published_source_catalog_search_doc AS
 SELECT live.id,live.entity_kind,live.entity_key,live.catalog_entry_id,live.series_id,live.file_source_id,live.body,live.id AS rowid FROM source_catalog_search_doc live
 WHERE NOT EXISTS(SELECT 1 FROM source_catalog_search_doc_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.id=live.id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.id,b.entity_kind,b.entity_key,b.catalog_entry_id,b.series_id,b.file_source_id,b.body,b.id AS rowid FROM source_catalog_search_doc_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN source_catalog_search_doc live ON b.id=live.id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_source_catalog_search_doc_insert AFTER INSERT ON source_catalog_search_doc
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_search_doc_publication_before(operation_id,existed,id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.entity_kind,NEW.entity_key,NEW.catalog_entry_id,NEW.series_id,NEW.file_source_id,NEW.body)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,entity_kind=excluded.entity_kind,entity_key=excluded.entity_key,catalog_entry_id=excluded.catalog_entry_id,series_id=excluded.series_id,file_source_id=excluded.file_source_id,body=excluded.body
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_search_doc_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_search_doc_insert BEFORE INSERT ON source_catalog_search_doc
 WHEN EXISTS(SELECT 1 FROM source_catalog_search_doc_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=NEW.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_search_doc_insert BEFORE INSERT ON source_catalog_search_doc
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=NEW.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_search_doc_update BEFORE UPDATE OF id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body ON source_catalog_search_doc
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id OR OLD.entity_kind IS NOT NEW.entity_kind OR OLD.entity_key IS NOT NEW.entity_key OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.series_id IS NOT NEW.series_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.body IS NOT NEW.body)
 BEGIN
 INSERT INTO source_catalog_search_doc_publication_before(operation_id,existed,id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.entity_kind,OLD.entity_key,OLD.catalog_entry_id,OLD.series_id,OLD.file_source_id,OLD.body)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,entity_kind=excluded.entity_kind,entity_key=excluded.entity_key,catalog_entry_id=excluded.catalog_entry_id,series_id=excluded.series_id,file_source_id=excluded.file_source_id,body=excluded.body
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_search_doc_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_new_key_source_catalog_search_doc BEFORE UPDATE ON source_catalog_search_doc
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id)
 BEGIN
 INSERT INTO source_catalog_search_doc_publication_before(operation_id,existed,id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.entity_kind,NEW.entity_key,NEW.catalog_entry_id,NEW.series_id,NEW.file_source_id,NEW.body)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,entity_kind=excluded.entity_kind,entity_key=excluded.entity_key,catalog_entry_id=excluded.catalog_entry_id,series_id=excluded.series_id,file_source_id=excluded.file_source_id,body=excluded.body WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_search_doc_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_source_catalog_search_doc_update BEFORE UPDATE OF id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body ON source_catalog_search_doc
 WHEN EXISTS(SELECT 1 FROM source_catalog_search_doc_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.id=OLD.id) OR (b.id=NEW.id)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.id IS NOT NEW.id OR OLD.entity_kind IS NOT NEW.entity_kind OR OLD.entity_key IS NOT NEW.entity_key OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.series_id IS NOT NEW.series_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.body IS NOT NEW.body)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_search_doc_update BEFORE UPDATE OF id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body ON source_catalog_search_doc
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.id IS NOT NEW.id OR OLD.entity_kind IS NOT NEW.entity_kind OR OLD.entity_key IS NOT NEW.entity_key OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.series_id IS NOT NEW.series_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.body IS NOT NEW.body)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_search_doc_delete BEFORE DELETE ON source_catalog_search_doc
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_search_doc_publication_before(operation_id,existed,id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.entity_kind,OLD.entity_key,OLD.catalog_entry_id,OLD.series_id,OLD.file_source_id,OLD.body)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,entity_kind=excluded.entity_kind,entity_key=excluded.entity_key,catalog_entry_id=excluded.catalog_entry_id,series_id=excluded.series_id,file_source_id=excluded.file_source_id,body=excluded.body
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_search_doc_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_search_doc_delete BEFORE DELETE ON source_catalog_search_doc
 WHEN EXISTS(SELECT 1 FROM source_catalog_search_doc_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=OLD.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_search_doc_delete BEFORE DELETE ON source_catalog_search_doc
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE source_catalog_entry_admission_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 catalog_entry_id INTEGER,
 generation INTEGER,
 files INTEGER,
 audio INTEGER,
 non_audio INTEGER,
 updated_at TEXT,
 PRIMARY KEY(catalog_entry_id)
);
CREATE INDEX source_catalog_entry_admission_publication_before_operation ON source_catalog_entry_admission_publication_before(operation_id);

CREATE VIEW published_source_catalog_entry_admission AS
 SELECT live.catalog_entry_id,live.generation,live.files,live.audio,live.non_audio,live.updated_at FROM source_catalog_entry_admission live
 WHERE NOT EXISTS(SELECT 1 FROM source_catalog_entry_admission_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.catalog_entry_id=live.catalog_entry_id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.catalog_entry_id,b.generation,b.files,b.audio,b.non_audio,b.updated_at FROM source_catalog_entry_admission_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN source_catalog_entry_admission live ON b.catalog_entry_id=live.catalog_entry_id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_source_catalog_entry_admission_insert AFTER INSERT ON source_catalog_entry_admission
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_entry_admission_publication_before(operation_id,existed,catalog_entry_id,generation,files,audio,non_audio,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.catalog_entry_id,NEW.generation,NEW.files,NEW.audio,NEW.non_audio,NEW.updated_at)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,generation=excluded.generation,files=excluded.files,audio=excluded.audio,non_audio=excluded.non_audio,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_admission_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_entry_admission_insert BEFORE INSERT ON source_catalog_entry_admission
 WHEN EXISTS(SELECT 1 FROM source_catalog_entry_admission_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=NEW.catalog_entry_id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_entry_admission_insert BEFORE INSERT ON source_catalog_entry_admission
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=NEW.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_entry_admission_update BEFORE UPDATE OF catalog_entry_id,generation,files,audio,non_audio,updated_at ON source_catalog_entry_admission
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.generation IS NOT NEW.generation OR OLD.files IS NOT NEW.files OR OLD.audio IS NOT NEW.audio OR OLD.non_audio IS NOT NEW.non_audio OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN
 INSERT INTO source_catalog_entry_admission_publication_before(operation_id,existed,catalog_entry_id,generation,files,audio,non_audio,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.catalog_entry_id,OLD.generation,OLD.files,OLD.audio,OLD.non_audio,OLD.updated_at)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,generation=excluded.generation,files=excluded.files,audio=excluded.audio,non_audio=excluded.non_audio,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_admission_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_new_key_source_catalog_entry_admission BEFORE UPDATE ON source_catalog_entry_admission
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id)
 BEGIN
 INSERT INTO source_catalog_entry_admission_publication_before(operation_id,existed,catalog_entry_id,generation,files,audio,non_audio,updated_at) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.catalog_entry_id,NEW.generation,NEW.files,NEW.audio,NEW.non_audio,NEW.updated_at)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,generation=excluded.generation,files=excluded.files,audio=excluded.audio,non_audio=excluded.non_audio,updated_at=excluded.updated_at WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_admission_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_source_catalog_entry_admission_update BEFORE UPDATE OF catalog_entry_id,generation,files,audio,non_audio,updated_at ON source_catalog_entry_admission
 WHEN EXISTS(SELECT 1 FROM source_catalog_entry_admission_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.catalog_entry_id=OLD.catalog_entry_id) OR (b.catalog_entry_id=NEW.catalog_entry_id)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.generation IS NOT NEW.generation OR OLD.files IS NOT NEW.files OR OLD.audio IS NOT NEW.audio OR OLD.non_audio IS NOT NEW.non_audio OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_entry_admission_update BEFORE UPDATE OF catalog_entry_id,generation,files,audio,non_audio,updated_at ON source_catalog_entry_admission
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=OLD.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.generation IS NOT NEW.generation OR OLD.files IS NOT NEW.files OR OLD.audio IS NOT NEW.audio OR OLD.non_audio IS NOT NEW.non_audio OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_entry_admission_delete BEFORE DELETE ON source_catalog_entry_admission
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_entry_admission_publication_before(operation_id,existed,catalog_entry_id,generation,files,audio,non_audio,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.catalog_entry_id,OLD.generation,OLD.files,OLD.audio,OLD.non_audio,OLD.updated_at)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,generation=excluded.generation,files=excluded.files,audio=excluded.audio,non_audio=excluded.non_audio,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_entry_admission_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_entry_admission_delete BEFORE DELETE ON source_catalog_entry_admission
 WHEN EXISTS(SELECT 1 FROM source_catalog_entry_admission_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=OLD.catalog_entry_id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_entry_admission_delete BEFORE DELETE ON source_catalog_entry_admission
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=OLD.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE catalog_series_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 id INTEGER,
 title TEXT,
 source_id INTEGER,
 source_key TEXT,
 created_at TEXT,
 PRIMARY KEY(id)
);
CREATE INDEX catalog_series_publication_before_operation ON catalog_series_publication_before(operation_id);

CREATE VIEW published_catalog_series AS
 SELECT live.id,live.title,live.work_id,live.source_id,live.source_key,live.created_at,live.id AS rowid FROM catalog_series live
 WHERE NOT EXISTS(SELECT 1 FROM catalog_series_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.id=live.id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.id,b.title,live.work_id,b.source_id,b.source_key,b.created_at,b.id AS rowid FROM catalog_series_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN catalog_series live ON b.id=live.id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_catalog_series_insert AFTER INSERT ON catalog_series
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO catalog_series_publication_before(operation_id,existed,id,title,source_id,source_key,created_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.title,NEW.source_id,NEW.source_key,NEW.created_at)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,source_id=excluded.source_id,source_key=excluded.source_key,created_at=excluded.created_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',NEW.id) ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_catalog_series_insert BEFORE INSERT ON catalog_series
 WHEN EXISTS(SELECT 1 FROM catalog_series_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=NEW.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_catalog_series_insert BEFORE INSERT ON catalog_series
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=NEW.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_catalog_series_update BEFORE UPDATE OF id,title,source_id,source_key,created_at ON catalog_series
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id OR OLD.title IS NOT NEW.title OR OLD.source_id IS NOT NEW.source_id OR OLD.source_key IS NOT NEW.source_key OR OLD.created_at IS NOT NEW.created_at)
 BEGIN
 INSERT INTO catalog_series_publication_before(operation_id,existed,id,title,source_id,source_key,created_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.title,OLD.source_id,OLD.source_key,OLD.created_at)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,source_id=excluded.source_id,source_key=excluded.source_key,created_at=excluded.created_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',OLD.id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',NEW.id) ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_new_key_catalog_series BEFORE UPDATE ON catalog_series
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id)
 BEGIN
 INSERT INTO catalog_series_publication_before(operation_id,existed,id,title,source_id,source_key,created_at) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.title,NEW.source_id,NEW.source_key,NEW.created_at)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,source_id=excluded.source_id,source_key=excluded.source_key,created_at=excluded.created_at WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_catalog_series_update BEFORE UPDATE OF id,title,source_id,source_key,created_at ON catalog_series
 WHEN EXISTS(SELECT 1 FROM catalog_series_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.id=OLD.id) OR (b.id=NEW.id)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.id IS NOT NEW.id OR OLD.title IS NOT NEW.title OR OLD.source_id IS NOT NEW.source_id OR OLD.source_key IS NOT NEW.source_key OR OLD.created_at IS NOT NEW.created_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_catalog_series_update BEFORE UPDATE OF id,title,source_id,source_key,created_at ON catalog_series
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.id IS NOT NEW.id OR OLD.title IS NOT NEW.title OR OLD.source_id IS NOT NEW.source_id OR OLD.source_key IS NOT NEW.source_key OR OLD.created_at IS NOT NEW.created_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_catalog_series_delete BEFORE DELETE ON catalog_series
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO catalog_series_publication_before(operation_id,existed,id,title,source_id,source_key,created_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.title,OLD.source_id,OLD.source_key,OLD.created_at)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,title=excluded.title,source_id=excluded.source_id,source_key=excluded.source_key,created_at=excluded.created_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',OLD.id) ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_catalog_series_delete BEFORE DELETE ON catalog_series
 WHEN EXISTS(SELECT 1 FROM catalog_series_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=OLD.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_catalog_series_delete BEFORE DELETE ON catalog_series
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE catalog_series_member_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 series_id INTEGER,
 catalog_entry_id INTEGER,
 position INTEGER,
 evidence TEXT,
 PRIMARY KEY(catalog_entry_id)
);
CREATE INDEX catalog_series_member_publication_before_operation ON catalog_series_member_publication_before(operation_id);

CREATE VIEW published_catalog_series_member AS
 SELECT live.series_id,live.catalog_entry_id,live.position,live.evidence FROM catalog_series_member live
 WHERE NOT EXISTS(SELECT 1 FROM catalog_series_member_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.catalog_entry_id=live.catalog_entry_id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.series_id,b.catalog_entry_id,b.position,b.evidence FROM catalog_series_member_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN catalog_series_member live ON b.catalog_entry_id=live.catalog_entry_id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_catalog_series_member_insert AFTER INSERT ON catalog_series_member
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO catalog_series_member_publication_before(operation_id,existed,series_id,catalog_entry_id,position,evidence)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.series_id,NEW.catalog_entry_id,NEW.position,NEW.evidence)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,series_id=excluded.series_id,position=excluded.position,evidence=excluded.evidence
 WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_member_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',NEW.series_id) ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_catalog_series_member_insert BEFORE INSERT ON catalog_series_member
 WHEN EXISTS(SELECT 1 FROM catalog_series_member_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=NEW.catalog_entry_id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_catalog_series_member_insert BEFORE INSERT ON catalog_series_member
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=NEW.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_catalog_series_member_update BEFORE UPDATE OF series_id,catalog_entry_id,position,evidence ON catalog_series_member
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.series_id IS NOT NEW.series_id OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.position IS NOT NEW.position OR OLD.evidence IS NOT NEW.evidence)
 BEGIN
 INSERT INTO catalog_series_member_publication_before(operation_id,existed,series_id,catalog_entry_id,position,evidence)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.series_id,OLD.catalog_entry_id,OLD.position,OLD.evidence)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,series_id=excluded.series_id,position=excluded.position,evidence=excluded.evidence
 WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_member_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',OLD.series_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',NEW.series_id) ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_new_key_catalog_series_member BEFORE UPDATE ON catalog_series_member
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id)
 BEGIN
 INSERT INTO catalog_series_member_publication_before(operation_id,existed,series_id,catalog_entry_id,position,evidence) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.series_id,NEW.catalog_entry_id,NEW.position,NEW.evidence)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,series_id=excluded.series_id,position=excluded.position,evidence=excluded.evidence WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_member_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_catalog_series_member_update BEFORE UPDATE OF series_id,catalog_entry_id,position,evidence ON catalog_series_member
 WHEN EXISTS(SELECT 1 FROM catalog_series_member_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.catalog_entry_id=OLD.catalog_entry_id) OR (b.catalog_entry_id=NEW.catalog_entry_id)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.series_id IS NOT NEW.series_id OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.position IS NOT NEW.position OR OLD.evidence IS NOT NEW.evidence)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_catalog_series_member_update BEFORE UPDATE OF series_id,catalog_entry_id,position,evidence ON catalog_series_member
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=OLD.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.series_id IS NOT NEW.series_id OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.position IS NOT NEW.position OR OLD.evidence IS NOT NEW.evidence)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_catalog_series_member_delete BEFORE DELETE ON catalog_series_member
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO catalog_series_member_publication_before(operation_id,existed,series_id,catalog_entry_id,position,evidence)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.series_id,OLD.catalog_entry_id,OLD.position,OLD.evidence)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,series_id=excluded.series_id,position=excluded.position,evidence=excluded.evidence
 WHERE (SELECT state FROM source_catalog_publication WHERE id=catalog_series_member_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',OLD.series_id) ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_catalog_series_member_delete BEFORE DELETE ON catalog_series_member
 WHEN EXISTS(SELECT 1 FROM catalog_series_member_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=OLD.catalog_entry_id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_catalog_series_member_delete BEFORE DELETE ON catalog_series_member
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=OLD.catalog_entry_id) AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE source_catalog_episode_pending_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 source_id INTEGER,
 parent_root TEXT,
 catalog_entry_root TEXT,
 catalog_entry_id INTEGER,
 PRIMARY KEY(catalog_entry_id)
);
CREATE INDEX source_catalog_episode_pending_publication_before_operation ON source_catalog_episode_pending_publication_before(operation_id);

CREATE VIEW published_source_catalog_episode_pending AS
 SELECT live.source_id,live.parent_root,live.catalog_entry_root,live.catalog_entry_id FROM source_catalog_episode_pending live
 WHERE NOT EXISTS(SELECT 1 FROM source_catalog_episode_pending_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.catalog_entry_id=live.catalog_entry_id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.source_id,b.parent_root,b.catalog_entry_root,b.catalog_entry_id FROM source_catalog_episode_pending_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN source_catalog_episode_pending live ON b.catalog_entry_id=live.catalog_entry_id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_source_catalog_episode_pending_insert AFTER INSERT ON source_catalog_episode_pending
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_episode_pending_publication_before(operation_id,existed,source_id,parent_root,catalog_entry_root,catalog_entry_id)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.source_id,NEW.parent_root,NEW.catalog_entry_root,NEW.catalog_entry_id)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,parent_root=excluded.parent_root,catalog_entry_root=excluded.catalog_entry_root
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_pending_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_episode_pending_insert BEFORE INSERT ON source_catalog_episode_pending
 WHEN EXISTS(SELECT 1 FROM source_catalog_episode_pending_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=NEW.catalog_entry_id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_episode_pending_insert BEFORE INSERT ON source_catalog_episode_pending
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=NEW.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_episode_pending_update BEFORE UPDATE OF source_id,parent_root,catalog_entry_root,catalog_entry_id ON source_catalog_episode_pending
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.catalog_entry_root IS NOT NEW.catalog_entry_root OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id)
 BEGIN
 INSERT INTO source_catalog_episode_pending_publication_before(operation_id,existed,source_id,parent_root,catalog_entry_root,catalog_entry_id)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.source_id,OLD.parent_root,OLD.catalog_entry_root,OLD.catalog_entry_id)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,parent_root=excluded.parent_root,catalog_entry_root=excluded.catalog_entry_root
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_pending_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',NEW.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_new_key_source_catalog_episode_pending BEFORE UPDATE ON source_catalog_episode_pending
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id)
 BEGIN
 INSERT INTO source_catalog_episode_pending_publication_before(operation_id,existed,source_id,parent_root,catalog_entry_root,catalog_entry_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.source_id,NEW.parent_root,NEW.catalog_entry_root,NEW.catalog_entry_id)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,parent_root=excluded.parent_root,catalog_entry_root=excluded.catalog_entry_root WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_pending_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_source_catalog_episode_pending_update BEFORE UPDATE OF source_id,parent_root,catalog_entry_root,catalog_entry_id ON source_catalog_episode_pending
 WHEN EXISTS(SELECT 1 FROM source_catalog_episode_pending_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.catalog_entry_id=OLD.catalog_entry_id) OR (b.catalog_entry_id=NEW.catalog_entry_id)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.catalog_entry_root IS NOT NEW.catalog_entry_root OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_episode_pending_update BEFORE UPDATE OF source_id,parent_root,catalog_entry_root,catalog_entry_id ON source_catalog_episode_pending
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.catalog_entry_root IS NOT NEW.catalog_entry_root OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_episode_pending_delete BEFORE DELETE ON source_catalog_episode_pending
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_episode_pending_publication_before(operation_id,existed,source_id,parent_root,catalog_entry_root,catalog_entry_id)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.source_id,OLD.parent_root,OLD.catalog_entry_root,OLD.catalog_entry_id)
 ON CONFLICT(catalog_entry_id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,parent_root=excluded.parent_root,catalog_entry_root=excluded.catalog_entry_root
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_pending_publication_before.operation_id)='committed';
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'entry',OLD.catalog_entry_id) ON CONFLICT DO NOTHING;
 INSERT INTO source_catalog_publication_entity(operation_id,entity_kind,entity_id) SELECT (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),'series',series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id ON CONFLICT DO NOTHING;

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_episode_pending_delete BEFORE DELETE ON source_catalog_episode_pending
 WHEN EXISTS(SELECT 1 FROM source_catalog_episode_pending_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.catalog_entry_id=OLD.catalog_entry_id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_episode_pending_delete BEFORE DELETE ON source_catalog_episode_pending
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE source_catalog_episode_publication_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 source_id INTEGER,
 parent_root TEXT,
 state TEXT,
 updated_at TEXT,
 PRIMARY KEY(source_id,parent_root)
);
CREATE INDEX source_catalog_episode_publication_publication_before_operation ON source_catalog_episode_publication_publication_before(operation_id);

CREATE VIEW published_source_catalog_episode_publication AS
 SELECT live.source_id,live.parent_root,live.state,live.updated_at FROM source_catalog_episode_publication live
 WHERE NOT EXISTS(SELECT 1 FROM source_catalog_episode_publication_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.source_id=live.source_id AND b.parent_root=live.parent_root AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.source_id,b.parent_root,b.state,b.updated_at FROM source_catalog_episode_publication_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN source_catalog_episode_publication live ON b.source_id=live.source_id AND b.parent_root=live.parent_root
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_source_catalog_episode_publication_insert AFTER INSERT ON source_catalog_episode_publication
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_episode_publication_publication_before(operation_id,existed,source_id,parent_root,state,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.source_id,NEW.parent_root,NEW.state,NEW.updated_at)
 ON CONFLICT(source_id,parent_root) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,state=excluded.state,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_publication_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_episode_publication_insert BEFORE INSERT ON source_catalog_episode_publication
 WHEN EXISTS(SELECT 1 FROM source_catalog_episode_publication_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.source_id=NEW.source_id AND b.parent_root=NEW.parent_root) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_episode_publication_insert BEFORE INSERT ON source_catalog_episode_publication
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=NEW.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_episode_publication_update BEFORE UPDATE OF source_id,parent_root,state,updated_at ON source_catalog_episode_publication
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.state IS NOT NEW.state OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN
 INSERT INTO source_catalog_episode_publication_publication_before(operation_id,existed,source_id,parent_root,state,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.source_id,OLD.parent_root,OLD.state,OLD.updated_at)
 ON CONFLICT(source_id,parent_root) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,state=excluded.state,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_publication_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_new_key_source_catalog_episode_publication BEFORE UPDATE ON source_catalog_episode_publication
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root)
 BEGIN
 INSERT INTO source_catalog_episode_publication_publication_before(operation_id,existed,source_id,parent_root,state,updated_at) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.source_id,NEW.parent_root,NEW.state,NEW.updated_at)
 ON CONFLICT(source_id,parent_root) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,state=excluded.state,updated_at=excluded.updated_at WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_publication_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_source_catalog_episode_publication_update BEFORE UPDATE OF source_id,parent_root,state,updated_at ON source_catalog_episode_publication
 WHEN EXISTS(SELECT 1 FROM source_catalog_episode_publication_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.source_id=OLD.source_id AND b.parent_root=OLD.parent_root) OR (b.source_id=NEW.source_id AND b.parent_root=NEW.parent_root)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.state IS NOT NEW.state OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_episode_publication_update BEFORE UPDATE OF source_id,parent_root,state,updated_at ON source_catalog_episode_publication
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.state IS NOT NEW.state OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_source_catalog_episode_publication_delete BEFORE DELETE ON source_catalog_episode_publication
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO source_catalog_episode_publication_publication_before(operation_id,existed,source_id,parent_root,state,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.source_id,OLD.parent_root,OLD.state,OLD.updated_at)
 ON CONFLICT(source_id,parent_root) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,state=excluded.state,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=source_catalog_episode_publication_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_source_catalog_episode_publication_delete BEFORE DELETE ON source_catalog_episode_publication
 WHEN EXISTS(SELECT 1 FROM source_catalog_episode_publication_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.source_id=OLD.source_id AND b.parent_root=OLD.parent_root) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_source_catalog_episode_publication_delete BEFORE DELETE ON source_catalog_episode_publication
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE work_source_presence_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 work_id INTEGER,
 file_source_id INTEGER,
 presence_type TEXT,
 remote_id TEXT,
 remote_code TEXT,
 source_url TEXT,
 availability TEXT,
 raw_json TEXT,
 last_seen_at TEXT,
 last_checked_at TEXT,
 created_at TEXT,
 updated_at TEXT,
 PRIMARY KEY(work_id,file_source_id,presence_type)
);
CREATE INDEX work_source_presence_publication_before_operation ON work_source_presence_publication_before(operation_id);

CREATE VIEW published_work_source_presence AS
 SELECT live.work_id,live.file_source_id,live.presence_type,live.remote_id,live.remote_code,live.source_url,live.availability,live.raw_json,live.last_seen_at,live.last_checked_at,live.created_at,live.updated_at FROM work_source_presence live
 WHERE NOT EXISTS(SELECT 1 FROM work_source_presence_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.work_id=live.work_id AND b.file_source_id=live.file_source_id AND b.presence_type=live.presence_type AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.work_id,b.file_source_id,b.presence_type,b.remote_id,b.remote_code,b.source_url,b.availability,b.raw_json,b.last_seen_at,b.last_checked_at,b.created_at,b.updated_at FROM work_source_presence_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN work_source_presence live ON b.work_id=live.work_id AND b.file_source_id=live.file_source_id AND b.presence_type=live.presence_type
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_work_source_presence_insert AFTER INSERT ON work_source_presence
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND NEW.presence_type='source'
 BEGIN
 INSERT INTO work_source_presence_publication_before(operation_id,existed,work_id,file_source_id,presence_type,remote_id,remote_code,source_url,availability,raw_json,last_seen_at,last_checked_at,created_at,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.work_id,NEW.file_source_id,NEW.presence_type,NEW.remote_id,NEW.remote_code,NEW.source_url,NEW.availability,NEW.raw_json,NEW.last_seen_at,NEW.last_checked_at,NEW.created_at,NEW.updated_at)
 ON CONFLICT(work_id,file_source_id,presence_type) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,remote_id=excluded.remote_id,remote_code=excluded.remote_code,source_url=excluded.source_url,availability=excluded.availability,raw_json=excluded.raw_json,last_seen_at=excluded.last_seen_at,last_checked_at=excluded.last_checked_at,created_at=excluded.created_at,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=work_source_presence_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_work_source_presence_insert BEFORE INSERT ON work_source_presence
 WHEN EXISTS(SELECT 1 FROM work_source_presence_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.work_id=NEW.work_id AND b.file_source_id=NEW.file_source_id AND b.presence_type=NEW.presence_type) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_work_source_presence_insert BEFORE INSERT ON work_source_presence
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=NEW.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND NEW.presence_type='source'
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_work_source_presence_update BEFORE UPDATE OF work_id,file_source_id,presence_type,remote_id,remote_code,source_url,availability,raw_json,last_seen_at,last_checked_at,created_at,updated_at ON work_source_presence
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND OLD.presence_type='source' AND (OLD.work_id IS NOT NEW.work_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.presence_type IS NOT NEW.presence_type OR OLD.remote_id IS NOT NEW.remote_id OR OLD.remote_code IS NOT NEW.remote_code OR OLD.source_url IS NOT NEW.source_url OR OLD.availability IS NOT NEW.availability OR OLD.raw_json IS NOT NEW.raw_json OR OLD.last_seen_at IS NOT NEW.last_seen_at OR OLD.last_checked_at IS NOT NEW.last_checked_at OR OLD.created_at IS NOT NEW.created_at OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN
 INSERT INTO work_source_presence_publication_before(operation_id,existed,work_id,file_source_id,presence_type,remote_id,remote_code,source_url,availability,raw_json,last_seen_at,last_checked_at,created_at,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.work_id,OLD.file_source_id,OLD.presence_type,OLD.remote_id,OLD.remote_code,OLD.source_url,OLD.availability,OLD.raw_json,OLD.last_seen_at,OLD.last_checked_at,OLD.created_at,OLD.updated_at)
 ON CONFLICT(work_id,file_source_id,presence_type) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,remote_id=excluded.remote_id,remote_code=excluded.remote_code,source_url=excluded.source_url,availability=excluded.availability,raw_json=excluded.raw_json,last_seen_at=excluded.last_seen_at,last_checked_at=excluded.last_checked_at,created_at=excluded.created_at,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=work_source_presence_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_new_key_work_source_presence BEFORE UPDATE ON work_source_presence
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.work_id IS NOT NEW.work_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.presence_type IS NOT NEW.presence_type)
 BEGIN
 INSERT INTO work_source_presence_publication_before(operation_id,existed,work_id,file_source_id,presence_type,remote_id,remote_code,source_url,availability,raw_json,last_seen_at,last_checked_at,created_at,updated_at) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.work_id,NEW.file_source_id,NEW.presence_type,NEW.remote_id,NEW.remote_code,NEW.source_url,NEW.availability,NEW.raw_json,NEW.last_seen_at,NEW.last_checked_at,NEW.created_at,NEW.updated_at)
 ON CONFLICT(work_id,file_source_id,presence_type) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,remote_id=excluded.remote_id,remote_code=excluded.remote_code,source_url=excluded.source_url,availability=excluded.availability,raw_json=excluded.raw_json,last_seen_at=excluded.last_seen_at,last_checked_at=excluded.last_checked_at,created_at=excluded.created_at,updated_at=excluded.updated_at WHERE (SELECT state FROM source_catalog_publication WHERE id=work_source_presence_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_work_source_presence_update BEFORE UPDATE OF work_id,file_source_id,presence_type,remote_id,remote_code,source_url,availability,raw_json,last_seen_at,last_checked_at,created_at,updated_at ON work_source_presence
 WHEN EXISTS(SELECT 1 FROM work_source_presence_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.work_id=OLD.work_id AND b.file_source_id=OLD.file_source_id AND b.presence_type=OLD.presence_type) OR (b.work_id=NEW.work_id AND b.file_source_id=NEW.file_source_id AND b.presence_type=NEW.presence_type)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.work_id IS NOT NEW.work_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.presence_type IS NOT NEW.presence_type OR OLD.remote_id IS NOT NEW.remote_id OR OLD.remote_code IS NOT NEW.remote_code OR OLD.source_url IS NOT NEW.source_url OR OLD.availability IS NOT NEW.availability OR OLD.raw_json IS NOT NEW.raw_json OR OLD.last_seen_at IS NOT NEW.last_seen_at OR OLD.last_checked_at IS NOT NEW.last_checked_at OR OLD.created_at IS NOT NEW.created_at OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_work_source_presence_update BEFORE UPDATE OF work_id,file_source_id,presence_type,remote_id,remote_code,source_url,availability,raw_json,last_seen_at,last_checked_at,created_at,updated_at ON work_source_presence
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND OLD.presence_type='source' AND (OLD.work_id IS NOT NEW.work_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.presence_type IS NOT NEW.presence_type OR OLD.remote_id IS NOT NEW.remote_id OR OLD.remote_code IS NOT NEW.remote_code OR OLD.source_url IS NOT NEW.source_url OR OLD.availability IS NOT NEW.availability OR OLD.raw_json IS NOT NEW.raw_json OR OLD.last_seen_at IS NOT NEW.last_seen_at OR OLD.last_checked_at IS NOT NEW.last_checked_at OR OLD.created_at IS NOT NEW.created_at OR OLD.updated_at IS NOT NEW.updated_at)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_work_source_presence_delete BEFORE DELETE ON work_source_presence
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND OLD.presence_type='source'
 BEGIN
 INSERT INTO work_source_presence_publication_before(operation_id,existed,work_id,file_source_id,presence_type,remote_id,remote_code,source_url,availability,raw_json,last_seen_at,last_checked_at,created_at,updated_at)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.work_id,OLD.file_source_id,OLD.presence_type,OLD.remote_id,OLD.remote_code,OLD.source_url,OLD.availability,OLD.raw_json,OLD.last_seen_at,OLD.last_checked_at,OLD.created_at,OLD.updated_at)
 ON CONFLICT(work_id,file_source_id,presence_type) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,remote_id=excluded.remote_id,remote_code=excluded.remote_code,source_url=excluded.source_url,availability=excluded.availability,raw_json=excluded.raw_json,last_seen_at=excluded.last_seen_at,last_checked_at=excluded.last_checked_at,created_at=excluded.created_at,updated_at=excluded.updated_at
 WHERE (SELECT state FROM source_catalog_publication WHERE id=work_source_presence_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_work_source_presence_delete BEFORE DELETE ON work_source_presence
 WHEN EXISTS(SELECT 1 FROM work_source_presence_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.work_id=OLD.work_id AND b.file_source_id=OLD.file_source_id AND b.presence_type=OLD.presence_type) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_work_source_presence_delete BEFORE DELETE ON work_source_presence
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND OLD.presence_type='source'
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE media_file_location_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 id INTEGER,
 media_item_id INTEGER,
 file_source_id INTEGER,
 location_type TEXT,
 path TEXT,
 stream_url TEXT,
 download_url TEXT,
 remote_hash TEXT,
 size_bytes INTEGER,
 duration_seconds INTEGER,
 availability TEXT,
 last_checked_at TEXT,
 file_version TEXT,
 PRIMARY KEY(id)
);
CREATE INDEX media_file_location_publication_before_operation ON media_file_location_publication_before(operation_id);

CREATE VIEW published_media_file_location AS
 SELECT live.id,live.media_item_id,live.file_source_id,live.location_type,live.path,live.stream_url,live.download_url,live.remote_hash,live.size_bytes,live.duration_seconds,live.availability,live.last_checked_at,live.file_version,live.id AS rowid FROM media_file_location live
 WHERE NOT EXISTS(SELECT 1 FROM media_file_location_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.id=live.id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.id,b.media_item_id,b.file_source_id,b.location_type,b.path,b.stream_url,b.download_url,b.remote_hash,b.size_bytes,b.duration_seconds,b.availability,b.last_checked_at,b.file_version,b.id AS rowid FROM media_file_location_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN media_file_location live ON b.id=live.id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_media_file_location_insert AFTER INSERT ON media_file_location
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND NEW.location_type='catalog_stream'
 BEGIN
 INSERT INTO media_file_location_publication_before(operation_id,existed,id,media_item_id,file_source_id,location_type,path,stream_url,download_url,remote_hash,size_bytes,duration_seconds,availability,last_checked_at,file_version)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.media_item_id,NEW.file_source_id,NEW.location_type,NEW.path,NEW.stream_url,NEW.download_url,NEW.remote_hash,NEW.size_bytes,NEW.duration_seconds,NEW.availability,NEW.last_checked_at,NEW.file_version)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,media_item_id=excluded.media_item_id,file_source_id=excluded.file_source_id,location_type=excluded.location_type,path=excluded.path,stream_url=excluded.stream_url,download_url=excluded.download_url,remote_hash=excluded.remote_hash,size_bytes=excluded.size_bytes,duration_seconds=excluded.duration_seconds,availability=excluded.availability,last_checked_at=excluded.last_checked_at,file_version=excluded.file_version
 WHERE (SELECT state FROM source_catalog_publication WHERE id=media_file_location_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_media_file_location_insert BEFORE INSERT ON media_file_location
 WHEN EXISTS(SELECT 1 FROM media_file_location_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=NEW.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_media_file_location_insert BEFORE INSERT ON media_file_location
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=NEW.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND NEW.location_type='catalog_stream'
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_media_file_location_update BEFORE UPDATE OF id,media_item_id,file_source_id,location_type,path,stream_url,download_url,remote_hash,size_bytes,duration_seconds,availability,last_checked_at,file_version ON media_file_location
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND OLD.location_type='catalog_stream' AND (OLD.id IS NOT NEW.id OR OLD.media_item_id IS NOT NEW.media_item_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.location_type IS NOT NEW.location_type OR OLD.path IS NOT NEW.path OR OLD.stream_url IS NOT NEW.stream_url OR OLD.download_url IS NOT NEW.download_url OR OLD.remote_hash IS NOT NEW.remote_hash OR OLD.size_bytes IS NOT NEW.size_bytes OR OLD.duration_seconds IS NOT NEW.duration_seconds OR OLD.availability IS NOT NEW.availability OR OLD.last_checked_at IS NOT NEW.last_checked_at OR OLD.file_version IS NOT NEW.file_version)
 BEGIN
 INSERT INTO media_file_location_publication_before(operation_id,existed,id,media_item_id,file_source_id,location_type,path,stream_url,download_url,remote_hash,size_bytes,duration_seconds,availability,last_checked_at,file_version)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.media_item_id,OLD.file_source_id,OLD.location_type,OLD.path,OLD.stream_url,OLD.download_url,OLD.remote_hash,OLD.size_bytes,OLD.duration_seconds,OLD.availability,OLD.last_checked_at,OLD.file_version)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,media_item_id=excluded.media_item_id,file_source_id=excluded.file_source_id,location_type=excluded.location_type,path=excluded.path,stream_url=excluded.stream_url,download_url=excluded.download_url,remote_hash=excluded.remote_hash,size_bytes=excluded.size_bytes,duration_seconds=excluded.duration_seconds,availability=excluded.availability,last_checked_at=excluded.last_checked_at,file_version=excluded.file_version
 WHERE (SELECT state FROM source_catalog_publication WHERE id=media_file_location_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_new_key_media_file_location BEFORE UPDATE ON media_file_location
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id)
 BEGIN
 INSERT INTO media_file_location_publication_before(operation_id,existed,id,media_item_id,file_source_id,location_type,path,stream_url,download_url,remote_hash,size_bytes,duration_seconds,availability,last_checked_at,file_version) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.media_item_id,NEW.file_source_id,NEW.location_type,NEW.path,NEW.stream_url,NEW.download_url,NEW.remote_hash,NEW.size_bytes,NEW.duration_seconds,NEW.availability,NEW.last_checked_at,NEW.file_version)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,media_item_id=excluded.media_item_id,file_source_id=excluded.file_source_id,location_type=excluded.location_type,path=excluded.path,stream_url=excluded.stream_url,download_url=excluded.download_url,remote_hash=excluded.remote_hash,size_bytes=excluded.size_bytes,duration_seconds=excluded.duration_seconds,availability=excluded.availability,last_checked_at=excluded.last_checked_at,file_version=excluded.file_version WHERE (SELECT state FROM source_catalog_publication WHERE id=media_file_location_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_key_conflict_media_file_location_update BEFORE UPDATE OF id,media_item_id,file_source_id,location_type,path,stream_url,download_url,remote_hash,size_bytes,duration_seconds,availability,last_checked_at,file_version ON media_file_location
 WHEN EXISTS(SELECT 1 FROM media_file_location_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE ((b.id=OLD.id) OR (b.id=NEW.id)) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND (OLD.id IS NOT NEW.id OR OLD.media_item_id IS NOT NEW.media_item_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.location_type IS NOT NEW.location_type OR OLD.path IS NOT NEW.path OR OLD.stream_url IS NOT NEW.stream_url OR OLD.download_url IS NOT NEW.download_url OR OLD.remote_hash IS NOT NEW.remote_hash OR OLD.size_bytes IS NOT NEW.size_bytes OR OLD.duration_seconds IS NOT NEW.duration_seconds OR OLD.availability IS NOT NEW.availability OR OLD.last_checked_at IS NOT NEW.last_checked_at OR OLD.file_version IS NOT NEW.file_version)
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_media_file_location_update BEFORE UPDATE OF id,media_item_id,file_source_id,location_type,path,stream_url,download_url,remote_hash,size_bytes,duration_seconds,availability,last_checked_at,file_version ON media_file_location
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND OLD.location_type='catalog_stream' AND (OLD.id IS NOT NEW.id OR OLD.media_item_id IS NOT NEW.media_item_id OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.location_type IS NOT NEW.location_type OR OLD.path IS NOT NEW.path OR OLD.stream_url IS NOT NEW.stream_url OR OLD.download_url IS NOT NEW.download_url OR OLD.remote_hash IS NOT NEW.remote_hash OR OLD.size_bytes IS NOT NEW.size_bytes OR OLD.duration_seconds IS NOT NEW.duration_seconds OR OLD.availability IS NOT NEW.availability OR OLD.last_checked_at IS NOT NEW.last_checked_at OR OLD.file_version IS NOT NEW.file_version)
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TRIGGER publication_before_media_file_location_delete BEFORE DELETE ON media_file_location
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND OLD.location_type='catalog_stream'
 BEGIN
 INSERT INTO media_file_location_publication_before(operation_id,existed,id,media_item_id,file_source_id,location_type,path,stream_url,download_url,remote_hash,size_bytes,duration_seconds,availability,last_checked_at,file_version)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.media_item_id,OLD.file_source_id,OLD.location_type,OLD.path,OLD.stream_url,OLD.download_url,OLD.remote_hash,OLD.size_bytes,OLD.duration_seconds,OLD.availability,OLD.last_checked_at,OLD.file_version)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,media_item_id=excluded.media_item_id,file_source_id=excluded.file_source_id,location_type=excluded.location_type,path=excluded.path,stream_url=excluded.stream_url,download_url=excluded.download_url,remote_hash=excluded.remote_hash,size_bytes=excluded.size_bytes,duration_seconds=excluded.duration_seconds,availability=excluded.availability,last_checked_at=excluded.last_checked_at,file_version=excluded.file_version
 WHERE (SELECT state FROM source_catalog_publication WHERE id=media_file_location_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_key_conflict_media_file_location_delete BEFORE DELETE ON media_file_location
 WHEN EXISTS(SELECT 1 FROM media_file_location_publication_before b JOIN source_catalog_publication p ON p.id=b.operation_id
 WHERE (b.id=OLD.id) AND p.state='preparing' AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 BEGIN SELECT RAISE(ABORT,'catalog publication key is owned by another operation'); END;

CREATE TRIGGER publication_conflict_media_file_location_delete BEFORE DELETE ON media_file_location
 WHEN EXISTS(SELECT 1 FROM source_catalog_publication p WHERE p.file_source_id=OLD.file_source_id AND p.state='preparing'
 AND p.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)) AND OLD.location_type='catalog_stream'
 BEGIN SELECT RAISE(ABORT,'catalog publication is owned by another operation'); END;

CREATE TABLE content_translation_publication_before (
 operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
 existed INTEGER NOT NULL,
 id INTEGER,
 source_id INTEGER,
 browsable INTEGER,
 PRIMARY KEY(id)
);
CREATE INDEX content_translation_publication_before_operation ON content_translation_publication_before(operation_id);

CREATE VIEW published_content_translation AS
 SELECT live.id,live.cache_key,live.entity_kind,live.entity_id,live.entity_key,live.field,live.language,live.source_text,live.translated_text,live.origin,live.model,live.template_version,live.created_at,live.updated_at,live.config_key,live.source_id,live.browsable,live.id AS rowid FROM content_translation live
 WHERE NOT EXISTS(SELECT 1 FROM content_translation_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.id=live.id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.id,live.cache_key,live.entity_kind,live.entity_id,live.entity_key,live.field,live.language,live.source_text,live.translated_text,live.origin,live.model,live.template_version,live.created_at,live.updated_at,live.config_key,b.source_id,b.browsable,b.id AS rowid FROM content_translation_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN content_translation live ON b.id=live.id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

CREATE TRIGGER publication_before_content_translation_insert AFTER INSERT ON content_translation
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO content_translation_publication_before(operation_id,existed,id,source_id,browsable)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.source_id,NEW.browsable)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,browsable=excluded.browsable
 WHERE (SELECT state FROM source_catalog_publication WHERE id=content_translation_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_before_content_translation_update BEFORE UPDATE OF id,source_id,browsable ON content_translation
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id OR OLD.source_id IS NOT NEW.source_id OR OLD.browsable IS NOT NEW.browsable)
 BEGIN
 INSERT INTO content_translation_publication_before(operation_id,existed,id,source_id,browsable)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.source_id,OLD.browsable)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,browsable=excluded.browsable
 WHERE (SELECT state FROM source_catalog_publication WHERE id=content_translation_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_new_key_content_translation BEFORE UPDATE ON content_translation
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0 AND (OLD.id IS NOT NEW.id)
 BEGIN
 INSERT INTO content_translation_publication_before(operation_id,existed,id,source_id,browsable) VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),0,NEW.id,NEW.source_id,NEW.browsable)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,browsable=excluded.browsable WHERE (SELECT state FROM source_catalog_publication WHERE id=content_translation_publication_before.operation_id)='committed';
 END;

CREATE TRIGGER publication_before_content_translation_delete BEFORE DELETE ON content_translation
 WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)>0
 BEGIN
 INSERT INTO content_translation_publication_before(operation_id,existed,id,source_id,browsable)
 VALUES((SELECT operation_id FROM source_catalog_publication_writer WHERE id=1),1,OLD.id,OLD.source_id,OLD.browsable)
 ON CONFLICT(id) DO UPDATE SET operation_id=excluded.operation_id,existed=excluded.existed,source_id=excluded.source_id,browsable=excluded.browsable
 WHERE (SELECT state FROM source_catalog_publication WHERE id=content_translation_publication_before.operation_id)='committed';

 END;

CREATE TRIGGER publication_external_translation_scope AFTER UPDATE OF source_id,browsable ON content_translation
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
 UPDATE source_catalog_publication_entity SET after_translation_id=0,translation_prepared=0
 WHERE entity_kind=NEW.entity_kind AND entity_id=NEW.entity_id
   AND EXISTS(SELECT 1 FROM source_catalog_publication op WHERE op.id=operation_id AND op.state='preparing');
 UPDATE content_translation_publication_before SET source_id=NEW.source_id,browsable=NEW.browsable
 WHERE id=NEW.id AND operation_id IN(SELECT id FROM source_catalog_publication WHERE state='preparing');
END;
-- An FTS document id cannot be reused while an older operation retains it.
CREATE TRIGGER publication_search_allocate_id BEFORE INSERT ON source_catalog_search_doc
WHEN NEW.id=-1 AND NOT EXISTS(SELECT 1 FROM source_catalog_search_doc WHERE entity_kind=NEW.entity_kind AND entity_key=NEW.entity_key)
BEGIN
 INSERT INTO source_catalog_search_doc(id,entity_kind,entity_key,catalog_entry_id,series_id,file_source_id,body)
 VALUES(max(COALESCE((SELECT max(id) FROM source_catalog_search_doc),0),COALESCE((SELECT max(id) FROM source_catalog_search_doc_publication_before),0))+1,NEW.entity_kind,NEW.entity_key,NEW.catalog_entry_id,NEW.series_id,NEW.file_source_id,NEW.body);
 SELECT RAISE(IGNORE);
END;
CREATE VIRTUAL TABLE source_catalog_publication_search USING fts5(body,content='source_catalog_search_doc_publication_before',content_rowid='id',tokenize='trigram');
CREATE TRIGGER publication_search_insert AFTER INSERT ON source_catalog_search_doc_publication_before WHEN NEW.existed=1 BEGIN
 INSERT INTO source_catalog_publication_search(rowid,body) VALUES(NEW.id,NEW.body);
END;
CREATE TRIGGER publication_search_delete AFTER DELETE ON source_catalog_search_doc_publication_before WHEN OLD.existed=1 BEGIN
 INSERT INTO source_catalog_publication_search(source_catalog_publication_search,rowid,body) VALUES('delete',OLD.id,OLD.body);
END;
CREATE TRIGGER publication_search_update AFTER UPDATE ON source_catalog_search_doc_publication_before BEGIN
 INSERT INTO source_catalog_publication_search(source_catalog_publication_search,rowid,body) SELECT 'delete',OLD.id,OLD.body WHERE OLD.existed=1;
 INSERT INTO source_catalog_publication_search(rowid,body) SELECT NEW.id,NEW.body WHERE NEW.existed=1;
END;

DROP TRIGGER translation_revision_catalog_series_delete;
CREATE TRIGGER translation_revision_catalog_series_delete
AFTER DELETE ON catalog_series
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_catalog_series_insert;
CREATE TRIGGER translation_revision_catalog_series_insert
AFTER INSERT ON catalog_series
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_catalog_series_member_delete;
CREATE TRIGGER translation_revision_catalog_series_member_delete
AFTER DELETE ON catalog_series_member
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_catalog_series_member_insert;
CREATE TRIGGER translation_revision_catalog_series_member_insert
AFTER INSERT ON catalog_series_member
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_catalog_series_member_update;
CREATE TRIGGER translation_revision_catalog_series_member_update
AFTER UPDATE OF series_id,catalog_entry_id ON catalog_series_member
WHEN (OLD.series_id IS NOT NEW.series_id OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_catalog_series_update;
CREATE TRIGGER translation_revision_catalog_series_update
AFTER UPDATE OF title,work_id ON catalog_series
WHEN (OLD.title IS NOT NEW.title OR OLD.work_id IS NOT NEW.work_id) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_content_translation_delete;
CREATE TRIGGER translation_revision_content_translation_delete
AFTER DELETE ON content_translation
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_content_translation_insert;
CREATE TRIGGER translation_revision_content_translation_insert
AFTER INSERT ON content_translation
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_content_translation_update;
CREATE TRIGGER translation_revision_content_translation_update
AFTER UPDATE OF entity_kind,entity_id,entity_key,field,language,source_text,translated_text,origin,config_key ON content_translation
WHEN (OLD.entity_key IS NOT NEW.entity_key OR OLD.entity_kind IS NOT NEW.entity_kind OR OLD.entity_id IS NOT NEW.entity_id OR OLD.field IS NOT NEW.field OR OLD.language IS NOT NEW.language OR OLD.source_text IS NOT NEW.source_text OR OLD.translated_text IS NOT NEW.translated_text OR OLD.origin IS NOT NEW.origin OR OLD.config_key IS NOT NEW.config_key) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_entry_admission_delete;
CREATE TRIGGER translation_revision_source_catalog_entry_admission_delete
AFTER DELETE ON source_catalog_entry_admission
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_entry_admission_insert;
CREATE TRIGGER translation_revision_source_catalog_entry_admission_insert
AFTER INSERT ON source_catalog_entry_admission
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_entry_admission_update;
CREATE TRIGGER translation_revision_source_catalog_entry_admission_update
AFTER UPDATE OF catalog_entry_id,generation,files,audio,non_audio ON source_catalog_entry_admission
WHEN (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.generation IS NOT NEW.generation OR OLD.files IS NOT NEW.files OR OLD.audio IS NOT NEW.audio OR OLD.non_audio IS NOT NEW.non_audio) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_entry_delete;
CREATE TRIGGER translation_revision_source_catalog_entry_delete
AFTER DELETE ON source_catalog_entry
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_entry_insert;
CREATE TRIGGER translation_revision_source_catalog_entry_insert
AFTER INSERT ON source_catalog_entry
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_entry_update;
CREATE TRIGGER translation_revision_source_catalog_entry_update
AFTER UPDATE OF title,file_source_id,relocated_to_id,last_seen_generation ON source_catalog_entry
WHEN (OLD.title IS NOT NEW.title OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.relocated_to_id IS NOT NEW.relocated_to_id OR OLD.last_seen_generation IS NOT NEW.last_seen_generation) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_episode_pending_delete;
CREATE TRIGGER translation_revision_source_catalog_episode_pending_delete
AFTER DELETE ON source_catalog_episode_pending
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_episode_pending_insert;
CREATE TRIGGER translation_revision_source_catalog_episode_pending_insert
AFTER INSERT ON source_catalog_episode_pending
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_episode_pending_update;
CREATE TRIGGER translation_revision_source_catalog_episode_pending_update
AFTER UPDATE OF catalog_entry_id,source_id,parent_root ON source_catalog_episode_pending
WHEN (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_episode_publication_delete;
CREATE TRIGGER translation_revision_source_catalog_episode_publication_delete
AFTER DELETE ON source_catalog_episode_publication
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_episode_publication_insert;
CREATE TRIGGER translation_revision_source_catalog_episode_publication_insert
AFTER INSERT ON source_catalog_episode_publication
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_episode_publication_update;
CREATE TRIGGER translation_revision_source_catalog_episode_publication_update
AFTER UPDATE OF source_id,parent_root,state ON source_catalog_episode_publication
WHEN (OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.state IS NOT NEW.state) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_file_delete;
CREATE TRIGGER translation_revision_source_catalog_file_delete
AFTER DELETE ON source_catalog_file
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_file_insert;
CREATE TRIGGER translation_revision_source_catalog_file_insert
AFTER INSERT ON source_catalog_file
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

DROP TRIGGER translation_revision_source_catalog_file_update;
CREATE TRIGGER translation_revision_source_catalog_file_update
AFTER UPDATE OF catalog_entry_id,kind,observed_generation,title,relative_path ON source_catalog_file
WHEN (OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.kind IS NOT NEW.kind OR OLD.observed_generation IS NOT NEW.observed_generation
OR OLD.title IS NOT NEW.title OR OLD.relative_path IS NOT NEW.relative_path) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE VIEW catalog_entry_searchable_live AS
SELECT e.id AS catalog_entry_id, e.title AS title,
  CASE WHEN e.relocated_to_id IS NULL AND e.file_source_id IS NOT NULL
    AND trim(e.title)<>''
    AND EXISTS(SELECT 1 FROM file_source src
               WHERE src.id=e.file_source_id
                 AND src.enabled=1 AND src.source_type='openlist')
    AND NOT EXISTS(
      SELECT 1 FROM source_catalog_episode_pending pending
      WHERE pending.catalog_entry_id=e.id
        AND NOT EXISTS(
          SELECT 1 FROM source_catalog_episode_publication publication
          WHERE publication.source_id=pending.source_id
            AND publication.parent_root=pending.parent_root
            AND publication.state='published'))
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

CREATE VIEW content_translation_scope_live AS
SELECT ct.id AS translation_id,
  CASE ct.entity_kind
    WHEN 'entry' THEN (SELECT e.file_source_id FROM source_catalog_entry e WHERE e.id=ct.entity_id)
    WHEN 'series' THEN (SELECT s.source_id FROM catalog_series s WHERE s.id=ct.entity_id)
    ELSE NULL END AS source_id,
  CASE ct.entity_kind
    WHEN 'entry' THEN (CASE WHEN ct.field='title' AND EXISTS(
      SELECT 1 FROM catalog_entry_searchable_live entry_scope
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
          JOIN catalog_entry_searchable_live member_scope ON member_scope.catalog_entry_id=m.catalog_entry_id
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

DROP TRIGGER content_translation_scope_entry_delete;
CREATE TRIGGER content_translation_scope_entry_delete
AFTER DELETE ON source_catalog_entry
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=OLD.id;
END;

DROP TRIGGER content_translation_scope_entry_update;
CREATE TRIGGER content_translation_scope_entry_update
AFTER UPDATE OF title,file_source_id,relocated_to_id,last_seen_generation ON source_catalog_entry
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='entry' AND entity_id=NEW.id;
END;

DROP TRIGGER content_translation_scope_file_delete;
CREATE TRIGGER content_translation_scope_file_delete
AFTER DELETE ON source_catalog_file
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=OLD.catalog_entry_id;
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='series' AND entity_id IN (
    SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id);
END;

DROP TRIGGER content_translation_scope_file_insert;
CREATE TRIGGER content_translation_scope_file_insert
AFTER INSERT ON source_catalog_file
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=NEW.catalog_entry_id;
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='series' AND entity_id IN (
    SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id);
END;

DROP TRIGGER content_translation_scope_file_update;
CREATE TRIGGER content_translation_scope_file_update
AFTER UPDATE OF catalog_entry_id,kind,observed_generation ON source_catalog_file
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=OLD.catalog_entry_id;
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=NEW.catalog_entry_id;
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='series' AND entity_id IN (
    SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=OLD.catalog_entry_id
    UNION SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=NEW.catalog_entry_id);
END;

DROP TRIGGER content_translation_scope_pending_delete;
CREATE TRIGGER content_translation_scope_pending_delete
AFTER DELETE ON source_catalog_episode_pending
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='entry' AND entity_id=OLD.catalog_entry_id;
END;

DROP TRIGGER content_translation_scope_pending_insert;
CREATE TRIGGER content_translation_scope_pending_insert
AFTER INSERT ON source_catalog_episode_pending
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='entry' AND entity_id=NEW.catalog_entry_id;
END;

DROP TRIGGER content_translation_scope_publication_insert;
CREATE TRIGGER content_translation_scope_publication_insert
AFTER INSERT ON source_catalog_episode_publication
WHEN (NEW.state='published') AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='entry' AND entity_id IN (
    SELECT pending.catalog_entry_id FROM source_catalog_episode_pending pending
    WHERE pending.source_id=NEW.source_id AND pending.parent_root=NEW.parent_root);
END;

DROP TRIGGER content_translation_scope_publication_update;
CREATE TRIGGER content_translation_scope_publication_update
AFTER UPDATE OF state ON source_catalog_episode_publication
WHEN (OLD.state IS NOT NEW.state) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='entry' AND entity_id IN (
    SELECT pending.catalog_entry_id FROM source_catalog_episode_pending pending
    WHERE pending.source_id=NEW.source_id AND pending.parent_root=NEW.parent_root);
END;

DROP TRIGGER content_translation_scope_series_delete;
CREATE TRIGGER content_translation_scope_series_delete
AFTER DELETE ON catalog_series
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='series' AND entity_id=OLD.id;
END;

DROP TRIGGER content_translation_scope_series_member_delete;
CREATE TRIGGER content_translation_scope_series_member_delete
AFTER DELETE ON catalog_series_member
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='series' AND entity_id=OLD.series_id;
END;

DROP TRIGGER content_translation_scope_series_member_insert;
CREATE TRIGGER content_translation_scope_series_member_insert
AFTER INSERT ON catalog_series_member
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='series' AND entity_id=NEW.series_id;
END;

DROP TRIGGER content_translation_scope_series_member_update;
CREATE TRIGGER content_translation_scope_series_member_update
AFTER UPDATE OF series_id,catalog_entry_id ON catalog_series_member
WHEN (OLD.series_id IS NOT NEW.series_id OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='series' AND entity_id IN (OLD.series_id,NEW.series_id);
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='entry' AND entity_id=OLD.catalog_entry_id;
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='entry' AND entity_id=NEW.catalog_entry_id;
END;

DROP TRIGGER content_translation_scope_series_update;
CREATE TRIGGER content_translation_scope_series_update
AFTER UPDATE OF title,source_id ON catalog_series
WHEN (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='series' AND entity_id=NEW.id;
END;

DROP TRIGGER content_translation_scope_series_work_insert;
CREATE TRIGGER content_translation_scope_series_work_insert
AFTER INSERT ON catalog_series
WHEN (NEW.work_id IS NOT NULL) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='work' AND entity_id=NEW.work_id;
END;

DROP TRIGGER content_translation_scope_series_work_update;
CREATE TRIGGER content_translation_scope_series_work_update
AFTER UPDATE OF work_id ON catalog_series
WHEN (OLD.work_id IS NOT NEW.work_id) AND (SELECT operation_id FROM source_catalog_publication_writer WHERE id=1)=0
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='work' AND entity_id IN (COALESCE(OLD.work_id,-1),COALESCE(NEW.work_id,-1));
END;

DROP TRIGGER content_translation_scope_work_delete;
CREATE TRIGGER content_translation_scope_work_delete
AFTER DELETE ON work
BEGIN
  UPDATE content_translation SET browsable=0
  WHERE entity_kind='work' AND entity_id=OLD.id;
END;

DROP TRIGGER content_translation_scope_work_update;
CREATE TRIGGER content_translation_scope_work_update
AFTER UPDATE OF title,description ON work
BEGIN
  UPDATE content_translation SET (source_id,browsable)=(
    SELECT scope.source_id,scope.browsable
    FROM content_translation_scope_live scope
    WHERE scope.translation_id=content_translation.id)
  WHERE entity_kind='work' AND entity_id=NEW.id;
END;

DROP VIEW content_translation_scope;
CREATE VIEW content_translation_scope AS
SELECT ct.id AS translation_id,
  CASE ct.entity_kind
    WHEN 'entry' THEN (SELECT e.file_source_id FROM published_source_catalog_entry e WHERE e.id=ct.entity_id)
    WHEN 'series' THEN (SELECT s.source_id FROM published_catalog_series s WHERE s.id=ct.entity_id)
    ELSE NULL END AS source_id,
  CASE ct.entity_kind
    WHEN 'entry' THEN (CASE WHEN ct.field='title' AND EXISTS(
      SELECT 1 FROM catalog_entry_searchable entry_scope
      WHERE entry_scope.catalog_entry_id=ct.entity_id
        AND entry_scope.searchable=1 AND entry_scope.title=ct.source_text)
      THEN 1 ELSE 0 END)
    WHEN 'series' THEN (CASE WHEN ct.field='title' AND EXISTS(
      SELECT 1 FROM published_catalog_series s
      JOIN file_source src ON src.id=s.source_id
      WHERE s.id=ct.entity_id AND s.source_id IS NOT NULL
        AND src.enabled=1 AND src.source_type='openlist'
        AND trim(s.title)<>'' AND s.title=ct.source_text
        AND (SELECT COUNT(*) FROM published_catalog_series_member m WHERE m.series_id=s.id)>=2
        AND EXISTS(
          SELECT 1 FROM published_catalog_series_member m
          JOIN catalog_entry_searchable member_scope ON member_scope.catalog_entry_id=m.catalog_entry_id
          WHERE m.series_id=s.id AND member_scope.searchable=1))
      THEN 1 ELSE 0 END)
    ELSE (CASE WHEN EXISTS(
      SELECT 1 FROM work w
      WHERE w.id=ct.entity_id
        AND NOT EXISTS(SELECT 1 FROM published_catalog_series s WHERE s.work_id=w.id)
        AND ((ct.field='title' AND trim(w.title)<>'' AND w.title=ct.source_text)
          OR (ct.field='description' AND trim(w.description)<>'' AND w.description=ct.source_text)))
      THEN 1 ELSE 0 END)
  END AS browsable
FROM content_translation ct;

DROP VIEW catalog_entry_searchable;
CREATE VIEW catalog_entry_searchable AS
SELECT e.id AS catalog_entry_id, e.title AS title,
  CASE WHEN e.relocated_to_id IS NULL AND e.file_source_id IS NOT NULL
    AND trim(e.title)<>''
    AND EXISTS(SELECT 1 FROM file_source src
               WHERE src.id=e.file_source_id
                 AND src.enabled=1 AND src.source_type='openlist')
    AND NOT EXISTS(
      SELECT 1 FROM published_source_catalog_episode_pending pending
      WHERE pending.catalog_entry_id=e.id
        AND NOT EXISTS(
          SELECT 1 FROM published_source_catalog_episode_publication publication
          WHERE publication.source_id=pending.source_id
            AND publication.parent_root=pending.parent_root
            AND publication.state='published'))
    AND COALESCE(
      (SELECT adm.audio>0 AND adm.files<=5000
              AND NOT (adm.non_audio>2000 AND adm.audio<5)
       FROM published_source_catalog_entry_admission adm
       WHERE adm.catalog_entry_id=e.id AND adm.generation=e.last_seen_generation),
      (SELECT live.audio>0 AND live.files<=5000
              AND NOT (live.non_audio>2000 AND live.audio<5)
       FROM (SELECT
         (SELECT count(*) FROM (SELECT 1 FROM published_source_catalog_file af
           WHERE af.catalog_entry_id=e.id AND af.kind<>'folder'
             AND af.observed_generation=e.last_seen_generation LIMIT 5001)) AS files,
         (SELECT count(*) FROM (SELECT 1 FROM published_source_catalog_file aaf
           WHERE aaf.catalog_entry_id=e.id AND aaf.kind='audio'
             AND aaf.observed_generation=e.last_seen_generation LIMIT 5)) AS audio,
         (SELECT count(*) FROM (SELECT 1 FROM published_source_catalog_file anf
           WHERE anf.catalog_entry_id=e.id AND anf.kind<>'audio' AND anf.kind<>'folder'
             AND anf.observed_generation=e.last_seen_generation LIMIT 2001)) AS non_audio) AS live))
  THEN 1 ELSE 0 END AS searchable
FROM published_source_catalog_entry e;
