# Catalog series

A catalog series groups independently addressable episodes. It is separate from
provider-owned `party_series` metadata. Its persistent `seriesId` does not encode
a path or display title. Each episode retains its catalog id, optional exact
work id, source directory and playback timeline.

## Discovery

An authored `Episode 1`, `Ep. 1` or `第1集` marker, or a child of an explicitly
labelled `Series: Example`, `Example Series`, `系列：示例` or `示例系列` directory,
provides source-local grouping evidence. The Chinese and Japanese form requires
its counter: `第3話` is an episode, while `第3章` is a chapter inside one work and
is treated as a track container instead. Shared creators, similar titles,
topics and numeric track names do not. Format and track subdirectories stay
inside their episode, including `Episode 1 (MP3)` and `Episode 2 [FLAC]`.
Duplicate episode numbers remain independent for review.
A series contains at most 100 members; additional entries stay independent.
A single-member series is not displayed as a series card: the entry keeps its
own card until a second evidenced member exists, so an isolated `Episode 1`
does not create a creator-named series.
When a complete scan sees the same episode set under a renamed series
directory, the old series is folded into the new one only when both series
directories share the same comparable parent container. A series directly
below the source root, or one whose parent cannot be derived from live members,
stays separate for explicit review rather than being matched by episode names
alone.
`ep` and `ep.` count as episode markers only at a word boundary. An `ep`
inside an English word, as in `Deep 2` or `Step 2`, is not series evidence.

A bare numbered label is not series evidence either. One source may publish
`T001 …`, `T002 …`, `A89 …` and `B031 …` side by side, where the letter and
number are a local catalogue convention for unrelated works. Such a label is
neither a series name, nor an episode marker, nor an explicitly labelled series
directory, and grouping on the shared prefix would mix different works
(US-30). These entries stay independent; an editor groups the ones that really
belong together through the preview and confirm flow described below (AC-14).

Loose audio files can carry the same explicit evidence: a folder containing
`Example Episode1` through `Example Episode6` becomes one series with six
independently addressable episodes. A bare numbered run such as `01.mp3` through
`06.mp3`, or track containers such as `Disc1`/`Disc2`, stays one work. A folder
whose direct children are only format copies (`Episode 1 (MP3)`,
`Episode 1 (FLAC)`, `Episode 2 (MP3)`) likewise forms one series: both formats
of Episode 1 belong to that episode, and no format directory becomes a card.
Format variants without an episode marker, such as `MP3` beside `FLAC`, remain
formats of the enclosing release.

A finite scan partitions a new root only when every playable file belongs to
an evidenced direct child. Full indexing attaches membership in the same
transaction as catalog publication. Published containers keep their existing
identity when later observations reveal episode children; a later numberless
work below a reviewed grouping folder still receives its own card. Reading or
grouping does not register works, download media, or change personal records.
A complete refresh marks episode cards that the listing no longer contains as
`missing`, keeping their id, work link and personal records; the series view
keeps listing them with `status: "missing"` so their personal records remain
reachable, and reports them as available again when the source republishes them.

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
records remain intact. [Shared associations](shared-associations.md) provide
same-level series or episode association with record-owner conflict confirmation. Private values contribute only to an internal digest.

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
