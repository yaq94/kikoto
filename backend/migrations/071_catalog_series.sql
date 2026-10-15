CREATE TABLE catalog_series (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    work_id INTEGER UNIQUE REFERENCES work(id) ON DELETE RESTRICT,
    source_id INTEGER REFERENCES file_source(id) ON DELETE SET NULL,
    source_key TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX catalog_series_source ON catalog_series(source_id,source_key)
    WHERE source_id IS NOT NULL AND source_key IS NOT NULL;

CREATE TABLE catalog_series_member (
    series_id INTEGER NOT NULL REFERENCES catalog_series(id) ON DELETE RESTRICT,
    catalog_entry_id INTEGER NOT NULL UNIQUE REFERENCES source_catalog_entry(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL CHECK(position > 0),
    evidence TEXT NOT NULL CHECK(evidence IN ('directory','manual')),
    PRIMARY KEY(series_id,position)
);

CREATE TABLE catalog_series_preview (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
    payload_json TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','completed')),
    series_id INTEGER REFERENCES catalog_series(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX catalog_series_preview_actor ON catalog_series_preview(actor_id,status,created_at);
