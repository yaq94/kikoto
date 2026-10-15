-- kikoto:foreign_keys=off
-- Preserve work ids and every referring row while relaxing code uniqueness.
-- Views and triggers are restored verbatim after the table swap.
DROP VIEW "metadata_tag_resolution";
DROP VIEW "work_primary_circle";
DROP TRIGGER "metadata_snapshot_card_summary_insert";
DROP TRIGGER "metadata_snapshot_card_summary_update";
DROP TRIGGER "metadata_tags_snapshot_delete";
DROP TRIGGER "metadata_tags_snapshot_insert";
DROP TRIGGER "metadata_tags_snapshot_update";
DROP TRIGGER "recommendation_catalog_work_credit_delete";
DROP TRIGGER "recommendation_catalog_work_credit_insert";
DROP TRIGGER "recommendation_catalog_work_credit_update";
DROP TRIGGER "recommendation_catalog_work_delete";
DROP TRIGGER "recommendation_catalog_work_insert";
DROP TRIGGER "recommendation_catalog_work_party_delete";
DROP TRIGGER "recommendation_catalog_work_party_insert";
DROP TRIGGER "recommendation_catalog_work_party_update";
DROP TRIGGER "recommendation_catalog_work_tag_delete";
DROP TRIGGER "recommendation_catalog_work_tag_insert";
DROP TRIGGER "recommendation_catalog_work_tag_update";
DROP TRIGGER "recommendation_names_genre_delete";
DROP TRIGGER "recommendation_names_genre_insert";
DROP TRIGGER "recommendation_names_genre_update";
DROP TRIGGER "recommendation_names_metadata_tag_delete";
DROP TRIGGER "recommendation_names_metadata_tag_insert";
DROP TRIGGER "recommendation_names_metadata_tag_name_delete";
DROP TRIGGER "recommendation_names_metadata_tag_name_insert";
DROP TRIGGER "recommendation_names_metadata_tag_name_update";
DROP TRIGGER "recommendation_names_metadata_tag_provider_name_delete";
DROP TRIGGER "recommendation_names_metadata_tag_provider_name_insert";
DROP TRIGGER "recommendation_names_metadata_tag_provider_name_update";
DROP TRIGGER "recommendation_names_metadata_tag_update";
DROP TRIGGER "recommendation_names_party_alias_delete";
DROP TRIGGER "recommendation_names_party_alias_insert";
DROP TRIGGER "recommendation_names_party_alias_update";
DROP TRIGGER "recommendation_names_party_delete";
DROP TRIGGER "recommendation_names_party_insert";
DROP TRIGGER "recommendation_names_party_update";
DROP TRIGGER "recommendation_names_person_alias_delete";
DROP TRIGGER "recommendation_names_person_alias_insert";
DROP TRIGGER "recommendation_names_person_alias_update";
DROP TRIGGER "recommendation_names_person_delete";
DROP TRIGGER "recommendation_names_person_insert";
DROP TRIGGER "recommendation_names_person_update";
DROP TRIGGER "recommendation_names_tag_delete";
DROP TRIGGER "recommendation_names_tag_insert";
DROP TRIGGER "recommendation_names_tag_update";
DROP TRIGGER "recommendation_revision_config_delete";
DROP TRIGGER "recommendation_revision_config_insert";
DROP TRIGGER "recommendation_revision_config_update";
DROP TRIGGER "recommendation_revision_profile_dlsite_genre_name_delete";
DROP TRIGGER "recommendation_revision_profile_dlsite_genre_name_insert";
DROP TRIGGER "recommendation_revision_profile_dlsite_genre_name_update";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_delete";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_insert";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_name_delete";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_name_insert";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_name_update";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_provider_name_delete";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_provider_name_insert";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_provider_name_update";
DROP TRIGGER "recommendation_revision_profile_metadata_tag_update";
DROP TRIGGER "recommendation_revision_profile_party_alias_delete";
DROP TRIGGER "recommendation_revision_profile_party_alias_insert";
DROP TRIGGER "recommendation_revision_profile_party_alias_update";
DROP TRIGGER "recommendation_revision_profile_party_delete";
DROP TRIGGER "recommendation_revision_profile_party_insert";
DROP TRIGGER "recommendation_revision_profile_party_update";
DROP TRIGGER "recommendation_revision_profile_person_alias_delete";
DROP TRIGGER "recommendation_revision_profile_person_alias_insert";
DROP TRIGGER "recommendation_revision_profile_person_alias_update";
DROP TRIGGER "recommendation_revision_profile_person_delete";
DROP TRIGGER "recommendation_revision_profile_person_insert";
DROP TRIGGER "recommendation_revision_profile_person_update";
DROP TRIGGER "recommendation_revision_profile_tag_delete";
DROP TRIGGER "recommendation_revision_profile_tag_insert";
DROP TRIGGER "recommendation_revision_profile_tag_update";
DROP TRIGGER "recommendation_revision_tag_namespace_update";
DROP TRIGGER "recommendation_revision_user_state_delete";
DROP TRIGGER "recommendation_revision_user_state_insert";
DROP TRIGGER "recommendation_revision_user_state_update";
DROP TRIGGER "recommendation_revision_work_credit_delete";
DROP TRIGGER "recommendation_revision_work_credit_insert";
DROP TRIGGER "recommendation_revision_work_credit_update";
DROP TRIGGER "recommendation_revision_work_delete";
DROP TRIGGER "recommendation_revision_work_insert";
DROP TRIGGER "recommendation_revision_work_party_delete";
DROP TRIGGER "recommendation_revision_work_party_insert";
DROP TRIGGER "recommendation_revision_work_party_update";
DROP TRIGGER "recommendation_revision_work_tag_delete";
DROP TRIGGER "recommendation_revision_work_tag_insert";
DROP TRIGGER "recommendation_revision_work_tag_update";
DROP TRIGGER "remote_title_variant_delete";
DROP TRIGGER "remote_title_variant_insert";
DROP TRIGGER "remote_title_variant_update";
DROP TRIGGER "validate_workflow_definition_insert";
DROP TRIGGER "validate_workflow_definition_update";
DROP TRIGGER "validate_workflow_trigger_insert";
DROP TRIGGER "validate_workflow_trigger_update";
DROP TRIGGER "work_search_code_alias_delete";
DROP TRIGGER "work_search_code_alias_insert";
DROP TRIGGER "work_search_code_alias_update";
DROP TRIGGER "work_search_credit_delete";
DROP TRIGGER "work_search_credit_insert";
DROP TRIGGER "work_search_credit_update";
DROP TRIGGER "work_search_dlsite_genre_delete";
DROP TRIGGER "work_search_dlsite_genre_insert";
DROP TRIGGER "work_search_dlsite_genre_name_delete";
DROP TRIGGER "work_search_dlsite_genre_name_insert";
DROP TRIGGER "work_search_dlsite_genre_name_update";
DROP TRIGGER "work_search_dlsite_genre_update";
DROP TRIGGER "work_search_edition_delete";
DROP TRIGGER "work_search_edition_insert";
DROP TRIGGER "work_search_edition_update";
DROP TRIGGER "work_search_metadata_tag_display_update";
DROP TRIGGER "work_search_metadata_tag_name_delete";
DROP TRIGGER "work_search_metadata_tag_name_insert";
DROP TRIGGER "work_search_metadata_tag_name_update";
DROP TRIGGER "work_search_metadata_tag_provider_name_delete";
DROP TRIGGER "work_search_metadata_tag_provider_name_insert";
DROP TRIGGER "work_search_metadata_tag_provider_name_update";
DROP TRIGGER "work_search_metadata_tag_state_before";
DROP TRIGGER "work_search_metadata_tag_state_update";
DROP TRIGGER "work_search_override_delete";
DROP TRIGGER "work_search_override_insert";
DROP TRIGGER "work_search_override_update";
DROP TRIGGER "work_search_party_alias_delete";
DROP TRIGGER "work_search_party_alias_insert";
DROP TRIGGER "work_search_party_alias_update";
DROP TRIGGER "work_search_party_external_id_delete";
DROP TRIGGER "work_search_party_external_id_insert";
DROP TRIGGER "work_search_party_external_id_update";
DROP TRIGGER "work_search_party_link_delete";
DROP TRIGGER "work_search_party_link_insert";
DROP TRIGGER "work_search_party_link_update";
DROP TRIGGER "work_search_party_update";
DROP TRIGGER "work_search_person_update";
DROP TRIGGER "work_search_shared_genre_delete";
DROP TRIGGER "work_search_shared_genre_insert";
DROP TRIGGER "work_search_shared_genre_update";
DROP TRIGGER "work_search_tag_link_delete";
DROP TRIGGER "work_search_tag_link_insert";
DROP TRIGGER "work_search_tag_link_update";
DROP TRIGGER "work_search_tag_update";
DROP TRIGGER "work_search_variant_delete";
DROP TRIGGER "work_search_variant_insert";
DROP TRIGGER "work_search_variant_update";
DROP TRIGGER "work_search_work_delete";
DROP TRIGGER "work_search_work_insert";
DROP TRIGGER "work_search_work_update";
DROP TRIGGER "work_snapshot_projection_catalog_delete";
DROP TRIGGER "work_snapshot_projection_catalog_update";
DROP TRIGGER "work_snapshot_projection_credit_delete";
DROP TRIGGER "work_snapshot_projection_party_delete";
DROP TRIGGER "work_snapshot_projection_party_update";
DROP TRIGGER "work_title_language_edition_delete";
DROP TRIGGER "work_title_language_edition_insert";
DROP TRIGGER "work_title_language_edition_update";
DROP TRIGGER "work_title_language_override_delete";
DROP TRIGGER "work_title_language_override_insert";
DROP TRIGGER "work_title_language_override_update";
DROP TRIGGER "work_title_language_variant_delete";
DROP TRIGGER "work_title_language_variant_insert";
DROP TRIGGER "work_title_language_variant_update";
DROP TRIGGER "work_title_language_work_insert";
DROP TRIGGER "work_title_language_work_update";

CREATE TABLE work_next (
  id INTEGER PRIMARY KEY,
  primary_code TEXT NOT NULL DEFAULT '',
  work_type TEXT NOT NULL DEFAULT 'audio',
  title TEXT NOT NULL,
  title_kana TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  release_date TEXT,
  age_rating TEXT NOT NULL DEFAULT '',
  cover_asset_id INTEGER,
  duration_seconds INTEGER,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
, rating_average REAL, sales_count INTEGER CHECK(sales_count IS NULL OR sales_count >= 0), regular_price INTEGER CHECK(regular_price IS NULL OR regular_price >= 0), current_price INTEGER CHECK(current_price IS NULL OR current_price >= 0), price_currency TEXT NOT NULL DEFAULT '', is_permanently_free INTEGER CHECK(is_permanently_free IS NULL OR is_permanently_free IN (0, 1)), recommendation_explore_key INTEGER NOT NULL DEFAULT 0);
INSERT INTO work_next SELECT * FROM work;
DROP TABLE work;
ALTER TABLE work_next RENAME TO work;
CREATE INDEX idx_work_created_at
  ON work(created_at, id);
CREATE INDEX idx_work_demo_eligibility
  ON work(is_permanently_free, age_rating, id);
CREATE INDEX idx_work_primary_code_upper
  ON work(UPPER(primary_code));
CREATE INDEX idx_work_rating_average
  ON work(rating_average DESC, id DESC);
CREATE INDEX idx_work_recommendation_explore ON work(recommendation_explore_key, id);
CREATE INDEX idx_work_release_date
  ON work(release_date, created_at, id);
CREATE INDEX idx_work_release_date_nulls_last
  ON work(release_date IS NULL, release_date, created_at, id);
CREATE INDEX idx_work_sales_count
  ON work(sales_count DESC, id DESC);
CREATE UNIQUE INDEX idx_work_numbered_code ON work(primary_code) WHERE primary_code <> '';

CREATE TABLE logical_work_next (
  id INTEGER PRIMARY KEY,
  canonical_work_id INTEGER REFERENCES work(id) ON DELETE SET NULL,
  canonical_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO logical_work_next SELECT * FROM logical_work;
DROP TABLE logical_work;
ALTER TABLE logical_work_next RENAME TO logical_work;
CREATE INDEX idx_fk_logical_work_canonical_work_id ON logical_work(canonical_work_id);
CREATE UNIQUE INDEX idx_logical_work_numbered_code ON logical_work(canonical_code) WHERE canonical_code <> '';

CREATE VIEW metadata_tag_resolution AS
 WITH RECURSIVE resolution(source_tag_id,tag_id,next_tag_id) AS (
 SELECT tag_id,tag_id,merged_into_tag_id FROM metadata_tag
 UNION
 SELECT resolution.source_tag_id,concept.tag_id,concept.merged_into_tag_id FROM resolution
 JOIN metadata_tag AS concept ON concept.tag_id=resolution.next_tag_id
 ) SELECT source_tag_id,tag_id AS resolved_tag_id FROM resolution WHERE next_tag_id IS NULL;

CREATE VIEW work_primary_circle AS
WITH family_target AS (
  SELECT
    work.id AS work_id,
    COALESCE(logical.canonical_work_id, work.id) AS canonical_work_id
  FROM work
  LEFT JOIN work_edition AS edition ON edition.work_id = work.id
  LEFT JOIN logical_work AS logical ON logical.id = edition.logical_work_id
), ranked AS (
  SELECT
    target.work_id,
    relation.party_id,
    party.display_name,
    COALESCE(external.external_id, '') AS external_id,
    relation.role,
    relation.source,
    ROW_NUMBER() OVER (
      PARTITION BY target.work_id
      ORDER BY
        CASE
          WHEN relation.work_id = target.work_id AND relation.source = 'manual_override' THEN 0
          WHEN relation.work_id = target.canonical_work_id AND relation.source = 'manual_override' THEN 1
          WHEN relation.work_id = target.canonical_work_id
            AND edition.maker_id <> ''
            AND UPPER(COALESCE(external.external_id, '')) = UPPER(edition.maker_id) THEN 2
          WHEN relation.work_id = target.canonical_work_id
            AND relation_provider.code = 'dlsite'
            AND relation.source IN ('dlsite_snapshot', 'dlsite_product', 'dlsite_edition') THEN 3
          WHEN relation.work_id = target.canonical_work_id
            AND relation_provider.code = 'dlsite' THEN 4
          WHEN edition.maker_id <> ''
            AND UPPER(COALESCE(external.external_id, '')) = UPPER(edition.maker_id) THEN 5
          WHEN relation_provider.code = 'dlsite'
            AND relation.source IN ('dlsite_snapshot', 'dlsite_product', 'dlsite_edition') THEN 6
          WHEN relation_provider.code = 'dlsite' THEN 7
          WHEN relation.source = 'remote_source' THEN 9
          ELSE 8
        END,
        relation.party_id ASC
    ) AS position
  FROM family_target AS target
  INNER JOIN work_party AS relation
    ON relation.work_id = target.work_id
    OR relation.work_id = target.canonical_work_id
  INNER JOIN party ON party.id = relation.party_id
  LEFT JOIN work_edition AS edition ON edition.work_id = relation.work_id
  LEFT JOIN metadata_provider AS relation_provider ON relation_provider.id = relation.provider_id
  LEFT JOIN party_external_id AS external ON external.id = (
    SELECT candidate.id
    FROM party_external_id AS candidate
    LEFT JOIN metadata_provider AS candidate_provider ON candidate_provider.id = candidate.provider_id
    WHERE candidate.party_id = party.id
    ORDER BY
      CASE WHEN candidate_provider.code = 'dlsite' THEN 0 ELSE 1 END,
      candidate.is_primary DESC,
      candidate.id ASC
    LIMIT 1
  )
  WHERE relation.role = 'circle'
)
SELECT work_id, party_id, display_name, external_id, role, source
FROM ranked
WHERE position = 1;

CREATE TRIGGER metadata_snapshot_card_summary_insert
AFTER INSERT ON metadata_snapshot
BEGIN
  INSERT INTO metadata_snapshot_card_summary_dirty (snapshot_id)
  SELECT NEW.id WHERE NOT EXISTS (
    SELECT 1 FROM metadata_snapshot_card_summary_dirty WHERE snapshot_id = NEW.id
  );
END;

CREATE TRIGGER metadata_snapshot_card_summary_update
AFTER UPDATE OF snapshot_json ON metadata_snapshot
WHEN OLD.snapshot_json IS NOT NEW.snapshot_json
BEGIN
  DELETE FROM metadata_snapshot_card_summary WHERE snapshot_id = NEW.id;
  INSERT INTO metadata_snapshot_card_summary_dirty (snapshot_id)
  SELECT NEW.id WHERE NOT EXISTS (
    SELECT 1 FROM metadata_snapshot_card_summary_dirty WHERE snapshot_id = NEW.id
  );
END;

CREATE TRIGGER metadata_tags_snapshot_delete BEFORE DELETE ON metadata_snapshot BEGIN
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id)
 SELECT projection.work_id FROM work_metadata_tag_projection AS projection JOIN work ON work.id=projection.work_id WHERE source_work_id=OLD.work_id;
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id) SELECT id FROM work WHERE id=OLD.work_id;
END;

CREATE TRIGGER metadata_tags_snapshot_insert AFTER INSERT ON metadata_snapshot BEGIN
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id)
 SELECT projection.work_id FROM work_metadata_tag_projection AS projection JOIN work ON work.id=projection.work_id WHERE source_work_id=NEW.work_id;
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id) SELECT id FROM work WHERE id=NEW.work_id;
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id)
 SELECT sibling.work_id FROM work_edition AS edition JOIN work_edition AS sibling ON sibling.logical_work_id=edition.logical_work_id JOIN work ON work.id=sibling.work_id WHERE edition.work_id=NEW.work_id;
END;

CREATE TRIGGER metadata_tags_snapshot_update AFTER UPDATE OF snapshot_json,work_id,provider_id,request_locale ON metadata_snapshot BEGIN
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id)
 SELECT projection.work_id FROM work_metadata_tag_projection AS projection JOIN work ON work.id=projection.work_id WHERE source_work_id IN (OLD.work_id,NEW.work_id);
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id) SELECT id FROM work WHERE id IN (OLD.work_id,NEW.work_id);
 INSERT OR IGNORE INTO work_metadata_tag_dirty(work_id)
 SELECT sibling.work_id FROM work_edition AS edition JOIN work_edition AS sibling ON sibling.logical_work_id=edition.logical_work_id JOIN work ON work.id=sibling.work_id WHERE edition.work_id IN (OLD.work_id,NEW.work_id);
END;

CREATE TRIGGER recommendation_catalog_work_credit_delete AFTER DELETE ON work_credit BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (OLD.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_credit_insert AFTER INSERT ON work_credit BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (NEW.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_credit_update AFTER UPDATE ON work_credit BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (OLD.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (NEW.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_delete AFTER DELETE ON work BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (OLD.id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_insert AFTER INSERT ON work BEGIN
  UPDATE work SET recommendation_explore_key = (NEW.id * 1103515245 + 12345) % 2147483647 WHERE id = NEW.id;
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (NEW.id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_party_delete AFTER DELETE ON work_party BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (OLD.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_party_insert AFTER INSERT ON work_party BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (NEW.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_party_update AFTER UPDATE ON work_party BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (OLD.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (NEW.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_tag_delete AFTER DELETE ON work_tag BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (OLD.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_tag_insert AFTER INSERT ON work_tag BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (NEW.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_catalog_work_tag_update AFTER UPDATE ON work_tag BEGIN
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (OLD.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_catalog_dirty(work_id) VALUES (NEW.work_id) ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_genre_delete AFTER DELETE ON dlsite_genre_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) SELECT 'tag', tag_id FROM metadata_tag WHERE dlsite_genre_id = OLD.genre_id ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_genre_insert AFTER INSERT ON dlsite_genre_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) SELECT 'tag', tag_id FROM metadata_tag WHERE dlsite_genre_id = NEW.genre_id ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_genre_update AFTER UPDATE ON dlsite_genre_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) SELECT 'tag', tag_id FROM metadata_tag WHERE dlsite_genre_id = OLD.genre_id ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) SELECT 'tag', tag_id FROM metadata_tag WHERE dlsite_genre_id = NEW.genre_id ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_delete AFTER DELETE ON metadata_tag BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) SELECT 'tag', id FROM tag WHERE namespace IN ('dlsite', 'metadata') ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_insert AFTER INSERT ON metadata_tag BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) SELECT 'tag', id FROM tag WHERE namespace IN ('dlsite', 'metadata') ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_name_delete AFTER DELETE ON metadata_tag_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', OLD.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_name_insert AFTER INSERT ON metadata_tag_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', NEW.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_name_update AFTER UPDATE ON metadata_tag_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', OLD.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', NEW.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_provider_name_delete AFTER DELETE ON metadata_tag_provider_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', OLD.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_provider_name_insert AFTER INSERT ON metadata_tag_provider_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', NEW.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_provider_name_update AFTER UPDATE ON metadata_tag_provider_name BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', OLD.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', NEW.tag_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_metadata_tag_update AFTER UPDATE ON metadata_tag BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) SELECT 'tag', id FROM tag WHERE namespace IN ('dlsite', 'metadata') ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_party_alias_delete AFTER DELETE ON party_alias BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', OLD.party_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_party_alias_insert AFTER INSERT ON party_alias BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', NEW.party_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_party_alias_update AFTER UPDATE ON party_alias BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', OLD.party_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', NEW.party_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_party_delete AFTER DELETE ON party BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', OLD.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_party_insert AFTER INSERT ON party BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', NEW.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_party_update AFTER UPDATE ON party BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', OLD.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('circle', NEW.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_person_alias_delete AFTER DELETE ON person_alias BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', OLD.person_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_person_alias_insert AFTER INSERT ON person_alias BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', NEW.person_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_person_alias_update AFTER UPDATE ON person_alias BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', OLD.person_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', NEW.person_id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_person_delete AFTER DELETE ON person BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', OLD.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_person_insert AFTER INSERT ON person BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', NEW.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_person_update AFTER UPDATE ON person BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', OLD.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('voice', NEW.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_tag_delete AFTER DELETE ON tag BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', OLD.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_catalog_dirty(work_id) SELECT work_id FROM work_tag WHERE tag_id = OLD.id ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_tag_insert AFTER INSERT ON tag BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', NEW.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_catalog_dirty(work_id) SELECT work_id FROM work_tag WHERE tag_id = NEW.id ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_names_tag_update AFTER UPDATE ON tag BEGIN
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', OLD.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_name_dirty(kind, entity_id) VALUES ('tag', NEW.id) ON CONFLICT(kind, entity_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_catalog_dirty(work_id) SELECT work_id FROM work_tag WHERE tag_id = OLD.id ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
  INSERT INTO recommendation_catalog_dirty(work_id) SELECT work_id FROM work_tag WHERE tag_id = NEW.id ON CONFLICT(work_id) DO UPDATE SET retry_count = 0, retry_after = 0;
END;

CREATE TRIGGER recommendation_revision_config_delete
AFTER DELETE ON app_setting
WHEN OLD.key = 'recommendation_config'
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_config_insert
AFTER INSERT ON app_setting
WHEN NEW.key = 'recommendation_config'
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_config_update
AFTER UPDATE OF value_json ON app_setting
WHEN NEW.key = 'recommendation_config' AND NEW.value_json <> OLD.value_json
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_dlsite_genre_name_delete
AFTER DELETE ON dlsite_genre_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_dlsite_genre_name_insert
AFTER INSERT ON dlsite_genre_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_dlsite_genre_name_update
AFTER UPDATE OF genre_id, name ON dlsite_genre_name
WHEN OLD.genre_id IS NOT NEW.genre_id OR OLD.name IS NOT NEW.name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_delete
AFTER DELETE ON metadata_tag
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_insert
AFTER INSERT ON metadata_tag
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_name_delete
AFTER DELETE ON metadata_tag_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_name_insert
AFTER INSERT ON metadata_tag_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_name_update
AFTER UPDATE OF tag_id, name ON metadata_tag_name
WHEN OLD.tag_id IS NOT NEW.tag_id OR OLD.name IS NOT NEW.name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_provider_name_delete
AFTER DELETE ON metadata_tag_provider_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_provider_name_insert
AFTER INSERT ON metadata_tag_provider_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_provider_name_update
AFTER UPDATE OF tag_id, name ON metadata_tag_provider_name
WHEN OLD.tag_id IS NOT NEW.tag_id OR OLD.name IS NOT NEW.name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_metadata_tag_update
AFTER UPDATE OF tag_id, dlsite_genre_id, merged_into_tag_id ON metadata_tag
WHEN OLD.tag_id IS NOT NEW.tag_id OR OLD.dlsite_genre_id IS NOT NEW.dlsite_genre_id OR OLD.merged_into_tag_id IS NOT NEW.merged_into_tag_id
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_party_alias_delete
AFTER DELETE ON party_alias
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_party_alias_insert
AFTER INSERT ON party_alias
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_party_alias_update
AFTER UPDATE OF party_id, alias ON party_alias
WHEN OLD.party_id IS NOT NEW.party_id OR OLD.alias IS NOT NEW.alias
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_party_delete
AFTER DELETE ON party
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_party_insert
AFTER INSERT ON party
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_party_update
AFTER UPDATE OF display_name, manual_name, provider_name ON party
WHEN OLD.display_name IS NOT NEW.display_name OR OLD.manual_name IS NOT NEW.manual_name OR OLD.provider_name IS NOT NEW.provider_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_person_alias_delete
AFTER DELETE ON person_alias
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_person_alias_insert
AFTER INSERT ON person_alias
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_person_alias_update
AFTER UPDATE OF person_id, alias ON person_alias
WHEN OLD.person_id IS NOT NEW.person_id OR OLD.alias IS NOT NEW.alias
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_person_delete
AFTER DELETE ON person
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_person_insert
AFTER INSERT ON person
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_person_update
AFTER UPDATE OF display_name ON person
WHEN OLD.display_name IS NOT NEW.display_name
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_tag_delete
AFTER DELETE ON tag
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_tag_insert
AFTER INSERT ON tag
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_profile_tag_update
AFTER UPDATE OF display_name, namespace ON tag
WHEN OLD.display_name IS NOT NEW.display_name OR OLD.namespace IS NOT NEW.namespace
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_tag_namespace_update
AFTER UPDATE OF namespace ON tag
WHEN OLD.namespace IS NOT NEW.namespace
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_user_state_delete
AFTER DELETE ON user_work_state
BEGIN
  UPDATE recommendation_user_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE user_id = OLD.user_id;
END;

CREATE TRIGGER recommendation_revision_user_state_insert
AFTER INSERT ON user_work_state
BEGIN
  INSERT INTO recommendation_user_revision (user_id, revision, updated_at)
  VALUES (NEW.user_id, 1, CURRENT_TIMESTAMP)
  ON CONFLICT(user_id) DO UPDATE SET
    revision = recommendation_user_revision.revision + 1,
    updated_at = CURRENT_TIMESTAMP;
END;

CREATE TRIGGER recommendation_revision_user_state_update
AFTER UPDATE OF listening_status, favorite ON user_work_state
WHEN OLD.listening_status IS NOT NEW.listening_status OR OLD.favorite IS NOT NEW.favorite
BEGIN
  INSERT INTO recommendation_user_revision (user_id, revision, updated_at)
  VALUES (NEW.user_id, 1, CURRENT_TIMESTAMP)
  ON CONFLICT(user_id) DO UPDATE SET
    revision = recommendation_user_revision.revision + 1,
    updated_at = CURRENT_TIMESTAMP;
END;

CREATE TRIGGER recommendation_revision_work_credit_delete
AFTER DELETE ON work_credit
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_credit_insert
AFTER INSERT ON work_credit
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_credit_update
AFTER UPDATE ON work_credit
WHEN OLD.work_id IS NOT NEW.work_id OR OLD.person_id IS NOT NEW.person_id OR OLD.role IS NOT NEW.role
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_delete
AFTER DELETE ON work
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_insert
AFTER INSERT ON work
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_party_delete
AFTER DELETE ON work_party
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_party_insert
AFTER INSERT ON work_party
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_party_update
AFTER UPDATE ON work_party
WHEN OLD.work_id IS NOT NEW.work_id OR OLD.party_id IS NOT NEW.party_id OR OLD.role IS NOT NEW.role
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_tag_delete
AFTER DELETE ON work_tag
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_tag_insert
AFTER INSERT ON work_tag
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER recommendation_revision_work_tag_update
AFTER UPDATE ON work_tag
WHEN OLD.work_id IS NOT NEW.work_id OR OLD.tag_id IS NOT NEW.tag_id
BEGIN
  UPDATE recommendation_input_revision
  SET revision = revision + 1, updated_at = CURRENT_TIMESTAMP
  WHERE id = 1;
END;

CREATE TRIGGER remote_title_variant_delete AFTER DELETE ON remote_metadata_title_variant
BEGIN
  INSERT INTO work_title_language_dirty(work_id) SELECT OLD.work_id
  WHERE OLD.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
  INSERT INTO work_search_dirty(work_id) SELECT OLD.work_id
  WHERE OLD.work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER remote_title_variant_insert AFTER INSERT ON remote_metadata_title_variant
BEGIN
  INSERT INTO work_title_language_dirty(work_id) SELECT NEW.work_id
  WHERE NEW.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
  INSERT INTO work_search_dirty(work_id) SELECT NEW.work_id
  WHERE NEW.work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER remote_title_variant_update AFTER UPDATE ON remote_metadata_title_variant
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT work_id FROM (SELECT OLD.work_id AS work_id UNION SELECT NEW.work_id)
  WHERE work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
  INSERT INTO work_search_dirty(work_id)
  SELECT work_id FROM (SELECT OLD.work_id AS work_id UNION SELECT NEW.work_id)
  WHERE work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER validate_workflow_definition_insert
BEFORE INSERT ON workflow_definition
BEGIN
  SELECT CASE
    WHEN NEW.scope = 'system' AND NEW.editable != 0
      THEN RAISE(ABORT, 'system workflow definitions are not editable')
    WHEN NEW.scope = 'system' AND NEW.owner_user_id IS NOT NULL
      THEN RAISE(ABORT, 'system workflow definitions cannot have an owner')
    WHEN NEW.scope = 'user' AND NEW.editable != 1
      THEN RAISE(ABORT, 'user workflow definitions must be editable')
  END;
END;

CREATE TRIGGER validate_workflow_definition_update
BEFORE UPDATE ON workflow_definition
BEGIN
  SELECT CASE
    WHEN NEW.scope = 'system' AND NEW.editable != 0
      THEN RAISE(ABORT, 'system workflow definitions are not editable')
    WHEN NEW.scope = 'system' AND NEW.owner_user_id IS NOT NULL
      THEN RAISE(ABORT, 'system workflow definitions cannot have an owner')
    WHEN NEW.scope = 'user' AND NEW.editable != 1
      THEN RAISE(ABORT, 'user workflow definitions must be editable')
  END;
END;

CREATE TRIGGER validate_workflow_trigger_insert
BEFORE INSERT ON workflow_trigger
BEGIN
  SELECT CASE
    WHEN NEW.trigger_type NOT IN ('startup', 'schedule', 'filesystem_event', 'source_poll')
      THEN RAISE(ABORT, 'invalid workflow trigger type')
  END;
END;

CREATE TRIGGER validate_workflow_trigger_update
BEFORE UPDATE ON workflow_trigger
BEGIN
  SELECT CASE
    WHEN NEW.trigger_type NOT IN ('startup', 'schedule', 'filesystem_event', 'source_poll')
      THEN RAISE(ABORT, 'invalid workflow trigger type')
  END;
END;

CREATE TRIGGER work_search_code_alias_delete
AFTER DELETE ON work_code_alias
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_edition WHERE logical_work_id = OLD.logical_work_id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_code_alias_insert
AFTER INSERT ON work_code_alias
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_edition WHERE logical_work_id = NEW.logical_work_id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_code_alias_update
AFTER UPDATE OF logical_work_id, primary_code ON work_code_alias
WHEN OLD.logical_work_id IS NOT NEW.logical_work_id OR OLD.primary_code IS NOT NEW.primary_code
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_edition WHERE logical_work_id IN (OLD.logical_work_id, NEW.logical_work_id)
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_credit_delete
AFTER DELETE ON work_credit
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
END;

CREATE TRIGGER work_search_credit_insert
AFTER INSERT ON work_credit
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_credit_update
AFTER UPDATE OF work_id, person_id, role ON work_credit
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_dlsite_genre_delete
AFTER DELETE ON work_dlsite_genre
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
END;

CREATE TRIGGER work_search_dlsite_genre_insert
AFTER INSERT ON work_dlsite_genre
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_dlsite_genre_name_delete
AFTER DELETE ON dlsite_genre_name
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_dlsite_genre WHERE genre_id = OLD.genre_id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_dlsite_genre_name_insert
AFTER INSERT ON dlsite_genre_name
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_dlsite_genre WHERE genre_id = NEW.genre_id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_dlsite_genre_name_update
AFTER UPDATE OF genre_id, name ON dlsite_genre_name
WHEN OLD.genre_id IS NOT NEW.genre_id OR OLD.name IS NOT NEW.name
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_dlsite_genre WHERE genre_id IN (OLD.genre_id, NEW.genre_id)
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_dlsite_genre_update
AFTER UPDATE OF work_id, genre_id ON work_dlsite_genre
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_edition_delete
AFTER DELETE ON work_edition
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
END;

CREATE TRIGGER work_search_edition_insert
AFTER INSERT ON work_edition
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_edition_update
AFTER UPDATE OF work_id, logical_work_id ON work_edition
WHEN OLD.work_id IS NOT NEW.work_id OR OLD.logical_work_id IS NOT NEW.logical_work_id
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_display_update AFTER UPDATE OF display_name ON tag
WHEN OLD.display_name IS NOT NEW.display_name
BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (NEW.id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_name_delete AFTER DELETE ON metadata_tag_name

BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (OLD.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_name_insert AFTER INSERT ON metadata_tag_name

BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (NEW.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_name_update AFTER UPDATE OF name,language ON metadata_tag_name
WHEN OLD.name IS NOT NEW.name OR OLD.language IS NOT NEW.language
BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (OLD.tag_id,NEW.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_provider_name_delete AFTER DELETE ON metadata_tag_provider_name
BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (OLD.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_provider_name_insert AFTER INSERT ON metadata_tag_provider_name
BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (NEW.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_provider_name_update AFTER UPDATE OF name,language ON metadata_tag_provider_name
WHEN OLD.name IS NOT NEW.name OR OLD.language IS NOT NEW.language
BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (OLD.tag_id,NEW.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_state_before BEFORE UPDATE OF hidden,merged_into_tag_id ON metadata_tag
WHEN OLD.hidden IS NOT NEW.hidden OR OLD.merged_into_tag_id IS NOT NEW.merged_into_tag_id
BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (OLD.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_metadata_tag_state_update AFTER UPDATE OF hidden,merged_into_tag_id ON metadata_tag
WHEN OLD.hidden IS NOT NEW.hidden OR OLD.merged_into_tag_id IS NOT NEW.merged_into_tag_id
BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (NEW.tag_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_override_delete
AFTER DELETE ON work_manual_override
WHEN OLD.field_name IN ('title', 'circle', 'series', 'voice_actors')
BEGIN
  INSERT INTO work_search_dirty(work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
END;

CREATE TRIGGER work_search_override_insert
AFTER INSERT ON work_manual_override
WHEN NEW.field_name IN ('title', 'circle', 'series', 'voice_actors')
BEGIN
  INSERT INTO work_search_dirty(work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_override_update
AFTER UPDATE OF work_id, field_name, language, value_json ON work_manual_override
WHEN OLD.field_name IN ('title', 'circle', 'series', 'voice_actors')
  OR NEW.field_name IN ('title', 'circle', 'series', 'voice_actors')
BEGIN
  INSERT INTO work_search_dirty(work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
  INSERT INTO work_search_dirty(work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_party_alias_delete AFTER DELETE ON party_alias
BEGIN
 INSERT INTO work_search_dirty(work_id) SELECT DISTINCT work_id FROM work_party WHERE party_id=OLD.party_id
   AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=work_party.work_id);
END;

CREATE TRIGGER work_search_party_alias_insert AFTER INSERT ON party_alias
BEGIN
 INSERT INTO work_search_dirty(work_id) SELECT DISTINCT work_id FROM work_party WHERE party_id=NEW.party_id
   AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=work_party.work_id);
END;

CREATE TRIGGER work_search_party_alias_update AFTER UPDATE OF alias,party_id ON party_alias
BEGIN
 INSERT INTO work_search_dirty(work_id) SELECT DISTINCT work_id FROM work_party WHERE party_id IN (OLD.party_id,NEW.party_id)
   AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=work_party.work_id);
END;

CREATE TRIGGER work_search_party_external_id_delete
AFTER DELETE ON party_external_id
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_party WHERE party_id = OLD.party_id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_party_external_id_insert
AFTER INSERT ON party_external_id
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_party WHERE party_id = NEW.party_id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_party_external_id_update
AFTER UPDATE OF party_id, external_id ON party_external_id
WHEN OLD.party_id IS NOT NEW.party_id OR OLD.external_id IS NOT NEW.external_id
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_party WHERE party_id IN (OLD.party_id, NEW.party_id)
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_party_link_delete
AFTER DELETE ON work_party
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
END;

CREATE TRIGGER work_search_party_link_insert
AFTER INSERT ON work_party
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_party_link_update
AFTER UPDATE OF work_id, party_id, role ON work_party
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_party_update
AFTER UPDATE OF display_name ON party
WHEN OLD.display_name IS NOT NEW.display_name
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_party WHERE party_id = NEW.id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_person_update
AFTER UPDATE OF display_name ON person
WHEN OLD.display_name IS NOT NEW.display_name
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_credit WHERE person_id = NEW.id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_shared_genre_delete AFTER DELETE ON dlsite_genre_name BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (SELECT tag_id FROM metadata_tag WHERE dlsite_genre_id=OLD.genre_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_shared_genre_insert AFTER INSERT ON dlsite_genre_name BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (SELECT tag_id FROM metadata_tag WHERE dlsite_genre_id=NEW.genre_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_shared_genre_update AFTER UPDATE OF name,language,genre_id ON dlsite_genre_name BEGIN
 INSERT INTO work_search_dirty(work_id)
 SELECT DISTINCT link.work_id FROM work_tag AS link JOIN metadata_tag_resolution AS resolved ON resolved.resolved_tag_id=link.tag_id
 WHERE resolved.source_tag_id IN (SELECT tag_id FROM metadata_tag WHERE dlsite_genre_id=NEW.genre_id OR dlsite_genre_id=OLD.genre_id) AND NOT EXISTS(SELECT 1 FROM work_search_dirty AS dirty WHERE dirty.work_id=link.work_id);
END;

CREATE TRIGGER work_search_tag_link_delete
AFTER DELETE ON work_tag
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
END;

CREATE TRIGGER work_search_tag_link_insert
AFTER INSERT ON work_tag
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_tag_link_update
AFTER UPDATE OF work_id, tag_id ON work_tag
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_tag_update
AFTER UPDATE OF namespace, display_name ON tag
WHEN OLD.namespace IS NOT NEW.namespace OR OLD.display_name IS NOT NEW.display_name
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT DISTINCT work_id FROM work_tag WHERE tag_id = NEW.id
    AND work_id NOT IN (SELECT work_id FROM work_search_dirty);
END;

CREATE TRIGGER work_search_variant_delete
AFTER DELETE ON dlsite_metadata_variant
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
END;

CREATE TRIGGER work_search_variant_insert
AFTER INSERT ON dlsite_metadata_variant
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_variant_update
AFTER UPDATE OF work_id, title ON dlsite_metadata_variant
WHEN OLD.work_id IS NOT NEW.work_id OR OLD.title IS NOT NEW.title
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.work_id);
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.work_id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.work_id);
END;

CREATE TRIGGER work_search_work_delete
AFTER DELETE ON work
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT OLD.id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = OLD.id);
END;

CREATE TRIGGER work_search_work_insert
AFTER INSERT ON work
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.id);
END;

CREATE TRIGGER work_search_work_update
AFTER UPDATE OF primary_code, title ON work
WHEN OLD.primary_code IS NOT NEW.primary_code OR OLD.title IS NOT NEW.title
BEGIN
  INSERT INTO work_search_dirty (work_id)
  SELECT NEW.id WHERE NOT EXISTS (SELECT 1 FROM work_search_dirty WHERE work_id = NEW.id);
END;

CREATE TRIGGER work_snapshot_projection_catalog_delete
AFTER DELETE ON party_catalog_item
WHEN OLD.catalog_status = 'imported'
BEGIN
  DELETE FROM work_snapshot_projection
  WHERE kind = 'dlsite_party'
    AND work_id IN (SELECT id FROM work WHERE primary_code = OLD.primary_code);
END;

CREATE TRIGGER work_snapshot_projection_catalog_update
AFTER UPDATE ON party_catalog_item
WHEN OLD.catalog_status = 'imported' AND (
  OLD.party_id IS NOT NEW.party_id
  OR OLD.primary_code IS NOT NEW.primary_code
  OR OLD.title IS NOT NEW.title
  OR OLD.release_date IS NOT NEW.release_date
  OR OLD.url IS NOT NEW.url
  OR OLD.catalog_status IS NOT NEW.catalog_status
  OR OLD.dlsite_available IS NOT NEW.dlsite_available
  OR OLD.raw_json IS NOT NEW.raw_json
)
BEGIN
  DELETE FROM work_snapshot_projection
  WHERE kind = 'dlsite_party'
    AND work_id IN (SELECT id FROM work WHERE primary_code = OLD.primary_code);
END;

CREATE TRIGGER work_snapshot_projection_credit_delete
AFTER DELETE ON work_credit
BEGIN
  DELETE FROM work_snapshot_projection
  WHERE work_id = OLD.work_id AND kind = 'voice_credit';
END;

CREATE TRIGGER work_snapshot_projection_party_delete
AFTER DELETE ON work_party
BEGIN
  DELETE FROM work_snapshot_projection
  WHERE work_id = OLD.work_id AND kind = 'dlsite_party';
END;

CREATE TRIGGER work_snapshot_projection_party_update
AFTER UPDATE ON work_party
WHEN OLD.work_id IS NOT NEW.work_id
  OR OLD.party_id IS NOT NEW.party_id
  OR OLD.role IS NOT NEW.role
  OR OLD.provider_id IS NOT NEW.provider_id
  OR OLD.source IS NOT NEW.source
BEGIN
  DELETE FROM work_snapshot_projection
  WHERE work_id IN (OLD.work_id, NEW.work_id) AND kind = 'dlsite_party';
END;

CREATE TRIGGER work_title_language_edition_delete AFTER DELETE ON work_edition
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT OLD.work_id AS work_id UNION SELECT work_id FROM work_edition WHERE logical_work_id IN (OLD.logical_work_id)) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_edition_insert AFTER INSERT ON work_edition
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT work_id FROM work_edition WHERE logical_work_id IN (NEW.logical_work_id)) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_edition_update AFTER UPDATE OF work_id, logical_work_id, is_canonical, metadata_language ON work_edition
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT OLD.work_id AS work_id UNION SELECT NEW.work_id UNION SELECT work_id FROM work_edition WHERE logical_work_id IN (OLD.logical_work_id, NEW.logical_work_id)) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_override_delete AFTER DELETE ON work_manual_override
WHEN OLD.field_name = 'title'
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT OLD.work_id AS work_id) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_override_insert AFTER INSERT ON work_manual_override
WHEN NEW.field_name = 'title'
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT NEW.work_id AS work_id) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_override_update AFTER UPDATE OF work_id, field_name, language, value_json ON work_manual_override
WHEN OLD.field_name = 'title' OR NEW.field_name = 'title'
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT OLD.work_id AS work_id UNION SELECT NEW.work_id) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_variant_delete AFTER DELETE ON dlsite_metadata_variant
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT work_id FROM work_edition WHERE logical_work_id IN (OLD.logical_work_id)) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_variant_insert AFTER INSERT ON dlsite_metadata_variant
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT work_id FROM work_edition WHERE logical_work_id IN (NEW.logical_work_id)) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_variant_update AFTER UPDATE OF logical_work_id, work_id, title, edition_language ON dlsite_metadata_variant
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT work_id FROM work_edition WHERE logical_work_id IN (OLD.logical_work_id, NEW.logical_work_id)) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_work_insert AFTER INSERT ON work
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT NEW.id AS work_id) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TRIGGER work_title_language_work_update AFTER UPDATE OF title ON work
WHEN OLD.title IS NOT NEW.title
BEGIN
  INSERT INTO work_title_language_dirty(work_id)
  SELECT DISTINCT queued.work_id FROM (SELECT NEW.id AS work_id UNION SELECT sibling.work_id FROM work_edition AS current
    JOIN work_edition AS sibling ON sibling.logical_work_id = current.logical_work_id
    WHERE current.work_id = NEW.id) AS queued
  WHERE queued.work_id NOT IN (SELECT work_id FROM work_title_language_dirty);
END;

CREATE TABLE source_catalog_entry (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_source_id INTEGER REFERENCES file_source(id) ON DELETE SET NULL,
  root_path TEXT NOT NULL,
  primary_code TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  work_id INTEGER REFERENCES work(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(file_source_id, root_path)
);
CREATE INDEX idx_source_catalog_work ON source_catalog_entry(work_id);
CREATE TABLE source_catalog_file (
  catalog_entry_id INTEGER NOT NULL REFERENCES source_catalog_entry(id) ON DELETE CASCADE,
  relative_path TEXT NOT NULL,
  title TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('folder','audio','text','other')),
  size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
  PRIMARY KEY(catalog_entry_id,relative_path)
);
