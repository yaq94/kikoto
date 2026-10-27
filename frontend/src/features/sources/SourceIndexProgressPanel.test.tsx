import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import "@/i18n";
import type { CatalogIndexProgress } from "@/lib/api";
import { SourceIndexProgressPanel } from "./SourceIndexProgressPanel";

function progress(overrides: Partial<CatalogIndexProgress> = {}): CatalogIndexProgress {
  return {
    runId: 53,
    status: "running",
    pauseReason: "",
    options: {
      concurrency: 2,
      intervalMs: 500,
      maxResponseBytes: 1048576,
      pageSize: 100,
      pagesPerBatch: 4,
      maxPagesPerDirectory: 200,
      minFreeBytes: 0,
    },
    requests: 14852,
    directories: 5,
    completedDirectories: 3,
    pendingDirectories: 1,
    failedDirectories: 1,
    restrictedDirectories: 0,
    unconfirmedEmptyDirectories: 0,
    observedEntries: 389854,
    indexedEntries: 41572,
    startedAt: "2026-10-20 20:08:09",
    updatedAt: "2026-10-27 11:45:27",
    partitions: [
      {
        path: "/asmr/alpha",
        directories: 2,
        completed: 1,
        failed: 1,
        restricted: 0,
        unconfirmedEmpty: 0,
      },
    ],
    partitionTotal: 1,
    details: [{ path: "/asmr/alpha/broken", status: "failed", nextPage: 1, errorCode: "upstream_unavailable" }],
    detailsTotal: 140,
    sidecarFailures: { stopped: 0, temporarilyUnavailable: 0, unreadable: 0, requestBudget: 0 },
    ...overrides,
  };
}

function render(overrides: Partial<CatalogIndexProgress> = {}, page = 1, pages = 1) {
  return renderToStaticMarkup(
    <SourceIndexProgressPanel
      progress={progress(overrides)}
      page={page}
      pages={pages}
      onPageChange={() => undefined}
    />,
  );
}

/** The disclosure element, without assuming where in the markup it starts. */
function disclosure(html: string) {
  const match = html.match(/<details[^>]*>/u);
  if (!match) throw new Error("no disclosure rendered");
  return match[0];
}

describe("SourceIndexProgressPanel", () => {
  it("renders a closed disclosure whose summary carries the status and counts", () => {
    const html = render();

    // A native disclosure is folded exactly when it has no `open` attribute;
    // that attribute is the only state the browser exposes for it.
    expect(disclosure(html)).not.toMatch(/open/u);
    expect(html).toContain("3 complete · 1 pending · 1 failed · 0 unconfirmed empty");
  });

  it("keeps the partition table and directory list inside the folded body", () => {
    const html = render();
    const body = html.slice(html.indexOf("</summary>"));

    expect(body).toContain("Partitions");
    expect(body).toContain("/asmr/alpha");
    expect(body).toContain("1 / 2");
    expect(body).toContain("/asmr/alpha/broken");
  });

  it("offers paging only when the run spans more than one page", () => {
    expect(render({}, 2, 3)).toContain("2 / 3");
    expect(render()).not.toContain("Previous page");
  });

  it("shows a settled run's finish time in the body", () => {
    const html = render({ status: "partial", finishedAt: "2026-10-27 11:45:27" });

    expect(html).toContain("Incomplete coverage");
    expect(html).toContain("Finished 2026-10-27 11:45:27");
  });
});
