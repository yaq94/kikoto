# ADR-0005: Source Field Translation Cache

## Status

Accepted.

## Context

Remote titles, tags and directory display names can be requested without a
registered `work`, `catalog_entry` or `catalog_series` identity. Searchable
translations use `content_translation`, whose rows are tied to those durable
entities and indexed for content search. Reusing that table for free-form source
fields would either require an unstable synthetic entity id or mix display
cache into the search corpus.

## Decision

Store successful model results for source-level fields in
`source_field_translation`. A row is keyed by source, work code, field, real
relative path, source text, target language, model, endpoint, reasoning switch
and template version.

The table is display cache, not search corpus. It has no FTS table or trigger,
and content search never reads it. Only non-empty model output is stored.
Source availability and the exact public field are revalidated before a cached
row is considered.

Rows are rebuildable. The existing `maxEntries` and `cacheBytes` maintenance
pass counts and evicts both `content_translation` model rows and
`source_field_translation` rows, oldest first. Native authored rows, personal
records and source identities are never eviction candidates.

## Consequences

- Repeated views across sessions and restarts reuse the same source-field
  translation without making it searchable content.
- The published `content_translation` schema and its entity constraint remain
  unchanged; migration `095` adds the independent cache table.
- Cache budget accounting covers two tables but keeps one bounded eviction
  policy and one maintenance path.
- Traditional Chinese continues to reuse the Simplified row and convert at
  display time; no separate Traditional copy is stored.
