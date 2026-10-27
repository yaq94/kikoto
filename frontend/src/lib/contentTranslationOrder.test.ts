import { beforeEach, describe, expect, it, vi } from "vitest";

const sendJSONBody = vi.fn();

vi.mock("@/lib/apiTransport", () => ({
  ApiError: class ApiError extends Error {},
  apiTransport: { sendJSONBody: (...args: unknown[]) => sendJSONBody(...args) },
}));

const { translateCoalesced } = await import("./contentTranslationApi");

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
