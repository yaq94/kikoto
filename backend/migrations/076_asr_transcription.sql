-- A speech transcription of one local audio track. The row is the durable
-- task, its checkpoint and its result: the job appends the cues of each
-- transcribed chunk, the finished WebVTT waits for review, and acceptance
-- publishes it into the work folder's lyrics.
CREATE TABLE asr_transcription (
  id TEXT PRIMARY KEY,
  workflow_run_id INTEGER NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
  user_id INTEGER REFERENCES user_account(id) ON DELETE SET NULL,
  work_id INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
  audio_media_item_id INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
  audio_location_id INTEGER NOT NULL REFERENCES media_file_location(id) ON DELETE CASCADE,
  folder_id INTEGER NOT NULL REFERENCES work_folder_location(id) ON DELETE CASCADE,
  provider_id TEXT NOT NULL,
  provider_name TEXT NOT NULL,
  model TEXT NOT NULL,
  language TEXT NOT NULL DEFAULT '' CHECK(language IN ('', 'ja-jp', 'zh-cn', 'zh-tw', 'en-us', 'ko-kr')),
  config_hash TEXT NOT NULL,
  source_version TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending', 'requesting', 'failed', 'ready', 'publishing', 'accepted', 'rejected', 'cancelled')),
  failure_code TEXT NOT NULL DEFAULT '',
  offset_ms INTEGER NOT NULL DEFAULT 0,
  cues_json TEXT NOT NULL DEFAULT '[]',
  value_text TEXT NOT NULL DEFAULT '',
  lyrics_path TEXT NOT NULL DEFAULT '',
  lyrics_media_item_id INTEGER REFERENCES media_item(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_asr_transcription_work ON asr_transcription(work_id, created_at);
CREATE INDEX idx_asr_transcription_audio ON asr_transcription(audio_media_item_id, state);
CREATE INDEX idx_fk_asr_transcription_workflow_run_id ON asr_transcription(workflow_run_id);
CREATE INDEX idx_fk_asr_transcription_user_id ON asr_transcription(user_id);
CREATE INDEX idx_fk_asr_transcription_audio_location_id ON asr_transcription(audio_location_id);
CREATE INDEX idx_fk_asr_transcription_folder_id ON asr_transcription(folder_id);
CREATE INDEX idx_fk_asr_transcription_lyrics_media_item_id ON asr_transcription(lyrics_media_item_id);

-- An accepted transcription assigns its lyrics file with its own origin. No
-- other table references media_lyrics_assignment, so the rebuild needs no
-- foreign-key suspension.
CREATE TABLE media_lyrics_assignment_next (
  audio_media_item_id INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
  language TEXT NOT NULL DEFAULT '' CHECK(language IN ('', 'ja-jp', 'zh-cn', 'zh-tw', 'en-us', 'ko-kr')),
  lyrics_media_item_id INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
  origin TEXT NOT NULL DEFAULT 'manual' CHECK(origin IN ('manual', 'remote_fetch', 'library_import', 'transcription')),
  assigned_by_user_id INTEGER REFERENCES user_account(id) ON DELETE SET NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(audio_media_item_id, language),
  CHECK(audio_media_item_id <> lyrics_media_item_id)
);

INSERT INTO media_lyrics_assignment_next (
  audio_media_item_id, language, lyrics_media_item_id, origin, assigned_by_user_id, updated_at
)
SELECT audio_media_item_id, language, lyrics_media_item_id, origin, assigned_by_user_id, updated_at
FROM media_lyrics_assignment;

DROP TABLE media_lyrics_assignment;

ALTER TABLE media_lyrics_assignment_next RENAME TO media_lyrics_assignment;

CREATE INDEX idx_fk_media_lyrics_assignment_lyrics_media_item_id
  ON media_lyrics_assignment(lyrics_media_item_id);
CREATE INDEX idx_fk_media_lyrics_assignment_assigned_by_user_id
  ON media_lyrics_assignment(assigned_by_user_id);
