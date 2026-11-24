import { expect, test, type Page } from "@playwright/test";
import type { CatalogEntryDetail, CatalogTrack, CurrentUser } from "../../src/lib/api";
import { authenticatedStateFixture, remoteTrackFixture, rolePermissions } from "./fixtures/api";
import { mockApplication, persistedTrack, seedPlayer, silentWav } from "./fixtures/player-library";

test.use({ screenshot: "off", trace: "off", video: "off" });

function catalogDetail(overrides: Partial<CatalogEntryDetail> = {}): CatalogEntryDetail {
  return {
    catalogEntryId: 1,
    sourceId: 1,
    sourceName: "Example Remote A",
    primaryCode: "",
    title: "Example catalog work",
    workId: null,
    seriesId: null,
    status: "available",
    checkedAt: "2026-11-24T00:00:00Z",
    checkPending: false,
    relocationCandidates: [],
    personalRating: 3,
    personalNote: "Example saved note",
    ...overrides,
  };
}

function catalogTrack(title: string): CatalogTrack {
  return {
    ...remoteTrackFixture({ title, streamUrl: `/api/source-catalog/1/media?path=${encodeURIComponent(title)}` }),
    path: title,
    children: [],
  };
}

async function mockCatalogApplication(page: Page, role: CurrentUser["role"] = "admin") {
  await mockApplication(page, undefined, false, 1, 0, [], undefined, {
    authenticated: true,
    permissions: rolePermissions[role],
  });
  await page.route("**/api/auth/me", (route) =>
    route.fulfill({ json: authenticatedStateFixture({ role, permissions: rolePermissions[role], devMode: false }) }),
  );
  await page.route("**/api/shared-associations?*", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/work-matching?*", (route) =>
    route.fulfill({ json: { status: "no_match", rule: "", candidateCount: 0, candidates: [] } }),
  );
}

test("catalog refresh failure preserves the directory, private draft and playing track, then retries @desktop", async ({
  page,
}) => {
  await page.clock.setFixedTime(new Date("2026-11-24T00:00:01Z"));
  await mockCatalogApplication(page);
  await seedPlayer(page, persistedTrack, 1);
  const wav = silentWav(180);
  await page.route("**/api/media/1/stream*", (route) => route.fulfill({ contentType: "audio/wav", body: wav }));
  let detail = catalogDetail();
  let tracks = [catalogTrack("saved.wav")];
  await page.route("**/api/source-catalog/1", (route) => route.fulfill({ json: detail }));
  await page.route("**/api/source-catalog/1/tracks?*", (route) => route.fulfill({ json: { tracks } }));
  let releaseRefresh!: () => void;
  const refreshGate = new Promise<void>((resolve) => {
    releaseRefresh = resolve;
  });
  let refreshes = 0;
  await page.route("**/api/source-catalog/1/refresh", async (route) => {
    refreshes += 1;
    if (refreshes === 1) {
      await refreshGate;
      detail = { ...detail, status: "unavailable" };
      return route.fulfill({ status: 502, json: { error: "Source temporarily unavailable", retryable: true } });
    }
    detail = { ...detail, status: "available" };
    tracks = [catalogTrack("saved.wav"), catalogTrack("recovered.wav")];
    return route.fulfill({ json: { status: "available" } });
  });
  try {
    await page.goto("/catalog/1");
    const catalog = page.getByRole("region", { name: "Source catalog work", exact: true });
    await expect(catalog.getByRole("heading", { level: 1 })).toHaveText("Example catalog work");
    await expect(catalog.getByRole("button", { name: /^saved\.wav/ })).toBeVisible();
    const note = catalog.getByLabel("Personal note", { exact: true });
    await note.fill("Example private draft");
    await page.getByRole("button", { name: "Play", exact: true }).click();
    const audio = page.locator("audio");
    await expect(audio).toHaveJSProperty("paused", false);
    await expect.poll(() => audio.evaluate((el: HTMLAudioElement) => el.currentTime)).toBeGreaterThan(0.1);
    const originalAudio = await audio.elementHandle();
    const before = await audio.evaluate((el: HTMLAudioElement) => ({ src: el.currentSrc, time: el.currentTime }));
    const refresh = catalog.getByRole("button", { name: "Check sources", exact: true });
    await refresh.click();
    await expect.poll(() => refreshes).toBe(1);
    await expect(catalog.getByRole("button", { name: /^saved\.wav/ })).toBeVisible();
    await expect(note).toHaveValue("Example private draft");
    await expect(catalog.getByRole("button", { name: "Checking sources", exact: true })).toBeDisabled();
    releaseRefresh();
    await expect(catalog.getByRole("alert")).toBeVisible();
    await expect(catalog.getByRole("status", { name: "Source status", exact: true })).toContainText(
      "Source temporarily unavailable",
    );
    await expect(catalog.getByRole("button", { name: /^saved\.wav/ })).toBeVisible();
    await expect(note).toHaveValue("Example private draft");
    await refresh.click();
    await expect(catalog.getByRole("button", { name: /^recovered\.wav/ })).toBeVisible();
    await expect(catalog.getByRole("alert")).toHaveCount(0);
    await expect(note).toHaveValue("Example private draft");
    expect(refreshes).toBe(2);
    expect(await originalAudio!.evaluate((el) => el.isConnected && el === document.querySelector("audio"))).toBe(true);
    await expect(audio).toHaveJSProperty("paused", false);
    expect(await audio.evaluate((el: HTMLAudioElement) => el.currentSrc)).toBe(before.src);
    await expect.poll(() => audio.evaluate((el: HTMLAudioElement) => el.currentTime)).toBeGreaterThan(before.time);
  } finally {
    releaseRefresh();
  }
});

test("a contributor sees the completed background catalog check without a write request @desktop", async ({ page }) => {
  await page.clock.install({ time: new Date("2026-11-24T00:00:01Z") });
  await mockCatalogApplication(page, "contributor");
  let completed = false;
  let entryReads = 0;
  let refreshes = 0;
  await page.route("**/api/source-catalog/1", (route) => {
    entryReads += 1;
    return route.fulfill({
      json: catalogDetail({
        status: completed ? "available" : "unavailable",
        checkedAt: completed ? "2026-11-24T00:00:02Z" : "2026-11-20T00:00:00Z",
        checkPending: !completed,
      }),
    });
  });
  await page.route("**/api/source-catalog/1/tracks?*", (route) =>
    route.fulfill({ json: { tracks: [catalogTrack(completed ? "recovered.wav" : "saved.wav")] } }),
  );
  await page.route("**/api/source-catalog/1/refresh", (route) => {
    refreshes += 1;
    return route.fulfill({ status: 403, json: { error: "Permission denied" } });
  });
  await page.goto("/catalog/1");
  const catalog = page.getByRole("region", { name: "Source catalog work", exact: true });
  await expect(catalog.getByRole("button", { name: /^saved\.wav/ })).toBeVisible();
  await expect(catalog.getByRole("status", { name: "Source status", exact: true })).toContainText(
    "Source temporarily unavailable",
  );
  const note = catalog.getByLabel("Personal note", { exact: true });
  await note.fill("Example contributor draft");
  completed = true;
  await page.clock.runFor(3_000);
  await expect(catalog.getByRole("button", { name: /^recovered\.wav/ })).toBeVisible({ timeout: 3_000 });
  await expect(catalog.getByRole("status", { name: "Source status", exact: true })).toHaveCount(0);
  await expect(note).toHaveValue("Example contributor draft");
  await expect(catalog.getByRole("button", { name: "Reload saved directory", exact: true })).toBeEnabled();
  const settledReads = entryReads;
  await page.clock.runFor(30_000);
  expect(entryReads).toBe(settledReads);
  expect(refreshes).toBe(0);
});

test("a pending catalog check stops automatic reads and can be reloaded by a contributor @desktop", async ({
  page,
}) => {
  await page.clock.install({ time: new Date("2026-11-24T00:00:01Z") });
  await mockCatalogApplication(page, "contributor");
  let completed = false;
  let entryReads = 0;
  await page.route("**/api/source-catalog/1", (route) => {
    entryReads += 1;
    return route.fulfill({ json: catalogDetail({ checkPending: !completed }) });
  });
  await page.route("**/api/source-catalog/1/tracks?*", (route) =>
    route.fulfill({ json: { tracks: [catalogTrack(completed ? "recovered.wav" : "saved.wav")] } }),
  );
  await page.goto("/catalog/1");
  const catalog = page.getByRole("region", { name: "Source catalog work", exact: true });
  await expect(catalog.getByRole("button", { name: /^saved\.wav/ })).toBeVisible();
  await expect(catalog.getByRole("button", { name: "Checking sources", exact: true })).toBeDisabled();
  const note = catalog.getByLabel("Personal note", { exact: true });
  await note.fill("Example pending draft");
  const openedReads = entryReads;
  await page.clock.runFor(30_000);
  await expect(catalog.getByRole("button", { name: "Reload saved directory", exact: true })).toBeEnabled();
  expect(entryReads - openedReads).toBeGreaterThan(0);
  expect(entryReads - openedReads).toBeLessThanOrEqual(10);
  const stoppedReads = entryReads;
  await page.clock.runFor(30_000);
  expect(entryReads).toBe(stoppedReads);
  await expect(catalog.getByRole("button", { name: /^saved\.wav/ })).toBeVisible();
  await expect(note).toHaveValue("Example pending draft");
  completed = true;
  await catalog.getByRole("button", { name: "Reload saved directory", exact: true }).click();
  await expect(catalog.getByRole("button", { name: /^recovered\.wav/ })).toBeVisible();
  await expect(note).toHaveValue("Example pending draft");
});

test("a reader reloads the saved catalog without tracking or source-writing permission @desktop", async ({ page }) => {
  await page.clock.install({ time: new Date("2026-11-24T00:00:01Z") });
  await mockCatalogApplication(page, "user");
  let entryReads = 0;
  let refreshes = 0;
  let reloaded = false;
  await page.route("**/api/source-catalog/1", (route) => {
    entryReads += 1;
    return route.fulfill({ json: catalogDetail({ workId: 1, checkPending: true }) });
  });
  await page.route("**/api/source-catalog/1/tracks?*", (route) =>
    route.fulfill({ json: { tracks: [catalogTrack(reloaded ? "new-saved.wav" : "saved.wav")] } }),
  );
  await page.route("**/api/source-catalog/1/refresh", (route) => {
    refreshes += 1;
    return route.fulfill({ status: 403, json: { error: "Permission denied" } });
  });
  await page.goto("/catalog/1");
  const catalog = page.getByRole("region", { name: "Source catalog work", exact: true });
  await expect(catalog.getByRole("button", { name: /^saved\.wav/ })).toBeVisible();
  await expect(catalog.getByRole("button", { name: "Check sources", exact: true })).toHaveCount(0);
  const openedReads = entryReads;
  await page.clock.runFor(30_000);
  expect(entryReads).toBe(openedReads);
  reloaded = true;
  await catalog.getByRole("button", { name: "Reload saved directory", exact: true }).click();
  await expect(catalog.getByRole("button", { name: /^new-saved\.wav/ })).toBeVisible();
  expect(refreshes).toBe(0);
});

test("a delayed opening directory cannot replace the result of a completed catalog refresh @desktop", async ({
  page,
}) => {
  await mockCatalogApplication(page);
  await page.route("**/api/source-catalog/1", (route) => route.fulfill({ json: catalogDetail() }));
  let releaseOpening!: () => void;
  const openingGate = new Promise<void>((resolve) => {
    releaseOpening = resolve;
  });
  let trackReads = 0;
  let refreshed = false;
  await page.route("**/api/source-catalog/1/tracks?*", async (route) => {
    trackReads += 1;
    if (!refreshed) {
      await openingGate;
      return route.fulfill({ json: { tracks: [catalogTrack("old-opening.wav")] } });
    }
    return route.fulfill({ json: { tracks: [catalogTrack("refreshed.wav")] } });
  });
  await page.route("**/api/source-catalog/1/refresh", (route) => {
    refreshed = true;
    return route.fulfill({ json: { status: "available" } });
  });
  try {
    await page.goto("/catalog/1");
    const catalog = page.getByRole("region", { name: "Source catalog work", exact: true });
    await expect(catalog.getByRole("heading", { level: 1 })).toHaveText("Example catalog work");
    await expect.poll(() => trackReads).toBeGreaterThan(0);
    await catalog.getByRole("button", { name: "Check sources", exact: true }).click();
    await expect(catalog.getByRole("button", { name: /^refreshed\.wav/ })).toBeVisible();
    const openingResponse = page.waitForResponse(
      (response) => response.url().includes("/api/source-catalog/1/tracks") && response.status() === 200,
    );
    releaseOpening();
    await openingResponse;
    await expect(catalog.getByRole("button", { name: /^old-opening\.wav/ })).toHaveCount(0, { timeout: 1_000 });
    await expect(catalog.getByRole("button", { name: /^refreshed\.wav/ })).toBeVisible({ timeout: 1_000 });
  } finally {
    releaseOpening();
  }
});
