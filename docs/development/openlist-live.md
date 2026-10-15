# OpenList single-file verification

`TestOpenListLiveRangeSample` uses a disposable production-mode application,
administrator session and migrated SQLite database. It accepts one explicitly
authorized anonymous audio file through `KIKOTO_OPENLIST_LIVE_URL` and
`KIKOTO_OPENLIST_LIVE_FILE`. Missing values skip the test; a skip is not evidence
of source access or playback.

The sample reads file metadata, then requests `bytes=0-31` and
`bytes=1024-1055` through the application's catalog media endpoint. Each request
must return 206, the requested Content-Range and exactly 32 bytes. The test
consumes at most 33 bytes per response and rejects a full 200 response. Metadata
success alone cannot pass. This check does not prove audio decoding or whole-site
catalog coverage.

## Explicit proxy configuration

OpenList metadata and media use the remote-source proxy scope. Administrators
can choose an OpenList override in **Settings → Proxy**. The Settings API accepts
`proxy.routes.sources[sourceId]` for OpenList and both Kikoeru source types;
local and unknown source ids are excluded. An explicit `direct` override takes
precedence over the global remote route; an absent override inherits it.

`KIKOTO_OPENLIST_LIVE_PROXY_CONFIG` optionally contains the complete `proxy`
object from the Settings PATCH contract. The fixture submits it with its real
administrator session to `PATCH /api/settings` before any source request. Its
isolated OpenList source id is `7`. For example, the following inert payload
selects a source-specific proxy:

```json
{
  "proxies": [
    {"id":"example","kind":"custom","scheme":"http","host":"192.0.2.10","port":8080}
  ],
  "routes": {
    "dlsite":{"enabled":false,"proxyIds":[]},
    "remote":{"enabled":false,"proxyIds":[]},
    "other":{"enabled":false,"proxyIds":[]},
    "sources":{"7":{"mode":"proxy","proxyIds":["example"]}}
  },
  "directFallback": false
}
```

Supply real values through an authorized private runner's process environment;
do not place credentials, source paths, proxy addresses or configuration files
in Git, command arguments or test reports. Optional proxy credentials use the
existing `username` and `password` fields. Settings responses expose
`hasPassword`; omitting `password` on a later save retains it, while an explicit
empty string clears it. The isolated database contains this private configuration
for the lifetime of the test. It is never copied from a production database.

Without the opt-in JSON, the sample uses its empty application proxy settings.
`HTTP_PROXY` and `HTTPS_PROXY` do not select an application source proxy. Invalid
proxy settings fail before contacting the source. This test fixture must never
be pointed at an existing application or database.

## Evidence

Logs record only resolved route type, proxy count, direct-fallback flag, sample
digest, requested ranges, status, byte count and byte digest. Metadata failures
add a fixed error category, numeric HTTP status (0 when no response arrived),
and a boolean derived from `Cf-Mitigated: challenge`. This flag distinguishes an
edge challenge from a generic permission failure without retaining the response
body. Media failures report the application HTTP status and fixed category.
No endpoint, raw URL, file path, credentials, raw error text, response body or
arbitrary upstream header is printed. A proxy route does not establish that an
upstream challenge has been resolved.

From `backend/`, with the authorized environment already supplied:

```sh
go test -mod=readonly ./internal/httpapi -run '^TestOpenListLiveRangeSample$' -count=1 -v -timeout=2m
```

Offline tests `TestOpenListLiveFixture*` exercise the same runner with local
origins and a synthetic CONNECT proxy. They verify both Range requests, safe
challenge and transport errors, invalid configuration and metadata-only failure.
`TestOpenListSourceProxySettingsSurviveRestart` verifies Settings permissions,
filtering, password retention and actual routing across a closed/reopened SQLite
database. These local checks are not LIVE evidence.
