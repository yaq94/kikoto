import { describe, expect, it } from "vitest";

import { parseRequestHeaderLines, requestHeaderLines } from "./remoteSourceModel";

describe("OpenList request header editing", () => {
  it("round-trips configured headers through the dialog text", () => {
    const headers = { "X-Requested-With": "kikoto", "Accept-Language": "ja-JP" };
    expect(requestHeaderLines(headers)).toBe("X-Requested-With: kikoto\nAccept-Language: ja-JP");
    expect(parseRequestHeaderLines(requestHeaderLines(headers))).toEqual(headers);
  });

  it("keeps header values containing a colon and drops malformed lines", () => {
    const parsed = parseRequestHeaderLines(
      ["X-Trace: a:b", "not-a-header", "  ", ": empty name", "X-Spaced :   trimmed  "].join("\n"),
    );
    expect(parsed).toEqual({ "X-Trace": "a:b", "X-Spaced": "trimmed" });
  });

  it("returns undefined so an emptied field is not submitted", () => {
    expect(requestHeaderLines(undefined)).toBe("");
    expect(parseRequestHeaderLines("")).toBeUndefined();
  });
});
