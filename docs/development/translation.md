# Translation service

`internal/translation` owns server-only configuration and the bounded completion
client. `httpapi/translation_settings.go` and `translation_trial.go` compose
permissions, persistence, outbound routing and HTTP error responses. The React
`features/translation` entry owns the administrator form and its typed API.

## API and persistence

All three routes require `sources:write`:

- `GET /api/settings/translation` returns `baseUrl`, `model`, `hasApiKey`,
  `apiKeyMask`, `timeoutSeconds` and `disableThinking`.
- `PATCH /api/settings/translation` accepts optional `baseUrl`, `model`, `apiKey`,
  `timeoutSeconds` and `disableThinking`. An omitted key is preserved and an
  empty string clears it. The response uses the same redacted projection as GET.
- `POST /api/settings/translation/test` accepts `text` and `targetLanguage`
  (`zh-Hans`, `en`, `ja` or `ko`). It returns complete plain text, the configured
  model name, template version and elapsed milliseconds.

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

The trial endpoint allows one active request per application, with no waiting
queue. Another trial returns `translation_busy`. Input is at most 8 KiB, HTTP
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
Kikoto session. Failures never include successful translation text.

## Verification

HTTP and restart tests use production routes, real sessions and migrated SQLite:

```sh
cd backend
go test ./internal/httpapi -run '^TestTranslation(AdminConfiguration|Configuration|Trial)' -count=1 -v
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

`TestTranslationLiveConfiguredService` is skipped by normal tests. For a finite
dedicated HTTPS sample, inject `KIKOTO_TRANSLATION_LIVE_CONFIG` as process-only
JSON containing `baseUrl`, `apiKey` and `model`. Do not put those values in
arguments, source, logs or tracked files. The test saves them through the real
API into a disposable private test database and issues one short Japanese to
Simplified Chinese request; cleanup closes and removes the test database.
