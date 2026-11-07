import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("persistent translation maintenance avoids full payload scans and shares one run @desktop", async ({ page }) => {
  await page.goto("/");
  const result = await page.evaluate(async () => {
    // Vite serves the production cache module; only its native IDB boundary is
    // observed. Each browser context owns a new disposable database.
    const modulePath = "/src/lib/contentTranslationCache.ts";
    const { createContentTranslationCache } = await import(modulePath);
    let getAll = 0;
    const original = IDBObjectStore.prototype.getAll;
    IDBObjectStore.prototype.getAll = function (...args) {
      getAll++;
      return original.apply(this, args);
    };
    const cache = createContentTranslationCache({
      idbEntryLimit: 2,
      pruneDelayMs: 60_000,
      now: (() => {
        let tick = 0;
        return () => ++tick;
      })(),
    });
    const input = (n: number) => ({ workId: n, field: "title", text: `Example ${n}`, language: "zh-cn" });
    const value = (n: number) => ({
      text: `Translated ${n}`,
      original: `Example ${n}`,
      status: "translated",
      language: "zh-cn",
      version: "model-a",
    });
    await cache.put("account-a", input(1), value(1));
    await cache.put("account-a", input(2), value(2));
    await cache.get("account-a", input(1));
    await cache.put("account-a", input(3), value(3));
    await Promise.all([cache.prune(), cache.prune(), cache.prune()]);
    const reopened = createContentTranslationCache();
    const present = await Promise.all(
      [1, 2, 3].map(async (n) => !!(await reopened.get("account-a", input(n), "model-a"))),
    );
    const wrongAccount = await reopened.get("account-b", input(1), "model-a");
    const wrongVersion = await reopened.get("account-a", input(1), "model-b");
    IDBObjectStore.prototype.getAll = original;
    return { getAll, present, wrongAccount, wrongVersion };
  });
  expect(result.present).toEqual([true, false, true]);
  expect(result.wrongAccount).toBeNull();
  expect(result.wrongVersion).toBeNull();
  expect(result.getAll).toBe(0);
});

test("legacy cache migration and byte eviction remain bounded across concurrent writers @desktop", async ({ page }) => {
  await page.goto("/");
  const result = await page.evaluate(async () => {
    const legacy = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("kikoto-translations", 1);
      request.onupgradeneeded = () => {
        const store = request.result.createObjectStore("entries", { keyPath: "key" });
        store.createIndex("lastUsedAt", "lastUsedAt");
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const seed = legacy.transaction("entries", "readwrite");
    for (let i = 0; i < 300; i++)
      seed
        .objectStore("entries")
        .put({
          key: `legacy-${i.toString().padStart(3, "0")}`,
          text: "t".repeat(100),
          original: "o",
          status: "translated",
          storedAt: 1,
          lastUsedAt: i,
          version: "legacy",
        });
    await new Promise<void>((resolve, reject) => {
      seed.oncomplete = () => resolve();
      seed.onerror = () => reject(seed.error);
    });
    legacy.close();
    const modulePath = "/src/lib/contentTranslationCache.ts";
    const { createContentTranslationCache } = await import(modulePath);
    const mutations = new WeakMap<IDBTransaction, number>();
    let maximum = 0,
      payloadScans = 0;
    const record = (tx: IDBTransaction) => {
      const count = (mutations.get(tx) ?? 0) + 1;
      mutations.set(tx, count);
      maximum = Math.max(maximum, count);
    };
    const put = IDBObjectStore.prototype.put,
      del = IDBObjectStore.prototype.delete,
      all = IDBObjectStore.prototype.getAll,
      cursor = IDBObjectStore.prototype.openCursor;
    IDBObjectStore.prototype.put = function (...args) {
      record(this.transaction);
      return put.apply(this, args);
    };
    IDBObjectStore.prototype.delete = function (...args) {
      record(this.transaction);
      return del.apply(this, args);
    };
    IDBObjectStore.prototype.getAll = function (...args) {
      payloadScans++;
      return all.apply(this, args);
    };
    IDBObjectStore.prototype.openCursor = function (...args) {
      if (this.name === "entries") payloadScans++;
      return cursor.apply(this, args);
    };
    const cache = createContentTranslationCache({ idbByteLimit: 1500, pruneDelayMs: 0 });
    const maintenance = cache.prune();
    const input = { workId: 1, field: "title", text: "Current", language: "zh-cn" };
    await cache.put("account", input, {
      text: "Current translated",
      original: "Current",
      language: "zh-cn",
      status: "translated",
      version: "current",
    });
    await maintenance;
    await cache.prune();
    const migratedScans = payloadScans;
    await Promise.all([cache.prune(), cache.prune(), cache.prune()]);
    const steadyScans = payloadScans - migratedScans;
    IDBObjectStore.prototype.put = put;
    IDBObjectStore.prototype.delete = del;
    IDBObjectStore.prototype.getAll = all;
    IDBObjectStore.prototype.openCursor = cursor;
    const db = await new Promise<IDBDatabase>((resolve) => {
      const r = indexedDB.open("kikoto-translations", 2);
      r.onsuccess = () => resolve(r.result);
    });
    const rows = await new Promise<Array<Record<string, unknown>>>((resolve) => {
      const r = db.transaction("entries").objectStore("entries").getAll();
      r.onsuccess = () => resolve(r.result);
    });
    const bytes = rows.reduce((sum, row) => sum + new TextEncoder().encode(JSON.stringify(row)).byteLength, 0);
    const current = await cache.get("account", input, "current");
    db.close();
    return {
      maximum,
      migratedScans,
      steadyScans,
      bytes,
      rows: rows.length,
      current: current?.text,
      persistent: cache.persistenceAvailable(),
    };
  });
  expect(result.persistent).toBe(true);
  expect(result.migratedScans).toBeGreaterThan(1);
  expect(result.maximum).toBeLessThanOrEqual(129);
  expect(result.steadyScans).toBe(0);
  expect(result.bytes).toBeLessThanOrEqual(1500);
  expect(result.rows).toBeGreaterThan(0);
  expect(result.current).toBe("Current translated");
});
