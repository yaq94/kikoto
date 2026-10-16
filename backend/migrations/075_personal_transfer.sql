-- Opaque transfer references preserve exact local objects across personal backups.
-- They are aliases only: shared identity and source ownership remain independent.
CREATE TABLE personal_transfer_ref (
 ref TEXT PRIMARY KEY,
 work_id INTEGER REFERENCES work(id) ON DELETE CASCADE,
 catalog_entry_id INTEGER REFERENCES source_catalog_entry(id) ON DELETE CASCADE,
 series_id INTEGER REFERENCES catalog_series(id) ON DELETE CASCADE,
 is_primary INTEGER NOT NULL DEFAULT 0 CHECK(is_primary IN (0,1)),
 object_kind TEXT NOT NULL DEFAULT 'work' CHECK(object_kind IN ('work','episode','series')),
 CHECK((work_id IS NOT NULL)+(catalog_entry_id IS NOT NULL)+(series_id IS NOT NULL)=1),
 CHECK(is_primary=0 OR work_id IS NOT NULL)
);
CREATE UNIQUE INDEX personal_transfer_work_primary ON personal_transfer_ref(work_id) WHERE is_primary=1;
CREATE INDEX personal_transfer_work ON personal_transfer_ref(work_id);
CREATE UNIQUE INDEX personal_transfer_catalog ON personal_transfer_ref(catalog_entry_id) WHERE catalog_entry_id IS NOT NULL;
CREATE UNIQUE INDEX personal_transfer_series ON personal_transfer_ref(series_id) WHERE series_id IS NOT NULL;
INSERT INTO personal_transfer_ref(ref,work_id,is_primary) SELECT 'w:'||lower(hex(randomblob(16))),id,1 FROM work;
INSERT INTO personal_transfer_ref(ref,catalog_entry_id) SELECT 'c:'||lower(hex(randomblob(16))),id FROM source_catalog_entry;
INSERT INTO personal_transfer_ref(ref,series_id,object_kind) SELECT 's:'||lower(hex(randomblob(16))),id,'series' FROM catalog_series;
CREATE TRIGGER personal_transfer_work_insert AFTER INSERT ON work
BEGIN
 INSERT INTO personal_transfer_ref(ref,work_id,is_primary) VALUES('w:'||lower(hex(randomblob(16))),NEW.id,1);
END;
CREATE TRIGGER personal_transfer_catalog_insert AFTER INSERT ON source_catalog_entry
BEGIN
 INSERT INTO personal_transfer_ref(ref,catalog_entry_id) VALUES('c:'||lower(hex(randomblob(16))),NEW.id);
END;
CREATE TRIGGER personal_transfer_series_insert AFTER INSERT ON catalog_series
BEGIN
 INSERT INTO personal_transfer_ref(ref,series_id,object_kind) VALUES('s:'||lower(hex(randomblob(16))),NEW.id,'series');
END;

-- An unmatched resume point remains portable without inventing media or a timeline.
CREATE TABLE personal_transfer_progress (
 user_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
 work_id INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
 progress_json TEXT NOT NULL CHECK(json_valid(progress_json)),
 PRIMARY KEY(user_id,work_id)
);
