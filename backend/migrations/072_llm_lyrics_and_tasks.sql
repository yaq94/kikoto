CREATE TABLE llm_lyrics_asset (
 id TEXT PRIMARY KEY,
 work_id INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
 source_location_id INTEGER REFERENCES media_file_location(id) ON DELETE SET NULL,
 audio_media_item_id INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
 folder_id INTEGER NOT NULL REFERENCES work_folder_location(id),
 language TEXT NOT NULL,
 source_hash TEXT NOT NULL,
 value_text TEXT NOT NULL,
 extension TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'publishing' CHECK(status IN ('publishing','accepted','revoked')),
 relative_path TEXT NOT NULL DEFAULT '',
 media_item_id INTEGER REFERENCES media_item(id) ON DELETE SET NULL,
 created_by_user_id INTEGER REFERENCES user_account(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_llm_lyrics_audio ON llm_lyrics_asset(audio_media_item_id,language,status);

CREATE TABLE llm_translation_task (
 id INTEGER PRIMARY KEY,
 workflow_run_id INTEGER NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
 work_id INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
 dedup_key TEXT NOT NULL UNIQUE,
 input_hash TEXT NOT NULL,
 policy_hash TEXT NOT NULL,
 config_hash TEXT NOT NULL,
 request_json TEXT NOT NULL,
 result_json TEXT NOT NULL DEFAULT '{}',
 execution_json TEXT NOT NULL DEFAULT '{}',
 request_index INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','requesting','ready','failed','uncertain','invalid','cancelled')),
 preview_id TEXT REFERENCES llm_translation_preview(id),
 failure_code TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
