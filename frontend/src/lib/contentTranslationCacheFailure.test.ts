import { describe, expect, it } from "vitest";
import { createContentTranslationCache } from "./contentTranslationCache";

const input = { workId: 1, field: "title" as const, text: "Example title", language: "zh-cn" };
const result = {
  text: "Example translation",
  original: input.text,
  status: "translated" as const,
  language: "zh-cn",
  version: "A",
};
const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 10));

describe("translation cache failure isolation", () => {
  it("closes a connection that arrives after a blocked open degraded to memory", async () => {
    let open!: { result: unknown; onblocked?: () => void; onsuccess?: () => void };
    let closed = 0;
    const factory = {
      open() {
        open = {
          result: {
            close() {
              closed++;
            },
          },
        };
        queueMicrotask(() => open.onblocked?.());
        return open;
      },
    };
    const cache = createContentTranslationCache({
      indexedDB: factory as unknown as IDBFactory,
      hashText: async (value) => value,
    });
    await expect(cache.get("scope", input)).resolves.toBeNull();
    open.onsuccess?.();
    expect(cache.persistenceAvailable()).toBe(false);
    expect(closed).toBe(1);
  });

  it.each(["early-read", "quota"])("contains %s failure and closes its connection", async (mode) => {
    let closed = 0;
    const failure = new DOMException(
      "Synthetic storage failure",
      mode === "quota" ? "QuotaExceededError" : "AbortError",
    );
    const db = {
      close() {
        closed++;
      },
      transaction() {
        const tx = {
          error: failure,
          onabort: null as null | (() => void),
          objectStore(name: string) {
            return {
              get() {
                const request = {
                  result: undefined,
                  error: failure,
                  onsuccess: null as null | (() => void),
                  onerror: null as null | (() => void),
                };
                setTimeout(() => {
                  if (mode === "early-read" && name === "entry_metadata") {
                    request.onerror?.();
                    tx.onabort?.();
                  } else request.onsuccess?.();
                }, 0);
                return request;
              },
              put() {
                if (name === "entries") setTimeout(() => tx.onabort?.(), 0);
              },
            };
          },
        };
        return tx;
      },
    };
    const factory = {
      open() {
        const request = { result: db, onsuccess: null as null | (() => void) };
        queueMicrotask(() => request.onsuccess?.());
        return request;
      },
    };
    const cache = createContentTranslationCache({
      indexedDB: factory as unknown as IDBFactory,
      hashText: async (value) => value,
    });
    await expect(cache.put("scope", input, result)).resolves.toBe(true);
    await tick();
    expect(cache.persistenceAvailable()).toBe(false);
    expect(closed).toBe(1);
    await expect(cache.get("scope", input)).resolves.toMatchObject({ text: result.text });
    await cache.prune();
  });
});
