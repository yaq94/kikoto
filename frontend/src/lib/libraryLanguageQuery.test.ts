import { beforeEach, describe, expect, it, vi } from "vitest";

const getJSON = vi.hoisted(() => vi.fn());

vi.mock("@/lib/apiTransport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/apiTransport")>();
  return { ...actual, getJSON };
});

import { libraryApi } from "./libraryApi";
import { remoteSourceApi } from "./remoteSourceApi";

describe("display language query wiring", () => {
  beforeEach(() => {
    getJSON.mockReset();
    getJSON.mockResolvedValue({});
  });

  it("adds lang after the existing local query parameters", async () => {
    await libraryApi.listWorksPage(2, 24, "voice", "all", "all", "recent", "desc", 1, false, undefined, "session", "zh-cn");
    expect(getJSON.mock.calls[0][0]).toBe(
      "/api/works?page=2&pageSize=24&scope=all&status=all&sort=recent&direction=desc&seed=1&recommendBadges=false&recommendationSession=session&q=voice&lang=zh-cn",
    );
  });

  it("leaves the origin request unchanged", async () => {
    await libraryApi.listWorksPage(1, 24, "", "all", "all", "recent", "desc", 1, false, undefined, "", "origin");
    expect(getJSON.mock.calls[0][0]).toBe(
      "/api/works?page=1&pageSize=24&scope=all&status=all&sort=recent&direction=desc&seed=1&recommendBadges=false",
    );
  });

  it("adds lang to a remote source page", async () => {
    await remoteSourceApi.listRemoteSourceWorks(3, 4, 24, "title", "recent", "desc", 1, false, undefined, "", "ja");
    expect(getJSON.mock.calls[0][0]).toBe(
      "/api/remote-sources/3/works?page=4&pageSize=24&sort=recent&direction=desc&seed=1&recommendBadges=false&q=title&lang=ja",
    );
  });
});
