-- A creator folder can hold many independent works as loose audio files, and
-- one work can span a numbered run of files. Both need their own card without
-- changing where the files live. source_root records the real directory, while
-- root_path becomes the work's identity; an empty source_root keeps the old
-- meaning of root_path.
ALTER TABLE source_catalog_entry ADD COLUMN source_root TEXT NOT NULL DEFAULT '';

-- The split decision is taken once per directory, when its listing completes.
-- single keeps one card, prefix groups files that share a work code such as
-- N006, and file groups the loose files by base title, so 亦弟亦妹1..6 stays one
-- work while unrelated titles become separate cards.
ALTER TABLE source_catalog_directory ADD COLUMN work_split TEXT NOT NULL DEFAULT '';
