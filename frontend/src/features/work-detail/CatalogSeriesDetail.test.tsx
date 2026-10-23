import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import "@/i18n";
import type { CatalogSeriesEpisode } from "@/lib/api";
import { CatalogSeriesEpisodeItem, openCatalogSeriesEpisode } from "./CatalogSeriesDetail";

vi.mock("@/app/workDetailNavigation", () => ({ openWorkDetail: vi.fn() }));

import { openWorkDetail } from "@/app/workDetailNavigation";

const missingEpisode: CatalogSeriesEpisode = {
  catalogEntryId: 9,
  sourceId: 3,
  sourceName: "Example source",
  primaryCode: "RJ000009",
  title: "Missing episode",
  workId: null,
  status: "missing",
  position: 2,
};

function renderEpisode(episode: CatalogSeriesEpisode) {
  return renderToStaticMarkup(
    <CatalogSeriesEpisodeItem episode={episode} seriesId={17} seriesTitle="Example series" />,
  );
}

describe("CatalogSeriesEpisodeItem", () => {
  it("marks a missing episode without removing its open action", () => {
    const missing = renderEpisode(missingEpisode);
    expect(missing).toContain('data-episode-status="missing"');
    expect(missing).toContain("Missing from source");
    expect(missing).toContain("Open episode");
    expect(missing).not.toMatch(/<button[^>]*\sdisabled(?:=""|>)/);

    const available = renderEpisode({ ...missingEpisode, catalogEntryId: 10, status: "available" });
    expect(available).toContain('data-episode-status="available"');
    expect(available).not.toContain("Missing from source");
  });

  it("opens a missing episode with the series return context", () => {
    vi.mocked(openWorkDetail).mockClear();
    vi.mocked(openWorkDetail).mockReturnValue(true);

    expect(openCatalogSeriesEpisode(17, "Example series", missingEpisode.catalogEntryId)).toBe(true);
    expect(openWorkDetail).toHaveBeenCalledWith(
      { kind: "catalog", catalogEntryId: missingEpisode.catalogEntryId },
      { returnTo: "/catalog-series/17", returnLabel: "Example series" },
    );
  });
});
