import { describe, expect, it } from "vitest";

import { storedWorkCollectionLayout, workCardLayout, workCollectionStyle } from "./workCollectionLayoutModel";

const defaults = { mobileColumns: "auto", desktopColumns: "auto" } as const;

describe("stored work collection layout", () => {
  it("starts with full cards at both widths", () => {
    expect(storedWorkCollectionLayout(null, defaults)).toEqual({
      mobileColumns: "auto",
      desktopColumns: "auto",
      mobileCardMode: "full",
      desktopCardMode: "full",
    });
  });

  it("keeps a card mode per width and drops values it does not know", () => {
    const stored = JSON.stringify({ mobileCardMode: "mini", desktopCardMode: "dense", desktopColumns: 6 });
    expect(storedWorkCollectionLayout(stored, defaults)).toEqual({
      mobileColumns: "auto",
      desktopColumns: 6,
      mobileCardMode: "mini",
      desktopCardMode: "full",
    });
  });

  it("reads a stored compact switch as the compact phone mode only", () => {
    const layout = storedWorkCollectionLayout(JSON.stringify({ mobileColumns: 2, mobileCompact: true }), defaults);
    expect(layout.mobileCardMode).toBe("compact");
    expect(layout.desktopCardMode).toBe("full");
  });

  it("falls back to the defaults when the stored value is not JSON", () => {
    expect(storedWorkCollectionLayout("{", defaults).mobileCardMode).toBe("full");
  });
});

describe("work card layout", () => {
  it("uses the regular card for the full mode", () => {
    expect(workCardLayout("full", true, "auto")).toBeNull();
    expect(workCardLayout("full", false, 2)).toBeNull();
  });

  it("shows compact cards as rows in one phone column and as tiles elsewhere", () => {
    expect(workCardLayout("compact", false, "auto")).toBe("row");
    expect(workCardLayout("compact", false, 1)).toBe("row");
    expect(workCardLayout("compact", false, 2)).toBe("tile");
    expect(workCardLayout("compact", true, 1)).toBe("tile");
  });

  it("shows mini cards as covers at every width", () => {
    expect(workCardLayout("mini", false, 1)).toBe("cover");
    expect(workCardLayout("mini", true, "auto")).toBe("cover");
  });
});

describe("work collection grid", () => {
  it("pairs cover cards in automatic phone columns and keeps every other choice", () => {
    const regular = workCollectionStyle("auto", "auto") as Record<string, string>;
    const covers = workCollectionStyle("auto", "auto", "cover") as Record<string, string>;
    expect(regular["--mobile-grid-template"]).toContain("16rem");
    expect(covers["--mobile-grid-template"]).toBe("repeat(2, minmax(0, 1fr))");
    expect(covers["--desktop-grid-template"]).toBe(regular["--desktop-grid-template"]);
    expect(workCollectionStyle(1, 6, "cover")).toEqual(workCollectionStyle(1, 6));
  });
});
