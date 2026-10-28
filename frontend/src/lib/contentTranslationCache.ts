import type { ContentTranslationInput, ContentTranslationResult } from "@/lib/contentTranslationApi";

export const CONTENT_TRANSLATION_DB_NAME = "kikoto-translations";
export const CONTENT_TRANSLATION_STORE_NAME = "entries";
export const CONTENT_TRANSLATION_DB_VERSION = 1;
export const CONTENT_TRANSLATION_MEMORY_LIMIT = 4_000;
export const CONTENT_TRANSLATION_IDB_ENTRY_LIMIT = 100_000;
export const CONTENT_TRANSLATION_IDB_BYTE_LIMIT = 50 * 1024 * 1024;

export type CachedTranslationStatus = "translated" | "existing" | "native";

/** The value stored in IndexedDB and the memory cache (without its key). */
export type CachedTranslation = {
  text: string;
  original: string;
  status: CachedTranslationStatus;
  storedAt: number;
  lastUsedAt: number;
  /**
   * Model and prompt template that produced this text, when the server
   * reported one. The key already separates the original text and the target
   * language; this field separates the model and the template.
   */
  version?: string;
};

/** The row stored in IndexedDB. `key` is the store's keyPath. */
export type CachedTranslationRecord = CachedTranslation & { key: string };

export type ContentTranslationCache = {
  /**
   * Reads a successful row. When `expectedVersion` is known, a row produced by
   * another model or template — or a row with no version at all — is deleted
   * and reported as a miss. Without an expected version the previous behaviour
   * (any row for this key) is kept.
   */
  get(scope: string, input: ContentTranslationInput, expectedVersion?: string): Promise<CachedTranslation | null>;
  /** Whether a successful result is already known, without refreshing its use time. */
  has(scope: string, input: ContentTranslationInput, expectedVersion?: string): Promise<boolean>;
  /** Stores a successful result; failed results are ignored. */
  put(scope: string, input: ContentTranslationInput, result: ContentTranslationResult): Promise<boolean>;
  /** Evicts the least recently used rows until both IndexedDB limits hold. */
  prune(): Promise<void>;
  clearMemory(): void;
  memorySize(): number;
  /** False once IndexedDB was found unusable; the cache then serves memory only. */
  persistenceAvailable(): boolean;
};

export type ContentTranslationCacheOptions = {
  indexedDB?: IDBFactory | null;
  subtle?: SubtleCrypto | null;
  now?: () => number;
  memoryLimit?: number;
  idbEntryLimit?: number;
  idbByteLimit?: number;
  pruneDelayMs?: number;
  hashText?: (text: string) => Promise<string>;
};

export type ContentTranslationCacheEntityKind = "entry" | "work" | "series";

export function isCacheableTranslationStatus(status: string): status is CachedTranslationStatus {
  return status === "translated" || status === "existing" || status === "native";
}

/**
 * The entity component of the cache key, ordered exactly like the server's
 * `translationEntity`: series, then catalog entry, then work.
 */
export function contentTranslationEntity(input: ContentTranslationInput): {
  kind: ContentTranslationCacheEntityKind;
  id: number;
} | null {
  if (input.seriesId !== undefined) return { kind: "series", id: input.seriesId };
  if (input.catalogEntryId !== undefined) return { kind: "entry", id: input.catalogEntryId };
  if (input.workId !== undefined) return { kind: "work", id: input.workId };
  return null;
}

/**
 * Identity component of the browser cache key.
 *
 * A durable object — series, catalogue entry or work — speaks for itself. A
 * remote card has no such object until someone registers it, but the source
 * page addresses it by source and code, and that pair is stable across visits.
 * Left out of the cache, every revisit re-requested every remote title; the
 * content hash inside the key still invalidates when the text changes, and the
 * stored version when the model or template does. A directory path is part of
 * the identity because two folders may hold files with the same name.
 */
export function contentTranslationCacheIdentity(input: ContentTranslationInput): string | null {
  const entity = contentTranslationEntity(input);
  if (entity) return entity.kind + ":" + entity.id;
  if (input.sourceId === undefined || !input.code) return null;
  const path = input.path ? "|" + encodeURIComponent(input.path) : "";
  return "source:" + input.sourceId + ":" + encodeURIComponent(input.code) + path;
}

/**
 * `<accountScope>|<identity>|<field>|<language>|<contentHash>`, where identity
 * is the durable entity or, for a remote card, its source, code and path.
 */
export function buildContentTranslationCacheKey(
  accountScope: string,
  input: ContentTranslationInput,
  contentHash: string,
): string | null {
  const identity = contentTranslationCacheIdentity(input);
  if (!identity) return null;
  return accountScope + "|" + identity + "|" + input.field + "|" + input.language + "|" + contentHash;
}

function toHex(buffer: ArrayBuffer) {
  return [...new Uint8Array(buffer)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

/**
 * SHA-256 of the source text. Without Web Crypto the text itself becomes the
 * version component, so a stale browser still gets a correct cache identity.
 */
export async function contentTranslationContentHash(
  text: string,
  subtle: SubtleCrypto | null = globalThis.crypto?.subtle ?? null,
): Promise<string> {
  if (!subtle?.digest) return text;
  try {
    return toHex(await subtle.digest("SHA-256", new TextEncoder().encode(text)));
  } catch {
    return text;
  }
}

const encoder = new TextEncoder();

function estimateBytes(record: CachedTranslationRecord) {
  return encoder.encode(JSON.stringify(record)).byteLength;
}

function withoutKey(record: CachedTranslationRecord): CachedTranslation {
  return {
    text: record.text,
    original: record.original,
    status: record.status,
    storedAt: record.storedAt,
    lastUsedAt: record.lastUsedAt,
    version: record.version,
  };
}

/**
 * Whether a stored row can answer a request whose model/template version is
 * known. A missing row version is stale once an expectation exists; a caller
 * that does not know the version keeps the previous permissive behaviour.
 */
export function contentTranslationVersionMatches(storedVersion: string | undefined, expectedVersion?: string): boolean {
  if (!expectedVersion) return true;
  return storedVersion === expectedVersion;
}

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB request failed"));
  });
}

function transactionDone(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error ?? new Error("IndexedDB transaction failed"));
    transaction.onabort = () => reject(transaction.error ?? new Error("IndexedDB transaction aborted"));
  });
}

export function createContentTranslationCache(options: ContentTranslationCacheOptions = {}): ContentTranslationCache {
  const factory = options.indexedDB === undefined ? (globalThis.indexedDB ?? null) : options.indexedDB;
  const subtle = options.subtle === undefined ? (globalThis.crypto?.subtle ?? null) : options.subtle;
  const now = options.now ?? (() => Date.now());
  const memoryLimit = Math.max(1, options.memoryLimit ?? CONTENT_TRANSLATION_MEMORY_LIMIT);
  const idbEntryLimit = Math.max(1, options.idbEntryLimit ?? CONTENT_TRANSLATION_IDB_ENTRY_LIMIT);
  const idbByteLimit = Math.max(1, options.idbByteLimit ?? CONTENT_TRANSLATION_IDB_BYTE_LIMIT);
  const pruneDelayMs = Math.max(0, options.pruneDelayMs ?? 1_000);
  const hashText = options.hashText ?? ((text: string) => contentTranslationContentHash(text, subtle));

  const memory = new Map<string, CachedTranslationRecord>();
  const hashes = new Map<string, Promise<string>>();
  let dbPromise: Promise<IDBDatabase | null> | null = null;
  let persistenceDisabled = !factory;
  let pruneTimer: ReturnType<typeof setTimeout> | null = null;
  let pruneRunning: Promise<void> | null = null;

  function contentHash(text: string): Promise<string> {
    let pending = hashes.get(text);
    if (!pending) {
      pending = hashText(text);
      hashes.set(text, pending);
      if (hashes.size > 2_000) {
        const oldest = hashes.keys().next().value;
        if (oldest !== undefined) hashes.delete(oldest);
      }
    }
    return pending;
  }

  async function keyFor(scope: string, input: ContentTranslationInput): Promise<string | null> {
    if (!contentTranslationCacheIdentity(input)) return null;
    return buildContentTranslationCacheKey(scope, input, await contentHash(input.text));
  }

  function remember(key: string, record: CachedTranslationRecord) {
    memory.delete(key);
    if (memory.size >= memoryLimit) {
      const oldest = memory.keys().next().value;
      if (oldest !== undefined) memory.delete(oldest);
    }
    memory.set(key, record);
  }

  function disablePersistence() {
    persistenceDisabled = true;
    dbPromise = null;
  }

  function openDatabase(): Promise<IDBDatabase | null> {
    if (persistenceDisabled || !factory) return Promise.resolve(null);
    if (dbPromise) return dbPromise;
    dbPromise = new Promise((resolve) => {
      let request: IDBOpenDBRequest;
      try {
        request = factory.open(CONTENT_TRANSLATION_DB_NAME, CONTENT_TRANSLATION_DB_VERSION);
      } catch {
        disablePersistence();
        resolve(null);
        return;
      }
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains(CONTENT_TRANSLATION_STORE_NAME)) {
          const store = db.createObjectStore(CONTENT_TRANSLATION_STORE_NAME, { keyPath: "key" });
          store.createIndex("lastUsedAt", "lastUsedAt");
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => {
        disablePersistence();
        resolve(null);
      };
      request.onblocked = () => {
        disablePersistence();
        resolve(null);
      };
    });
    return dbPromise;
  }

  async function idbGet(key: string): Promise<CachedTranslationRecord | null> {
    const db = await openDatabase();
    if (!db) return null;
    try {
      const value = await requestResult(
        db.transaction(CONTENT_TRANSLATION_STORE_NAME, "readonly").objectStore(CONTENT_TRANSLATION_STORE_NAME).get(key),
      );
      return (value as CachedTranslationRecord | undefined) ?? null;
    } catch {
      disablePersistence();
      return null;
    }
  }

  async function idbDelete(key: string): Promise<void> {
    const db = await openDatabase();
    if (!db) return;
    try {
      const transaction = db.transaction(CONTENT_TRANSLATION_STORE_NAME, "readwrite");
      transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME).delete(key);
      await transactionDone(transaction);
    } catch {
      disablePersistence();
    }
  }

  async function idbPut(record: CachedTranslationRecord): Promise<boolean> {
    const db = await openDatabase();
    if (!db) return false;
    try {
      const transaction = db.transaction(CONTENT_TRANSLATION_STORE_NAME, "readwrite");
      transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME).put(record);
      await transactionDone(transaction);
      return true;
    } catch {
      // Quota exhaustion and private-mode failures leave the memory cache intact.
      disablePersistence();
      return false;
    }
  }

  async function runPrune() {
    const db = await openDatabase();
    if (!db) return;
    let records: CachedTranslationRecord[];
    try {
      records = (await requestResult(
        db.transaction(CONTENT_TRANSLATION_STORE_NAME, "readonly").objectStore(CONTENT_TRANSLATION_STORE_NAME).getAll(),
      )) as CachedTranslationRecord[];
    } catch {
      disablePersistence();
      return;
    }
    let bytes = records.reduce((sum, record) => sum + estimateBytes(record), 0);
    let entries = records.length;
    if (entries <= idbEntryLimit && bytes <= idbByteLimit) return;
    // A memory hit refreshes recency without a synchronous disk write, so the
    // in-memory timestamp is the newer one for rows read in this session.
    const recency = new Map<string, number>();
    for (const [key, record] of memory) recency.set(key, record.lastUsedAt);
    const lastUsed = (record: CachedTranslationRecord) => recency.get(record.key) ?? record.lastUsedAt;
    records.sort((first, second) => lastUsed(first) - lastUsed(second));
    const expired: string[] = [];
    for (const record of records) {
      if (entries <= idbEntryLimit && bytes <= idbByteLimit) break;
      bytes -= estimateBytes(record);
      entries -= 1;
      expired.push(record.key);
    }
    if (expired.length === 0) return;
    try {
      const transaction = db.transaction(CONTENT_TRANSLATION_STORE_NAME, "readwrite");
      const store = transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME);
      for (const key of expired) store.delete(key);
      await transactionDone(transaction);
    } catch {
      disablePersistence();
    }
  }

  function schedulePrune() {
    if (persistenceDisabled || pruneTimer !== null) return;
    pruneTimer = setTimeout(() => {
      pruneTimer = null;
      // One prune at a time; a write during the walk waits for the next tick.
      pruneRunning = runPrune().finally(() => {
        pruneRunning = null;
      });
    }, pruneDelayMs);
  }

  return {
    async get(scope, input, expectedVersion) {
      const key = await keyFor(scope, input);
      if (!key) return null;
      const inMemory = memory.get(key);
      if (inMemory) {
        if (!contentTranslationVersionMatches(inMemory.version, expectedVersion)) {
          // A row produced by another model or template is not a cache hit.
          memory.delete(key);
          void idbDelete(key);
          return null;
        }
        const touched = { ...inMemory, lastUsedAt: now() };
        remember(key, touched);
        // Recency is what the persistent eviction order reads; keep it fresh
        // without delaying the caller.
        void idbPut(touched);
        return withoutKey(touched);
      }
      const stored = await idbGet(key);
      if (!stored || !isCacheableTranslationStatus(stored.status)) return null;
      if (!contentTranslationVersionMatches(stored.version, expectedVersion)) {
        await idbDelete(key);
        return null;
      }
      const touched = { ...stored, lastUsedAt: now() };
      remember(key, touched);
      // Persist the recency so a later prune does not evict a row just read.
      await idbPut(touched);
      return withoutKey(touched);
    },

    async has(scope, input, expectedVersion) {
      const key = await keyFor(scope, input);
      if (!key) return false;
      const inMemory = memory.get(key);
      if (inMemory) {
        if (!contentTranslationVersionMatches(inMemory.version, expectedVersion)) {
          memory.delete(key);
          void idbDelete(key);
          return false;
        }
        return true;
      }
      const stored = await idbGet(key);
      if (!stored || !isCacheableTranslationStatus(stored.status)) return false;
      if (!contentTranslationVersionMatches(stored.version, expectedVersion)) {
        await idbDelete(key);
        return false;
      }
      remember(key, stored);
      return true;
    },

    async put(scope, input, result) {
      if (!isCacheableTranslationStatus(result.status)) return false;
      const key = await keyFor(scope, input);
      if (!key) return false;
      const timestamp = now();
      const record: CachedTranslationRecord = {
        key,
        text: result.text,
        original: result.original,
        status: result.status,
        storedAt: timestamp,
        lastUsedAt: timestamp,
        // An absent server version stays absent: a row without version is
        // still usable while the expected version is unknown.
        version: result.version || undefined,
      };
      remember(key, record);
      await idbPut(record);
      schedulePrune();
      return true;
    },

    async prune() {
      if (pruneTimer !== null) {
        clearTimeout(pruneTimer);
        pruneTimer = null;
      }
      if (pruneRunning) await pruneRunning;
      pruneRunning = runPrune().finally(() => {
        pruneRunning = null;
      });
      await pruneRunning;
    },

    clearMemory() {
      memory.clear();
    },

    memorySize() {
      return memory.size;
    },

    persistenceAvailable() {
      return !persistenceDisabled;
    },
  };
}

/** The browser-wide cache used by the translation component and prefetch queue. */
export const contentTranslationCache = createContentTranslationCache();
