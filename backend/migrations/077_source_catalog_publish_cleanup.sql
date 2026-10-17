-- A rescan publishes an entry's current files but leaves rows observed by an
-- older generation in place. A directory that stops being its own work then
-- keeps every old track, so a category card still lists children that now have
-- their own cards. The cleanup cursor records how far this generation has
-- retired stale file rows for entries it republished; it never touches an entry
-- the generation did not publish.
ALTER TABLE source_catalog_scan ADD COLUMN publish_cleanup_cursor INTEGER NOT NULL DEFAULT 0;

-- The file-title search checks one candidate at a time, and SQLite has no
-- usable index on title alone. Cover the catalog entry as well so the lookup
-- answers from the index without reading the table row.
CREATE INDEX source_catalog_file_title
    ON source_catalog_file(catalog_entry_id,title);
