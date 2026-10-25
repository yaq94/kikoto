# Translation service

`internal/translation` owns server-only configuration, the bounded completion
client, the two-lane request queue, name protection and language detection.
`httpapi/content_translation*.go` resolves readable public fields;
`translation_runtime.go` composes them with the model and the scheduler. The React
`features/translation` entry owns the administrator form and its typed API, while
`components/content-translation` renders translated public text.

## API and persistence

Administrator routes require `sources:write`:

- `GET /api/settings/translation` returns `baseUrl`, `model`, `hasApiKey`,
  `apiKeyMask`, `timeoutSeconds`, `disableThinking`, `concurrency`, `queueLimit`,
  `maxEntries`, `cacheBytes` and `minFreeBytes`.
- `PATCH /api/settings/translation` accepts optional `baseUrl`, `model`, `apiKey`,
  `timeoutSeconds`, `disableThinking` and the five resource limits above. An omitted key is preserved and an
  empty string clears it. The response uses the same redacted projection as GET.
- `POST /api/settings/translation/test` accepts `text` and `targetLanguage`
  (`zh-Hans`, `en`, `ja` or `ko`). It returns complete plain text, the configured
  model name, template version and elapsed milliseconds.
- `GET /api/settings/translation/runtime` returns per-language corpus counts,
  the soft byte budget, the prefill view and the queue counters.
- `GET /api/content-translations/version` returns the opaque `version` string
  the translation responses embed. It is readable by any `library:read` caller
  and lets the browser reject a cached row from another model, endpoint,
  reasoning switch or prompt template before the first translation of a
  session. Every single, batch and trial response carries the same `version`;
  the browser stores it with the row and deletes a row whose version differs.
- `GET /api/settings/translation/prefill` returns the durable prefill cursor,
  per-language coverage, the night-window flag, the recent rate and the last
  error. `POST` the same route with `{"status":"running"|"paused"}` pauses or
  resumes the pass.
- `POST /api/content-translations/batch` accepts a display `language` and up to
  200 items, each with a client `key` and the same selector as the single
  endpoint, including `code`. Every item reports its own `status` and `code`;
  one failure does not cancel the rest, and an unknown code reports `not_found`
  rather than a source outage.

Configuration is persisted transactionally in `app_setting.translation_service`.
The secret is stored with the other private server data; it is not encrypted by
the application. It is absent from runtime settings and public responses. No
translation text or result is retained by the trial endpoint. Interface language
and metadata language preferences are independent of trial input.

`translation.NewStore(db).Load(ctx)` returns the private `Config` for a trusted
server-side caller. `Update(ctx, Patch)` returns only redacted `Settings`.
`translation.Translate(ctx, client, cfg, Input)` performs the completion; its
caller must provide the configured-origin policy client. Callers must never
serialize `Config` into an API response.

## Public content

`POST /api/content-translations` requires an authenticated account with
`library:read`. It accepts a resource selector (`workId`, a persisted `code`, or
remote `sourceId` plus `code`), `field` (`title`, `description`, `tag`, `directory`; a
remote work listing has no description, so `sourceId` with `description` is rejected as
invalid input),
`text`, and metadata `language` (`origin`, `zh-cn`, `zh-tw`, `en-us`, `ja-jp`,
`ko-kr`). `mediaId` and `path` narrow directory selection. Numeric `workId`
preserves the selected edition and does not require a product code.
`catalogEntryId` selects a source catalog entry without falling back to another
resource. Its title is checked against the stored entry title; directory fields
require the exact real relative `path` and its stored display title. The shared
catalog loaders recheck source enabled/type and Demo restrictions before cache
lookup. Content reads do not register works or download media.

The server reloads the selected public field and verifies the exact text before
reading cache or generating. Changed text returns 409; missing content returns
404; unavailable remote sources return 503. Remote enabled state is checked on
every request, including cache hits. Private fields and arbitrary text are
rejected. Existing target-language titles, descriptions and system-tag names
take precedence over generation. Missing Traditional Chinese fields use the
matching authored Simplified field, or obtain its generated Simplified translation
through the shared queue and cache. OpenCC `s2t` converts that text locally before
protected names are restored; selecting either script never sends a Traditional
target to the model. Authored Traditional fields retain precedence.

Remote titles, tags and directory display names have no durable work or
catalogue-entry identity. Their successful model output is stored in
`source_field_translation`, keyed by source, work code, field, real relative
path, original text, target language, model, endpoint, reasoning switch and
template version. Repeating the same browse therefore reuses the row instead of
calling the model again. This table is display cache, not searchable corpus: it
has no FTS table or trigger and is never read by library or source search. The
source is still revalidated before a cached row is considered, so disabling or
restricting a source cannot be bypassed through this cache.

Responses contain `text`, `original`, `language`, `status` (`original`, `existing`,
`translated`, `failed`), optional error `code`, and optional `cacheStatus`.
`origin` resolves original text without calling the model. Circle and voice actor
names are replaced with placeholders before generation; missing, duplicated or
modified placeholders reject the output. Personal notes and personal tags have
no automatic translation path. Conversion failures return the verified original,
`status=failed` and `translation_conversion_failed`; the same original/retry
controls remain available. Conversion accepts at most 256 KiB of valid UTF-8 text.

The pure Go dependency `github.com/longbridgeapp/opencc` is pinned to `v0.3.13`.
It embeds the upstream OpenCC dictionaries and requires no C runtime, dictionary
installation or outbound request. The `s2t` converter initializes once per process
on first use; subsequent conversions read the shared immutable dictionaries.
Only the Simplified model output enters the disk cache. Traditional text is
computed for each response, so a dictionary upgrade cannot reuse a stale
Traditional derivative or overwrite Simplified text. Frontend state keys retain
the selected display language. Cache budgets and model/template invalidation
apply to the shared Simplified entry.

`ContentLanguageProvider` reads the account's display preference independently
of UI locale. `ContentTranslationScope` supplies the resource and shared
original/retry controls; `ContentResourceScope` changes the resource for a source
directory. `TranslatedText` accepts a public field, display text, optional
resource, media id and path. It renders plain text, translates visible dynamic
content, retains original text while waiting or after failure, and cancels stale
requests on language, resource, page or account changes. The content-language
event refreshes text without remounting the detail or changing the selected
directory, edition, media identity or player state.

## Resource budgets and cache

| Setting | Default | Accepted range |
| --- | --- | --- |
| `concurrency` | 8 | 1–16 |
| `queueLimit` | 2048 | 0–4096 |
| `maxEntries` | 0 (unlimited) | 0–2,000,000 |
| `timeoutSeconds` | 60 | 1–60 |
| `cacheBytes` | 256 MiB | 4 KiB–4 GiB |
| `minFreeBytes` | 2 GiB | 0–1 TiB |

Interactive content requests and the background prefill pass run on separate
lanes of one scheduler. Interactive work is always chosen first; a queued
background job is promoted when a viewer asks for the same content. Identical
in-flight requests share one job, and cancelling the last reader cancels it. The
queue holds no database connection while waiting; at most 512 readers are
admitted. The timeout includes waiting. Trials require an immediately free lane
and otherwise return `translation_busy`.

The interactive lane holds 256 waiting items and the background lane follows
`queueLimit`. Total concurrency is `concurrency`. Outside 00:00–08:00
Asia/Shanghai the background lane is limited to two simultaneous calls, so a
viewer still finds a free worker; inside the window it may use every slot.

`content_translation` is durable search corpus, not an LRU cache. It stores one
row per `cache_key` with `origin` `native` or `model`; `origin=native` rows hold
the source text unchanged and never increment model usage. There is no
`last_used_at`. Current `field='title'` rows for `work`, `source_catalog_entry`
and `catalog_series`, plus `field='description'` rows for `work`, are required
for language-specific search and are never eviction candidates. `tag` and
`directory` rows remain display-only; `source_catalog_entry` has no description
column, so entry descriptions are not invented. Migration 099 adds a
`config_key` covering the model, base URL, template version and reasoning
switch; a row with another config key is stale and is not part of current
coverage, while pre-099 rows remain legacy candidates until re-prefilled. Only
`tag`/`directory` model rows and all `source_field_translation` rows are
rebuildable display cache; they may be evicted within the configured budgets.
`source_field_translation` has no FTS table and is never read by content search.

Two administrator budgets bound the rebuildable display cache in a maintenance
pass that runs beside the prefill worker: `maxEntries` limits the number of
evictable model rows (zero means unlimited), and `cacheBytes` bounds their
measured SQLite page footprint through `dbstat`, including indexes and FTS
shadow tables. Budgets below 1 MiB fall back to UTF-8 text bytes because one
SQLite page is already larger than the configured value. Native rows carry
authored text and are never eviction candidates; personal records and source
identities are in other tables and are never touched. Superseded rows are
removed before live rows. The pass runs bounded batches so the SQLite writer is
never held for a whole trim. If the budget is exceeded only by persistent
title/description rows, the status reports `over_budget`; it does not delete search-critical
translations. When `minFreeBytes` is not met, new rows are not written at all:
the caller still returns the translation it already fetched, reports
`paused_low_disk`, and the durable copy waits for space.

A content change produces a new `cache_key` because the source text is part of
it. The previous row is stale and the periodic retention pass removes it as
superseded; it is never counted as current coverage. `zh-tw` viewers read the
`zh-Hans` row and convert it with OpenCC `s2t` at display time, so the corpus
does not store a second full-text copy. A title or description detected as
`zh-Hant` is written as a native row and participates in searches for both
scripts. The background prefill enumerates entry title, series title and local
work title/description. Its durable cursor carries kind, field, id and
language, in that order, so one entity receives every model language before the
scan moves on. It writes native rows for text already in the target language
before queueing model calls. A failed item is stored in
`translation_prefill_failure` with its source hash, attempt count and next retry
time; `NULL` next retry means no attempt is scheduled. The attempt budget
survives a restart, and changed source text starts a new hash instead of
inheriting the old failure. `complete` is written only when the effective corpus
has no missing, stale, failed or retry item. `zh-tw` is a display projection and
is not a separate prefill queue; native `zh-Hant` rows are reported, but a
missing one does not block completion because the `zh-Hans` projection still
serves the reader.

Display-language search applies only to text clauses and covers `title` for
entries and series, plus `title` and `description` for works; `tag` and
`directory` translations are not search inputs.
`zh-cn`, `en-us`, `ja-jp` and `ko-kr` map to the stored tokens `zh-Hans`, `en`,
`ja` and `ko`; `zh-tw` reads `zh-Hans` with OpenCC `s2t` and native `zh-Hant`. A
release code or another structured identifier remains searchable regardless of
display language. `origin` searches the original source index and never reads
`content_translation`; target-language text searches never merge original-title
FTS hits as a fallback. One- and two-character queries use the complete
effective set. Migration 099 keeps the required `(language, entity_kind, field,
id, translated_text)` index and adds `idx_content_translation_short_current`
with `origin` and `config_key`, so the current-row predicate stays covered.
The short query uses `LIKE` on the covered column; SQLite's ASCII
case-insensitive comparison replaces a `lower()` call per candidate row. The
45.6 万行 `translation-bench` run measured p95 49.45 ms for the short query and
29.07 ms for the three-character FTS query.

The `config_key` migration, persistent title/description rows, strict
display-language search and complete short-query behavior are implemented:
`translationPersistentCorpusSQL` excludes current corpus rows from eviction,
`idx_content_translation_short_current` serves the keyset walk, and the short
query no longer uses a newest-row window. The 45.6 万行 benchmark numbers above
come from the local `translation-bench` run; the production-scale measurement
belongs to ticket 24.

Free space is read from the nearest existing parent before the data directory
exists. Low space pauses new background generation (`paused_low_disk`) while
keeping stored rows readable; raising free space permits later generation. The
prefill worker checks `minFreeBytes` plus a write headroom before starting a
batch, records `paused_low_disk` in `translation_prefill_state.last_error`, and
resumes on its own once the floor is met again; an operator-requested `paused`
status has an empty `last_error` and stays paused. An interactive request keeps
its model result rather than discarding a paid call, but the corpus row is not
written while the volume is below the floor. The response reports
`paused_low_disk`; the prefill pass records the item as a pending failure so a
later pass retries it. Failed and empty generations are never stored. Stored
rows report `hit`, freshly generated rows `stored`.
## Model contract and limits

The default model invocation name is `Index-Translate-35B-A3B`; a configured name
is sent without replacement. The base URL is unset until an administrator saves
the service URL. The caller appends `/chat/completions` to that base.

Template `index-user-v1` uses one user message with automatic source language:

```text
请将以下文本翻译为{target-language}，直接输出翻译结果，不要进行任何解释。

{source-text}
```

The language names are 简体中文, 英语, 日语 and 韩语. This is the unconstrained
single-user template from the [Index model card](https://huggingface.co/IndexTeam/Index-Translate-35B-A3B-preview)
and [official client](https://github.com/bilibili/Index-Translate/blob/main/inference/llm/call_api.py).
Requests set `temperature=0`, `stream=false`, and a token budget derived from the
input length: 256 for short text, up to 4096. The default
`disableThinking=true` sends `chat_template_kwargs.enable_thinking=false`.
Administrators may disable that extension for compatible services that reject it;
there is no automatic parameter downgrade or repeated generation.

Input is at most 8 KiB, HTTP
request JSON at most 16 KiB, and buffered response data at most 256 KiB. The
whole completion timeout defaults to 60 seconds and is adjustable from 1 to 60.
Request cancellation and application shutdown cancel the upstream request.

The shared outbound policy restricts requests to the administrator's exact
configured origin, including an intentional private or reserved address. Unusable
addresses remain rejected. DNS is resolved and validated once per connection,
then the transport dials that numeric address. All redirects are refused. TLS
certificate verification remains enabled. The Other proxy scope is used, but a
completion body cannot be replayed by proxy failover; a failure never triggers
another generation automatically.

Only one string completion with `finish_reason=stop`, nonempty content and no
thinking blocks is accepted. Errors distinguish empty output, output-limit
truncation, invalid or unfinished responses, authentication, rate limits,
rejected parameters, redirects, destination policy, size limits, timeouts,
cancellation and generic upstream failures. Public errors contain fixed codes
and messages, never upstream bodies, credentials, private endpoints or transport
error details. Upstream 401 becomes HTTP 422 so it does not invalidate the user's
Kikoto session. Trial failures never include successful translation text; public
content failures retain the verified original text.

## Verification

HTTP and restart tests use production routes, real sessions and migrated SQLite:

The real numbered chain currently ends at 100 on this branch; 097 through 100 are
committed and `compat/099_dev.sql` and `compat/100_dev.sql` are generated.
Local Go tests and `cmd/translation-bench` still use
`internal/migrationfixture`, which copies the packaged catalog and injects no-op
placeholders for merged migrations in memory or a temporary directory. Those
files are test fixtures only and are not committed. Regenerate a development
compat baseline with
`cd backend && go run ./cmd/schema-baseline -migrations . -version-file ../../VERSION`;
do not hand-write a `compat/NNN_dev.sql` while the chain has a gap.

```sh
cd backend
go test ./internal/httpapi -run '^Test(ContentTranslation|SourceFieldTranslation|Translation(AdminConfiguration|Configuration|Trial|Retention))' -count=1 -v
```

The opt-in Playwright test starts a disposable real backend process and an
external model stub. Build with `make backend-build`, install the existing
Playwright browser with `make frontend-playwright-install PLAYWRIGHT_INSTALL_ARGS=chromium`,
and run a Vite instance on a dedicated port with `VITE_API_BASE_URL` pointing at
the test backend (default `http://127.0.0.1:17611`). Set `PLAYWRIGHT_BASE_URL` to
that Vite instance and `KIKOTO_E2E_BACKEND` to the absolute built binary path;
`KIKOTO_E2E_API_URL` can override the backend address. Select
`translation-settings.spec.ts --project=desktop-chromium --workers=1` through
`make frontend-e2e E2E_ARGS='...'`. On Windows, install dependencies before
starting Vite and use Make's `-o frontend-install -o frontend-playwright-install`
for the test invocation to avoid replacing a loaded native dependency. The test
copies a Windows executable into its private temporary directory, shuts down
the owned process and model stub, then removes that directory. Screenshots,
traces and video are disabled; assertions cover controls, text and real API state.

For the content workflow, run `go run ./tests/content-translation-fixture` from
`backend`, then Vite with `VITE_API_BASE_URL=http://127.0.0.1:17612` on port 3112.
Run `make -o frontend-playwright-install frontend-e2e
E2E_ARGS='content-translation.spec.ts --project=desktop-chromium --workers=1'`
with `PLAYWRIGHT_BASE_URL=http://127.0.0.1:3112` and
`KIKOTO_CONTENT_TRANSLATION_URL=http://127.0.0.1:17612`. The fixture uses production
routes, real sessions and migrated disposable SQLite, replacing only the external
model. `POST /__fixture/stop` closes it and removes its owned temporary directory.
The three tests disable screenshots, traces and video, and verify text,
preferences, cancellation, pagination, original/retry controls and dynamic files.
`traditional-translation.spec.ts` uses the same fixture for two additional
nonvisual script-switching, name-preservation and failure/retry scenarios. Override
`CONTENT_TRANSLATION_FIXTURE_ADDR`, `PLAYWRIGHT_BASE_URL` and
`KIKOTO_CONTENT_TRANSLATION_URL` together when choosing dedicated test ports.

`TestTranslationLiveConfiguredService` is skipped by normal tests. For a finite
dedicated HTTPS sample, inject `KIKOTO_TRANSLATION_LIVE_CONFIG` as process-only
JSON containing `baseUrl`, `apiKey` and `model`. Do not put those values in
arguments, source, logs or tracked files. The test saves them through the real
API into a disposable private test database and issues one short Japanese to
Simplified Chinese request; cleanup closes and removes the test database.
`TestContentTranslationTraditionalLiveConfiguredService` uses the same opt-in
configuration for two neutral Japanese/Chinese public titles. It requests
Traditional, Simplified and Traditional again through authenticated content routes,
checks one stored generation and two cache hits per title, and logs only the
synthetic text, configured model and timing. It is skipped in ordinary tests.
