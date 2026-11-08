-- Kind filtering and name pagination share one ordered range. A directory
-- without folders must not scan every audio observation under the writer.
DROP INDEX source_catalog_observation_revision;
CREATE INDEX source_catalog_observation_revision
    ON source_catalog_observation(file_source_id,directory_path,revision,kind,name);

CREATE INDEX source_catalog_listing_item_kind_order
    ON source_catalog_listing_item(listing_id,directory_path,kind,name);
