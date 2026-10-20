-- Cleanup walks every published card and asks which folders below it now have
-- their own entry. A card's real root is source_root when it was split from a
-- folder and root_path otherwise. Without an index on that root, SQLite
-- scanned the whole entry table through the relocated_to_id index for every
-- card in a batch.
CREATE INDEX source_catalog_entry_live_root
    ON source_catalog_entry(file_source_id, root_path)
    WHERE relocated_to_id IS NULL;

CREATE INDEX source_catalog_entry_source_root
    ON source_catalog_entry(file_source_id, source_root)
    WHERE source_root<>'';
