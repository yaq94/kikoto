# Core Boundaries

## Unified Work Identity

A work exists once in the database. Local folders, remote sources, cache files,
and tracked state describe availability for that work; they do not create new
work identities.

A persisted `work.id` is the stable internal reference within its database.
Numbered ingestion uses a normalized `primary_code`, such as the synthetic
example `RJ00000000`, to find an existing work. Codes, aliases and declared
language editions can resolve to a canonical work; an explicit numeric reference
continues to select its exact edition. The
[HTTP reference contract](backend.md#api-shape) applies this distinction to reads
and personal-state updates.

A source location, display title or signed media URL is not a permanent identity.
The [accepted identity decision](../adr/0001-work-identity-and-personal-state.md)
also permits numberless admission within an explicitly scoped source workflow.
The current numbered ingestion and schema still require codes; that constraint
must be extended at admission, never worked around with invented product codes.

## Metadata Sources

Metadata sources answer:

```text
What is this work?
```

Examples:

- DLsite metadata.
- Manual user overrides.
- Future metadata importers.

Metadata belongs in work, metadata snapshot, tag, party, person, credit, and
edition tables.

## File Sources

File sources answer:

```text
Where can this work be played, cached, or fetched?
```

Examples:

- Local folders.
- Kikoeru-compatible remote sources.
- Cache files.

File availability belongs in source presence and media file location tables.

## User State

User state is separate from provider metadata and file availability. Listening
marks, favorites, tags, notes, and playback progress should survive source
replacement and metadata refreshes.
