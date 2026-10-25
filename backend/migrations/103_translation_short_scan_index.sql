-- A 1-2 character query walks one (language, entity_kind, field) range and
-- matches with instr(). The 099 index placed origin and config_key before id,
-- so a caller that asks for keyset order had to build a temp b-tree over the
-- whole range. This index keeps id adjacent to the range prefix and still
-- covers the current-row predicate and the text, so an ordered page is a plain
-- index read and the complete-set count needs no table lookup.
CREATE INDEX idx_content_translation_short_scan
  ON content_translation(language, entity_kind, field, id, origin, config_key, translated_text);

-- Superseded by the scan index above: both only served the short path, and
-- keeping them would add write cost to every prefill insert.
DROP INDEX idx_content_translation_short_current;
DROP INDEX idx_content_translation_short;
