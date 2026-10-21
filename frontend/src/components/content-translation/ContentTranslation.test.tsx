import { describe, expect, it } from "vitest";

import { createCacheVersionNotifier } from "./ContentTranslation";

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
