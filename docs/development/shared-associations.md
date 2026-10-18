# Shared associations

Shared associations identify the same work, episode or series across sources.
High-confidence candidate selection and its budgets are described in
[Work matching](work-matching.md).
`shared_identity_member` maps retained `work_id` references to one
`identity_work_id`; `shared_work_identity` includes unassociated works with equal
ids. Edition families, source catalog ids, series memberships, media locations,
listening state, favorites and playback cursors retain their exact references.
See [the identity decision](../decisions/ADR-0005-shared-identity-preserves-playback-editions.md).

## Preview and confirmation

`POST /api/shared-associations/previews` requires `library:write` and accepts
`left` and `right`, each `{kind: "work" | "catalog" | "series", id}`. It stores
a durable preview without registering works or changing shared identity.
Different evaluation levels are rejected. A preview contains titles, retained
work ids, public source labels and only the requesting user's review values.
Private source paths and other users' records remain in the protected snapshot.

`POST /api/shared-associations/{id}/start` requires `library:write`, the original
preview actor and its opaque `version`. It calls `startSharedAssociationTx`, the
submission boundary used by manual and automatic association callers. Callers
must authorize shared changes; automatic callers cannot bypass its fresh
snapshot and owner-decision checks. A pending operation keeps original identity
and reviews readable until every necessary owner has decided.

`POST /api/shared-associations/{id}/decision` accepts the same `version`, a
`rating` from 0–5 or null, and a string `note`. Each conflicted field must be
explicit. The authenticated record owner needs no shared-edit permission;
request JSON cannot select another user. Different nonempty values on the same
user's same field conflict. Empty fields complement existing content, and
different users never conflict with one another.

`GET /api/shared-associations/{id}` reads the operation and the current user's
own choice. `GET /api/shared-associations?kind=catalog&id=1` discovers applicable
operations for an object and its retained work references. Other users receive
only the necessary pending/completed state, without owner identities or counts.
Readers can inspect existing shared operations when anonymous access is enabled.
Demo does not expose or mutate these operations.

Final confirmation acquires the database writer before reloading evidence,
checks all decisions, updates reviews and publishes the identity mapping in one
transaction. A failure rolls everything back. Related rating/note, membership,
identity or source changes invalidate the preview, including changes subsequently
reverted to an old value. Playback progress does not invalidate it. Reloading
the same database restores pending operations and valid choices.

Operations retain before and applied review snapshots and original identity
mappings for splitting. Snapshots are protected application state and are not
returned as raw JSON by any public endpoint. Each side is bounded to 100 source
locations, a union to 100 works, and each evaluation identity to 1,000 reviewers.
An actor can create at most 32 recent pending previews in one day. Exceeding a
bound rejects the operation without dropping records.

## Read and write contract

Library cards and pagination return one shared identity. Search and source
filters can still find it using any retained member. Source catalogs collapse
associated entries within the selected source. Series detail presents the
associated series' members; episode membership and playback remain independent.
Directory responses preserve each directory's exact work reference.

All existing review endpoints keep `user_work_state` as their read/write seam.
Database triggers synchronize only a single user's `rating` and `note` across
confirmed members, preserving other state. Difference guards stop recursive
propagation. Association writes final values while original groups are separate,
then publishes the complete mapping. Personal deletion is expressed through the
existing explicit score and note clear operations, preserving other state.

Cross-feature SQL consumers use these stable contracts:

- Resolve an evaluation with `SELECT identity_work_id FROM shared_work_identity
  WHERE work_id=?`. Fetch its members from the same view by `identity_work_id`.
- Read only the authenticated user's `rating,note` from `user_work_state` at the
  resolved identity. A missing row has null rating and empty note.
- `shared_review_revision(user_id,work_id,revision)` records rating/note changes;
  a missing revision is zero. It excludes listening and playback changes.
- Personal import first resolves and groups incoming references by shared
  identity, previews conflicts against that user's current record, then writes
  one final review per identity transactionally. Sequential trigger writes are
  not a conflict-resolution policy and must not choose between differing imports.

## Splitting an association

`POST /api/shared-associations/{id}/split-preview` requires `library:write` and
captures the original two sides, their source ownership and the current shared
review state. `GET /api/shared-associations/{id}/splits` lists pending or completed
splits and the viewer's drafts. `GET /api/shared-splits/{id}` returns only that
viewer’s original reviews, current review, change classification and allocation.

The creating editor submits `POST /api/shared-splits/{id}/start` with its opaque
`version` and `library:write`. Changed or newly added reviews remain shared and
addressable until each record owner posts `{version, allocation}` to
`/api/shared-splits/{id}/decision`; `allocation` is `left`, `right` or `both`.
Choosing has no shared-edit permission requirement and cannot target another
user. An explicit score/note clear is a change and requires an allocation too.

The chosen side receives the current review; an unchosen side restores its
original review. Unchanged records restore the captured original values without
new decisions. Original and applied records remain in the protected operation
snapshots. The final transaction removes the active mapping, restores any prior
groups, applies the allocations, marks the association `undone` and marks the
split `completed`. Review synchronization is confined to the restored groups.
No work, personal-state row, series member or media location is physically
deleted by splitting.

The split rechecks identity, sources, membership and rating/note revisions before
each mutation. Stale previews cannot commit, and a new preview never reuses old
choices. A failed transaction leaves the shared relationship and records intact.
Pending choices survive application shutdown and reopening the same database.
Nested associations split in reverse order; splitting an older association while
a later one includes its members is rejected. Both resulting objects are
independently browsable and editable through their retained references.

## Functional checks

`TestSharedAssociation*` and `TestSharedSplit*` use real HTTP, sessions and migrated SQLite, including
closing and reopening the application and the same disk database. The synthetic
OpenList fixture supports `shared-association.spec.ts`, which checks preview,
stale refresh, permissions, two record owners, reload and HTTP readback.
`shared-split.spec.ts` covers original/current record evidence, private owner
allocation, reopening and final independent HTTP readback. Screenshots, traces
and videos are disabled in both functional tests.
