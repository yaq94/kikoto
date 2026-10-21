# OpenList catalog boundary

OpenList sources use `sourceType: "openlist"` and an administrator-configured
HTTP(S) API endpoint. `config.openListRoots` contains at most 8 explicit audio
work directories. A root owns its audio tracks and format subdirectories. Explicit episode or
series-directory evidence groups independent entries as described in
[Catalog series](catalog-series.md). An explicit bounded scan visits at most 32
directories, 8 levels, 2,000 entries, and 100 pages per root. Only a completely
read root containing audio becomes a catalog entry. Archives and subtitles
alone are excluded. Password-protected directories are never unlocked.

In Settings, add a remote source, select OpenList, and enter its site base URL.
Finite work-directory paths are optional. Open the source in Library and choose
**Index all anonymous audio** for the full source, or **Scan configured
directories** for selected work roots. Search uses stored titles and product
codes. Open a card to browse format folders and play a track; saving its personal
review registers only the necessary work information. The site root is not a
finite work root.

`POST /api/remote-sources/{id}/catalog/scan` requires `sources:write` and scans
only the configured roots. It returns per-root outcomes; partial, inaccessible,
cancelled, and over-limit observations are not complete coverage. This finite
entry point does not perform a whole-site crawl or schedule daily updates.

## Recoverable full index

`POST /api/remote-sources/{id}/catalog/index` requires `sources:write` and
queues a `source_catalog_index` workflow. It traverses anonymous directories
from `/`, regardless of the finite work-root selection. A repeated start joins
the active run. Starting a cancelled, failed, paused or partial scan resumes
its saved directory/page cursors; starting a complete scan begins a new
observation generation. Completed directories survive retries. Changing page
size restarts only incomplete page sequences.

`GET /api/remote-sources/{id}/catalog/index?page=1` requires the same permission.
It returns run id, timestamps, attempted pages, observed entries, indexed entries,
completed/pending/failed/restricted/unconfirmed-empty directory counts and
paginated partition and unresolved-directory details (50 per list). It never
reports a whole-source percentage with an unknown denominator. An empty answer
remains unconfirmed; permission failures and unfinished ranges prevent complete
coverage. The existing workflow cancel and retry endpoints stop or resume the
same scan without discarding its published catalog or private reviews.

## Directory exclusions

`GET` and `POST /api/remote-sources/{id}/catalog/exclusions` require
`sources:write`. `POST` accepts `{"path":"/Work/Extras","mode":"whole","note":"..."}`.
The path must be absolute, below `/`, and free of `..`; repeated slashes are
normalized. An exclusion covers the path and every descendant.

`mode` is `whole` (default) or `whitelist`. A whitelist is a subtree exception
inside an excluded tree: it defeats a whole rule only when it is at least as
specific as that rule and still covers the path, so an outer exclusion keeps its
other children excluded. Its own files, directory rows, observations and audio
roots are kept and stay playable; ancestors on the way to it are kept as a
bridge that the next incremental sync may list. Creating a whitelist after a
whole rule deleted its rows marks the bridge as changed and starts a sync, so
the exception is republished instead of staying empty.

`DELETE /api/remote-sources/{id}/catalog/exclusions/{exclusionId}` removes one
rule by the id returned from the list. Deleting a whitelist re-applies the whole
rule above it; deleting a whole rule restores the entry to a discoverable state
and the next scan decides whether the files are available. Existing entries and
every work, review, series and relocation reference are retained. Only catalog
files, observations, directory rows and audio roots that remain excluded are
removed, including files of a parent card that fall inside the excluded path.

The Library source panel shows index status, counts, timestamps, partition
coverage and unresolved directories. **Index limits** configures the next start
or resume; active settings are read-only. **Cancel**, **Resume index** and
**Refresh progress** operate on persisted state. Reloading the page returns to
the same run. Polling is limited to active scans and requests are cancelled when
the panel unmounts; newly published cards refresh the source results.

Scan options are saved with the source's checkpoint and reused by an empty
start request. An active run keeps its options until it is stopped and resumed.

A full scan that reached the end of the tree enables daily updates for that
source. The next due time is persisted in `app_setting` and is checked by the
existing workflow coordinator on startup and while the application runs. A
stopped application catches up with one update when it starts; elapsed days do
not queue multiple crawls. A run that still has unconfirmed empty ranges also
schedules the next re-observation, but the daily trigger resumes the same
generation instead of creating an independent full crawl, so new content below
a transient empty answer is eventually discovered. Failed and restricted ranges
retry on the same schedule; a low-disk pause waits for the operator. Finite
scans and disabled sources do not enable the schedule.

Daily dispatch uses the same transaction as manual dispatch: queued/running
scans are joined, and cancelled, failed, paused or partial scans resume their
existing generation. The due time advances with dispatch, so repeated coordinator
checks do not continually retry a paused source. A complete success schedules
the next update for 24 hours later. Daily work uses the saved limits, existing
source request policy and low-space pause, and only complete coverage reports
success. Manual starts still require `sources:write`.

| Option | Starting value | Accepted range |
| --- | --- | --- |
| `concurrency` | 1 | 1–4 metadata requests within the single workflow executor |
| `intervalMs` | 1000 | 100–60000 between metadata starts, shared per origin |
| `maxResponseBytes` | 2097152 | 1024–8388608 |
| `pageSize` | 100 | 1–500 |
| `pagesPerBatch` | 8 | 1–32 page or publication steps |
| `maxPagesPerDirectory` | 10000 | 1–100000; reaching the bound leaves an explicit retryable range |
| `minFreeBytes` | 2147483648 | 0–1099511627776 |

The values above are starting points for a source that has not been calibrated.
They are not a ceiling and not a throughput recommendation for every host. A
saved per-source value always wins over the starting value, so raising the rate
for one source cannot silently change another. The pacing options bound how
often a metadata request may start and how many run at once; they do not bound
database work.

`make source-index-performance` measures the whole worker over a synthetic tree
and reports completed directories per minute, connection-pool waits, refusals
and process CPU for each option combination. Measure on the deployment host,
then raise concurrency or shorten the interval until the origin rate limit or
the host write capacity is the binding constraint, and record the measurement
next to the chosen value. Do not treat the starting value as the answer to a
capacity question.

On a 32-processor development host, a 965-directory tree completes at about
352 directories per minute with concurrency 4, a 150 ms interval and 32 steps
per batch, using about 20 ms of CPU per directory. Throughput is bound by
request pacing, not by the worker's tick.

Choose concurrency 2, a 150 ms interval and 32 steps per batch for a first
complete scan, and keep the defaults elsewhere. Expect the per-directory cost to
rise with catalog size on a two-processor host; when the process log shows
`database is locked` or pool waits, reduce concurrency to 1 and postpone
recommendation and metadata projection rather than shortening the interval.

The worker releases its executor after each batch. A bounded invocation that
settles back to the queue while directories remain is claimed again at once
instead of waiting for the coordinator tick; a retry delay, a paused source or a
terminal run keeps the normal tick, and higher-priority queued work still wins
the claim query. Each page commits validated names and its next cursor together.
Child discovery and catalog publication write at most 100 objects per
transaction. Low or unknown free space on the database volume pauses new
observations and publication while stored cards remain readable. The disk check
reserves additional response/WAL headroom; the database volume must also be
monitored by the operator.

Numbered directories own their descendants. A directory with audio, or with
only recognized format subdirectories, owns its track/format tree; other
directories remain traversal containers. Evidenced episodes own their descendants;
existing published containers retain their identity and require explicit review
to split. Series membership is attached in the same publication transaction. No title matching merges catalog identities.
Only roots with observed playable audio become searchable cards. Source-returned
URLs are discarded; neither discovery nor publication registers works or
downloads media. Missing observations do not delete cards or private records.

Directory pages with changing totals restart their bounded sequence up to two
times, then remain failed. Repeated pages cannot count as progress. A manual
retry resets failed/restricted/empty observations while retaining completed
ones. The `source_catalog_scan`, `source_catalog_directory` and
`source_catalog_observation` tables retain the current source generation and
stable staging keys rather than copying the whole tree on each batch.

There is one scan reference per source, so workflow retention pins only that
source's current/recent run. A new complete-index generation replaces the
reference and releases the old run for normal history retention. After complete
successful coverage, obsolete observation revisions and previous-generation
directories are reclaimed in batches of at most 100 staging objects. Partial,
failed and restricted coverage cannot retire these observations. Published
catalog entries, file snapshots and personal records are outside this cleanup.
Progress exposes `stagingDirectories` and `stagingObservations` for the retained
metadata footprint; the database reuses freed pages under its normal storage policy.

Full indexing uses the existing outbound source policy and a dedicated paced
metadata lane. Metadata redirects are refused, response size and 30-second
request deadlines apply, and cancellation releases every in-flight request.
Publication reads the durable `publish_pending` queue:

- `source_catalog_observation.publish_pending` is set by page persistence and
  cleared when a batch publishes the observation;
- the partial index
  `source_catalog_observation(file_source_id,directory_path,name) WHERE publish_pending=1`
  orders pending rows;
- `source_catalog_directory_publish_root`, a partial covering index over
  complete directory roots, supplies the revision and path without a table
  lookup;
- `source_catalog_audio_root(file_source_id,generation,catalog_root)` holds one
  row per generation and root with at least one complete directory that has
  audio.

The publisher drives each batch from the generation's completed directories:
a candidate needs a non-empty `catalog_root`, an audio root for the same
generation, and a pending observation, which the partial index seeks. The join
is forced to start from directories (`CROSS JOIN`); letting the planner start
from the pending index sorts a large backlog from an earlier generation before
`LIMIT` applies. The directory primary key orders the scan and the observation
primary key orders each directory's rows, so a batch stops after the rows it
publishes and needs no temporary sort. `refreshCatalogAudioRootTx` runs after each directory status
change: it adds a root when a completed directory has audio and removes it when
the last such directory becomes incomplete, so a rescan that deletes tracks
retires the root.

The worker publishes at most eight batches of 50 rows per invocation, so a long
backlog does not hold the single SQLite writer across the whole scan; the
remaining rows stay queued and the job returns to the workflow queue. An
exhausted queue returns without another publication query. When no directory and
no publish row remain, the cleanup pass visits only entries this generation
republished and deletes file rows an older generation left there. It records
progress in `source_catalog_scan.publish_cleanup_cursor`. The cursor only moves
forward, so one pass terminates: a card whose subtree was failed, restricted or
unconfirmed-empty keeps the rows an older generation left there, and a later
generation that observes the subtree retires them.
Routine tests use synthetic complete trees; a real index run must separately
record its actual ranges, timestamps and unresolved directories.

## Release sidecars

An indexed card can carry a `readme`, `info`, `credit` or `staff` file beside
its audio, including `.md`, `.nfo` and saved `.html` pages. Every text extension
(`.md`, `.markdown`, `.nfo`, `.txt`, `.html`, `.htm`) also needs one of those
metadata words in its file name. The extension alone is not enough: the same
formats hold lyrics, notes and logs, and reading them would spend requests on
text that cannot carry credits. `readme.md` and `staff.nfo` qualify while
`lyrics.md` does not.

The accepted names are a shared vocabulary in code, not a per-format check:
`readme`, the spaced or joined forms `read me`/`read_me`/`read-me`, `info`,
`credit`, `staff`, `介绍`, `介紹`, `紹介`, `説明書`, `はじめに`, `クレジット`,
`スタッフ`, `りどみ`, `りーどみー` and `リードミー`. ASCII hints must be a whole
word, so `trackinfo.txt` and `infomation.txt` are not credit files; CJK hints
match anywhere in the name. The publication path applies the same predicate
after reading the committed rows, so a name the reader accepts can never be
skipped when a card is queued. A file outside these names is a known miss, not
an implied acceptance: release pages that use an unlisted word need that word
added to the shared list rather than a second SQL rule.

The index reads at most six such files per card, at most
256 KiB each, through the same source client, origin policy and request pacing
as metadata. UTF-8, UTF-16 and Shift-JIS bodies are decoded before parsing. The
file is not a track.

Publication only marks a card `sidecar_pending`. A bounded warm pass runs when
an index invocation finds no remaining directory or publish work, and the
independent sidecar worker continues the durable queue. Each pass is bounded by
12 upstream requests and 20 seconds, and the index job never waits for the
whole queue. A source that answers no file at all is not continued at once, so
its normal retry delay applies. A definitively unreadable card is retried at
most five times; a rescan does not reset that budget, so a deleted readme is
not requested on every daily pass. The credit-file revision (`path|size|mtime`)
is stored beside the evidence; when that revision changes, the attempt budget
resets and the card is read again. A readme that a complete
snapshot no longer publishes clears the old evidence together with its failure
and retry fields. A card republished while its readme's directory is still
failed, restricted or unconfirmed-empty keeps its evidence: an incomplete range
cannot prove that the file was deleted.

Two failure classes have separate counters. A
definitive answer - `404`/`410` for the object or its body, a body above the
read limit, or an invalid object - is permanent and spends one of five
`sidecar_attempts`. Anything else (connection failure, `5xx`, authentication
refusal, rate limit, timeout, a local database read) is temporary: it
increments `sidecar_transient_attempts`, writes `sidecar_retry_at` and leaves
the permanent budget untouched. A card with both a deleted file and a temporary
failure follows the temporary path, because settling it would drop the file
that can still be read when the source recovers.

The temporary wait starts at 15 minutes, doubles per failure and is capped at
24 hours. The timestamp is produced by SQLite's own clock
(`datetime('now','+N seconds')`) so it compares correctly with the
`CURRENT_TIMESTAMP` used by the due queries. After eight temporary failures the
card leaves the automatic queue (`sidecar_pending=0`) instead of probing a dead
source forever; its attempts and failure category stay visible. `sidecar_failure`
holds `temporarily_unavailable` or `unreadable` so an operator can tell those
cases apart without reading logs. A pass that stops on the request budget keeps
the card pending without writing a failure category.

A rescan is not a read: when the credit-file revision is unchanged, the daily
publication pass keeps the attempts, the backoff and the stopped state exactly
as the worker left them. Only a genuinely new revision reopens a card
automatically.

`sidecar_revision` is the durable comparison point. The queue never uses a
fetch timestamp: the same unchanged file keeps its exhausted budget, while a
replaced file is retried without waiting for a manual reset.

### Reopening the credit queue

`POST /api/remote-sources/{id}/catalog/sidecars/retry` requires `sources:write`
and reopens every card of that source that is pending, has spent attempts, or
records a failure. It clears `sidecar_attempts`, `sidecar_transient_attempts`,
`sidecar_retry_at` and `sidecar_failure` and sets `sidecar_pending=1`; stored
credit evidence, work associations and personal records are not touched. Use it
after fixing a share password, DNS entry or upstream outage. Cards that were
never attempted and have no failure are not reopened by this route; a full
rebuild still uses a force index, and the daily pass keeps them queued on its
own. The source index panel shows how many cards have stopped, split by
category, and offers this retry as a button once the count is above zero.

Only explicitly labelled lines become evidence. A line such as `配音：…`,
`声优: …`, `CV：…` or `キャスト: …` names a voice actor; `社团：…`,
`社團：…`, `サークル: …` or `Circle: …` names a circle. Unlabelled prose never
becomes a credit. The source's modification time and the file size form the
cache revision; when the source omits a timestamp, the stored digest is reused
until the size changes. Stored evidence contains only the relative path, size,
modification time, digest, labels and fetch time; it never supplies a product
code, registers a work or downloads audio. Re-reading identical evidence does
not advance the matching revision, so the matching queue is not restarted for a
timestamp-only change.

`GET /api/remote-sources/{id}/works` uses the existing paged source card and
search response. OpenList cards add `catalogEntryId` and may have an empty
`primaryCode`; an absent `workId` means the entry has not been registered.
`GET /api/source-catalog/{id}` reads the current entry and only the viewer's
personal review. `GET .../tracks` reads its persisted directory snapshot.
`GET .../media?path=<relative path>` selects a file from that snapshot, resolves
its current URL from OpenList, and streams it without creating a media cache. It
does not mutate stored state, so it follows the instance media model rather than
repeating the `playback:use` gate: a session is required, or an enabled
anonymous-read policy for `GET`/`HEAD`. Playback preparation creates media
identities, so it keeps its own `playback:use` permission.

### Format copies and auxiliary folders

A release often publishes its audio below a copy folder rather than beside the
readme: `MP3`, `2-mp3`, `01_mp3`, `FLAC 24bit`, `1.音声`, `ボイス`. Those
folders are part of the release above them and never become a card of their
own. The admission rule strips a leading order marker (`2-`, `01_`, `[2] `)
before matching the format word, so a numbered copy is recognized without
treating a title such as `24.歡迎光臨閃亮銀CV床貓` as a format. Brackets and
quality markers are ignored for that comparison, so `(MP3)`, `[FLAC]`,
`MP3 (V0)` and `FLAC24bit` are copies too. A format word
followed by a quality marker (`MP3 320kbps`, `FLAC 24bit`) still matches; a
title that merely starts with one (`MP3 Collection`) does not. Artwork and
script folders (`ART`, `photo`, `3-photo`, `台本`) are auxiliary for the same
reason: they belong to the release, not to a second playable work.

The rule uses categories, not a list of observed directory names. A directory
whose only playable audio sits in a nested format copy is published as the
release card; the format directory itself is not searched as a work. The
end-to-end regression `TestSourceCatalogFormatCopyDoesNotCreateACard` indexes
`MP3`, `2-mp3` and `1.音声` releases through the real HTTP route and asserts one
card with both audio files; `TestSourceCatalogFormatDirectoryOrdinalIsNotSampleSpecific`
pins the category boundary on both sides.

A release that an older generation already published as its own format card
keeps that card only until the release card exists again. Once a rescan
publishes the parent, the stale format card becomes an alias of it: the id is
kept, `relocated_to_id` points at the release, and its file rows are removed.
A saved link still resolves through that alias. A personal review or an import
reference is not moved to the successor: a card that already owns a work or a
series membership keeps its identity, and a retired card without a registered
work is reported by the importer as an unregistered source instead of being
guessed onto another object. A card that
already owns a work, a series membership or an incoming relocation keeps its
identity for explicit review instead of being merged into a guessed parent.
`TestSourceCatalogStaleFormatCardRetiresToReleaseOnRescan` covers the
retirement, and `TestSourceCatalogFormatCardWithWorkOrSeriesIsNotRetired` pins
the retention guard.

`GET /api/works/{id}/catalog-directories` projects catalog entries into the saved
work's source tabs. Numeric references retain the exact edition; numbered
references and unregistered numbered entries use existing canonical/alias
relationships. Numberless entries require an existing `work_id`. Each response
entry carries `directoryType: "catalog"`, `catalogEntryId`, source id/name,
work id, title, product code, status and UTC `checkedAt`. Reading this projection,
tracks or translations does not register works or media.

A creator or tag folder that holds many independent works as loose audio is
published as several cards. Each completed directory revision recomputes the
split decision in `source_catalog_directory.work_split`: `single` keeps one
card, `prefix` groups files by a repeated work code such as `N006`, and `file`
groups direct audio by work title. A small trailing number (up to 30) is a
track marker, so `Example Episode1..6` and `Alpha Rain 1..3` stay one work each.
A larger number belongs to the title, so `Room 101` and `Room 102` stay two
works; a zero-padded code such as `T001`/`T002` is likewise part of the identity.
Named parts of one release (`Prologue`/`Main Story`/`Epilogue`, `表妹上`/`中`/`下`)
and a numbered run with a `Bonus` or `Extra` file also stay on one card.
Split cards store the real directory in `source_catalog_entry.source_root` and a
`::<group>` identity suffix in `root_path`; playback and refresh resolve files
through `source_root`. A folder whose audio is almost entirely a size-identical
copy of its parent's files keeps one card instead of republishing every work.
The old folder card is retired to the first split card only after every row
below it has been published, so existing personal references still resolve.
Split cards from an older generation that the current generation did not
republish are retired with their file rows; a card with a work, a series
membership, or an incoming relocation keeps its identity for explicit review.
An explicit refresh rewrites the whole root from one complete listing, so a
card the listing no longer contains is marked `missing` instead of being
deleted: its identity, work link and personal records stay available, and a
later scan that publishes it again restores `available`.

`POST /api/source-catalog/{id}/refresh` requires `sources:write`. It scans only
that entry's observed root with the same per-root limits and a five-minute
overall deadline. It publishes a complete playable snapshot atomically. An
upstream error or empty audio result retains the previous snapshot. Source tab
selection and content-language changes do not trigger a scan. The directory's
**Check sources** action requests this explicit refresh; read-only viewers
reload the saved snapshot.

`POST /api/source-catalog/{id}/playback` requires `playback:use` and an already
registered work. Its body supplies `paths` (1–256 exact audio paths, at most
16 KiB total JSON), or one `path`. The response's `targets` contain path, work id,
media item id, location id and stream URL. Unknown files return 404 and an
unregistered work returns 409. Preparation creates only missing media identities
and locations in one transaction. Both catalog pages and saved-work tabs use
`catalog:<catalogEntryId>:<relative path>` as the playback identity and preserve
the existing work/media progress API. Different entries never share a timeline
because their display names match. `catalog_stream` locations use the same
Range streaming path through `/api/media/{locationId}/stream` and do not enter
the Kikoeru file cache. Preview playback of an unregistered entry remains
temporary; saving a review performs the explicit registration described below.

The persistent catalog id is independent of display text, source paths and
signed links. `source_catalog_entry` keeps source id, observed root path, title,
optional product code, and optional `work_id`; `source_catalog_file` keeps the
entry's real relative file paths and media kinds. Paths locate observations;
renaming a root does not transfer an old identity or review by title matching.
Temporary URLs are never persisted as identities.

### Source lifecycle

Catalog details and work directory tabs report `available`, `unavailable`,
`maintenance`, `restricted`, `unconfirmed_empty`, `missing`, or `disabled`.
Failed requests and unconfirmed empty directories keep the saved tree and
personal records. Explicit directory refresh records its failure status and a
successful refresh restores availability. A full scan confirms missing entries
only after all its directory ranges are complete; cancelled or partial scans
cannot declare a missing work. Availability belongs to each source separately.
A 404 or 410 answer is a definitive absence and is recorded as an empty
directory rather than a failed range, so a deleted folder cannot hold the whole
source at `partial` forever. Status matching is path-bounded: a failed `/Work`
does not mark `/Work2`. Publishing a card again resets it to `available`.

Each complete scan records a bounded fingerprint of the audio file paths, sizes
and explicit SHA-256 hashes provided by the source. A moved root retains its
catalog id, work and playback identities only when the complete content evidence
uniquely matches one missing entry and one newly discovered, unregistered entry
without series membership or a conflicting product code. Size, title, translation
and signed URL alone cannot transfer identity. Other matches appear as at most
eight `relocationCandidates` on the retained detail; opening a candidate does
not move any records. Shared association follows its own confirmation workflow.

Removing a configured source retains catalog metadata, directory snapshots and
registered works without deleting stored media files. Reviewed works remain
discoverable in the regular Library for their users. Saved catalog links and personal reviews remain readable, and
editing an existing review does not require a usable upstream. Playback and
network refresh still require an enabled OpenList source. The default directory
for a numberless work prefers an available catalog entry; explicitly selecting
another tab does not alter playback. The UI names unavailable states and keeps
candidate links separate from the saved review.

Manual no-source cleanup requires `sources:write` and `library:write`. Any
account-owned reference, including reviews, favorites, tags, progress, lyrics
preferences and listening records, defers deletion of the shared work family
with reason `personal_records`. Unreferenced unavailable families can be deleted.
Media cleanup can remove its selected files while retaining a referenced family;
the workflow reports partial completion rather than claiming that work was
forgotten. Lifecycle reconciliation processes at most 100 entries per transaction
and persists its stage and cursor for restart recovery.

`PATCH /api/source-catalog/{id}/user-state` uses the existing personal review
payload and response. It requires `favorites:write` and, only if the work is
absent, `remote:track`. Registration and the personal update commit together,
return `workId`, and register necessary information only. Repeated or concurrent
registration of the same entry uses that same work. Numbered entries resolve
existing canonical and alias relationships. Empty codes never enter numbered
resolvers. Registered objects use `/api/works/{workId}` and its existing personal
state API; numeric edition semantics remain unchanged.

`work.primary_code` and `logical_work.canonical_code` use an empty string for
code absence, with uniqueness applying only to nonempty values.
`work_edition.primary_code` can likewise be empty. Work ids, existing foreign
keys and personal state are retained through upgrades. This representation is
not a generated product code.

Metadata reads use the shared source transport with a serialized lane, a default
one-second minimum request interval, a 30-second request deadline, a 2 MiB body
limit and cancellable queue admission. The explicit API origin is the metadata
destination; media uses the configured source origin/host policy. Only configured
origins have the private-address exception. Media URLs and redirects are checked
by the shared policy; errors expose fixed messages. Range requests are passed
through, streamed bytes and request lifetime are bounded, and every new playback
request resolves a fresh URL. A failed expired URL is refreshed at most once.

### Request identity and anti-bot refusals

Every OpenList metadata request declares `Kikoto/<version>` by default. The Go
default client identity is rejected by common anonymous-gateway bot rules even
when the request is otherwise valid, so leaving it unset is not a neutral choice.
`config.openListUserAgent` (one line, at most 256 characters) replaces the
declared identity, for example a browser identity the operator has decided to
present. `config.openListHeaders` adds at most 8 whitelisted headers: custom
`X-` names and `Accept-Language`. The configuration layer rejects `Host`,
`Content-Length`, `Connection`, `Transfer-Encoding`, `Upgrade`, proxy headers,
`Cookie`/`Set-Cookie`, `Authorization`, `Accept`, `Accept-Encoding`,
`Content-Type`, `User-Agent` and any value containing a line break. Loading
stored configuration applies the same bounds, so an unsafe stored value cannot
reach a request.

An HTTP 403/429 answer marked `Cf-Mitigated: challenge`, or an HTML refusal
carrying the documented interstitial markers, is classified as a challenge
rather than a permission decision. A challenge answer is never parsed as
business JSON, is not retried within the request, and keeps the existing origin
backoff budget instead of adding a tighter loop. Challenge refusals and ordinary
anonymous-permission refusals therefore remain distinguishable in catalog
progress (`challenge_detected` versus `access_restricted`), and the server's
public error keeps its fixed message.

The behavior is covered without a network by `internal/openlist` request tests
(default and overridden identity, header whitelist, challenge classification
versus a plain refusal, no retry) and by `internal/httpapi` tests that drive a
catalog scan, a media request and the full-index worker through the application.
`TestOpenListIdentitySelfCheck` (opt-in via `KIKOTO_OPENLIST_SELFCHECK_URL` and
optional `KIKOTO_OPENLIST_SELFCHECK_FILE`) is an operator probe of one
authorized source: it logs only the declared identity, stage, byte counts and
SHA-256 digests for `bytes=0-31` and `bytes=1024-1055`, and is not playback
acceptance. The playback acceptance remains
[the two-range LIVE sample](openlist-live.md).

The outbound read boundary is `loadSourceCatalogEntry(ctx, entryID)`;
it checks the enabled source type before returning stored original text and
source identity. `loadRetainedSourceCatalogEntry(ctx, entryID)` resolves retained
catalog links for metadata and private review reads even after source removal.
`loadSourceCatalogFiles(ctx, entryID)` returns immutable real
relative paths separately from display names. Callers first load the entry to
enforce its source visibility before loading files. Content translation validates
`catalogEntryId` plus field/path against these reads without registering a work.
Cards and catalog details use the shared translation resource scope; directory
labels retain their real paths and use the same original/retry controls as titles.
See [public content translation](translation.md#public-content).

For local functional checks, `go run ./tests/openlist-fixture` accepts
`OPENLIST_HTTP_ADDR` (default `127.0.0.1:17617`) and `OPENLIST_FRONTEND_ORIGIN`
(default `http://127.0.0.1:3117`). Point Vite at that API, set
`PLAYWRIGHT_BASE_URL` and `OPENLIST_API_URL`, then select `openlist-catalog.spec.ts`
through `make frontend-e2e`. The fixture replaces only the external source/model;
the two tests use real API sessions, catalog reads, translations and reviews.
`POST /__fixture/stop` shuts down the fixture and its disposable database.
The same fixture supports `openlist-proxy.spec.ts` for the administrator's
source-specific proxy selection. See [single-file verification](openlist-live.md)
for the explicit LIVE proxy contract, sanitized diagnostics and offline tests.
