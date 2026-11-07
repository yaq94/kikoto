-- Directory classification persists derived name evidence and bounded cursors.
ALTER TABLE source_catalog_directory ADD COLUMN classification_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_listing ADD COLUMN partition_cursor TEXT NOT NULL DEFAULT '';
CREATE TABLE source_catalog_grouping (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
 root_path TEXT NOT NULL,
 version TEXT NOT NULL,
 phase TEXT NOT NULL DEFAULT 'names',
 cursor TEXT NOT NULL DEFAULT '',
 mode TEXT NOT NULL DEFAULT '',
 parent_ready INTEGER NOT NULL DEFAULT 0,
 parent_needed INTEGER NOT NULL DEFAULT 0,
 parent_cursor_path TEXT NOT NULL DEFAULT '',
 parent_cursor_name TEXT NOT NULL DEFAULT '',
 UNIQUE(file_source_id,root_path,version)
);
CREATE TABLE source_catalog_grouping_name (
 grouping_id INTEGER NOT NULL REFERENCES source_catalog_grouping(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 normalized_name TEXT NOT NULL,
 folded_name TEXT NOT NULL,
 size_bytes INTEGER NOT NULL,
 content_hash TEXT NOT NULL,
 work_prefix TEXT NOT NULL,
 identity_title TEXT NOT NULL,
 base_title TEXT NOT NULL,
 part_base TEXT NOT NULL,
 part_rank INTEGER NOT NULL,
 structured_key TEXT NOT NULL,
 structured_position INTEGER NOT NULL,
 prefix_position INTEGER NOT NULL,
 episode_series TEXT NOT NULL,
 episode_position INTEGER NOT NULL,
 episode_json TEXT NOT NULL,
 folded_base TEXT NOT NULL DEFAULT '',
 group_key TEXT NOT NULL DEFAULT '',
 target_json TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(grouping_id,name)
) WITHOUT ROWID;
CREATE INDEX source_catalog_grouping_prefix ON source_catalog_grouping_name(grouping_id,work_prefix,prefix_position,name);
CREATE INDEX source_catalog_grouping_part ON source_catalog_grouping_name(grouping_id,part_base,part_rank,name);
CREATE INDEX source_catalog_grouping_structured ON source_catalog_grouping_name(grouping_id,structured_key,structured_position);
CREATE INDEX source_catalog_grouping_episode ON source_catalog_grouping_name(grouping_id,episode_series,episode_position);
CREATE INDEX source_catalog_grouping_hash ON source_catalog_grouping_name(grouping_id,content_hash,name);
CREATE INDEX source_catalog_grouping_size ON source_catalog_grouping_name(grouping_id,size_bytes,name);
CREATE INDEX source_catalog_grouping_normalized ON source_catalog_grouping_name(grouping_id,normalized_name,size_bytes,name);
CREATE INDEX source_catalog_grouping_folded ON source_catalog_grouping_name(grouping_id,folded_name,size_bytes,name);
CREATE TABLE source_catalog_grouping_folder (
 grouping_id INTEGER NOT NULL REFERENCES source_catalog_grouping(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 canonical TEXT NOT NULL,
 position INTEGER NOT NULL,
 PRIMARY KEY(grouping_id,name)
) WITHOUT ROWID;
CREATE INDEX source_catalog_grouping_folder_canonical ON source_catalog_grouping_folder(grouping_id,canonical,position);
CREATE TABLE source_catalog_grouping_bucket (
 grouping_id INTEGER NOT NULL REFERENCES source_catalog_grouping(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,
 key TEXT NOT NULL,
 payload TEXT NOT NULL,
 PRIMARY KEY(grouping_id,kind,key)
) WITHOUT ROWID;
CREATE TABLE source_catalog_grouping_target (
 grouping_id INTEGER NOT NULL REFERENCES source_catalog_grouping(id) ON DELETE CASCADE,
 key TEXT NOT NULL,
 target_json TEXT NOT NULL,
 PRIMARY KEY(grouping_id,key)
) WITHOUT ROWID;
CREATE TABLE source_catalog_finite_directory (
 listing_id INTEGER NOT NULL REFERENCES source_catalog_listing(id) ON DELETE CASCADE,
 path TEXT NOT NULL,
 depth INTEGER NOT NULL,
 next_page INTEGER NOT NULL DEFAULT 1,
 expected_total INTEGER NOT NULL DEFAULT -1,
 received INTEGER NOT NULL DEFAULT 0,
 complete INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(listing_id,path)
) WITHOUT ROWID;
CREATE INDEX source_catalog_finite_pending ON source_catalog_finite_directory(listing_id,complete,path);
CREATE UNIQUE INDEX source_catalog_finite_active ON source_catalog_listing(file_source_id,directory_path) WHERE observed_version='finite' AND state IN ('fetching','complete','applying');
CREATE TABLE source_catalog_finite_target (
 listing_id INTEGER NOT NULL REFERENCES source_catalog_listing(id) ON DELETE CASCADE,
 root_path TEXT NOT NULL,
 source_root TEXT NOT NULL,
 title TEXT NOT NULL,
 evidence_json TEXT NOT NULL DEFAULT '',
 catalog_entry_id INTEGER,
 prepared INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(listing_id,root_path)
) WITHOUT ROWID;
CREATE TABLE source_catalog_finite_file (
 listing_id INTEGER NOT NULL REFERENCES source_catalog_listing(id) ON DELETE CASCADE,
 input_path TEXT NOT NULL,
 root_path TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 PRIMARY KEY(listing_id,input_path)
) WITHOUT ROWID;
CREATE INDEX source_catalog_finite_target_files ON source_catalog_finite_file(listing_id,root_path,input_path);
CREATE INDEX source_catalog_finite_target_uncreated ON source_catalog_finite_target(listing_id,root_path) WHERE catalog_entry_id IS NULL;
CREATE INDEX source_catalog_finite_target_unprepared ON source_catalog_finite_target(listing_id,root_path) WHERE prepared=0;
CREATE INDEX source_catalog_finite_target_entry ON source_catalog_finite_target(listing_id,catalog_entry_id);
CREATE TABLE source_catalog_duplicate_comparison (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 child_grouping INTEGER NOT NULL REFERENCES source_catalog_grouping(id) ON DELETE CASCADE,
 parent_grouping INTEGER NOT NULL REFERENCES source_catalog_grouping(id) ON DELETE CASCADE,
 ancestor INTEGER NOT NULL DEFAULT 0,
 cursor TEXT NOT NULL DEFAULT '',
 complete INTEGER NOT NULL DEFAULT 0,
 duplicate INTEGER NOT NULL DEFAULT 0,
 matched INTEGER NOT NULL DEFAULT 0,
 named INTEGER NOT NULL DEFAULT 0,
 exact INTEGER NOT NULL DEFAULT 0,
 cleanup_cursor TEXT NOT NULL DEFAULT '',
 parent_cursor TEXT NOT NULL DEFAULT '',
 parent_ready INTEGER NOT NULL DEFAULT 0,
 unique_count INTEGER NOT NULL DEFAULT 0,
 UNIQUE(child_grouping,parent_grouping)
);
CREATE TABLE source_catalog_duplicate_match (
 comparison_id INTEGER NOT NULL REFERENCES source_catalog_duplicate_comparison(id) ON DELETE CASCADE,
 child_name TEXT NOT NULL,
 parent_name TEXT NOT NULL,
 PRIMARY KEY(comparison_id,child_name),
 UNIQUE(comparison_id,parent_name)
) WITHOUT ROWID;
CREATE INDEX source_catalog_duplicate_parent ON source_catalog_duplicate_comparison(parent_grouping,id);
CREATE TABLE source_catalog_duplicate_available (
 comparison_id INTEGER NOT NULL REFERENCES source_catalog_duplicate_comparison(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 normalized_name TEXT NOT NULL,
 folded_name TEXT NOT NULL,
 size_bytes INTEGER NOT NULL,
 content_hash TEXT NOT NULL,
 PRIMARY KEY(comparison_id,name)
) WITHOUT ROWID;
CREATE INDEX source_catalog_duplicate_available_hash ON source_catalog_duplicate_available(comparison_id,content_hash,name);
CREATE INDEX source_catalog_duplicate_available_normalized ON source_catalog_duplicate_available(comparison_id,normalized_name,size_bytes,name);
CREATE INDEX source_catalog_duplicate_available_folded ON source_catalog_duplicate_available(comparison_id,folded_name,size_bytes,name);
