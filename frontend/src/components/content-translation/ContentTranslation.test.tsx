import { describe, expect, it } from "vitest";

import type { ContentTranslationStatus } from "@/lib/contentTranslationApi";

import { contentTranslationDisplayState, createCacheVersionNotifier } from "./ContentTranslation";

/**
 * The provider used to pass an inline arrow as `noteVersion`, so every render
 * gave the effect a new dependency and the effect could feed its own render.
 * The notifier is now created once; these assertions pin the state rule that
 * makes a repeated call a no-op for React (`Object.is(current, current)`).
 */
describe("createCacheVersionNotifier", () => {
  function harness() {
    let state = "";
    const writes: string[] = [];
    const setVersion = (update: (current: string) => string) => {
      const next = update(state);
      if (!Object.is(next, state)) writes.push(next);
      state = next;
    };
    return { note: createCacheVersionNotifier(setVersion), writes, state: () => state };
  }

  it("ignores an absent version and keeps the last known one", () => {
    const { note, writes, state } = harness();
    note(undefined);
    note("");
    expect(writes).toEqual([]);
    expect(state()).toBe("");
    note("model-a|v1");
    expect(writes).toEqual(["model-a|v1"]);
  });

  it("does not change state when the same version arrives twice", () => {
    const { note, writes, state } = harness();
    note("model-a|v1");
    note("model-a|v1");
    expect(writes).toEqual(["model-a|v1"]);
    expect(state()).toBe("model-a|v1");
    note("model-a|v2");
    expect(writes).toEqual(["model-a|v1", "model-a|v2"]);
  });

  it("returns one stable function for the provider to memoise", () => {
    const { note } = harness();
    const reference = note;
    note("model-a|v1");
    expect(note).toBe(reference);
  });
});

describe("contentTranslationDisplayState", () => {
  it("stays original when the field cannot be translated", () => {
    expect(contentTranslationDisplayState({ enabled: false, timedOut: true })).toBe("original");
  });

  it("does not claim progress for a field that has not asked yet", () => {
    expect(contentTranslationDisplayState({ enabled: true, visible: false, timedOut: false })).toBe("original");
    expect(contentTranslationDisplayState({ enabled: true, visible: false, timedOut: true })).toBe("original");
  });

  it("reports progress only until the bound passes", () => {
    expect(contentTranslationDisplayState({ enabled: true, timedOut: false })).toBe("pending");
    expect(contentTranslationDisplayState({ enabled: true, timedOut: true })).toBe("timeout");
  });

  it("keeps a settled answer even after the bound passed", () => {
    expect(contentTranslationDisplayState({ enabled: true, status: "translated", timedOut: true })).toBe("translated");
    expect(contentTranslationDisplayState({ enabled: true, status: "failed", timedOut: true })).toBe("failed");
  });

  it("passes every settled status through unchanged", () => {
    const settled: ContentTranslationStatus[] = ["original", "existing", "native", "translated", "failed"];
    for (const status of settled) {
      expect(contentTranslationDisplayState({ enabled: true, status, timedOut: true })).toBe(status);
    }
  });
});
