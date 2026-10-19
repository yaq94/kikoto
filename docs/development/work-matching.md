# Work matching

Matching reads saved work metadata and source catalog evidence. It makes no
outbound request, downloads no audio, and does not read media bytes to calculate
hashes. Source discovery and media playback keep their own request boundaries.

## Identity rules

Existing product codes and code aliases retain their normal resolution. Declared
edition relationships take precedence over heuristic candidates. Numeric edition
references, media locations and playback cursors keep their exact meaning.

Numberless candidates need the same original title (ASCII case and surrounding
spaces are ignored), plus either:

- The same nonempty voice-actor identity set and the same nonempty circle identity
  set or release date.
- The same explicitly labelled voice-actor names from a release sidecar on both
  sides. A title plus two matching labelled cast lists is two independent facts;
  a readme on only one side, or an unlabelled name in prose, is not.
- The same complete audio fingerprint already produced by the source index from
  valid, explicitly supplied SHA-256 hashes for every observed audio file.

The fingerprint is saved source evidence, not a request to download or hash audio.
Missing or incomplete hashes do not satisfy that rule; other independent metadata
can still qualify. File sizes, directory layouts, translations and title similarity
alone never authorize an association.

Different known codes without a declared relationship, two present but different
voice sets, two present but different circles, release dates, edition labels,
metadata languages or work types prevent a heuristic association. A missing
credit set is absence of evidence, not a conflict: an unregistered card usually
has no person rows at all. The same checks include retained members of an
existing shared identity. Series, episodes and works remain different evaluation
levels.
Episodes additionally need consistent series titles and positive episode positions.
A split pair always requires manual review.

All saved sources participate in candidate lookup. A queued, running, failed or
partial full source index pauses new associations until its requested ranges
complete; an unfinished range cannot prove uniqueness. Only a unique candidate at the
highest qualifying confidence is submitted. Multiple strong candidates remain
independent. Results retain explicit rule names and public candidate titles; no
personal review, source endpoint or filesystem path enters the matching result.

## Submission and recovery

`POST /api/work-matching` accepts `{kind: "work" | "catalog" | "series", id}` and
requires `library:write`. It evaluates and submits an eligible match through
`startSharedAssociationTx`. The background worker uses the same transaction.
Both paths preserve the [owner-decision contract](shared-associations.md): a
private conflict remains pending until every affected owner has confirmed.

Matching evidence includes a metadata revision and the complete bounded candidate
set. A metadata change, including a new candidate, invalidates an automatic
operation's evidence; changes subsequently reverted still require a fresh result.
The final owner decision rechecks evidence while holding the SQLite writer.
Refreshing a stale match creates a fresh operation without reusing old choices.
Concurrent and reverse-direction requests reuse a valid pending operation.

`GET /api/work-matching?kind=work&id=1` reads the saved result. Metadata changes
make old results appear queued until reevaluated. The Shared association panel
shows the result, its reason, candidate preview buttons and the owner-confirmation
operation. Manual preview and confirmation remain available for uncertain pairs.

`RunWorkMatchingWorker` is part of the production server lifecycle. Its durable
cursor covers every registered work, unregistered catalog entry and unregistered
series. Metadata triggers queue a new pass; each step handles one subject and
releases its database connection. Application shutdown cancels work and preserves
the cursor, results, pending associations and owner choices. Association changes
restart the pass so earlier subjects are also reconsidered.

## Resource limits

`GET` and `PUT /api/work-matching/settings` require `sources:write` and persist
`intervalMs` (default 1,000; range 100–60,000), `candidateLimit` (default 100;
range 1–1,000), and `minFreeBytes` (default 2 GiB; range 0–1 TiB).
`GET /api/work-matching/status` returns the current pass state, remaining subject
count, retry time and safe failure category with the same permission.

- One matching evaluation executes at a time. Concurrent manual requests receive
  429 instead of adding an in-memory queue. Request bodies are limited to 64 KiB
  and each evaluation to five seconds.
- Candidate overflow pauses the entire decision. No association is made from a
  truncated set. Increasing the limit queues reevaluation without removing any
  source or identity. The saved explanation contains at most 32 candidate summaries
  and each public title at most 512 characters; this display bound never changes
  uniqueness evaluation. Overflow counts are lower bounds.
- Evidence titles are limited to 8 KiB, other edition fields to 4 KiB combined,
  voice/circle sets to 100 identities, and a candidate evaluation to 2 MiB of
  serialized metadata. Only digests are retained while comparing candidates.
  Association objects retain the shared operation's existing bounds. Oversized
  evidence is reported as paused.
- At most 1,000 automatic operations are created per day. Saved matching results
  use one row per retained subject; association history remains available for
  splitting and owner decisions.
- A failed transaction rolls back and retries after 30 then 60 seconds. After
  three failures the subject has an explicit failed result and the pass continues.
  A partial pass retries after a minute. Low disk space pauses new matching while
  existing reads and personal-review writes remain available.

`TestWorkMatching*` covers HTTP, sessions, migrated SQLite, worker recovery,
candidate budgets and conflict isolation. `work-matching.spec.ts` and
`shared-association.spec.ts` exercise real frontend/backend confirmation with
synthetic inputs, screenshots, video and traces disabled.
