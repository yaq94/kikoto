CREATE TABLE work_llm_translation (
  work_id INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
  language TEXT NOT NULL CHECK (length(language) BETWEEN 2 AND 32),
  field_name TEXT NOT NULL CHECK (field_name IN ('title', 'description')),
  value_text TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  provider_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'accepted' CHECK (status IN ('proposed', 'accepted', 'rejected', 'superseded')),
  created_by_user_id INTEGER REFERENCES user_account(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(work_id, language, field_name)
);

CREATE INDEX idx_work_llm_translation_language ON work_llm_translation(language, field_name, updated_at);

CREATE TRIGGER work_llm_translation_insert AFTER INSERT ON work_llm_translation
BEGIN
  INSERT OR IGNORE INTO work_title_language_dirty(work_id) VALUES(NEW.work_id);
  INSERT OR IGNORE INTO work_search_dirty(work_id) VALUES(NEW.work_id);
END;

CREATE TRIGGER work_llm_translation_update AFTER UPDATE ON work_llm_translation
BEGIN
  INSERT OR IGNORE INTO work_title_language_dirty(work_id) VALUES(NEW.work_id);
  INSERT OR IGNORE INTO work_search_dirty(work_id) VALUES(NEW.work_id);
END;

CREATE TRIGGER work_llm_translation_delete AFTER DELETE ON work_llm_translation
BEGIN
  INSERT OR IGNORE INTO work_title_language_dirty(work_id) VALUES(OLD.work_id);
  INSERT OR IGNORE INTO work_search_dirty(work_id) VALUES(OLD.work_id);
END;
