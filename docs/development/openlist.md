# OpenList catalog boundary

OpenList sources use `sourceType: "openlist"` and an administrator-configured
HTTP(S) API endpoint. `config.openListRoots` contains at most 8 explicit audio
work directories. A root owns its audio tracks and format subdirectories; it
does not infer series or episodes. An explicit bounded scan visits at most 32
directories, 8 levels, 2,000 entries, and 100 pages per root. Only a completely
read root containing audio becomes a catalog entry. Archives and subtitles
alone are excluded. Password-protected directories are never unlocked.

In Settings, add a remote source, select OpenList, enter its site base URL and
one absolute work-directory path per line, then save. Open that source in
Library and choose **Scan configured directories**. Search uses the stored
titles and product codes. Open a card to browse format folders and play a track;
saving its personal review registers only the necessary work information.
Directories must be chosen explicitly; the site root is not a valid work root.

`POST /api/remote-sources/{id}/catalog/scan` requires `sources:write` and scans
only the configured roots. It returns per-root outcomes; partial, inaccessible,
cancelled, and over-limit observations are not complete coverage. This finite
entry point does not perform a whole-site crawl or schedule daily updates.

`GET /api/remote-sources/{id}/works` uses the existing paged source card and
search response. OpenList cards add `catalogEntryId` and may have an empty
`primaryCode`; an absent `workId` means the entry has not been registered.
`GET /api/source-catalog/{id}` reads the current entry and only the viewer's
personal review. `GET .../tracks` reads its persisted directory snapshot.
`GET .../media?path=<relative path>` selects a file from that snapshot, resolves
its current URL from OpenList, and streams it without creating a media cache.

`GET /api/works/{id}/catalog-directories` projects catalog entries into the saved
work's source tabs. Numeric references retain the exact edition; numbered
references and unregistered numbered entries use existing canonical/alias
relationships. Numberless entries require an existing `work_id`. Each response
entry carries `directoryType: "catalog"`, `catalogEntryId`, source id/name,
work id, title, product code, status and UTC `checkedAt`. Reading this projection,
tracks or translations does not register works or media.

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

The reusable server read boundary is `loadSourceCatalogEntry(ctx, entryID)`;
it checks the enabled source type before returning stored original text and
source identity. `loadSourceCatalogFiles(ctx, entryID)` returns immutable real
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
