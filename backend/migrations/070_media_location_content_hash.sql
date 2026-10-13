-- content_hash caches the SHA-256 of a local file for identical-file
-- detection. content_hash_version is the size and modification time the hash
-- was computed for; a file observed with another version has no usable hash
-- until it is hashed again.
ALTER TABLE media_file_location ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE media_file_location ADD COLUMN content_hash_version TEXT NOT NULL DEFAULT '';
