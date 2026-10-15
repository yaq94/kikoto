CREATE TABLE source_catalog_scan (
    file_source_id INTEGER PRIMARY KEY REFERENCES file_source(id) ON DELETE CASCADE,
    workflow_run_id INTEGER NOT NULL REFERENCES workflow_run(id),
    options_json TEXT NOT NULL,
    pause_reason TEXT NOT NULL DEFAULT '',
    requests INTEGER NOT NULL DEFAULT 0,
    started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE source_catalog_directory (
    file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    generation INTEGER NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    partition_path TEXT NOT NULL,
    catalog_root TEXT NOT NULL DEFAULT '',
    children_cursor TEXT NOT NULL DEFAULT '',
    next_page INTEGER NOT NULL DEFAULT 1,
    expected_total INTEGER NOT NULL DEFAULT -1,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    error_code TEXT NOT NULL DEFAULT '',
    completed_at TEXT,
    PRIMARY KEY(file_source_id,path)
);
CREATE INDEX source_catalog_directory_pending ON source_catalog_directory(file_source_id,generation,status,path);
CREATE INDEX source_catalog_directory_root ON source_catalog_directory(file_source_id,generation,catalog_root,status);

-- Only bounded, validated object metadata is staged. Media URLs are not stored.
CREATE TABLE source_catalog_observation (
    file_source_id INTEGER NOT NULL,
    directory_path TEXT NOT NULL,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL,
    kind TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    published_generation INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(file_source_id,directory_path,name),
    FOREIGN KEY(file_source_id,directory_path) REFERENCES source_catalog_directory(file_source_id,path) ON DELETE CASCADE
);
CREATE INDEX source_catalog_observation_revision ON source_catalog_observation(file_source_id,directory_path,revision,kind);
