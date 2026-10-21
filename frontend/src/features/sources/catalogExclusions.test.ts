import { describe, expect, it, vi } from "vitest";

vi.mock("@/lib/apiTransport", () => ({
  getJSON: vi.fn(),
  postJSONBody: vi.fn(),
  deleteJSON: vi.fn(),
}));

import { catalogExclusionPathProblem, normalizeCatalogExclusionPath } from "./catalogExclusions";

describe("catalog exclusion paths", () => {
  it("rejects empty, relative, root and parent paths", () => {
    expect(catalogExclusionPathProblem("")).toBe("empty");
    expect(catalogExclusionPathProblem("  ")).toBe("empty");
    expect(catalogExclusionPathProblem("A/B")).toBe("relative");
    expect(catalogExclusionPathProblem("/")).toBe("root");
    expect(catalogExclusionPathProblem("/A/../B")).toBe("parent");
  });

  it("normalizes repeated and trailing slashes", () => {
    expect(normalizeCatalogExclusionPath("//A///B//")).toBe("/A/B");
    expect(catalogExclusionPathProblem("//A///B//")).toBeNull();
  });
});
