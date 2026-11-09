import { expect, test } from "@playwright/test";
import type { CatalogIndexProgress } from "../../src/lib/api";
import { librarySourceFixture, remoteWorkFixture, remoteWorksResponseFixture } from "./fixtures/api";
import { mockRemoteSource } from "./fixtures/player-library";

test.use({ screenshot: "off", trace: "off", video: "off" });

for (const status of ["complete", "partial"]) {
  test(`source cards refresh when publication finishes ${status} with the same indexed count @desktop`, async ({
    page,
  }) => {
    await mockRemoteSource(page, () => {});
    const progress: CatalogIndexProgress = {
      runId: 1,
      status: "running",
      pauseReason: "",
      options: {
        concurrency: 2,
        intervalMs: 100,
        maxResponseBytes: 2097152,
        pageSize: 100,
        pagesPerBatch: 8,
        maxPagesPerDirectory: 1000,
        minFreeBytes: 0,
      },
      requests: 1,
      directories: 1,
      completedDirectories: 1,
      pendingDirectories: 0,
      failedDirectories: 0,
      restrictedDirectories: 0,
      unconfirmedEmptyDirectories: 0,
      observedEntries: 1,
      indexedEntries: 1,
      startedAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-01T00:00:01Z",
      partitions: [],
      partitionTotal: 0,
      details: [],
      detailsTotal: 0,
      sidecarFailures: { stopped: 0, temporarilyUnavailable: 0, unreadable: 0, requestBudget: 0 },
    };
    await page.route("**/api/library-sources", (route) =>
      route.fulfill({ json: [librarySourceFixture({ sourceType: "openlist" })] }),
    );
    await page.route("**/api/remote-sources/1/catalog/index*", (route) => route.fulfill({ json: progress }));
    await page.route("**/api/remote-sources/1/catalog/exclusions*", (route) => route.fulfill({ json: [] }));
    await page.route("**/api/remote-sources/1/works?*", (route) =>
      route.fulfill({
        json: remoteWorksResponseFixture(
          progress.status === "running" ? [] : [remoteWorkFixture({ title: "Example published work" })],
        ),
      }),
    );
    await page.goto("/library/source/example_remote_a");
    const region = page.getByRole("region", { name: "Full source index", exact: true });
    await expect(region.getByRole("status")).toContainText("Indexing");
    await expect(page.getByText("No remote works on this page.", { exact: true })).toBeVisible();
    progress.status = status;
    progress.finishedAt = "2026-01-01T00:00:02Z";
    await region.getByRole("button", { name: "Refresh progress", exact: true }).click();
    await expect(region.getByRole("status")).toContainText(status === "complete" ? "Complete" : "Incomplete coverage");
    await expect(page.getByRole("heading", { name: "Example published work", exact: true })).toBeVisible();
  });
}
