import type { ContentTranslationInput, ContentTranslationResult } from "@/lib/contentTranslationApi";

export const CONTENT_TRANSLATION_DB_NAME = "kikoto-translations";
export const CONTENT_TRANSLATION_STORE_NAME = "entries";
export const CONTENT_TRANSLATION_DB_VERSION = 2;
const METADATA_STORE = "entry_metadata";
const MAINTENANCE_STORE = "maintenance";
const MAINTENANCE_BATCH = 128;
type CacheMetadata = { key: string; bytes: number; lastUsedAt: number };
type CacheBudget = { key: "budget"; entries: number; bytes: number; ready: boolean; after?: string };
const emptyBudget = (): CacheBudget => ({ key: "budget", entries: 0, bytes: 0, ready: false });
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
  /** Pins writes to the server version confirmed for an account session. */
  setVersion(scope: string, version: string): void;
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
  const done = new Promise<void>((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error ?? new Error("IndexedDB transaction failed"));
    transaction.onabort = () => reject(transaction.error ?? new Error("IndexedDB transaction aborted"));
  });
  // A request can reject before its caller reaches await done. Observe the
  // transaction immediately while preserving its rejection for normal awaits.
  void done.catch(() => undefined);
  return done;
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
  const expectedVersions = new Map<string, string>();
  const hashes = new Map<string, Promise<string>>();
  let dbPromise: Promise<IDBDatabase | null> | null = null;
  let database: IDBDatabase | null = null;
  let persistenceDisabled = !factory;
  let pruneTimer: ReturnType<typeof setTimeout> | null = null;
  let pruneRunning: Promise<void> | null = null;
  let pruneRequested = false;

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
    database?.close();
    database = null;
    dbPromise = null;
    if (pruneTimer !== null) clearTimeout(pruneTimer);
    pruneTimer = null;
    pruneRequested = false;
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
        if (!db.objectStoreNames.contains(METADATA_STORE)) {
          const metadata = db.createObjectStore(METADATA_STORE, { keyPath: "key" });
          metadata.createIndex("lastUsedAt", ["lastUsedAt", "key"]);
        }
        if (!db.objectStoreNames.contains(MAINTENANCE_STORE)) {
          db.createObjectStore(MAINTENANCE_STORE, { keyPath: "key" });
        }
      };
      request.onsuccess = () => {
        const db = request.result;
        if (persistenceDisabled) {
          db.close();
          resolve(null);
          return;
        }
        database = db;
        db.onversionchange = () => {
          db.close();
          if (database === db) database = null;
          dbPromise = null;
        };
        resolve(db);
      };
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
      const transaction = writeTransaction(db);
      const done = transactionDone(transaction);
      const metadata = transaction.objectStore(METADATA_STORE);
      const state = transaction.objectStore(MAINTENANCE_STORE);
      const [previous, saved] = await Promise.all([
        requestResult(metadata.get(key)),
        requestResult(state.get("budget")),
      ]);
      const budget: CacheBudget = saved ?? emptyBudget();
      if (previous) {
        budget.entries -= 1;
        budget.bytes -= previous.bytes;
        metadata.delete(key);
      }
      transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME).delete(key);
      state.put(budget);
      await done;
    } catch {
      disablePersistence();
    }
  }

  async function idbPut(record: CachedTranslationRecord): Promise<boolean> {
    const db = await openDatabase();
    if (!db) return false;
    try {
      const bytes = estimateBytes(record);
      const transaction = writeTransaction(db);
      const done = transactionDone(transaction);
      const metadata = transaction.objectStore(METADATA_STORE);
      const state = transaction.objectStore(MAINTENANCE_STORE);
      const [previous, saved] = await Promise.all([
        requestResult(metadata.get(record.key)),
        requestResult(state.get("budget")),
      ]);
      const budget: CacheBudget = saved ?? emptyBudget();
      budget.entries += previous ? 0 : 1;
      budget.bytes += bytes - (previous?.bytes ?? 0);
      transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME).put(record);
      metadata.put({ key: record.key, bytes, lastUsedAt: record.lastUsedAt } satisfies CacheMetadata);
      state.put(budget);
      await done;
      return true;
    } catch {
      // Quota exhaustion and private-mode failures leave the memory cache intact.
      disablePersistence();
      return false;
    }
  }

  function writeTransaction(db: IDBDatabase) {
    return db.transaction([CONTENT_TRANSLATION_STORE_NAME, METADATA_STORE, MAINTENANCE_STORE], "readwrite");
  }

  // A memory hit changes only recency, never an old payload read before a newer
  // write. All tabs use the same transactional metadata and budget ledger.
  async function idbTouch(key: string, lastUsedAt: number) {
    const db = await openDatabase();
    if (!db) return;
    try {
      const transaction = writeTransaction(db);
      const done = transactionDone(transaction);
      const store = transaction.objectStore(METADATA_STORE);
      const metadata: CacheMetadata | undefined = await requestResult(store.get(key));
      if (metadata) store.put({ ...metadata, lastUsedAt: Math.max(metadata.lastUsedAt, lastUsedAt) });
      else {
        const state = transaction.objectStore(MAINTENANCE_STORE);
        const [record, saved] = await Promise.all([
          requestResult(transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME).get(key)),
          requestResult(state.get("budget")),
        ]);
        if (record) {
          const bytes = estimateBytes(record);
          const budget: CacheBudget = saved ?? emptyBudget();
          budget.entries += 1;
          budget.bytes += bytes;
          store.put({ key, bytes, lastUsedAt } satisfies CacheMetadata);
          state.put(budget);
        }
      }
      await done;
    } catch {
      disablePersistence();
    }
  }

  // Legacy payloads are accounted once, in bounded restartable transactions.
  // Concurrent writes already have metadata and are not counted twice.
  async function migrateBatch(db: IDBDatabase): Promise<boolean> {
    const transaction = writeTransaction(db);
    const done = transactionDone(transaction);
    const state = transaction.objectStore(MAINTENANCE_STORE);
    const budget: CacheBudget = (await requestResult(state.get("budget"))) ?? emptyBudget();
    if (budget.ready) {
      await done;
      return true;
    }
    const store = transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME);
    const metadata = transaction.objectStore(METADATA_STORE);
    const request = store.openCursor(
      budget.after === undefined ? undefined : IDBKeyRange.lowerBound(budget.after, true),
    );
    let visited = 0;
    await new Promise<void>((resolve, reject) => {
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        const cursor = request.result;
        if (!cursor) {
          budget.ready = true;
          state.put(budget);
          resolve();
          return;
        }
        const record = cursor.value as CachedTranslationRecord;
        const lookup = metadata.get(record.key);
        lookup.onerror = () => reject(lookup.error);
        lookup.onsuccess = () => {
          if (!lookup.result) {
            const bytes = estimateBytes(record);
            metadata.put({ key: record.key, bytes, lastUsedAt: record.lastUsedAt } satisfies CacheMetadata);
            budget.entries += 1;
            budget.bytes += bytes;
          }
          budget.after = record.key;
          visited += 1;
          if (visited >= MAINTENANCE_BATCH) {
            state.put(budget);
            resolve();
          } else cursor.continue();
        };
      };
    });
    await done;
    return budget.ready;
  }

  async function evictBatch(db: IDBDatabase): Promise<boolean> {
    const transaction = writeTransaction(db);
    const done = transactionDone(transaction);
    const state = transaction.objectStore(MAINTENANCE_STORE);
    const budget: CacheBudget = (await requestResult(state.get("budget"))) ?? emptyBudget();
    const overBudget = () => budget.entries > idbEntryLimit || budget.bytes > idbByteLimit;
    if (!overBudget()) {
      await done;
      return false;
    }
    const store = transaction.objectStore(CONTENT_TRANSLATION_STORE_NAME);
    const metadata = transaction.objectStore(METADATA_STORE);
    const request = metadata.index("lastUsedAt").openCursor();
    const expired: string[] = [];
    await new Promise<void>((resolve, reject) => {
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        const cursor = request.result;
        if (!cursor || !overBudget() || expired.length >= MAINTENANCE_BATCH) {
          state.put(budget);
          resolve();
          return;
        }
        const record = cursor.value as CacheMetadata;
        store.delete(record.key);
        cursor.delete();
        budget.entries -= 1;
        budget.bytes -= record.bytes;
        expired.push(record.key);
        cursor.continue();
      };
    });
    await done;
    for (const key of expired) memory.delete(key);
    return overBudget() && expired.length > 0;
  }

  async function runPrune() {
    const db = await openDatabase();
    if (!db) return;
    try {
      while (!(await migrateBatch(db))) await new Promise<void>((resolve) => setTimeout(resolve, 0));
      while (await evictBatch(db)) await new Promise<void>((resolve) => setTimeout(resolve, 0));
    } catch {
      disablePersistence();
    }
  }

  function startPrune(): Promise<void> {
    if (pruneRunning) return pruneRunning;
    pruneRunning = (async () => {
      do {
        pruneRequested = false;
        await runPrune();
      } while (pruneRequested && !persistenceDisabled);
    })().finally(() => {
      pruneRunning = null;
    });
    return pruneRunning;
  }

  function schedulePrune() {
    if (persistenceDisabled) return;
    pruneRequested = true;
    if (pruneTimer !== null || pruneRunning) return;
    pruneTimer = setTimeout(() => {
      pruneTimer = null;
      void startPrune();
    }, pruneDelayMs);
  }

  return {
    setVersion(scope, version) {
      expectedVersions.set(scope, version);
    },
    async get(scope, input, expectedVersion) {
      const key = await keyFor(scope, input);
      if (!key) return null;
      expectedVersion = expectedVersions.get(scope) || expectedVersion;
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
        void idbTouch(key, touched.lastUsedAt);
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
      await idbTouch(key, touched.lastUsedAt);
      return withoutKey(touched);
    },

    async has(scope, input, expectedVersion) {
      const key = await keyFor(scope, input);
      if (!key) return false;
      expectedVersion = expectedVersions.get(scope) || expectedVersion;
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
      const expected = expectedVersions.get(scope);
      if (expected && result.version !== expected) return false;
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
      await startPrune();
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
