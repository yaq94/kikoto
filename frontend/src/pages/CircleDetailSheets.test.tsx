import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import "@/i18n";
import { CircleCatalogOptionsSheet } from "./CircleDetailSheets";

describe("CircleCatalogOptionsSheet", () => {
  it("keeps catalog search, card mode, and page size controls in the mobile options surface", () => {
    const rendered = renderToStaticMarkup(
      <CircleCatalogOptionsSheet
        open
        onClose={() => undefined}
        isSeriesView={false}
        selectionMode={false}
        availabilityFilter="all"
        onAvailabilityFilterChange={() => undefined}
        query="sample"
        onQueryChange={() => undefined}
        pageSize={24}
        pageSizeOptions={[24, 48, 96]}
        onPageSizeChange={() => undefined}
        mobileColumns={2}
        onMobileColumnsChange={() => undefined}
        cardMode="compact"
        onCardModeChange={() => undefined}
        onSelectWorks={() => undefined}
      />,
    );

    expect(rendered).toContain('aria-label="Search circle catalog works"');
    expect(rendered).toContain('value="sample"');
    expect(rendered).toContain('aria-label="Clear circle catalog search"');
    expect(rendered).toContain('aria-label="Catalog work page size"');
    expect(rendered).toContain("24 per page");
    expect(rendered).toContain("96 per page");
    expect(rendered).toMatch(/aria-label="Compact cards with fewer details" aria-pressed="true"/);
    expect(rendered).toMatch(/aria-label="Mini cards with the cover and title only" aria-pressed="false"/);
    expect(rendered).toContain('aria-pressed="true"');
  });
});
