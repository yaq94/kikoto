import { expect, test, type Page } from "@playwright/test";

import type { CatalogExclusion } from "../../src/features/sources/catalogExclusions";
import {
  librarySourceFixture,
  remoteWorkFixture,
  remoteWorksResponseFixture,
  workflowRunDetailFixture,
  workflowRunFixture,
  workflowRunsPageFixture,
} from "./fixtures/api";
import { mockRemoteSource } from "./fixtures/player-library";

test.use({ screenshot: "off", trace: "off", video: "off" });

async function fixture(page: Page, canObserve = true) {
  const state = {
    status: "queued",
    runId: 0,
    rules: [] as CatalogExclusion[],
    writes: 0,
    retries: 0,
    saveFails: false,
    readFails: false,
    canManage: true,
    workflowReads: 0,
  };
  await mockRemoteSource(page, () => {}, canObserve ? {} : { permissions: ["library:read", "sources:write"] });
  await page.route("**/api/library-sources", (route) =>
    route.fulfill({ json: [librarySourceFixture({ sourceType: "openlist" })] }),
  );
  await page.route("**/api/remote-sources/1/catalog/index*", (route) =>
    route.fulfill({ json: { status: "idle", options: null } }),
  );
  await page.route("**/api/remote-sources/1/works?*", (route) =>
    route.fulfill({
      json: remoteWorksResponseFixture([
        remoteWorkFixture({
          title: state.status === "succeeded" ? "Example updated catalog" : "Example saved catalog",
        }),
      ]),
    }),
  );
  await page.route("**/api/remote-sources/1/catalog/exclusions*", async (route) => {
    if (route.request().method() === "GET")
      return state.readFails
        ? route.fulfill({ status: 503, json: { error: "private upstream detail" } })
        : route.fulfill({ json: state.rules });
    state.writes++;
    state.runId = 41;
    state.rules = [
      {
        id: 1,
        path: "/Example/Extras",
        mode: "whole",
        action: "exclude",
        note: "",
        enabled: true,
        createdAt: "2026-01-01",
      },
    ];
    return state.saveFails
      ? route.fulfill({ status: 500, json: { error: "private upstream detail" } })
      : route.fulfill({ status: 201, json: { ...state.rules[0], runId: 41 } });
  });
  await page.route("**/api/remote-sources/1/catalog/exclusions/1", (route) => {
    state.writes++;
    state.runId = 41;
    state.rules = [];
    return route.fulfill({ json: { ok: true, path: "/Example/Extras", mode: "whole", runId: 41 } });
  });
  const run = () => workflowRunFixture({ id: 41, workflowCode: "source_catalog_exclusion", status: state.status });
  await page.route("**/api/workflow-runs?*", (route) => {
    if (new URL(route.request().url()).searchParams.get("workflowCode") === "source_catalog_exclusion")
      state.workflowReads++;
    return route.fulfill({ json: workflowRunsPageFixture(state.runId ? [run()] : []) });
  });
  await page.route("**/api/workflow-runs/41", (route) =>
    route.fulfill({
      json: workflowRunDetailFixture(run(), {
        canManage: state.canManage,
        nodeRuns: [
          {
            id: 1,
            nodeId: "reconcile",
            nodeType: "source_catalog_exclusion",
            displayName: "Reconcile catalog exclusion",
            position: 1,
            status: state.status,
            inputJson: '{"sourceId":1}',
            outputJson: "{}",
            errorMessage: "",
            startedAt: "",
            finishedAt: "",
            createdAt: "2026-01-01",
          },
        ],
      }),
    }),
  );
  await page.route("**/api/workflow-runs/41/retry", (route) => {
    state.retries++;
    state.status = "queued";
    return route.fulfill({ status: 202, json: { runId: 41, status: "queued", message: "" } });
  });
  return state;
}

test("saved exclusion refreshes the catalog only after task success @desktop", async ({ page }) => {
  const state = await fixture(page);
  await page.goto("/library/source/example_remote_a");
  const region = page.getByRole("region", { name: "Directory exclusions", exact: true });
  await region.getByLabel("Directory path", { exact: true }).fill("/Example/Extras");
  await region.getByRole("button", { name: "Add exclusion", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Rule saved. Waiting to update the catalog.");
  await expect(page.getByRole("heading", { name: "Example saved catalog", exact: true })).toHaveCount(1);
  await page.reload();
  await expect(region.getByRole("status")).toContainText("Rule saved. Waiting to update the catalog.");
  expect(state.writes).toBe(1);
  state.status = "succeeded";
  await expect(region.getByRole("status")).toContainText("Catalog updated.");
  await expect(page.getByRole("heading", { name: "Example updated catalog", exact: true })).toHaveCount(1);
  expect(state.writes).toBe(1);
});

test("reopening recovers a failed saved operation and retries the same run @desktop", async ({ page }) => {
  const state = await fixture(page);
  state.saveFails = true;
  state.status = "failed";
  await page.goto("/library/source/example_remote_a");
  const region = page.getByRole("region", { name: "Directory exclusions", exact: true });
  await region.getByLabel("Directory path", { exact: true }).fill("/Example/Extras");
  await region.getByRole("button", { name: "Add exclusion", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("catalog update failed");
  await expect(region).not.toContainText("private upstream detail");
  await page.reload();
  await expect(region.getByRole("status")).toContainText("catalog update failed");
  await region.getByRole("button", { name: "Retry catalog update", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Waiting to update");
  state.status = "succeeded";
  await expect(page.getByRole("heading", { name: "Example updated catalog", exact: true })).toHaveCount(1);
  expect(state.writes).toBe(1);
  expect(state.retries).toBe(1);
});

test("a cancelled operation respects task ownership @desktop", async ({ page }) => {
  const state = await fixture(page);
  state.runId = 41;
  state.status = "cancelled";
  state.canManage = false;
  state.rules = [
    {
      id: 1,
      path: "/Example/Extras",
      mode: "whole",
      action: "exclude",
      note: "",
      enabled: true,
      createdAt: "2026-01-01",
    },
  ];
  await page.goto("/library/source/example_remote_a");
  const region = page.getByRole("region", { name: "Directory exclusions", exact: true });
  await expect(region.getByRole("status")).toContainText("catalog update was cancelled");
  await expect(region.getByRole("button", { name: "Retry catalog update", exact: true })).toHaveCount(0);
  await expect(region).toContainText("/Example/Extras");
  await expect(region.getByRole("button", { name: "Add exclusion", exact: true })).toBeDisabled();
  expect(state.retries).toBe(0);
});

test("status and rule read failures keep saved rules and recover through refresh @desktop", async ({ page }) => {
  const state = await fixture(page);
  await page.goto("/library/source/example_remote_a");
  const region = page.getByRole("region", { name: "Directory exclusions", exact: true });
  await region.getByLabel("Directory path", { exact: true }).fill("/Example/Extras");
  await region.getByRole("button", { name: "Add exclusion", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Waiting to update");
  await expect(region).toContainText("/Example/Extras");
  state.readFails = true;
  await page.route("**/api/workflow-runs/41", (route) =>
    route.fulfill({ status: 503, json: { error: "private upstream detail" } }),
  );
  await expect(region.getByRole("alert")).toContainText("Cannot check the catalog update");
  await region.getByRole("button", { name: "Refresh progress", exact: true }).click();
  await expect(region).toContainText("The request could not be confirmed");
  await expect(region).toContainText("/Example/Extras");
  await expect(region).not.toContainText("private upstream detail");
  state.readFails = false;
  await page.unroute("**/api/workflow-runs/41");
  await page.route("**/api/workflow-runs/41", (route) =>
    route.fulfill({
      json: workflowRunDetailFixture(
        workflowRunFixture({ id: 41, workflowCode: "source_catalog_exclusion", status: "succeeded" }),
        {
          canManage: true,
          nodeRuns: [
            {
              id: 1,
              nodeId: "reconcile",
              nodeType: "source_catalog_exclusion",
              displayName: "Reconcile",
              position: 1,
              status: "succeeded",
              inputJson: '{"sourceId":1}',
              outputJson: "{}",
              errorMessage: "",
              startedAt: "",
              finishedAt: "",
              createdAt: "",
            },
          ],
        },
      ),
    }),
  );
  await region.getByRole("button", { name: "Refresh progress", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Catalog updated.");
  expect(state.writes).toBe(1);
});

test("a late task response cannot update another source after navigation @desktop", async ({ page }) => {
  const state = await fixture(page);
  await page.route("**/api/library-sources", (route) =>
    route.fulfill({
      json: [
        librarySourceFixture({ sourceType: "openlist" }),
        librarySourceFixture({
          id: 2,
          code: "example_remote_b",
          displayName: "Example Remote B",
          sourceType: "openlist",
        }),
      ],
    }),
  );
  await page.route("**/api/remote-sources/2/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    return route.fulfill({
      json: path.endsWith("/exclusions")
        ? []
        : path.endsWith("/index")
          ? { status: "idle", options: null }
          : remoteWorksResponseFixture([]),
    });
  });
  await page.goto("/library/source/example_remote_a");
  const region = page.getByRole("region", { name: "Directory exclusions", exact: true });
  await region.getByLabel("Directory path", { exact: true }).fill("/Example/Extras");
  await region.getByRole("button", { name: "Add exclusion", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Waiting to update");
  let release!: () => void;
  let requested!: () => void;
  const pending = new Promise<void>((resolve) => {
    requested = resolve;
  });
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/workflow-runs/41", async (route) => {
    requested();
    await held;
    await route.fallback();
  });
  await pending;
  await page.getByRole("button", { name: "Example Remote B", exact: true }).click();
  state.status = "succeeded";
  release();
  await expect(page).toHaveURL(/example_remote_b$/);
  await expect(region).toContainText("No directories are excluded.");
  await expect(region).not.toContainText("/Example/Extras");
  await expect(region).not.toContainText("Catalog updated.");
  expect(state.writes).toBe(1);
});

test("removal observes synchronous task success and refreshes source content @desktop", async ({ page }) => {
  const state = await fixture(page);
  state.rules = [
    {
      id: 1,
      path: "/Example/Extras",
      mode: "whole",
      action: "exclude",
      note: "",
      enabled: true,
      createdAt: "2026-01-01",
    },
  ];
  await page.goto("/library/source/example_remote_a");
  const region = page.getByRole("region", { name: "Directory exclusions", exact: true });
  await expect(region.getByRole("button", { name: "Remove exclusion", exact: true })).toBeEnabled();
  state.status = "succeeded";
  await region.getByRole("button", { name: "Remove exclusion", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Catalog updated.");
  await expect(region).toContainText("No directories are excluded.");
  await expect(page.getByRole("heading", { name: "Example updated catalog", exact: true })).toHaveCount(1);
  expect(state.writes).toBe(1);
});

test("saving without workflow permission never claims the catalog is updated @desktop", async ({ page }) => {
  const state = await fixture(page, false);
  await page.goto("/library/source/example_remote_a");
  const region = page.getByRole("region", { name: "Directory exclusions", exact: true });
  await region.getByLabel("Directory path", { exact: true }).fill("/Example/Extras");
  await region.getByRole("button", { name: "Add exclusion", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("You need workflow access");
  await expect(region.getByRole("button", { name: "Retry catalog update", exact: true })).toHaveCount(0);
  expect(state.workflowReads).toBe(0);
});
