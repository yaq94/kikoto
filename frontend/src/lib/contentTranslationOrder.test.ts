import { beforeEach, describe, expect, it, vi } from "vitest";

const sendJSONBody = vi.fn();

vi.mock("@/lib/apiTransport", () => ({
  ApiError: class ApiError extends Error {},
  apiTransport: { sendJSONBody: (...args: unknown[]) => sendJSONBody(...args) },
}));

const { isContentTranslationInFlight, contentTranslationRequestKey, translateCoalesced } =
  await import("./contentTranslationApi");

type Body = { language: string; items: Array<{ key: string; field: string; text: string }> };

describe("translateCoalesced delivery order", () => {
  beforeEach(() => {
    sendJSONBody.mockReset();
  });

  it("asks for titles before badge text so a card's heading is not held behind every tag", async () => {
    const requests: Body[] = [];
    sendJSONBody.mockImplementation(async (_method: string, _path: string, body: Body) => {
      requests.push(body);
      return {
        items: body.items.map((item) => ({
          ...item,
          text: `t:${item.text}`,
          original: item.text,
          status: "translated",
        })),
      };
    });
    const inputs = [
      ...Array.from({ length: 20 }, (_, index) => ({
        workId: index + 1,
        field: "tag" as const,
        text: `tag-${index}`,
        language: "zh-cn",
      })),
      { workId: 99, field: "title" as const, text: "title", language: "zh-cn" },
    ];
    await Promise.all(inputs.map((input) => translateCoalesced(input)));

    expect(requests.length).toBeGreaterThan(1);
    expect(requests[0].items.map((item) => item.field)).toEqual(["title"]);
    expect(requests.slice(1).every((request) => request.items.every((item) => item.field === "tag"))).toBe(true);
  });
});

describe("translateCoalesced cancellation", () => {
  beforeEach(() => {
    sendJSONBody.mockReset();
  });

  it("settles every reader, including chunks behind an abort", async () => {
    sendJSONBody.mockImplementation(
      (_method: string, _path: string, _body: Body, options?: { signal?: AbortSignal }) =>
        new Promise((_resolve, reject) => {
          options?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), {
            once: true,
          });
        }),
    );
    const controller = new AbortController();
    const inputs = Array.from({ length: 40 }, (_, index) => ({
      workId: index + 1,
      field: "tag" as const,
      text: `tag-${index}`,
      language: "zh-cn",
    }));
    const pending = inputs.map((input) =>
      translateCoalesced(input, controller.signal).catch((error: unknown) => error),
    );
    await new Promise((resolve) => setTimeout(resolve, 40));
    controller.abort();
    const results = await Promise.all(pending);
    // The caller's abort settles its own promise first; the flush loop then
    // clears the shared key. Give it that turn before asserting.
    await new Promise((resolve) => setTimeout(resolve, 50));
    // Every reader settles; a chunk that never started must not leave a
    // promise that a later retry would join forever.
    expect(results.every((result) => result instanceof Error)).toBe(true);
    const key = contentTranslationRequestKey(inputs[39]);
    expect({ key, inFlight: isContentTranslationInFlight(key) }).toEqual({ key, inFlight: false });
  });
});
