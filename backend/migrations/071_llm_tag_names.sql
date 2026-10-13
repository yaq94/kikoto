CREATE TABLE metadata_tag_llm_name (
 tag_id INTEGER NOT NULL REFERENCES metadata_tag(tag_id) ON DELETE CASCADE,
 language TEXT NOT NULL,
 name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 512),
 source_text TEXT NOT NULL,
 provider_id TEXT NOT NULL,
 model TEXT NOT NULL,
 created_by_user_id INTEGER REFERENCES user_account(id) ON DELETE SET NULL,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(tag_id,language)
);
CREATE TABLE metadata_tag_llm_candidate (
 tag_id INTEGER NOT NULL REFERENCES metadata_tag(tag_id) ON DELETE CASCADE,
 language TEXT NOT NULL,
 source_text TEXT NOT NULL,
 config_hash TEXT NOT NULL,
 policy_hash TEXT NOT NULL,
 user_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
 name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 512),
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(tag_id,language,config_hash,policy_hash,user_id)
);
CREATE TRIGGER llm_tag_name_insert AFTER INSERT ON metadata_tag_llm_name BEGIN
 INSERT OR IGNORE INTO work_search_dirty(work_id) SELECT work_id FROM work_tag WHERE tag_id=NEW.tag_id;
END;
CREATE TRIGGER llm_tag_name_update AFTER UPDATE ON metadata_tag_llm_name BEGIN
 INSERT OR IGNORE INTO work_search_dirty(work_id) SELECT work_id FROM work_tag WHERE tag_id=NEW.tag_id;
END;
CREATE TRIGGER llm_tag_name_delete AFTER DELETE ON metadata_tag_llm_name BEGIN
 INSERT OR IGNORE INTO work_search_dirty(work_id) SELECT work_id FROM work_tag WHERE tag_id=OLD.tag_id;
END;
