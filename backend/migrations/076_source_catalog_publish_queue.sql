-- Publishing used to re-evaluate every complete directory with a correlated
-- EXISTS. A tail of five unpublished observations then took 36 seconds because
-- SQLite only applied LIMIT after filtering all 3,500 directories. The flag
-- mirrors published_generation = 0, while the partial index seeks directly to
-- rows that can be published. The root table replaces the per-row EXISTS: it
-- holds one row per generation and root that has complete audio.
ALTER TABLE source_catalog_observation ADD COLUMN publish_pending INTEGER NOT NULL DEFAULT 0
    CHECK(publish_pending IN (0,1));
UPDATE source_catalog_observation SET publish_pending=1 WHERE published_generation=0;

CREATE INDEX source_catalog_observation_pending
    ON source_catalog_observation(file_source_id,directory_path,name)
    WHERE publish_pending=1;

CREATE TABLE source_catalog_audio_root (
    file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    catalog_root TEXT NOT NULL,
    PRIMARY KEY(file_source_id,generation,catalog_root)
);

INSERT OR IGNORE INTO source_catalog_audio_root(file_source_id,generation,catalog_root)
SELECT DISTINCT a.file_source_id,a.generation,a.catalog_root
FROM source_catalog_directory a
JOIN source_catalog_observation b
  ON b.file_source_id=a.file_source_id
 AND b.directory_path=a.path
 AND b.revision=a.revision
WHERE a.status='complete' AND a.catalog_root<>'' AND b.kind='audio';
