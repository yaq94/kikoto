# Unreleased

> [!IMPORTANT]
> This release rebuilds the source catalog identity tables. Take a database
> snapshot before upgrading and keep the previous image available for rollback.

This release adds translation of catalog content, a recoverable full source
index, multi-source directories, and personal records for numberless works and
series.

<details>
<summary><strong>Content translation</strong></summary>

- Administrators configure one translation endpoint, model, and key; the key
  stays server-side and is returned masked.
- Titles, descriptions, system tags, and directory names follow the display
  language, with the original text always available.
- Traditional Chinese conversion reuses the Simplified translation and keeps
  circle and cast names verbatim.
- Translations are cached per source text, model, and template, with bounded
  concurrency, queueing, and storage.

</details>

<details>
<summary><strong>Source catalog</strong></summary>

- Anonymous OpenList sources expose numbered and numberless audio as catalog
  cards with directories and playback, without downloading media.
- A recoverable full index walks every directory page by page, stores durable
  checkpoints, and reports per-partition progress, failures, and unconfirmed
  empty directories.
- Daily updates reuse an unfinished scan instead of starting a second
  full-tree job; low disk space pauses the scan without losing progress.
- Renames, missing directories, deleted sources, and restored directories keep
  the card and its personal records.

</details>

<details>
<summary><strong>Works and personal records</strong></summary>

- One work shows every source directory and switches between them without
  interrupting the current track.
- Numbered works, numberless works, series, and episodes each keep private
  ratings and notes; a shared identity reuses the current user's record across
  sources.
- Shared association and split show a preview, require each affected user's own
  conflict decision, and keep both playback editions.
- Personal data export and import cover every record type and remain compatible
  with the numbered format.

</details>

<details>
<summary><strong>Reliability</strong></summary>

- The source index bounds each request, response size, batch, and write
  transaction, and releases the database between batches.
- Unknown and stale catalog directories are checked in the background while
  known directories stay readable.
- Browser tests cover the mobile directory skeleton, favorites navigation, and
  cover touch behavior without layout gates.

</details>
