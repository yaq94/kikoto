import { randomUUID } from "node:crypto";

const REQUEST_TIMEOUT_MS = 15_000;
const TARGET_LANGUAGE = "zh-cn";
const CATEGORIES = [
  "setup",
  "browse",
  "search",
  "media-range",
  "review-write",
  "review-read",
  "translation-prefill",
  "cleanup",
];

function usage(message) {
  if (message) console.error(`mixed-load: ${message}`);
  console.error(`Usage:
  KIKOTO_LOAD_BASE_URL=https://kikoto.example \\
  KIKOTO_LOAD_COOKIE='session=...' \\
  node scripts/mixed-load.mjs

Required environment:
  KIKOTO_LOAD_BASE_URL   Origin of the running Kikoto instance.
  KIKOTO_LOAD_COOKIE     Cookie header value for an authenticated session.

Optional environment:
  KIKOTO_LOAD_SECONDS     Load duration in seconds (default: 60).
  KIKOTO_LOAD_CONCURRENCY Number of concurrent workers (default: 8).
  KIKOTO_LOAD_WORK_ID    Work id to exercise; otherwise a playable work is discovered.

The script needs a library with at least one work and one media stream. It
writes and restores a personal rating/note, so the session needs the same
permissions as the production smoke test.

Exit codes: 0 all requests matched, 1 unexpected status/timeout/validation
failure, 2 missing or invalid environment, 130 interrupted.`);
}

const rawBaseURL = (process.env.KIKOTO_LOAD_BASE_URL || "").trim();
const rawCookie = (process.env.KIKOTO_LOAD_COOKIE || "").trim();
if (!rawBaseURL || !rawCookie) {
  usage("KIKOTO_LOAD_BASE_URL and KIKOTO_LOAD_COOKIE are required.");
  process.exit(2);
}

let parsedBaseURL;
try {
  parsedBaseURL = new URL(rawBaseURL);
} catch {
  usage("KIKOTO_LOAD_BASE_URL must be an absolute http(s) origin.");
  process.exit(2);
}
if (
  !["http:", "https:"].includes(parsedBaseURL.protocol) ||
  parsedBaseURL.username ||
  parsedBaseURL.password ||
  parsedBaseURL.pathname !== "/" ||
  parsedBaseURL.search ||
  parsedBaseURL.hash
) {
  usage("KIKOTO_LOAD_BASE_URL must be an http(s) origin without a path.");
  process.exit(2);
}
const baseURL = parsedBaseURL.origin;

const cookie = rawCookie.replace(/^Cookie:\s*/iu, "");
if (!cookie || /[\r\n]/u.test(cookie)) {
  usage("KIKOTO_LOAD_COOKIE must be one Cookie header value.");
  process.exit(2);
}

const rawSeconds = (process.env.KIKOTO_LOAD_SECONDS || "").trim();
const seconds = rawSeconds === "" ? 60 : Number(rawSeconds);
if (!Number.isFinite(seconds) || seconds <= 0) {
  usage("KIKOTO_LOAD_SECONDS must be a positive number.");
  process.exit(2);
}

const rawConcurrency = (process.env.KIKOTO_LOAD_CONCURRENCY || "").trim();
const concurrency = rawConcurrency === "" ? 8 : Number(rawConcurrency);
if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 128) {
  usage("KIKOTO_LOAD_CONCURRENCY must be an integer from 1 to 128.");
  process.exit(2);
}

const rawWorkID = (process.env.KIKOTO_LOAD_WORK_ID || "").trim();
let requestedWorkID = null;
if (rawWorkID !== "") {
  if (!/^\d+$/u.test(rawWorkID) || !Number.isSafeInteger(Number(rawWorkID))) {
    usage("KIKOTO_LOAD_WORK_ID must be a positive integer when set.");
    process.exit(2);
  }
  requestedWorkID = Number(rawWorkID);
  if (requestedWorkID <= 0) {
    usage("KIKOTO_LOAD_WORK_ID must be a positive integer when set.");
    process.exit(2);
  }
}

const metrics = Object.fromEntries(
  CATEGORIES.map((category) => [
    category,
    {
      count: 0,
      durations: [],
      statuses: {},
      errors: 0,
      timeouts: 0,
      unexpected: 0,
      validationErrors: 0,
    },
  ]),
);
const unexpectedStatuses = [];
const timeouts = [];
const requestErrors = [];
const validationErrors = [];
const setupErrors = [];
let interrupted = false;

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => {
    interrupted = true;
  });
}

function isTimeout(error) {
  return error?.name === "TimeoutError" || error?.name === "AbortError";
}

function recordDuration(metric, started) {
  metric.count += 1;
  metric.durations.push(performance.now() - started);
}

function recordTimeout(category, label) {
  metrics[category].timeouts += 1;
  timeouts.push({ category, label, timeoutMs: REQUEST_TIMEOUT_MS });
}

function recordRequestError(category, label, error) {
  metrics[category].errors += 1;
  requestErrors.push({
    category,
    label,
    name: error?.name || "Error",
  });
}

function recordValidationError(category, message) {
  metrics[category].validationErrors += 1;
  validationErrors.push({ category, message });
}

async function call(
  category,
  label,
  path,
  {
    method = "GET",
    headers: extraHeaders = {},
    body,
    expected = [200],
    read = "json",
  } = {},
) {
  const metric = metrics[category];
  const started = performance.now();
  let response;
  try {
    response = await fetch(new URL(path, baseURL), {
      method,
      redirect: "error",
      headers: { Cookie: cookie, ...extraHeaders },
      body,
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (error) {
    recordDuration(metric, started);
    if (isTimeout(error)) recordTimeout(category, label);
    else recordRequestError(category, label, error);
    return { ok: false, status: null, body: null };
  }

  const expectedStatuses = Array.isArray(expected) ? expected : [expected];
  let payload = null;
  let bodyError = null;
  try {
    if (read === "none") {
      await response.body?.cancel();
    } else if (read === "range") {
      const reader = response.body?.getReader();
      if (reader) {
        const { value } = await reader.read();
        payload = value?.byteLength || 0;
        await reader.cancel();
      } else {
        payload = 0;
      }
    } else {
      payload = await response.json();
    }
  } catch (error) {
    bodyError = error;
    if (isTimeout(error)) recordTimeout(category, label);
    else recordRequestError(category, label, error);
  }

  recordDuration(metric, started);
  const statusKey = String(response.status);
  metric.statuses[statusKey] = (metric.statuses[statusKey] || 0) + 1;
  const statusOk = expectedStatuses.includes(response.status);
  if (!statusOk) {
    metric.unexpected += 1;
    unexpectedStatuses.push({
      category,
      label,
      status: response.status,
      expected: expectedStatuses,
    });
  }
  return {
    ok: statusOk && bodyError === null,
    status: response.status,
    body: payload,
  };
}

// Discovery probes intentionally do not count as load requests. A work without
// a playable stream is skipped; the selected work is then exercised by every
// worker and its real request results are recorded above.
async function probeJSON(path) {
  try {
    const response = await fetch(new URL(path, baseURL), {
      redirect: "error",
      headers: { Cookie: cookie },
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
    if (response.status !== 200) {
      await response.body?.cancel();
      return null;
    }
    return await response.json();
  } catch {
    return null;
  }
}

function streamPathFromMedia(media) {
  const items = Array.isArray(media?.mediaItems) ? media.mediaItems : [];
  for (const item of items) {
    if (item?.kind !== "audio" && item?.kind !== "video") continue;
    if (!Array.isArray(item?.locations)) continue;
    for (const location of item.locations) {
      if (
        typeof location?.streamUrl !== "string" ||
        location.streamUrl === ""
      ) {
        continue;
      }
      if (
        typeof location.availability === "string" &&
        !["available", "remote"].includes(location.availability)
      ) {
        continue;
      }
      let streamURL;
      try {
        streamURL = new URL(location.streamUrl, baseURL);
      } catch {
        continue;
      }
      if (streamURL.origin !== baseURL) continue;
      streamURL.searchParams.set("forceDirect", "1");
      return `${streamURL.pathname}${streamURL.search}`;
    }
  }
  return null;
}

async function selectWork(works) {
  const candidates = [];
  if (requestedWorkID !== null) {
    const detail = await probeJSON(
      `/api/works/${requestedWorkID}?includeMedia=false`,
    );
    if (!detail) {
      setupErrors.push("The requested KIKOTO_LOAD_WORK_ID is not readable.");
      return null;
    }
    candidates.push({
      id: requestedWorkID,
      title: detail.title,
      primaryCode: detail.primaryCode,
    });
  } else {
    for (const entry of works.slice(0, 20)) {
      if (!Number.isSafeInteger(entry?.id) || entry.id <= 0) continue;
      candidates.push({
        id: entry.id,
        title: entry.title,
        primaryCode: entry.primaryCode,
      });
    }
  }
  if (candidates.length === 0) {
    setupErrors.push("The library returned no work to exercise.");
    return null;
  }

  for (const candidate of candidates) {
    const media = await probeJSON(`/api/works/${candidate.id}/media`);
    if (!media) continue;
    const streamPath = streamPathFromMedia(media);
    if (!streamPath) continue;
    const detail = await probeJSON(
      `/api/works/${candidate.id}?includeMedia=false`,
    );
    if (!detail) continue;
    return {
      id: candidate.id,
      title: detail.title || candidate.title || "",
      primaryCode: detail.primaryCode || candidate.primaryCode || "",
      streamPath,
      originalRating:
        typeof detail.personalRating === "number"
          ? detail.personalRating
          : null,
      originalNote:
        typeof detail.personalNote === "string" ? detail.personalNote : "",
    };
  }
  setupErrors.push(
    "No work with a playable media stream was found; set KIKOTO_LOAD_WORK_ID to a work that has media.",
  );
  return null;
}

function percentile(sorted, fraction) {
  if (sorted.length === 0) return null;
  const index = Math.max(0, Math.ceil(sorted.length * fraction) - 1);
  return sorted[index];
}

function round(value) {
  return value === null ? null : Math.round(value * 100) / 100;
}

const reviewNote = `mixed-load-${randomUUID()}`;
const loadStartedAt = Date.now();
let work = null;
let originalReview = null;
let searchQuery = "";

try {
  const worksResult = await call(
    "setup",
    "GET /api/works",
    "/api/works?page=1&pageSize=24",
    { expected: [200] },
  );
  if (!worksResult.ok) {
    setupErrors.push("The initial library read failed.");
  } else if (!Array.isArray(worksResult.body?.works)) {
    setupErrors.push("The initial library response has no works array.");
  } else {
    work = await selectWork(worksResult.body.works);
  }

  if (work) {
    searchQuery = String(work.title || work.primaryCode || "").trim();
    if (!searchQuery) {
      setupErrors.push(
        "The selected work has neither a title nor a primary code.",
      );
    }
    originalReview = {
      rating: work.originalRating,
      note: work.originalNote,
    };
  }

  const prefill = await call(
    "setup",
    "GET /api/settings/translation/prefill",
    "/api/settings/translation/prefill",
    { expected: [200] },
  );
  if (!prefill.ok) {
    setupErrors.push("The translation coverage read failed.");
  } else if (
    typeof prefill.body?.configKey !== "string" ||
    typeof prefill.body?.total !== "number"
  ) {
    setupErrors.push(
      "The translation coverage response lacks configKey or total.",
    );
  }

  if (setupErrors.length === 0 && work && !interrupted) {
    const deadline = Date.now() + seconds * 1000;
    const taskOrder = ["browse", "search", "media", "review", "translation"];
    let cursor = 0;

    async function runTask(name) {
      if (name === "browse") {
        const result = await call(
          "browse",
          "GET /api/works?page=1&pageSize=24",
          "/api/works?page=1&pageSize=24",
          { expected: [200] },
        );
        if (result.ok && !Array.isArray(result.body?.works)) {
          recordValidationError(
            "browse",
            "The library response has no works array.",
          );
        }
        return;
      }
      if (name === "search") {
        const path = `/api/works?q=${encodeURIComponent(searchQuery)}&lang=${TARGET_LANGUAGE}&pageSize=24`;
        const result = await call(
          "search",
          "GET /api/works?q=...&lang=zh-cn",
          path,
          { expected: [200] },
        );
        if (result.ok && !Array.isArray(result.body?.works)) {
          recordValidationError(
            "search",
            "The search response has no works array.",
          );
        }
        return;
      }
      if (name === "media") {
        const result = await call(
          "media-range",
          "GET /api/media/{id}/stream",
          work.streamPath,
          {
            headers: { Range: "bytes=0-1023" },
            expected: [206],
            read: "range",
          },
        );
        if (result.ok && !(result.body > 0)) {
          recordValidationError(
            "media-range",
            "The Range response contained no bytes.",
          );
        }
        return;
      }
      if (name === "review") {
        const write = await call(
          "review-write",
          "PATCH /api/works/{id}/user-state",
          `/api/works/${work.id}/user-state`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ rating: 5, note: reviewNote }),
            expected: [200],
          },
        );
        if (
          write.ok &&
          (write.body?.personalRating !== 5 ||
            write.body?.personalNote !== reviewNote)
        ) {
          recordValidationError(
            "review-write",
            "The personal review write did not echo the submitted values.",
          );
        }
        const read = await call(
          "review-read",
          "GET /api/works/{id}",
          `/api/works/${work.id}?includeMedia=false`,
          { expected: [200] },
        );
        if (
          read.ok &&
          (read.body?.personalRating !== 5 ||
            read.body?.personalNote !== reviewNote)
        ) {
          recordValidationError(
            "review-read",
            "The personal review read did not match the write.",
          );
        }
        return;
      }
      const result = await call(
        "translation-prefill",
        "GET /api/settings/translation/prefill",
        "/api/settings/translation/prefill",
        { expected: [200] },
      );
      if (
        result.ok &&
        (typeof result.body?.configKey !== "string" ||
          typeof result.body?.total !== "number")
      ) {
        recordValidationError(
          "translation-prefill",
          "The translation coverage response lacks configKey or total.",
        );
      }
    }

    const workers = Array.from({ length: concurrency }, async () => {
      while (!interrupted && Date.now() < deadline) {
        const task = taskOrder[cursor % taskOrder.length];
        cursor += 1;
        await runTask(task);
      }
    });
    await Promise.all(workers);
  }
} catch (error) {
  requestErrors.push({
    category: "runner",
    label: "mixed-load",
    name: error?.name || "Error",
  });
} finally {
  if (work && originalReview) {
    const restored = await call(
      "cleanup",
      "PATCH /api/works/{id}/user-state",
      `/api/works/${work.id}/user-state`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          rating: originalReview.rating,
          note: originalReview.note,
        }),
        expected: [200],
      },
    );
    if (!restored.ok) {
      recordValidationError(
        "cleanup",
        "The original personal rating and note could not be restored.",
      );
    }
  }
}

const categories = {};
let totalRequests = 0;
for (const category of CATEGORIES) {
  const metric = metrics[category];
  totalRequests += metric.count;
  const sorted = [...metric.durations].sort((left, right) => left - right);
  categories[category] = {
    count: metric.count,
    p50Ms: round(percentile(sorted, 0.5)),
    p95Ms: round(percentile(sorted, 0.95)),
    statuses: metric.statuses,
    unexpectedStatuses: metric.unexpected,
    errors: metric.errors,
    timeouts: metric.timeouts,
    validationErrors: metric.validationErrors,
  };
}

const ok =
  unexpectedStatuses.length === 0 &&
  timeouts.length === 0 &&
  requestErrors.length === 0 &&
  validationErrors.length === 0 &&
  setupErrors.length === 0;
const summary = {
  target: baseURL,
  configuredSeconds: seconds,
  durationSeconds: round((Date.now() - loadStartedAt) / 1000),
  concurrency,
  requests: totalRequests,
  categories,
  unexpectedStatuses,
  timeouts,
  errors: requestErrors,
  validationErrors,
  setupErrors,
  ok,
};
console.log(JSON.stringify(summary, null, 2));

if (interrupted) process.exitCode = 130;
else if (!ok) process.exitCode = 1;
