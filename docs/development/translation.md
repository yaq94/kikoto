# Translation service

`internal/translation` owns server-only configuration, the bounded completion
client, shared request queue, name protection and rebuildable disk cache.
`httpapi/content_translation*.go` resolves readable public fields;
`translation_runtime.go` composes those fields with the model and cache. The React
`features/translation` entry owns the administrator form and its typed API, while
`components/content-translation` renders translated public text.

## API and persistence

Administrator routes require `sources:write`:

- `GET /api/settings/translation` returns `baseUrl`, `model`, `hasApiKey`,
  `apiKeyMask`, `timeoutSeconds`, `disableThinking`, `concurrency`, `queueLimit`,
  `cacheBytes` and `minFreeBytes`.
- `PATCH /api/settings/translation` accepts optional `baseUrl`, `model`, `apiKey`,
  `timeoutSeconds`, `disableThinking` and the four resource limits above. An omitted key is preserved and an
  empty string clears it. The response uses the same redacted projection as GET.
- `POST /api/settings/translation/test` accepts `text` and `targetLanguage`
  (`zh-Hans`, `en`, `ja` or `ko`). It returns complete plain text, the configured
  model name, template version and elapsed milliseconds.
- `GET /api/settings/translation/runtime` returns `active`, `queued`, `readers`,
  `cacheEntries`, charged `cacheBytes` and `cacheStatus`.

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
remote `sourceId` plus `code`), `field` (`title`, `description`, `tag`, `directory`),
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
take precedence over generation. Existing Traditional Chinese fields are usable;
missing Traditional fields return a failed result without model generation.

Responses contain `text`, `original`, `language`, `status` (`original`, `existing`,
`translated`, `failed`), optional error `code`, and optional `cacheStatus`.
`origin` resolves original text without calling the model. Circle and voice actor
names are replaced with placeholders before generation; missing, duplicated or
modified placeholders reject the output. Personal notes and personal tags have
no automatic translation path.

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
| `concurrency` | 1 | 1–8 |
| `queueLimit` | 32 | 0–256 |
| `timeoutSeconds` | 60 | 1–60 |
| `cacheBytes` | 256 MiB | 4 KiB–4 GiB |
| `minFreeBytes` | 2 GiB | 0–1 TiB |

Content requests share a FIFO with administrator trials. Identical in-flight
content requests share one job; cancelling the last reader cancels it. The queue
holds no database connection while waiting. At most 512 readers are admitted;
full queues reject with a readable failure. The timeout includes waiting. Trials
require an immediately free lane and otherwise return `translation_busy`.
Configuration changes adjust the queue and cache; in-flight publication reloads
current storage limits. Lower concurrency drains existing calls before admitting
more, and a lower queue limit rejects excess waiting jobs.

Only successful, name-validated results are stored under `CacheRoot/translations`.
Cache identity includes original and protected text, target language, service URL,
model, thinking option, template version and protection version. File-backed
entries survive application restart. Least-recently-used eviction charges each
JSON file size rounded up to 4 KiB, with an independent 8192-entry ceiling. This
charge is a portable budget estimate, not exact allocation on every filesystem.
Unknown files, links, the business database, personal records and source identity
are outside cache eviction. Owned incomplete atomic-write files are discarded.

Free space is read from the nearest existing parent even before the cache
directory exists. Low space pauses new writes (`paused_low_disk`) while keeping
existing entries readable; raising free space or lowering the reserve permits
later successful writes. Failed generations are never cached. Storage failures
return an uncached successful translation with `unavailable`; an oversized cache
entry reports `entry_too_large`. Cache hits report `hit`, and writes `stored`.

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
Requests set `temperature=0`, `max_tokens=1024`, and `stream=false`. The default
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

```sh
cd backend
go test ./internal/httpapi -run '^Test(ContentTranslation|Translation(AdminConfiguration|Configuration|Trial))' -count=1 -v
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

`TestTranslationLiveConfiguredService` is skipped by normal tests. For a finite
dedicated HTTPS sample, inject `KIKOTO_TRANSLATION_LIVE_CONFIG` as process-only
JSON containing `baseUrl`, `apiKey` and `model`. Do not put those values in
arguments, source, logs or tracked files. The test saves them through the real
API into a disposable private test database and issues one short Japanese to
Simplified Chinese request; cleanup closes and removes the test database.
