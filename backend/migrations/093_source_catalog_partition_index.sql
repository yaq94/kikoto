-- The progress endpoint folds the directory rows of one generation by
-- partition_path. Without an index in that order SQLite read every matching
-- row and then built a temporary b-tree, so an admin page that polls while a
-- scan runs repeated the sort on each request. The composite index keeps the
-- group-by order and also serves count(DISTINCT partition_path).
CREATE INDEX source_catalog_directory_partition
  ON source_catalog_directory(file_source_id, generation, partition_path);
