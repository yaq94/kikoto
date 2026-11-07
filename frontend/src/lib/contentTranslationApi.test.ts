import { beforeEach, describe, expect, it, vi } from "vitest";

const sendJSONBody = vi.hoisted(() => vi.fn());

vi.mock("@/lib/apiTransport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/apiTransport")>();
  return {
    ...actual,
    apiTransport: { ...actual.apiTransport, sendJSONBody },
  };
});

import { ApiError } from "@/lib/apiTransport";
import {
  CONTENT_TRANSLATION_BATCH_LIMIT,
  ContentTranslationBatchError,
  contentTranslationApi,
  contentTranslationRequestKey,
  isContentTranslationInFlight,
  translateCoalesced,
  type ContentTranslationInput,
} from "./contentTranslationApi";

function input(index: number, language = "zh-cn"): ContentTranslationInput {
  return { catalogEntryId: 1000 + index, field: "title", text: `title ${index}`, language };
}

function echoResponse(body: { language: string; items: Array<Record<string, unknown>> }) {
  return {
    items: body.items.map((item) => ({
      key: item.key,
      text: `translated:${item.text}`,
      original: item.text,
      language: body.language,
      status: "translated",
    })),
  };
}

describe("contentTranslationApi.translateBatch", () => {
  beforeEach(() => {
    sendJSONBody.mockReset();
  });

  it("sends at most 200 items per request and merges results by key", async () => {
    sendJSONBody.mockImplementation(async (_method, _path, body) => echoResponse(body));
    const inputs = Array.from({ length: CONTENT_TRANSLATION_BATCH_LIMIT + 1 }, (_, index) => input(index));
    const results = await contentTranslationApi.translateBatch(inputs);

    expect(sendJSONBody).toHaveBeenCalledTimes(2);
    const firstBody = sendJSONBody.mock.calls[0][2];
    const secondBody = sendJSONBody.mock.calls[1][2];
    expect(firstBody.items).toHaveLength(200);
    expect(secondBody.items).toHaveLength(1);
    expect(firstBody.language).toBe("zh-cn");
    expect(results).toHaveLength(201);
    for (const item of inputs) {
      expect(results.get(contentTranslationRequestKey(item))?.text).toBe(`translated:${item.text}`);
    }
  });

  it("keeps languages in separate requests", async () => {
    sendJSONBody.mockImplementation(async (_method, _path, body) => echoResponse(body));
    await contentTranslationApi.translateBatch([input(1, "zh-cn"), input(2, "en")]);

    expect(sendJSONBody).toHaveBeenCalledTimes(2);
    const languages = sendJSONBody.mock.calls.map((call) => call[2].language).sort();
    expect(languages).toEqual(["en", "zh-cn"]);
  });

  it("sends the entity column and the per-item language", async () => {
    sendJSONBody.mockImplementation(async (_method, _path, body) => echoResponse(body));
    await contentTranslationApi.translateBatch([{ catalogEntryId: 7, field: "title", text: "x", language: "ja" }]);

    const item = sendJSONBody.mock.calls[0][2].items[0];
    expect(item).toMatchObject({
      catalogEntryId: 7,
      field: "title",
      text: "x",
      language: "ja",
    });
    expect(item).not.toHaveProperty("workId");
    expect(item).not.toHaveProperty("seriesId");
    expect(typeof item.key).toBe("string");
  });

  it("keeps source-level identity for uncached translations", async () => {
    sendJSONBody.mockImplementation(async (_method, _path, body) => echoResponse(body));
    await contentTranslationApi.translateBatch([
      { sourceId: 4, code: "RJ123", field: "title", text: "x", language: "ko" },
    ]);

    expect(sendJSONBody.mock.calls[0][2].items[0]).toMatchObject({ sourceId: 4, code: "RJ123" });
  });

  it("reports a 400 as an identifiable batch error", async () => {
    sendJSONBody.mockRejectedValue(
      new ApiError("A batch accepts between 1 and 200 items.", 400, "translation_invalid_input"),
    );

    await expect(contentTranslationApi.translateBatch([input(1)])).rejects.toMatchObject({
      name: "ContentTranslationBatchError",
      status: 400,
      code: "translation_invalid_input",
      retryable: false,
    } satisfies Partial<ContentTranslationBatchError>);
  });

  it("reports an incomplete response instead of dropping a card", async () => {
    sendJSONBody.mockResolvedValue({ items: [] });

    await expect(contentTranslationApi.translateBatch([input(1)])).rejects.toMatchObject({
      name: "ContentTranslationBatchError",
      code: "translation_batch_incomplete",
    });
  });

  it("coalesces fields rendered in one frame into a single batch", async () => {
    sendJSONBody.mockImplementation(async (_method, _path, body) => echoResponse(body));
    const [first, second] = await Promise.all([translateCoalesced(input(1)), translateCoalesced(input(2))]);

    expect(sendJSONBody).toHaveBeenCalledTimes(1);
    expect(sendJSONBody.mock.calls[0][2].items).toHaveLength(2);
    expect(first.text).toBe("translated:title 1");
    expect(second.text).toBe("translated:title 2");
  });

  it("shares two network slots across separate coalescing frames", async () => {
    vi.useFakeTimers();
    let active = 0;
    let maximum = 0;
    let drain = false;
    const releases: Array<() => void> = [];
    sendJSONBody.mockImplementation((_method, _path, body) => {
      active += 1;
      maximum = Math.max(maximum, active);
      return new Promise((resolve) => {
        const release = () => {
          active -= 1;
          resolve(echoResponse(body));
        };
        if (drain) release();
        else releases.push(release);
      });
    });
    const requests = [];
    for (let frame = 0; frame < 4; frame += 1) {
      requests.push(translateCoalesced(input(50 + frame)));
      await vi.advanceTimersByTimeAsync(17);
    }
    const beforeRelease = sendJSONBody.mock.calls.length;
    drain = true;
    for (const release of releases) release();
    const results = await Promise.all(requests);
    vi.useRealTimers();
    expect(beforeRelease).toBe(2);
    expect(maximum).toBe(2);
    expect(results.map((result) => result.text)).toEqual([50, 51, 52, 53].map((i) => `translated:title ${i}`));
  });

  it("removes an aborted queued batch and releases slots after failures", async () => {
    const releases: Array<(fail?: boolean) => void> = [];
    sendJSONBody.mockImplementation(
      (_method, _path, body) =>
        new Promise((resolve, reject) => {
          releases.push((fail) =>
            fail ? reject(new Error("synthetic transport failure")) : resolve(echoResponse(body)),
          );
        }),
    );
    const first = contentTranslationApi.translateBatch([input(60)]).catch((error) => error);
    const second = contentTranslationApi.translateBatch([input(61)]);
    const cancelled = new AbortController();
    const third = contentTranslationApi.translateBatch([input(62)], cancelled.signal).catch((error) => error);
    const fourth = contentTranslationApi.translateBatch([input(63)]);
    expect(sendJSONBody).toHaveBeenCalledTimes(2);
    cancelled.abort();
    expect((await third).name).toBe("AbortError");
    releases[0](true);
    await first;
    expect(sendJSONBody).toHaveBeenCalledTimes(3);
    expect(sendJSONBody.mock.calls[2][2].items[0].text).toBe("title 63");
    releases[1]();
    releases[2]();
    await Promise.all([second, fourth]);
    expect(isContentTranslationInFlight(contentTranslationRequestKey(input(62)))).toBe(false);
  });

  it("shares one queued request for the same field", async () => {
    sendJSONBody.mockImplementation(async (_method, _path, body) => echoResponse(body));
    const [first, second] = await Promise.all([translateCoalesced(input(1)), translateCoalesced(input(1))]);

    expect(sendJSONBody).toHaveBeenCalledTimes(1);
    expect(sendJSONBody.mock.calls[0][2].items).toHaveLength(1);
    expect(first.text).toBe(second.text);
  });

  it("counts a duplicate reader that joins after the request has started", async () => {
    let release!: () => void;
    let networkSignal!: AbortSignal;
    sendJSONBody.mockImplementation(
      (_method, _path, body, options) =>
        new Promise((resolve, reject) => {
          networkSignal = options.signal;
          release = () => resolve(echoResponse(body));
          networkSignal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
        }),
    );
    const firstController = new AbortController();
    const first = translateCoalesced(input(80), firstController.signal, "late-reader").catch((error) => error);
    await vi.waitFor(() => expect(sendJSONBody).toHaveBeenCalledTimes(1));
    const second = translateCoalesced(input(80), undefined, "late-reader").catch((error) => error);
    firstController.abort();
    const aborted = networkSignal.aborted;
    release();
    expect((await first).name).toBe("AbortError");
    expect(await second).toMatchObject({ text: "translated:title 80" });
    expect(aborted).toBe(false);
    expect(sendJSONBody).toHaveBeenCalledTimes(1);
  });

  it.each([false, true])("releases abandoned chunks after an earlier chunk settles (failure=%s)", async (failure) => {
    const requests: Array<{ signal: AbortSignal; release: (fail?: boolean) => void }> = [];
    sendJSONBody.mockImplementation(
      (_method, _path, body, options) =>
        new Promise((resolve, reject) => {
          requests.push({
            signal: options.signal,
            release: (fail) => (fail ? reject(new Error("synthetic failure")) : resolve(echoResponse(body))),
          });
          options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
        }),
    );
    const controllers = Array.from({ length: 33 }, () => new AbortController());
    const pending = controllers.map((controller, i) =>
      translateCoalesced(input(100 + i), controller.signal, "settled-readers").catch((error) => error),
    );
    await vi.waitFor(() => expect(requests).toHaveLength(2));
    requests[0].release(failure);
    await vi.waitFor(() => expect(requests).toHaveLength(3));
    for (let i = 16; i < 33; i++) controllers[i].abort();
    await Promise.all(pending);
    const aborted = requests.slice(1).map((request) => request.signal.aborted);
    // Drain the transport even on the old implementation, so this regression
    // cannot leave occupied global slots for another test.
    requests.slice(1).forEach((request) => request.release());
    sendJSONBody.mockImplementation(async (_method, _path, body) => echoResponse(body));
    await expect(translateCoalesced(input(150), undefined, "next-language")).resolves.toMatchObject({
      status: "translated",
    });
    expect(aborted).toEqual([true, true]);
  });

  it("skips a queued chunk whose only reader cancels while another chunk still waits", async () => {
    vi.useFakeTimers();
    const requests: Array<{ signal: AbortSignal; release: () => void }> = [];
    sendJSONBody.mockImplementation(
      (_method, _path, body, options) =>
        new Promise((resolve) => {
          requests.push({ signal: options.signal, release: () => resolve(echoResponse(body)) });
        }),
    );
    const controllers = Array.from({ length: 33 }, () => new AbortController());
    const pending = controllers.map((controller, i) =>
      translateCoalesced(input(200 + i), controller.signal, "cancelled-tail").catch((error) => error),
    );
    try {
      await vi.advanceTimersByTimeAsync(17);
      expect(requests).toHaveLength(2);
      controllers[32].abort();
      requests[0].release();
      await vi.advanceTimersByTimeAsync(0);
      const sentTexts = sendJSONBody.mock.calls.flatMap((call) =>
        call[2].items.map((item: { text: string }) => item.text),
      );
      const remainingAborted = requests[1].signal.aborted;
      // Drain even the buggy implementation before asserting the transport boundary.
      requests.forEach((request) => request.release());
      const results = await Promise.all(pending);
      expect(sentTexts).not.toContain("title 232");
      expect(remainingAborted).toBe(false);
      expect(results.slice(0, 32).every((result) => result.status === "translated")).toBe(true);
      expect(results[32]).toMatchObject({ name: "AbortError" });
      expect(isContentTranslationInFlight("cancelled-tail" + contentTranslationRequestKey(input(232)))).toBe(false);
    } finally {
      requests.forEach((request) => request.release());
      await vi.runAllTimersAsync();
      vi.useRealTimers();
    }
  });

  it("rechecks readers after waiting for a shared slot and passes an empty chunk's slot onward", async () => {
    vi.useFakeTimers();
    let drain = false;
    const releases: Array<() => void> = [];
    sendJSONBody.mockImplementation(
      (_method, _path, body) =>
        new Promise((resolve) => {
          const release = () => resolve(echoResponse(body));
          if (drain) release();
          else releases.push(release);
        }),
    );
    const blockers = [
      contentTranslationApi.translateBatch([input(280)]),
      contentTranslationApi.translateBatch([input(281)]),
    ];
    const controllers = Array.from({ length: 17 }, () => new AbortController());
    const pending = controllers.map((controller, i) =>
      translateCoalesced(input(300 + i), controller.signal, "waiting-slot").catch((error) => error),
    );
    try {
      await vi.advanceTimersByTimeAsync(17);
      expect(sendJSONBody).toHaveBeenCalledTimes(2);
      controllers.slice(0, 16).forEach((controller) => controller.abort());
      releases[0]();
      await vi.advanceTimersByTimeAsync(0);
      const sentAfterRelease = sendJSONBody.mock.calls
        .slice(2)
        .flatMap((call) => call[2].items.map((item: { text: string }) => item.text));
      const cancelledStillInflight = isContentTranslationInFlight(
        "waiting-slot" + contentTranslationRequestKey(input(300)),
      );
      const replacement = translateCoalesced(input(300), undefined, "waiting-slot");
      await vi.advanceTimersByTimeAsync(17);
      drain = true;
      releases.forEach((release) => release());
      const results = await Promise.all(pending);
      await Promise.all(blockers);
      await expect(replacement).resolves.toMatchObject({ text: "translated:title 300" });
      expect(sentAfterRelease).toEqual(["title 316"]);
      expect(cancelledStillInflight).toBe(false);
      expect(results.slice(0, 16).every((result) => result.name === "AbortError")).toBe(true);
      expect(results[16]).toMatchObject({ text: "translated:title 316" });
    } finally {
      drain = true;
      releases.forEach((release) => release());
      await vi.runAllTimersAsync();
      vi.useRealTimers();
    }
  });

  it.each(["none", "before-cancel", "after-cancel"] as const)(
    "keeps only active fields across queued languages, including a duplicate reader (%s)",
    async (join) => {
      vi.useFakeTimers();
      let drain = false;
      const releases: Array<() => void> = [];
      sendJSONBody.mockImplementation(
        (_method, _path, body) =>
          new Promise((resolve) => {
            const release = () => resolve(echoResponse(body));
            if (drain) release();
            else releases.push(release);
          }),
      );
      const blockers = [
        contentTranslationApi.translateBatch([input(360)]),
        contentTranslationApi.translateBatch([input(361)]),
      ];
      const cancelledField = new AbortController();
      const cancelledLanguage = new AbortController();
      const pending = [
        translateCoalesced(input(380), undefined, "queued-readers"),
        translateCoalesced(input(381), cancelledField.signal, "queued-readers").catch((error) => error),
        translateCoalesced(input(382, "en"), cancelledLanguage.signal, "queued-readers").catch((error) => error),
      ];
      try {
        await vi.advanceTimersByTimeAsync(17);
        expect(sendJSONBody).toHaveBeenCalledTimes(2);
        if (join === "before-cancel") pending.push(translateCoalesced(input(381), undefined, "queued-readers"));
        cancelledField.abort();
        cancelledLanguage.abort();
        if (join === "after-cancel") pending.push(translateCoalesced(input(381), undefined, "queued-readers"));
        drain = true;
        releases.forEach((release) => release());
        const results = await Promise.all(pending);
        await Promise.all(blockers);
        const sentTexts = sendJSONBody.mock.calls
          .slice(2)
          .flatMap((call) => call[2].items.map((item: { text: string }) => item.text));
        expect(sentTexts).toEqual(join === "none" ? ["title 380"] : ["title 380", "title 381"]);
        expect(results[0]).toMatchObject({ text: "translated:title 380" });
        expect(results[1]).toMatchObject({ name: "AbortError" });
        expect(results[2]).toMatchObject({ name: "AbortError" });
        if (join !== "none") expect(results[3]).toMatchObject({ text: "translated:title 381" });
      } finally {
        drain = true;
        releases.forEach((release) => release());
        await vi.runAllTimersAsync();
        vi.useRealTimers();
      }
    },
  );

  it("aborts a coalesced batch once every reader of that frame has left", async () => {
    let signal: AbortSignal | undefined;
    sendJSONBody.mockImplementation((_method, _path, _body, options) => {
      signal = options?.signal;
      return new Promise((_resolve, reject) => {
        options?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
      }) as never;
    });
    const controller = new AbortController();
    const pending = translateCoalesced(input(1), controller.signal).catch((error) => error);

    await vi.waitFor(() => expect(sendJSONBody).toHaveBeenCalled());
    controller.abort();
    await pending;

    expect(signal?.aborted).toBe(true);
  });

  it("keeps a coalesced batch running while another field still waits", async () => {
    let signal: AbortSignal | undefined;
    let release!: (value: unknown) => void;
    sendJSONBody.mockImplementation((_method, _path, body, options) => {
      signal = options?.signal;
      return new Promise((resolve) => (release = () => resolve(echoResponse(body)))) as never;
    });
    const left = new AbortController();
    const first = translateCoalesced(input(1), left.signal).catch((error) => error);
    const second = translateCoalesced(input(2));

    await vi.waitFor(() => expect(sendJSONBody).toHaveBeenCalled());
    left.abort();
    await first;
    expect(signal?.aborted).toBe(false);

    release(undefined);
    await expect(second).resolves.toMatchObject({ status: "translated" });
  });

  it("clears in-flight markers when a request settles", async () => {
    let release!: (value: unknown) => void;
    sendJSONBody.mockImplementation(() => new Promise((resolve) => (release = resolve)) as never);
    const item = input(1);
    const key = contentTranslationRequestKey(item);
    const pending = contentTranslationApi.translateBatch([item]);
    expect(isContentTranslationInFlight(key)).toBe(true);
    release(echoResponse({ language: "zh-cn", items: [{ key }] }));
    await pending;
    expect(isContentTranslationInFlight(key)).toBe(false);
  });
});
