# ADR-0008: Catalog Publication Snapshots

## Status

Accepted. Implementation and acceptance evidence are tracked separately from
this persistence decision.

## Decision

A catalog publication versions source-owned location, availability, display
metadata, file rows, admission and source membership. Stable entry and work
identities, shared identity associations and personal records remain live under
[ADR-0004](ADR-0004-work-identity-and-personal-state.md) and
[ADR-0005](ADR-0005-shared-identity-preserves-playback-editions.md). Publication
does not merge media editions, create another identity or copy personal state.

Use sparse copy-on-write before-images on the existing entry, file and member
tables. An operation stores the original row once per affected key while the
ordinary tables hold its working rows. A first insert has an explicit absent
before-image; deletion retains its old row. Existing data is snapshot zero and
requires no catalog-wide copy during upgrade. Composite-key changes record
both the old key and the absent new key. Foreign keys and `ON CONFLICT` continue
to operate on the original tables.

Cross-root relocation, format grouping and series membership changes share one
publication operation. Preparation includes derived admission and translation
scope for the affected identities. A single committed marker activates all
prepared changes, including catalog playback availability and source presence.
The marker transaction neither copies a file set nor rebuilds global scope.
Bounded cleanup reclaims committed before-images and operation metadata. SQLite
read transactions retain their own visible version while cleanup runs.

Only one pending operation owns a source. A before-image key also rejects a
different operation, including a shared series whose source field is null.
This establishes an explicit ordering for conflicting writes; different-source
operations cannot silently overwrite the same shared entity. Interrupted work
resumes its operation and durable checkpoints. A replacement generation first
finishes the pending generation. Failure or cancellation cannot commit its
marker merely to release ownership.

## Read and ownership boundaries

Public catalog list, admission, directory, track and playback reads use the
published projection. A short SQLite read transaction fixes the projection for
a request that needs several queries. Release it before network access or a
new write transaction. Source configuration and access permissions are current
configuration; an old catalog snapshot cannot restore a disabled source or
grant access.

`source_catalog_entry.work_id` and `catalog_series.work_id` stay live. Source
titles, roots, status, files and source membership are versioned. Translation
text, configuration and cache lifecycle stay live; only the source/browsable
projection participates in publication. `media_file_location` participates for
`catalog_stream` locations and `work_source_presence` for source presence;
local/cache/tracked storage state is independent. Explicit review or first
playback can create a stable identity during preparation: its bounded source
projection records the published state the user read and the working state
that will become visible when the marker commits.

Directory observations and listing checkpoints are preparation state. A
provider page and its cursor commit together; an incomplete, contradictory or
failed listing cannot justify deletion. A complete successful empty listing
can remove its file projection while retaining the evaluation object and its
personal records. A failed probe does not advance a successful probe cursor.

## Consequences

Sparse before-images avoid copying an entire source or root for a small change.
Lookups use entity keys and operation primary keys rather than scanning version
history. Readers and source-owned writers must enter the shared publication
boundary; a partial conversion of fetch/apply code does not satisfy this
decision. Every derived side effect and recovery path needs its own acceptance
evidence.

Staging does not increment the translation corpus revision per source row.
Committing the marker invalidates the corpus once; ordinary directory title or
relative-path changes outside a publication also invalidate retention. Source
configuration changes keep their immediate invalidation behavior.
