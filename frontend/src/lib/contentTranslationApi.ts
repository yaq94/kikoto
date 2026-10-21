import { ApiError, apiTransport } from "@/lib/apiTransport";

export type ContentTranslationResource = {
  workId?: number;
  sourceId?: number;
  code?: string;
  catalogEntryId?: number;
  seriesId?: number;
};
export type ContentTranslationRequest = ContentTranslationResource & {
  field: "title" | "description" | "tag" | "directory";
  text: string;
  language: string;
  mediaId?: number;
  path?: string;
};
/** `original` is the single-item status for `language=origin`; the batch route also reports `native`. */
export type ContentTranslationStatus = "original" | "existing" | "translated" | "native" | "failed";
export type ContentTranslationResult = {
  text: string;
  original: string;
  language: string;
  status: ContentTranslationStatus;
  code?: string;
  cacheStatus?: string;
  /**
   * Model and prompt template that produced this text. The cache stores it so
   * a row from an older model or template is not shown again.
   */
  version?: string;
};

/** A batch item. `key` is echoed by the server so responses may arrive in any order. */
export type ContentTranslationInput = ContentTranslationRequest & { key?: string };

export type ContentTranslationBatchResponse = {
  items: Array<ContentTranslationResult & { key: string }>;
};

/** The batch route rejected the request, or its answer could not be used as a whole. */
export class ContentTranslationBatchError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;

  constructor(message: string, code: string, status = 0, retryable = false) {
    super(message);
    this.name = "ContentTranslationBatchError";
    this.status = status;
    this.code = code;
    this.retryable = retryable;
  }
}

/** `POST /api/content-translations/batch` accepts at most 200 items. */
export const CONTENT_TRANSLATION_BATCH_LIMIT = 200;

/** Identity of a translation request inside this browser session. */
export function contentTranslationRequestKey(input: ContentTranslationInput): string {
  return JSON.stringify([
    input.workId ?? 0,
    input.sourceId ?? 0,
    input.code ?? "",
    input.catalogEntryId ?? 0,
    input.seriesId ?? 0,
    input.field,
    input.text,
    input.language,
    input.mediaId ?? 0,
    input.path ?? "",
  ]);
}

const inFlightKeys = new Set<string>();

/** True while a request for this key has not settled. */
export function isContentTranslationInFlight(key: string): boolean {
  return inFlightKeys.has(key) || coalesced.has(key) || coalescedInflight.has(key);
}

function itemBody(input: ContentTranslationInput, key: string) {
  return {
    key,
    ...(input.seriesId !== undefined ? { seriesId: input.seriesId } : {}),
    ...(input.catalogEntryId !== undefined ? { catalogEntryId: input.catalogEntryId } : {}),
    ...(input.workId !== undefined ? { workId: input.workId } : {}),
    ...(input.sourceId !== undefined ? { sourceId: input.sourceId } : {}),
    ...(input.code ? { code: input.code } : {}),
    field: input.field,
    text: input.text,
    language: input.language,
    ...(input.mediaId !== undefined ? { mediaId: input.mediaId } : {}),
    ...(input.path ? { path: input.path } : {}),
  };
}

function toBatchError(error: unknown): never {
  if (error instanceof DOMException && error.name === "AbortError") throw error;
  if (error instanceof ContentTranslationBatchError) throw error;
  if (error instanceof ApiError) {
    throw new ContentTranslationBatchError(
      error.message,
      error.code || (error.status === 400 ? "translation_batch_rejected" : "translation_batch_failed"),
      error.status,
      error.retryable,
    );
  }
  throw new ContentTranslationBatchError(
    error instanceof Error ? error.message : "translation batch failed",
    "translation_batch_failed",
  );
}

async function sendBatch(
  language: string,
  chunk: Array<{ input: ContentTranslationInput; key: string }>,
  signal?: AbortSignal,
): Promise<ContentTranslationBatchResponse> {
  try {
    return await apiTransport.sendJSONBody<ContentTranslationBatchResponse>(
      "POST",
      "/api/content-translations/batch",
      { language, items: chunk.map(({ input, key }) => itemBody(input, key)) },
      { signal },
    );
  } catch (error) {
    toBatchError(error);
  }
}

export const contentTranslationApi = {
  translate: (input: ContentTranslationRequest, signal: AbortSignal) =>
    apiTransport.sendJSONBody<ContentTranslationResult>("POST", "/api/content-translations", input, { signal }),

  /**
   * Reads the version the server currently uses. The provider calls this once
   * per session so a cached row is checked before it paints, even when no
   * translation request of this session has run yet.
   */
  version: (signal?: AbortSignal) =>
    apiTransport.getJSON<{ version: string }>("/api/content-translations/version", signal),

  /**
   * Translates inputs in server-sized batches and returns results by request
   * key (`input.key` when supplied, otherwise `contentTranslationRequestKey`).
   * Inputs of different languages go out as separate requests. A request over
   * 200 items is split rather than rejected or truncated.
   */
  async translateBatch(
    inputs: ContentTranslationInput[],
    signal?: AbortSignal,
  ): Promise<Map<string, ContentTranslationResult>> {
    const results = new Map<string, ContentTranslationResult>();
    if (inputs.length === 0) return results;
    const byLanguage = new Map<string, Array<{ input: ContentTranslationInput; key: string }>>();
    for (const input of inputs) {
      const key = input.key ?? contentTranslationRequestKey(input);
      const group = byLanguage.get(input.language);
      if (group) group.push({ input, key });
      else byLanguage.set(input.language, [{ input, key }]);
    }
    const requests: Array<Promise<void>> = [];
    for (const [language, items] of byLanguage) {
      for (let offset = 0; offset < items.length; offset += CONTENT_TRANSLATION_BATCH_LIMIT) {
        const chunk = items.slice(offset, offset + CONTENT_TRANSLATION_BATCH_LIMIT);
        const keys = chunk.map((item) => item.key);
        for (const key of keys) inFlightKeys.add(key);
        requests.push(
          sendBatch(language, chunk, signal)
            .then((response) => {
              if (!Array.isArray(response?.items)) {
                throw new ContentTranslationBatchError(
                  "translation batch response has no items",
                  "translation_batch_incomplete",
                );
              }
              const byKey = new Map(response.items.map((item) => [item.key, item]));
              for (const key of keys) {
                const item = byKey.get(key);
                if (!item) {
                  throw new ContentTranslationBatchError(
                    `translation batch response is missing ${key}`,
                    "translation_batch_incomplete",
                  );
                }
                results.set(key, item);
              }
            })
            .finally(() => {
              for (const key of keys) inFlightKeys.delete(key);
            }),
        );
      }
    }
    await Promise.all(requests);
    return results;
  },
};

type CoalescedWaiter = {
  resolve: (result: ContentTranslationResult) => void;
  reject: (error: unknown) => void;
  cleanup: () => void;
};

type CoalescedEntry = {
  input: ContentTranslationInput;
  waiters: Set<CoalescedWaiter>;
  // Called when the last interested reader leaves. The flush aborts the network
  // request only after every entry of that request has lost its readers, so a
  // language switch or navigation stops the stale model call without cutting
  // off a field another visible component still needs.
  empty?: () => void;
};

// A page renders many fields in one frame. Queueing for one short window turns
// them into a single batch instead of one request per card.
const COALESCE_DELAY_MS = 16;
const coalesced = new Map<string, CoalescedEntry>();
const coalescedInflight = new Map<string, Promise<ContentTranslationResult>>();
let coalesceTimer: ReturnType<typeof setTimeout> | null = null;

function abortError() {
  return new DOMException("Aborted", "AbortError");
}

function scheduleCoalescedFlush() {
  if (coalesceTimer !== null) return;
  coalesceTimer = setTimeout(() => {
    coalesceTimer = null;
    void flushCoalesced();
  }, COALESCE_DELAY_MS);
}

async function flushCoalesced() {
  const entries = [...coalesced.entries()].slice(0, CONTENT_TRANSLATION_BATCH_LIMIT);
  for (const [key] of entries) coalesced.delete(key);
  if (entries.length === 0) return;

  const deferred = new Map<
    string,
    {
      promise: Promise<ContentTranslationResult>;
      resolve: (value: ContentTranslationResult) => void;
      reject: (reason: unknown) => void;
    }
  >();
  for (const [key] of entries) {
    let resolve!: (value: ContentTranslationResult) => void;
    let reject!: (reason: unknown) => void;
    const promise = new Promise<ContentTranslationResult>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    // The promise is a join point for later callers; a caller-less failure
    // must not surface as an unhandled rejection.
    void promise.catch(() => undefined);
    deferred.set(key, { promise, resolve, reject });
    coalescedInflight.set(key, promise);
  }

  const fail = (error: unknown) => {
    for (const [key, entry] of entries) {
      coalescedInflight.delete(key);
      deferred.get(key)?.reject(error);
      for (const waiter of entry.waiters) {
        waiter.cleanup();
        waiter.reject(error);
      }
    }
  };

  const controller = new AbortController();
  for (const [, entry] of entries) {
    entry.empty = () => {
      if (entries.every(([, item]) => item.waiters.size === 0)) controller.abort();
    };
  }
  try {
    const results = await contentTranslationApi.translateBatch(
      entries.map(([key, entry]) => ({ ...entry.input, key })),
      controller.signal,
    );
    for (const [key, entry] of entries) {
      const result = results.get(key);
      if (!result) {
        const missing = new ContentTranslationBatchError(
          `translation batch response is missing ${key}`,
          "translation_batch_incomplete",
        );
        coalescedInflight.delete(key);
        deferred.get(key)?.reject(missing);
        for (const waiter of entry.waiters) {
          waiter.cleanup();
          waiter.reject(missing);
        }
        continue;
      }
      coalescedInflight.delete(key);
      deferred.get(key)?.resolve(result);
      for (const waiter of entry.waiters) {
        waiter.cleanup();
        waiter.resolve(result);
      }
    }
  } catch (error) {
    fail(error);
  }
  if (coalesced.size > 0) scheduleCoalescedFlush();
}

/** Batch-coalesced translation used by visible fields. */
export function translateCoalesced(
  input: ContentTranslationInput,
  signal?: AbortSignal,
): Promise<ContentTranslationResult> {
  const key = contentTranslationRequestKey(input);
  if (signal?.aborted) return Promise.reject(abortError());
  const inflight = coalescedInflight.get(key);
  if (inflight) {
    return new Promise<ContentTranslationResult>((resolve, reject) => {
      const onAbort = () => reject(abortError());
      signal?.addEventListener("abort", onAbort, { once: true });
      inflight.then(
        (result) => {
          signal?.removeEventListener("abort", onAbort);
          resolve(result);
        },
        (error) => {
          signal?.removeEventListener("abort", onAbort);
          reject(error);
        },
      );
    });
  }
  let entry = coalesced.get(key);
  if (!entry) {
    entry = { input, waiters: new Set() };
    coalesced.set(key, entry);
  }
  const target = entry;
  scheduleCoalescedFlush();
  return new Promise<ContentTranslationResult>((resolve, reject) => {
    const onAbort = () => {
      target.waiters.delete(waiter);
      if (target.waiters.size === 0) {
        coalesced.delete(key);
        target.empty?.();
      }
      reject(abortError());
    };
    const waiter: CoalescedWaiter = {
      resolve,
      reject,
      cleanup: () => signal?.removeEventListener("abort", onAbort),
    };
    target.waiters.add(waiter);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
