import { describe, expect, it } from "vitest";
import { metadataLanguageScopes } from "./metadataLanguageScope";

describe("metadata editor language scopes", () => {
  it("labels the actual canonical language as Original", () => {
    const scopes = metadataLanguageScopes(
      {
        metadataPresentation: {
          defaultVariantKey: "en",
          variants: [
            { key: "en", language: "en-us", title: "Example original", tags: [], origin: true },
            { key: "ja", language: "ja-jp", title: "Example Japanese", tags: [], origin: false },
          ],
        },
      },
      "en",
    );
    expect(scopes.map((scope) => scope.label)).toEqual(["All", "Original · English", "Japanese"]);
    expect(scopes[1].origin).toBe(true);
  });

  it("uses only the origin scope when a work has one variant", () => {
    const scopes = metadataLanguageScopes({
      metadataPresentation: {
        defaultVariantKey: "origin",
        variants: [{ key: "origin", language: "", title: "Example", tags: [], origin: true }],
      },
    });
    expect(scopes).toHaveLength(1);
    expect(scopes[0].key).toBe("edition:origin");
  });
});
