CREATE TABLE source_catalog_split_retirement (
    operation_id INTEGER NOT NULL REFERENCES source_catalog_publication(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    generation INTEGER NOT NULL,
    after_entry_id INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(operation_id, root_path)
) WITHOUT ROWID;

CREATE INDEX source_catalog_entry_split_retirement
    ON source_catalog_entry(file_source_id, id)
    WHERE source_root<>'' AND relocated_to_id IS NULL;
