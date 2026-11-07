-- Revision changes commit atomically with the rows that define coverage.
-- Completion is reusable only for this exact revision and configuration.
-- Source membership/admission changes also advance entities, so a batch cannot
-- overwrite a cursor reset while it was waiting for model output.
CREATE TABLE translation_corpus_revision (
  id INTEGER PRIMARY KEY CHECK(id=1),
  revision INTEGER NOT NULL DEFAULT 0,
  cache_revision INTEGER NOT NULL DEFAULT 0,
  entities INTEGER NOT NULL DEFAULT 0
);
INSERT INTO translation_corpus_revision(id) VALUES(1);
ALTER TABLE translation_prefill_state ADD COLUMN checked_revision INTEGER NOT NULL DEFAULT -1;

CREATE TRIGGER translation_revision_source_catalog_entry_insert
AFTER INSERT ON source_catalog_entry
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_entry_update
AFTER UPDATE OF title,file_source_id,relocated_to_id,last_seen_generation ON source_catalog_entry
WHEN OLD.title IS NOT NEW.title OR OLD.file_source_id IS NOT NEW.file_source_id OR OLD.relocated_to_id IS NOT NEW.relocated_to_id OR OLD.last_seen_generation IS NOT NEW.last_seen_generation
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_entry_delete
AFTER DELETE ON source_catalog_entry
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_file_source_insert
AFTER INSERT ON file_source
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_file_source_update
AFTER UPDATE OF enabled,source_type ON file_source
WHEN OLD.enabled IS NOT NEW.enabled OR OLD.source_type IS NOT NEW.source_type
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_file_source_delete
AFTER DELETE ON file_source
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_catalog_series_insert
AFTER INSERT ON catalog_series
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_catalog_series_update
AFTER UPDATE OF title,work_id ON catalog_series
WHEN OLD.title IS NOT NEW.title OR OLD.work_id IS NOT NEW.work_id
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_catalog_series_delete
AFTER DELETE ON catalog_series
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_catalog_series_member_insert
AFTER INSERT ON catalog_series_member
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_catalog_series_member_update
AFTER UPDATE OF series_id,catalog_entry_id ON catalog_series_member
WHEN OLD.series_id IS NOT NEW.series_id OR OLD.catalog_entry_id IS NOT NEW.catalog_entry_id
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_catalog_series_member_delete
AFTER DELETE ON catalog_series_member
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_work_insert
AFTER INSERT ON work
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_work_update
AFTER UPDATE OF title,description ON work
WHEN OLD.title IS NOT NEW.title OR OLD.description IS NOT NEW.description
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_work_delete
AFTER DELETE ON work
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_entry_admission_insert
AFTER INSERT ON source_catalog_entry_admission
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_entry_admission_update
AFTER UPDATE OF catalog_entry_id,generation,files,audio,non_audio ON source_catalog_entry_admission
WHEN OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.generation IS NOT NEW.generation OR OLD.files IS NOT NEW.files OR OLD.audio IS NOT NEW.audio OR OLD.non_audio IS NOT NEW.non_audio
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_entry_admission_delete
AFTER DELETE ON source_catalog_entry_admission
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_file_insert
AFTER INSERT ON source_catalog_file
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_file_update
AFTER UPDATE OF catalog_entry_id,kind,observed_generation ON source_catalog_file
WHEN OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.kind IS NOT NEW.kind OR OLD.observed_generation IS NOT NEW.observed_generation
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_file_delete
AFTER DELETE ON source_catalog_file
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_episode_pending_insert
AFTER INSERT ON source_catalog_episode_pending
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_episode_pending_update
AFTER UPDATE OF catalog_entry_id,source_id,parent_root ON source_catalog_episode_pending
WHEN OLD.catalog_entry_id IS NOT NEW.catalog_entry_id OR OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_episode_pending_delete
AFTER DELETE ON source_catalog_episode_pending
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_episode_publication_insert
AFTER INSERT ON source_catalog_episode_publication
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_episode_publication_update
AFTER UPDATE OF source_id,parent_root,state ON source_catalog_episode_publication
WHEN OLD.source_id IS NOT NEW.source_id OR OLD.parent_root IS NOT NEW.parent_root OR OLD.state IS NOT NEW.state
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_catalog_episode_publication_delete
AFTER DELETE ON source_catalog_episode_publication
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_content_translation_insert
AFTER INSERT ON content_translation
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_content_translation_update
AFTER UPDATE OF entity_kind,entity_id,entity_key,field,language,source_text,translated_text,origin,config_key ON content_translation
WHEN OLD.entity_key IS NOT NEW.entity_key OR OLD.entity_kind IS NOT NEW.entity_kind OR OLD.entity_id IS NOT NEW.entity_id OR OLD.field IS NOT NEW.field OR OLD.language IS NOT NEW.language OR OLD.source_text IS NOT NEW.source_text OR OLD.translated_text IS NOT NEW.translated_text OR OLD.origin IS NOT NEW.origin OR OLD.config_key IS NOT NEW.config_key
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_content_translation_delete
AFTER DELETE ON content_translation
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_translation_prefill_failure_insert
AFTER INSERT ON translation_prefill_failure
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_translation_prefill_failure_update
AFTER UPDATE OF entity_kind,entity_id,field,language,source_hash,config_key,attempts ON translation_prefill_failure
WHEN OLD.entity_kind IS NOT NEW.entity_kind OR OLD.entity_id IS NOT NEW.entity_id OR OLD.field IS NOT NEW.field OR OLD.language IS NOT NEW.language OR OLD.source_hash IS NOT NEW.source_hash OR OLD.config_key IS NOT NEW.config_key OR OLD.attempts IS NOT NEW.attempts
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_translation_prefill_failure_delete
AFTER DELETE ON translation_prefill_failure
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_field_translation_insert
AFTER INSERT ON source_field_translation
BEGIN
  UPDATE translation_corpus_revision SET cache_revision=cache_revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_field_translation_update
AFTER UPDATE OF source_id,work_code,field,path,language,source_text,translated_text ON source_field_translation
WHEN OLD.source_id IS NOT NEW.source_id OR OLD.work_code IS NOT NEW.work_code OR OLD.field IS NOT NEW.field OR OLD.path IS NOT NEW.path OR OLD.language IS NOT NEW.language OR OLD.source_text IS NOT NEW.source_text OR OLD.translated_text IS NOT NEW.translated_text
BEGIN
  UPDATE translation_corpus_revision SET cache_revision=cache_revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_source_field_translation_delete
AFTER DELETE ON source_field_translation
BEGIN
  UPDATE translation_corpus_revision SET cache_revision=cache_revision+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_config_insert
AFTER INSERT ON app_setting
WHEN NEW.key='translation_service'
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_config_update
AFTER UPDATE OF value_json ON app_setting
WHEN NEW.key='translation_service' AND OLD.value_json IS NOT NEW.value_json
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;

CREATE TRIGGER translation_revision_config_delete
AFTER DELETE ON app_setting
WHEN OLD.key='translation_service'
BEGIN
  UPDATE translation_corpus_revision SET revision=revision+1,entities=entities+1 WHERE id=1;
END;


