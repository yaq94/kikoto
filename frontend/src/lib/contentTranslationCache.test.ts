import { describe, expect, it } from "vitest";

import type { ContentTranslationInput, ContentTranslationResult } from "./contentTranslationApi";
import {
  buildContentTranslationCacheKey,
  contentTranslationContentHash,
  createContentTranslationCache,
  type CachedTranslationRecord,
} from "./contentTranslationCache";

type StoredRow = CachedTranslationRecord;

function fakeIndexedDB() {
  const stores = new Map<string, Map<string, StoredRow>>();
  const factory = {
    open(name: string) {
      const request = {
        result: undefined as unknown,
        onupgradeneeded: null as null | (() => void),
        onsuccess: null as null | (() => void),
        onerror: null as null | (() => void),
        onblocked: null as null | (() => void),
      };
      queueMicrotask(() => {
        let rows = stores.get(name);
        const upgraded = !rows;
        if (!rows) {
          rows = new Map();
          stores.set(name, rows);
        }
        const storeRows = rows;
        const store = {
          createIndex() {
            return {};
          },
        };
        const db = {
          objectStoreNames: { contains: () => !upgraded },
          createObjectStore: () => store,
          transaction: () => {
            let pending = 0;
            let completed = false;
            const finish = () => {
              if (pending === 0 && !completed) {
                completed = true;
                queueMicrotask(() => transaction.oncomplete?.());
              }
            };
            const run = <T>(
              apply: () => T,
            ): { result: T; onsuccess: null | (() => void); onerror: null | (() => void) } => {
              pending += 1;
              const req = {
                result: undefined as T,
                onsuccess: null as null | (() => void),
                onerror: null as null | (() => void),
              };
              queueMicrotask(() => {
                req.result = apply();
                req.onsuccess?.();
                pending -= 1;
                queueMicrotask(finish);
              });
              return req;
            };
            const transaction = {
              oncomplete: null as null | (() => void),
              onerror: null as null | (() => void),
              onabort: null as null | (() => void),
              error: null as unknown,
              objectStore: () => ({
                get: (key: string) => run(() => storeRows.get(key) as StoredRow | undefined),
                getAll: () => run(() => [...storeRows.values()].map((row) => ({ ...row }))),
                put: (row: StoredRow) =>
                  run(() => {
                    storeRows.set(row.key, { ...row });
                    return undefined;
                  }),
                delete: (key: string) =>
                  run(() => {
                    storeRows.delete(key);
                    return undefined;
                  }),
              }),
            };
            return transaction;
          },
        };
        request.result = db;
        if (upgraded) request.onupgradeneeded?.();
        request.onsuccess?.();
      });
      return request;
    },
  };
  return { factory: factory as unknown as IDBFactory, stores };
}

function input(index: number, overrides: Partial<ContentTranslationInput> = {}): ContentTranslationInput {
  return { catalogEntryId: 100 + index, field: "title", text: `text ${index}`, language: "zh-cn", ...overrides };
}

function success(text: string): ContentTranslationResult {
  return { text: `t:${text}`, original: text, language: "zh-cn", status: "translated" };
}

const hashText = async (text: string) => `hash:${text}`;

describe("content translation cache key", () => {
  it("is account, entity, field, language and content scoped", () => {
    expect(buildContentTranslationCacheKey("srv:user-7", input(3), "abc")).toBe("srv:user-7|entry:103|title|zh-cn|abc");
    expect(buildContentTranslationCacheKey("srv:user-7", input(3, { workId: 8 }), "abc")).toBe(
      "srv:user-7|entry:103|title|zh-cn|abc",
    );
    expect(
      buildContentTranslationCacheKey("srv:user-7", { workId: 8, field: "title", text: "x", language: "en" }, "abc"),
    ).toBe("srv:user-7|work:8|title|en|abc");
  });

  it("prefers a series id like the server's entity mapping", () => {
    expect(
      buildContentTranslationCacheKey(
        "srv:user-7",
        { seriesId: 9, catalogEntryId: 103, field: "title", text: "x", language: "ja" },
        "abc",
      ),
    ).toBe("srv:user-7|series:9|title|ja|abc");
  });

  it("has no key for source-level content", () => {
    expect(
      buildContentTranslationCacheKey(
        "srv:user-7",
        { sourceId: 2, code: "RJ1", field: "title", text: "x", language: "ko" },
        "abc",
      ),
    ).toBeNull();
  });

  it("hashes with SHA-256 and falls back to the text without Web Crypto", async () => {
    await expect(contentTranslationContentHash("abc", null)).resolves.toBe("abc");
    await expect(contentTranslationContentHash("abc")).resolves.toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    );
  });
});

describe("content translation cache", () => {
  it("evicts the least recently used entry from memory", async () => {
    let clock = 0;
    const cache = createContentTranslationCache({
      indexedDB: null,
      subtle: null,
      hashText,
      now: () => ++clock,
      memoryLimit: 2,
    });
    await cache.put("scope", input(1), success("one"));
    await cache.put("scope", input(2), success("two"));
    expect((await cache.get("scope", input(1)))?.text).toBe("t:one");
    await cache.put("scope", input(3), success("three"));

    expect(cache.memorySize()).toBe(2);
    await expect(cache.get("scope", input(2))).resolves.toBeNull();
    await expect(cache.get("scope", input(1))).resolves.not.toBeNull();
    await expect(cache.get("scope", input(3))).resolves.not.toBeNull();
  });

  it("does not cache failed results", async () => {
    const cache = createContentTranslationCache({ indexedDB: null, subtle: null, hashText });
    const failed: ContentTranslationResult = { ...success("x"), status: "failed", code: "translation_unavailable" };
    await expect(cache.put("scope", input(1), failed)).resolves.toBe(false);
    await expect(cache.get("scope", input(1))).resolves.toBeNull();
    expect(cache.memorySize()).toBe(0);
  });

  it("degrades to memory only when IndexedDB cannot open", async () => {
    const factory = {
      open() {
        throw new Error("denied");
      },
    } as unknown as IDBFactory;
    const cache = createContentTranslationCache({ indexedDB: factory, subtle: null, hashText });

    await cache.put("scope", input(1), success("one"));
    expect(cache.persistenceAvailable()).toBe(false);
    await expect(cache.get("scope", input(1))).resolves.toMatchObject({ text: "t:one" });
  });

  it("persists successful rows under the composed key", async () => {
    const { factory, stores } = fakeIndexedDB();
    const cache = createContentTranslationCache({ indexedDB: factory, subtle: null, hashText });
    await cache.put("srv:user-7", input(1), success("one"));
    await cache.prune();

    const rows = [...(stores.get("kikoto-translations")?.values() ?? [])];
    expect(rows).toHaveLength(1);
    expect(rows[0].key).toBe("srv:user-7|entry:101|title|zh-cn|hash:text 1");
    expect(rows[0]).toMatchObject({ text: "t:one", original: "one", status: "translated" });
  });

  it("evicts the oldest persisted rows by lastUsedAt", async () => {
    const { factory } = fakeIndexedDB();

    let clock = 0;
    const first = createContentTranslationCache({
      indexedDB: factory,
      subtle: null,
      hashText,
      now: () => ++clock,
      idbEntryLimit: 2,
      idbByteLimit: Number.MAX_SAFE_INTEGER,
      pruneDelayMs: 60_000,
    });
    await first.put("scope", input(1), success("one"));
    await first.put("scope", input(2), success("two"));
    clock += 10;
    await first.get("scope", input(1));
    await first.put("scope", input(3), success("three"));
    await first.prune();

    const second = createContentTranslationCache({ indexedDB: factory, subtle: null, hashText, pruneDelayMs: 60_000 });
    await expect(second.get("scope", input(2))).resolves.toBeNull();
    await expect(second.get("scope", input(1))).resolves.not.toBeNull();
    await expect(second.get("scope", input(3))).resolves.not.toBeNull();
  });
});

describe("contentTranslationCache model and template versioning", () => {
  it("invalidates a row produced by another model", async () => {
    const cache = createContentTranslationCache({ indexedDB: null, subtle: null, hashText });
    const item = input(1);
    await cache.put("scope", item, { ...success("one"), version: "model-a|v1" });
    await expect(cache.get("scope", item, "model-a|v1")).resolves.toMatchObject({ text: "t:one" });
    await expect(cache.get("scope", item, "model-b|v1")).resolves.toBeNull();
    expect(cache.memorySize()).toBe(0);
  });

  it("invalidates a row produced by another prompt template", async () => {
    const cache = createContentTranslationCache({ indexedDB: null, subtle: null, hashText });
    const item = input(2);
    await cache.put("scope", item, { ...success("two"), version: "model-a|v1" });
    await expect(cache.get("scope", item, "model-a|v2")).resolves.toBeNull();
    expect(cache.memorySize()).toBe(0);
  });

  it("treats a row without a version as stale once the version is known", async () => {
    const cache = createContentTranslationCache({ indexedDB: null, subtle: null, hashText });
    const item = input(3);
    await cache.put("scope", item, success("three"));
    await expect(cache.get("scope", item, "model-a|v1")).resolves.toBeNull();
    expect(cache.memorySize()).toBe(0);
  });

  it("keeps the previous behaviour while the expected version is unknown", async () => {
    const cache = createContentTranslationCache({ indexedDB: null, subtle: null, hashText });
    const versioned = input(4);
    const unversioned = input(5);
    await cache.put("scope", versioned, { ...success("four"), version: "model-a|v1" });
    await cache.put("scope", unversioned, success("five"));
    await expect(cache.get("scope", versioned)).resolves.toMatchObject({ text: "t:four" });
    await expect(cache.get("scope", unversioned)).resolves.toMatchObject({ text: "t:five" });
    expect(cache.memorySize()).toBe(2);
  });

  it("deletes a stale persisted row instead of serving it after a reload", async () => {
    const { factory, stores } = fakeIndexedDB();
    const first = createContentTranslationCache({ indexedDB: factory, subtle: null, hashText });
    const item = input(6);
    await first.put("scope", item, { ...success("six"), version: "model-a|v1" });

    const second = createContentTranslationCache({ indexedDB: factory, subtle: null, hashText });
    await expect(second.get("scope", item, "model-b|v1")).resolves.toBeNull();
    expect([...(stores.get("kikoto-translations")?.values() ?? [])]).toHaveLength(0);
  });

  it("applies the same rule to has()", async () => {
    const cache = createContentTranslationCache({ indexedDB: null, subtle: null, hashText });
    const item = input(7);
    await cache.put("scope", item, { ...success("seven"), version: "model-a|v1" });
    await expect(cache.has("scope", item, "model-a|v1")).resolves.toBe(true);
    // An unknown expectation still sees the row.
    await expect(cache.has("scope", item)).resolves.toBe(true);
    await expect(cache.has("scope", item, "model-a|v2")).resolves.toBe(false);
    // The mismatch above removed the row, so the unknown read is a miss now.
    await expect(cache.has("scope", item)).resolves.toBe(false);
  });
});
