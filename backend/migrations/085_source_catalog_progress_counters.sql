-- Progress used to aggregate every directory and observation on each poll and
-- on each worker step. These columns hold the same numbers as an incremental
-- projection; ?exact=1 keeps the aggregate path for reconciliation.
ALTER TABLE source_catalog_scan ADD COLUMN directories INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_scan ADD COLUMN completed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_scan ADD COLUMN pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_scan ADD COLUMN failed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_scan ADD COLUMN restricted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_scan ADD COLUMN empty INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_scan ADD COLUMN observed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_scan ADD COLUMN indexed INTEGER NOT NULL DEFAULT 0;
-- The publish cursor is the (directory, name) key of the last row a batch
-- consumed. Keyset pagination must resume across restarts from this exact
-- pair, because a directory can hold more rows than one batch.
ALTER TABLE source_catalog_scan ADD COLUMN publish_cursor_path TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_scan ADD COLUMN publish_cursor_name TEXT NOT NULL DEFAULT '';
-- Rows the publisher would still select in this generation, regardless of the
-- cursor position. The final-batch decision reads this instead of scanning
-- every pending observation before each commit.
ALTER TABLE source_catalog_scan ADD COLUMN publish_pending_remaining INTEGER NOT NULL DEFAULT 0;

-- Seed an in-flight generation once, so an upgraded database reports the same
-- numbers as the exact query before the next scan starts.
UPDATE source_catalog_scan SET
  directories=(SELECT count(*) FROM source_catalog_directory d
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id),
  completed=(SELECT count(*) FROM source_catalog_directory d
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
      AND (d.status='complete' OR (d.status='empty' AND d.error_code='not_found'))),
  pending=(SELECT count(*) FROM source_catalog_directory d
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
      AND d.status IN ('pending','listing_complete')),
  failed=(SELECT count(*) FROM source_catalog_directory d
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id AND d.status='failed'),
  restricted=(SELECT count(*) FROM source_catalog_directory d
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id AND d.status='restricted'),
  empty=(SELECT count(*) FROM source_catalog_directory d
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
      AND d.status='empty' AND d.error_code<>'not_found'),
  observed=(SELECT count(*) FROM source_catalog_directory d
    JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id),
  indexed=(SELECT count(*) FROM source_catalog_entry e
    WHERE e.file_source_id=source_catalog_scan.file_source_id AND e.last_seen_generation=source_catalog_scan.workflow_run_id),
  publish_pending_remaining=(SELECT count(*) FROM source_catalog_directory d
    JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
    WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
      AND d.status='complete' AND d.catalog_root<>'' AND o.publish_pending=1
      AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
        WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root));
