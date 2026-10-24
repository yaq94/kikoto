CREATE TABLE source_catalog_episode_pending (
    source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
    parent_root TEXT NOT NULL,
    catalog_entry_root TEXT NOT NULL,
    catalog_entry_id INTEGER NOT NULL REFERENCES source_catalog_entry(id) ON DELETE CASCADE,
    PRIMARY KEY(source_id, parent_root, catalog_entry_id)
);

CREATE INDEX source_catalog_episode_pending_entry
    ON source_catalog_episode_pending(catalog_entry_id);

CREATE INDEX source_catalog_episode_pending_parent
    ON source_catalog_episode_pending(source_id, parent_root);

CREATE UNIQUE INDEX source_catalog_episode_pending_root
    ON source_catalog_episode_pending(source_id, parent_root, catalog_entry_root);

CREATE TABLE source_catalog_episode_publication (
    source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
    parent_root TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending', 'published')),
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(source_id, parent_root)
);
