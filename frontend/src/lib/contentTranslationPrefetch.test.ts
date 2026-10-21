import { describe, expect, it, vi } from "vitest";

import {
  contentTranslationRequestKey,
  type ContentTranslationInput,
  type ContentTranslationResult,
} from "./contentTranslationApi";
import type { ContentTranslationCache } from "./contentTranslationCache";
import { buildPrefetchWindow, createTitlePrefetchQueue, type TitlePrefetchRequest } from "./contentTranslationPrefetch";

function translated(input: ContentTranslationInput): ContentTranslationResult {
  return { text: `t:${input.text}`, original: input.text, language: input.language, status: "translated" };
}

function memoryCache(): Pick<ContentTranslationCache, "has" | "put"> & { stored: Map<string, ContentTranslationResult> } {
  const stored = new Map<string, ContentTranslationResult>();
  return {
    stored,
    has: async () => false,
    put: async (_scope, input, result) => {
      stored.set(contentTranslationRequestKey(input), result);
      return true;
    },
  };
}

function request(window: TitlePrefetchRequest["window"], loadPage: TitlePrefetchRequest["loadPage"]): TitlePrefetchRequest {
  return { namespace: "local:all", scope: "scope", language: "zh-cn", window, loadPage };
}

describe("buildPrefetchWindow", () => {
  it("covers the current page and the next nine", () => {
    expect(buildPrefetchWindow(3, 24, 9)).toEqual({
      pages: [3, 4, 5, 6, 7, 8, 9, 10, 11, 12],
      pageSize: 24,
      maxTitles: 240,
    });
  });

  it("clamps a restored page zero to the first page", () => {
    expect(buildPrefetchWindow(0, 24, 1).pages).toEqual([1, 2]);
  });

  it("supports warming only the current page", () => {
    expect(buildPrefetchWindow(7, 10, 0)).toEqual({ pages: [7], pageSize: 10, maxTitles: 10 });
  });
});

describe("title prefetch queue", () => {
  it("sends 240 titles in batches of 200", async () => {
    const cache = memoryCache();
    const translateBatch = vi.fn(async (inputs: ContentTranslationInput[]) => {
      return new Map(inputs.map((input) => [contentTranslationRequestKey(input), translated(input)]));
    });
    const queue = createTitlePrefetchQueue({ cache, translateBatch });
    const warm = await queue.enqueue(
      request(buildPrefetchWindow(1, 24, 9), async (page) =>
        Array.from({ length: 24 }, (_, index) => ({
          workId: page * 100 + index,
          field: "title" as const,
          text: `${page}:${index}`,
          language: "zh-cn",
        })),
      ),
    );

    expect(translateBatch).toHaveBeenCalledTimes(2);
    expect(translateBatch.mock.calls[0][0]).toHaveLength(200);
    expect(translateBatch.mock.calls[1][0]).toHaveLength(40);
    expect(warm).toBe(240);
    expect(cache.stored.size).toBe(240);
  });

  it("skips cached, in-flight and non-title entries", async () => {
    const cache = memoryCache();
    cache.has = async (_scope, input) => input.text === "cached";
    const inFlight = new Set<string>([
      contentTranslationRequestKey({ workId: 2, field: "title", text: "busy", language: "zh-cn" }),
    ]);
    const translateBatch = vi.fn(async (inputs: ContentTranslationInput[]) => {
      return new Map(inputs.map((input) => [contentTranslationRequestKey(input), translated(input)]));
    });
    const queue = createTitlePrefetchQueue({
      cache,
      translateBatch,
      isInFlight: (key) => inFlight.has(key),
    });
    await queue.enqueue(
      request(buildPrefetchWindow(1, 10, 0), async () => [
        { workId: 1, field: "title", text: "cached", language: "zh-cn" },
        { workId: 2, field: "title", text: "busy", language: "zh-cn" },
        { workId: 3, field: "title", text: "fresh", language: "zh-cn" },
        { workId: 4, field: "tag", text: "not-a-title", language: "zh-cn" },
        { sourceId: 1, code: "RJ1", field: "title", text: "source", language: "zh-cn" },
      ]),
    );

    const sent = translateBatch.mock.calls[0][0];
    expect(sent.map((input) => input.text)).toEqual(["fresh", "source"]);
  });

  it("does not load the same page twice when a window overlaps", async () => {
    const cache = memoryCache();
    const translatedMap = vi.fn(async (inputs: ContentTranslationInput[]) => {
      return new Map(inputs.map((input) => [contentTranslationRequestKey(input), translated(input)]));
    });
    const queue = createTitlePrefetchQueue({ cache, translateBatch: translatedMap });
    const loadPage = vi.fn(async (page: number) => [
      { workId: page, field: "title" as const, text: `p${page}`, language: "zh-cn" },
    ]);

    await Promise.all([
      queue.enqueue(request(buildPrefetchWindow(1, 24, 2), loadPage)),
      queue.enqueue(request(buildPrefetchWindow(2, 24, 2), loadPage)),
    ]);

    expect(loadPage).toHaveBeenCalledTimes(4);
    expect(loadPage.mock.calls.map((call) => call[0]).sort()).toEqual([1, 2, 3, 4]);
  });

  it("keeps failed translations out of the cache", async () => {
    const cache = memoryCache();
    const queue = createTitlePrefetchQueue({
      cache,
      translateBatch: async () => {
        throw new Error("offline");
      },
    });
    const warm = await queue.enqueue(
      request(buildPrefetchWindow(1, 24, 0), async () => [
        { workId: 1, field: "title", text: "one", language: "zh-cn" },
      ]),
    );

    expect(warm).toBe(0);
    expect(cache.stored.size).toBe(0);
  });
});
