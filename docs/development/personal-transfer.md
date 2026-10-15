# Personal data transfer

`GET /api/user-data/export` and `POST /api/user-data/import[/preview]` use the
existing account-owned personal store. The exporter writes version 2;
version 1 and the Kikoeru readers retain numbered-code compatibility.
See [the user contract](../user/en/personal-data.md#export-and-import).

Each version 2 work has `identity: {ref, kind, title, references?}`. `kind` is
`work`, `episode` or `series`. `personal_transfer_ref` provides random opaque
aliases for exact work, catalog and series rows. Its primary work reference is
stable across restarts; imported aliases keep repeated transfers idempotent.
They neither replace `work.id` nor define shared evaluation identity. Only
personal records select which objects are exported. Source references carry no
configuration, paths or network addresses.

Version 2 list items use `workRef` to select a work in the same document.
Progress carries `mediaIdentity` for its exact edition and retains title,
track/disc numbers and position. The importer requires an unambiguous media
match. `personal_transfer_progress` retains unmatched version 2 resume data for
re-export without creating media. An actual playback cursor takes precedence
over retained import progress. No source or media request runs during transfer.

The importer acquires the writer before resolving references and reading
conflicts. It reuses exact local aliases, known numbered codes and declared code
aliases. Contradictory evidence or a known unregistered source produces an issue
instead of a guessed assignment. Missing version 2 works can be registered from
the necessary title and evaluation level with `remote:track`; an absent series
gets its own series object. Imports never reconstruct shared associations or
series membership. Unknown source references are retained as transfer aliases
and reported as having no source connection.

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
