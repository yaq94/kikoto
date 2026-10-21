-- Directory-first incremental sync state. The version columns describe one
-- directory, not its subtree: observed_version is the directory's own modified
-- value as reported by its parent, and listing_digest covers its direct entries.
ALTER TABLE source_catalog_directory ADD COLUMN observed_version TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_directory ADD COLUMN listing_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_directory ADD COLUMN last_verified_run INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_directory ADD COLUMN last_verified_at TEXT;
-- sync_state values:
--   ''          never probed by an incremental sync (adopt the first mtime)
--   'unchanged' the directory's own modified value did not move
--   'changed'   the directory must be listed (new, modified, or retrying a gap)
--   'listed'    the directory's direct entries were reconciled in this run
--   'probing'   a dirs request was attempted and its result is pending
--   'excluded'  the directory or an ancestor was removed from the source
ALTER TABLE source_catalog_directory ADD COLUMN sync_state TEXT NOT NULL DEFAULT '';

CREATE INDEX source_catalog_directory_sync
  ON source_catalog_directory(file_source_id,generation,sync_state,last_verified_run,path);

-- One durable progress row per incremental attempt. generation is not stored
-- here on purpose: an incremental run never creates a new observation
-- generation, it reconciles the directory rows of the last full run.
CREATE TABLE source_catalog_sync_run (
  id INTEGER PRIMARY KEY,
  file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
  started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at TEXT,
  status TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','succeeded','partial','failed','cancelled')),
  dirs_probed INTEGER NOT NULL DEFAULT 0,
  dirs_changed INTEGER NOT NULL DEFAULT 0,
  dirs_listed INTEGER NOT NULL DEFAULT 0,
  entries_published INTEGER NOT NULL DEFAULT 0,
  requests INTEGER NOT NULL DEFAULT 0,
  force INTEGER NOT NULL DEFAULT 0 CHECK(force IN (0,1)),
  note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX source_catalog_sync_run_source ON source_catalog_sync_run(file_source_id,id DESC);
CREATE UNIQUE INDEX source_catalog_sync_run_one_running
  ON source_catalog_sync_run(file_source_id) WHERE status='running';