# Catalog prune

`backend/cmd/catalog-prune` builds a reviewed dry-run report before it removes
cards that cannot pass catalog admission. Apply checks the report against the
current database and commits in bounded batches. Admission counts include only
non-folder files whose `observed_generation` matches the entry's
`last_seen_generation`. Folder rows and older generations cannot turn a playable
card into a deletion candidate or supply audio to an otherwise invalid card.
Each deleting transaction rechecks those counts, the admission decision and
protected references; a report cannot authorize deletion of a card that becomes
admissible between batches.

Candidates referenced by a series or an incoming relocation stay in place. A
candidate with a `personal_transfer_ref` also stays unless it owns a work that
can inherit the alias. A work-backed alias is repointed to that work and keeps
its opaque reference. A source-only card has no surviving work for the
reference, so the report lists it as `transfer_alias` and apply refuses such a
stale batch. This keeps every unresolved alias resolvable after a preview.

The report exposes both the admission candidates and the smaller deletion plan.
Operators should compare `candidateTotals` with `totals`; a large difference
usually means whitelist, series, relocation or transfer-alias protection kept a
card. Report file counts describe admission; apply file counts describe every
removed storage row, including obsolete generations and folders owned by a
removed card.
