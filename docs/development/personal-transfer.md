# Personal data transfer

`GET /api/user-data/export` and `POST /api/user-data/import[/preview]` use the
existing account-owned personal store. The exporter writes version 2 with an
optional episode `parent`; version 1 and the Kikoeru readers retain
numbered-code compatibility.
See [the user contract](../user/en/personal-data.md#export-and-import).

Each version 2 work has `identity: {ref, kind, title, references?, parent?}`.
`kind` is
`work`, `episode` or `series`. `personal_transfer_ref` provides random opaque
aliases for exact work, catalog and series rows. Its primary work reference is
stable across restarts; imported aliases keep repeated transfers idempotent.
They neither replace `work.id` nor define shared evaluation identity. Only
personal records select which objects are exported. Source references carry no
configuration, paths or network addresses.

A version 2 episode identity may carry `parent`. It contains the minimum
series identity (`ref`, `kind: "series"`, `title`, no rating, note or progress);
there is no nested parent. The `s:` parent reference belongs to the
`catalog_series` transfer row, never to the episode work. A parent series may
also appear as a top-level work; it is imported once and keeps its own personal
state. If the parent is missing, ambiguous, cyclic or points to another object,
the episode is blocked with `parent_conflict`/`parent_cycle`; it is not restored
as a standalone work. A version 2 file whose episode has no parent field keeps
its legacy behaviour and reports that the parent was not restored.

Version 2 list items use `workRef` to select a work in the same document.
Progress carries `mediaIdentity` for its exact edition and retains title,
track/disc numbers and position. The importer requires an unambiguous media
match. `personal_transfer_progress` retains unmatched version 2 resume data for
re-export without creating media. An actual playback cursor takes precedence
over retained import progress. No source or media request runs during transfer.

The importer acquires the writer before resolving references and reading
conflicts. It reuses exact local aliases, known numbered codes and declared code
aliases. Contradictory evidence produces an `identity_conflict` issue instead of
a guessed assignment. With `remote:track`, a known but unregistered catalog
entry, series or episode is rebuilt as one minimal work and linked back to the
existing catalog row; the link is never moved from another work. The same
reference then resolves to that work on later imports. Without registration
permission the whole import is rejected, as with any other missing work.

The importer keeps the evaluation level: a catalog episode must not become a
standalone work or series, and a series reference cannot become a work. On an
episode import that carries `parent` it writes `work.parent_work_id`, sets the episode's primary
`personal_transfer_ref.object_kind` to `episode`, and creates or reuses a
`catalog_series` row for an otherwise unknown parent. A personal-only series
has no `catalog_series_member` because there is no `source_catalog_entry`; the
series endpoint lists its children through `work.parent_work_id` and returns
`catalogEntryId=0`. It never downloads media, starts a media workflow,
reconstructs shared associations or invents source membership. Unknown source
references are retained as transfer aliases and reported as having no source
connection.

Review planning groups exact references through `shared_work_identity`.
Different nonempty incoming values block the whole evaluation group, including
when overwrite is selected. Compatible incoming fields produce one final
review write per shared identity. Listening marks, tags, lists and progress
retain their exact work references. Splitting a shared association keeps those
references independently importable. Every query and write of personal state
uses the authenticated user; import JSON cannot select an account.

Preview and result `issues` expose the involved reference, a stable reason and,
for review conflicts, only the caller's current and incoming fields. Safe
records can import alongside unresolved records. A list with an unresolved
version 2 reference remains unchanged. Imports commit atomically; repeated
listening totals merge by maximum. Existing transfer byte and collection limits
remain enforced, with at most 100 additional references per object.
