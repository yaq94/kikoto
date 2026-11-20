import { expect, test, type Page } from "@playwright/test";
import { librarySourceFixture, remoteWorkFixture, remoteWorksResponseFixture } from "./fixtures/api";
import { mockRemoteSource } from "./fixtures/player-library";
import { syntheticWorkCode } from "../../src/test-support/workCode";

test.use({ screenshot: "off", trace: "off", video: "off" });

async function openCatalog(page: Page, works = exampleEntries) {
  await mockRemoteSource(page, () => {});
  await page.route("**/api/library-sources", (route) =>
    route.fulfill({ json: [librarySourceFixture({ sourceType: "openlist" })] }),
  );
  await page.route("**/api/remote-sources/1/works?*", (route) =>
    route.fulfill({ json: remoteWorksResponseFixture(works) }),
  );
  await page.goto("/library/source/example_remote_a");
}

const exampleEntries = [
  remoteWorkFixture({ catalogEntryId: 1, remoteId: "1", title: "Example Work A", primaryCode: "", remoteCode: "" }),
  remoteWorkFixture({ catalogEntryId: 2, remoteId: "2", title: "Example Work B", primaryCode: "", remoteCode: "" }),
];

test("each source card stays busy until its own queue request finishes and can retry a failure @desktop", async ({
  page,
}) => {
  let firstRequests = 0;
  let finishFirst!: () => void;
  const firstCanFinish = new Promise<void>((resolve) => {
    finishFirst = resolve;
  });
  await openCatalog(page);
  await page.route("**/api/source-catalog/1/tracks", async (route) => {
    firstRequests++;
    if (firstRequests === 1) {
      await firstCanFinish;
      await route.fulfill({ status: 503, json: { error: "Example request failure" } });
    } else {
      await route.fulfill({ json: { tracks: [] } });
    }
  });
  await page.route("**/api/source-catalog/2/tracks", (route) => route.fulfill({ json: { tracks: [] } }));
  const triggers = page.getByRole("button", { name: "Add every track to the playback queue" });
  await expect(triggers).toHaveCount(2);
  const first = triggers.nth(0);
  const second = triggers.nth(1);
  await first.click();
  await expect.poll(() => firstRequests).toBe(1);
  try {
    await second.click();
    await expect(page.getByText("This entry has no playable track", { exact: true })).toBeVisible();
    await expect(first).toBeDisabled({ timeout: 1_000 });
    await expect(second).toBeEnabled();
  } finally {
    finishFirst();
  }
  await expect(first).toBeEnabled();
  await first.click();
  await expect.poll(() => firstRequests).toBe(2);
});

test("a source card stays busy after leaving and returning to its source @desktop", async ({ page }) => {
  let requests = 0;
  let finishRequest!: () => void;
  const pendingRequest = new Promise<void>((resolve) => {
    finishRequest = resolve;
  });
  await openCatalog(page);
  await page.route("**/api/source-catalog/1/tracks", async (route) => {
    requests++;
    if (requests === 1) {
      await pendingRequest;
      await route.fulfill({ status: 503, json: { error: "Example request failure" } });
    } else {
      await route.fulfill({ json: { tracks: [] } });
    }
  });
  const queueButtons = page.getByRole("button", { name: "Add every track to the playback queue" });
  const first = queueButtons.nth(0);
  await expect(queueButtons).toHaveCount(2);
  await first.click();
  try {
    await expect.poll(() => requests).toBe(1);
    await page.getByRole("button", { name: "Local", exact: true }).click();
    await expect(queueButtons).toHaveCount(0);
    await page.getByRole("button", { name: "Example Remote A", exact: true }).click();
    await expect(queueButtons).toHaveCount(2);
    // If the control is incorrectly re-enabled, exercise the user's repeated click.
    if (await first.isEnabled()) {
      const duplicate = page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/source-catalog/1/tracks",
      );
      await first.click();
      await duplicate;
    }
    expect(requests).toBe(1);
    await expect(first).toBeDisabled();
    await expect(queueButtons.nth(1)).toBeEnabled();
  } finally {
    finishRequest();
  }
  await expect(first).toBeEnabled();
  await first.click();
  await expect.poll(() => requests).toBe(2);
});

test("an unknown circle stays separate from the source label @desktop", async ({ page }) => {
  await openCatalog(page);
  await expect(page.getByRole("heading", { name: "Example Work A" })).toBeVisible();
  await expect(page.getByText("Unknown circle", { exact: true })).toHaveCount(2, { timeout: 1_000 });
  await expect(page.getByRole("button", { name: "Unknown circle", exact: true })).toHaveCount(0);
});

test("source cards display numberless credits and resolve a numbered circle by product code @desktop", async ({
  page,
}) => {
  const code = syntheticWorkCode("RJ", 54);
  await openCatalog(page, [
    { ...exampleEntries[0], circle: "Example numberless circle", voiceActors: ["Example numberless voice"] },
    { ...exampleEntries[1], circle: "Example numbered circle", primaryCode: code, remoteCode: code },
  ]);
  await expect(page.getByText("Example numberless circle", { exact: true })).toBeVisible();
  await expect(page.getByText("Example numberless voice", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Example numberless circle", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Example numberless voice", exact: true })).toHaveCount(0);
  await page.route(`**/api/works/${code}/entity-links/resolve`, (route) =>
    route.fulfill({ json: { kind: "circle", route: "/circles/1", resolved: true, fetched: false } }),
  );
  const request = page.waitForRequest(
    (candidate) => new URL(candidate.url()).pathname === `/api/works/${code}/entity-links/resolve`,
  );
  await page.getByRole("button", { name: "Example numbered circle", exact: true }).click();
  expect((await request).postDataJSON()).toEqual({ kind: "circle", name: "Example numbered circle" });
});
