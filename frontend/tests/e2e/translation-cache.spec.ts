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

test("native IndexedDB abort degrades without unhandled rejection or retained connection @desktop", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  const state = await page.evaluate(async () => {
    const modulePath = "/src/lib/contentTranslationCache.ts";
    const { createContentTranslationCache } = await import(modulePath);
    let closed = 0,
      aborted = false;
    const get = IDBObjectStore.prototype.get,
      close = IDBDatabase.prototype.close;
    IDBDatabase.prototype.close = function () {
      closed++;
      return close.call(this);
    };
    IDBObjectStore.prototype.get = function (...args) {
      const result = get.apply(this, args);
      if (this.name === "entry_metadata" && !aborted) {
        aborted = true;
        queueMicrotask(() => this.transaction.abort());
      }
      return result;
    };
    const cache = createContentTranslationCache();
    const input = { workId: 1, field: "title", text: "Example", language: "zh-cn" };
    const stored = await cache.put("failure-account", input, {
      text: "Translation",
      original: "Example",
      language: "zh-cn",
      status: "translated",
      version: "A",
    });
    await new Promise((resolve) => setTimeout(resolve, 20));
    IDBObjectStore.prototype.get = get;
    IDBDatabase.prototype.close = close;
    return {
      stored,
      aborted,
      closed,
      persistent: cache.persistenceAvailable(),
      text: (await cache.get("failure-account", input))?.text,
    };
  });
  expect(state).toEqual({ stored: true, aborted: true, closed: 1, persistent: false, text: "Translation" });
  expect(errors).toEqual([]);
});

test("native blocked upgrade closes the late unused connection @desktop", async ({ page }) => {
  await page.goto("/");
  const state = await page.evaluate(async () => {
    const old = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("kikoto-translations", 1);
      request.onupgradeneeded = () => request.result.createObjectStore("entries", { keyPath: "key" });
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const modulePath = "/src/lib/contentTranslationCache.ts";
    const { createContentTranslationCache } = await import(modulePath);
    let closed = 0;
    const close = IDBDatabase.prototype.close,
      open = IDBFactory.prototype.open;
    IDBDatabase.prototype.close = function () {
      if (this.version === 2) closed++;
      return close.call(this);
    };
    let ready!: () => void;
    const late = new Promise<void>((resolve) => {
      ready = resolve;
    });
    IDBFactory.prototype.open = function (...args) {
      const request = open.apply(this, args);
      request.addEventListener("success", () => setTimeout(ready, 0));
      return request;
    };
    const cache = createContentTranslationCache();
    const value = await cache.get("account", { workId: 1, field: "title", text: "Example", language: "zh-cn" });
    old.close();
    await late;
    IDBFactory.prototype.open = open;
    IDBDatabase.prototype.close = close;
    return { value, closed, persistent: cache.persistenceAvailable() };
  });
  expect(state).toEqual({ value: null, closed: 1, persistent: false });
});

test("two browser tabs preserve the shared cache budget during migration and eviction @desktop", async ({
  page,
  context,
}) => {
  await page.goto("/");
  await page.evaluate(async () => {
    const db = await new Promise<IDBDatabase>((resolve) => {
      const r = indexedDB.open("kikoto-translations", 1);
      r.onupgradeneeded = () => r.result.createObjectStore("entries", { keyPath: "key" });
      r.onsuccess = () => resolve(r.result);
    });
    const tx = db.transaction("entries", "readwrite");
    for (let i = 0; i < 300; i++)
      tx.objectStore("entries").put({
        key: `legacy-${i}`,
        text: "Legacy",
        original: "Original",
        status: "translated",
        storedAt: i,
        lastUsedAt: i,
        version: "A",
      });
    await new Promise<void>((resolve) => {
      tx.oncomplete = () => resolve();
    });
    db.close();
  });
  const second = await context.newPage();
  await second.goto("/");
  const work = async (tab: typeof page, side: number) =>
    tab.evaluate(async (side) => {
      const modulePath = "/src/lib/contentTranslationCache.ts";
      const { createContentTranslationCache } = await import(modulePath);
      const cache = createContentTranslationCache({ idbEntryLimit: 50, idbByteLimit: 12_000, pruneDelayMs: 0 });
      const migration = cache.prune();
      for (let i = 0; i < 70; i++) {
        const id = i % 5 === 0 ? 1 : side * 100 + i;
        const input = { workId: id, field: "title", text: `Example ${id}`, language: "zh-cn" };
        await cache.put("shared-account", input, {
          text: `Translation ${side}/${i}`,
          original: input.text,
          language: "zh-cn",
          status: "translated",
          version: "A",
        });
        await cache.get("shared-account", input, "A");
        if (i % 7 === 0) await cache.prune();
      }
      await migration;
      await cache.prune();
      return cache.persistenceAvailable();
    }, side);
  expect(await Promise.all([work(page, 1), work(second, 2)])).toEqual([true, true]);
  const ledger = await page.evaluate(async () => {
    const db = await new Promise<IDBDatabase>((resolve) => {
      const r = indexedDB.open("kikoto-translations", 2);
      r.onsuccess = () => resolve(r.result);
    });
    const tx = db.transaction(["entries", "entry_metadata", "maintenance"]);
    const read = <T>(r: IDBRequest<T>) =>
      new Promise<T>((resolve) => {
        r.onsuccess = () => resolve(r.result);
      });
    const [entries, metadata, budget] = await Promise.all([
      read(tx.objectStore("entries").getAll()),
      read(tx.objectStore("entry_metadata").getAll()),
      read(tx.objectStore("maintenance").get("budget")),
    ]);
    db.close();
    const sizes = entries.map((row) => ({ key: row.key, bytes: new TextEncoder().encode(JSON.stringify(row)).length }));
    return {
      entries: entries.length,
      metadata: metadata.length,
      bytes: sizes.reduce((sum, row) => sum + row.bytes, 0),
      budget,
      matching: sizes.every((row) => metadata.some((m) => m.key === row.key && m.bytes === row.bytes)),
    };
  });
  expect(ledger.matching).toBe(true);
  expect(ledger.metadata).toBe(ledger.entries);
  expect(ledger.budget.entries).toBe(ledger.entries);
  expect(ledger.budget.bytes).toBe(ledger.bytes);
  expect(ledger.entries).toBeLessThanOrEqual(50);
  expect(ledger.bytes).toBeLessThanOrEqual(12_000);
  await second.close();
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
      seed.objectStore("entries").put({
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
