import { beforeEach, describe, expect, it, vi } from "vitest";

const postJSONBody = vi.hoisted(() => vi.fn());

vi.mock("@/lib/apiTransport", () => ({
  getJSON: vi.fn(),
  postJSONBody,
  deleteJSON: vi.fn(),
}));

import {
  catalogExclusionApi,
  catalogExclusionMode,
  catalogExclusionPathProblem,
  normalizeCatalogExclusionPath,
  type CatalogExclusion,
} from "./catalogExclusions";

function stored(overrides: Partial<CatalogExclusion> = {}): CatalogExclusion {
  return {
    id: 1,
    path: "/Example",
    mode: "whole",
    action: "exclude",
    note: "",
    enabled: true,
    createdAt: "2026-10-21 00:00:00",
    ...overrides,
  };
}

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

describe("catalog exclusion rules", () => {
  beforeEach(() => {
    postJSONBody.mockReset();
  });

  it("submits the rule mode and enabled state with the path", async () => {
    postJSONBody.mockResolvedValue(stored({ mode: "whitelist" }));
    await catalogExclusionApi.create(7, {
      path: "/source/example/audio",
      mode: "whitelist",
      note: "",
      enabled: true,
    });
    expect(postJSONBody).toHaveBeenCalledWith("/api/remote-sources/7/catalog/exclusions", {
      path: "/source/example/audio",
      mode: "whitelist",
      note: "",
      enabled: true,
    });
    expect(postJSONBody.mock.calls[0][1].mode).toBe("whitelist");
  });

  it("submits the default whole-subtree rule", async () => {
    postJSONBody.mockResolvedValue(stored());
    await catalogExclusionApi.create(7, { path: "/Example/Extras", mode: "whole", enabled: true });
    expect(postJSONBody.mock.calls[0][1]).toMatchObject({ path: "/Example/Extras", mode: "whole", enabled: true });
  });

  it("echoes the stored mode and falls back to whole for older rows", () => {
    expect(catalogExclusionMode(stored({ mode: "whitelist" }).mode)).toBe("whitelist");
    expect(catalogExclusionMode(stored().mode)).toBe("whole");
    expect(catalogExclusionMode("")).toBe("whole");
    expect(catalogExclusionMode(undefined)).toBe("whole");
  });
});
