-- Keep both UNION ALL arms' column affinities identical. SQLite can then push
-- a correlated entity key into each arm instead of scanning the whole catalog
-- for every translation insertion or derived identity lookup.
DROP VIEW published_source_catalog_entry;
CREATE VIEW published_source_catalog_entry AS
 SELECT live.id,live.file_source_id,live.root_path,live.primary_code,live.title,live.work_id,live.created_at,live.updated_at,live.availability_status,live.status_checked_at,live.last_seen_generation,live.discovered_generation,live.content_fingerprint,live.layout_fingerprint,live.relocated_to_id,live.source_root,live.sidecar_json,live.sidecar_pending,live.sidecar_attempts,live.sidecar_revision,live.sidecar_transient_attempts,live.sidecar_retry_at,live.sidecar_failure,live.id AS rowid FROM source_catalog_entry live
 WHERE NOT EXISTS(SELECT 1 FROM source_catalog_entry_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE b.id=live.id AND op.state='preparing' AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1))
 UNION ALL
 SELECT b.id,b.file_source_id,b.root_path,b.primary_code,b.title,CAST(COALESCE(live.work_id,(SELECT work_id FROM source_catalog_entry identity_target WHERE identity_target.id=live.relocated_to_id)) AS INTEGER),b.created_at,b.updated_at,b.availability_status,b.status_checked_at,b.last_seen_generation,b.discovered_generation,b.content_fingerprint,b.layout_fingerprint,b.relocated_to_id,b.source_root,b.sidecar_json,b.sidecar_pending,b.sidecar_attempts,b.sidecar_revision,b.sidecar_transient_attempts,b.sidecar_retry_at,b.sidecar_failure,b.id AS rowid FROM source_catalog_entry_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id
 LEFT JOIN source_catalog_entry live ON b.id=live.id
 WHERE op.state='preparing' AND b.existed=1 AND op.id<>(SELECT operation_id FROM source_catalog_publication_writer WHERE id=1);

-- A name mismatch must not rescan every remaining size in the comparison.
CREATE INDEX source_catalog_duplicate_available_size
 ON source_catalog_duplicate_available(comparison_id,size_bytes,name);
