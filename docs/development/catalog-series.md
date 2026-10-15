# Catalog series

A catalog series groups independently addressable episodes. It is separate from
provider-owned `party_series` metadata. Its persistent `seriesId` does not encode
a path or display title. Each episode retains its catalog id, optional exact
work id, source directory and playback timeline.

## Discovery

An authored `Episode 1`, `Ep. 1` or `第1集` marker, or a child of an explicitly
labelled `Series: Example`, `Example Series`, `系列：示例` or `示例系列` directory,
provides source-local grouping evidence. Shared creators, similar titles,
topics and numeric track names do not. Format and track subdirectories stay
inside their episode. Duplicate episode numbers remain independent for review.
A series contains at most 100 members; additional entries stay independent.

A finite scan partitions a new root only when every playable file belongs to
an evidenced direct child. Full indexing attaches membership in the same
transaction as catalog publication. Published containers keep their existing
identity when later observations reveal episode children. Reading or grouping
does not register works, download media, or change personal records.

## Shared grouping

With `library:write`, the catalog detail's **Group into a series** action lets
an editor name a series, search and select independent entries from the source,
preview their order and confirm. The preview id remains in the page URL for
reopening. Saved membership is shared with other readers, including anonymous
readers when the instance allows anonymous access.

- `POST /api/catalog-series/previews` accepts `title` and 2–100 ordered
  `catalogEntryIds`. JSON is limited to 16 KiB; titles to 512 UTF-8 bytes.
- `GET /api/catalog-series/previews/{id}` reads only the actor's preview.
- `POST /api/catalog-series/previews/{id}/confirm` rechecks actor permission
  and the current member snapshot, then commits membership atomically.
  Repeated confirmation returns the completed series id.
- `GET /api/catalog-series/{id}` returns title, optional work id, ordered episodes
  and only the viewer's series `personalRating` and `personalNote`. Anonymous
  reads return an empty personal state; episode membership contains no private fields.

Previews expire after 24 hours. An actor may retain at most 32 unexpired pending
and 32 completed previews. Identity, location, title, source visibility,
membership or personal-record changes invalidate a pending preview. Existing
groups cannot be merged through this entry point; member work identities and
records remain intact. Private values contribute only to an internal digest.

Source browsing shows one series card and still matches episode titles during
search. Opening an episode selects its existing directory controls. Opening a
series, episode or format does not change the persistent player; selecting a
track uses that episode's real relative path and playback identity.

## Personal reviews

The series detail labels its editor **Series review**; each member labels its
editor **Episode review** with the episode title. `PATCH
/api/catalog-series/{id}/user-state` requires `favorites:write`. The first save
also requires `remote:track` and registers only the series' own numberless work,
logical work and edition in the same transaction as the personal update. It
never registers the members, creates a download, or assigns media to a series.

The payload accepts `rating` (0–5 or null) and `note` (string). Omitted fields
remain unchanged; null rating clears only the score and an empty note clears
only the note. Request bodies are limited to 64 KiB. Actor identity comes from
the authenticated session. Existing series and episode work references reuse
personal state across sources; grouping, expanding and changing format do not
copy records. Series and each episode retain separate work ids through restart.

Episodes use the [existing catalog review endpoint](openlist.md); source matching
and shared identity changes require their own confirmation boundaries.

## Translation and functional checks

The public translation contract accepts `seriesId` for the series title and
`catalogEntryId` for each episode. Original text and real file paths remain
unchanged. Previews display original text for confirmation.

Use `OPENLIST_SERIES_FIXTURE=1` with the [OpenList fixture](openlist.md) and select
`catalog-series.spec.ts` or `catalog-series-review.spec.ts`. The functional checks
cover independent review targets, partial clears, reload, card navigation, explicit
track selection, continuing playback, reload and confirmation. Screenshots,
trace and video are disabled. Synthetic audio does not establish compatibility
with real provider formats.
