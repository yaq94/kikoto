import { expect, test, type Page } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

async function chooseLanguage(page: Page, language: string) {
  await page.getByRole("button", { name: "User menu", exact: true }).click();
  await page.getByRole("combobox", { name: "Preferred metadata language", exact: true }).click();
  await page.getByRole("listbox").getByRole("option", { name: language, exact: true }).click();
  await page.keyboard.press("Escape");
}

test("the current build combines numbered and numberless content, translation, review and association @desktop", async ({
  page,
}, testInfo) => {
  test.setTimeout(120_000);
  const backend = process.env.INTEGRATED_ACCEPTANCE_URL;
  test.skip(!backend, "Start integrated-acceptance-fixture with the current production frontend build.");
  const api = async (method: "GET" | "POST" | "PATCH", path: string, data?: unknown, status = 200) => {
    const response = await page.request.fetch(`${backend}${path}`, { method, data });
    expect(response.status(), `${method} ${path}: ${await response.text()}`).toBe(status);
    return response.json();
  };
  const login = await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  expect(login.status(), await login.text()).toBe(200);
  await api("POST", "/api/library/onboarding/complete", { startupScan: false, watchFolders: false });
  const observedRequests: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/api/")) observedRequests.push(`${request.method()} ${new URL(request.url()).pathname}`);
  });

  await page.goto("/RJ00000000");
  await page.getByText("local-a.wav", { exact: true }).click();
  const audio = page.locator("audio");
  await expect(audio).toHaveJSProperty("paused", false);
  await page.getByRole("button", { name: "Collapse player", exact: true }).click();
  const queueState = () =>
    page.evaluate(() => {
      const key = Object.keys(localStorage).find((key) => key.startsWith("kikoto:player-queue:v2"));
      const state = JSON.parse(key ? (localStorage.getItem(key) ?? "null") : "null");
      return {
        index: state?.currentIndex,
        tracks: state?.queue.map((track: { mediaItemId: number; locationId: number; playbackKey: string }) => [
          track.mediaItemId,
          track.locationId,
          track.playbackKey,
        ]),
      };
    });
  await expect.poll(async () => (await queueState()).tracks?.length).toBe(2);
  const queue = await queueState();
  const stream = await audio.evaluate((element: HTMLAudioElement) => element.currentSrc);
  const startedAt = await audio.evaluate((element: HTMLAudioElement) => element.currentTime);

  await chooseLanguage(page, "Simplified Chinese");
  // Translation follows the visible region, so the Hero must be on screen before its title is asserted.
  await page.getByTestId("detail-hero").scrollIntoViewIfNeeded();
  await expect(page.getByTestId("detail-hero")).toContainText("听见森林里的音乐");
  const editor = page.getByRole("region", { name: "Personal review", exact: true });
  await editor.getByLabel("Personal rating", { exact: true }).selectOption("4");
  await editor.getByLabel("Personal note", { exact: true }).fill("Example combined numbered note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  expect(await api("GET", "/api/works/RJ00000000?includeMedia=false")).toMatchObject({
    personalRating: 4,
    personalNote: "Example combined numbered note",
  });
  await chooseLanguage(page, "Traditional Chinese");
  await page.getByTestId("detail-hero").scrollIntoViewIfNeeded();
  await expect(page.getByTestId("detail-hero")).toContainText("聽見森林裏的音樂");
  const preferences = await api("GET", "/api/auth/me/preferences");
  expect(preferences.metadataLanguages).toContain("zh-tw");

  // SPA navigation preserves the one active audio element and queue.
  await page.getByRole("button", { name: "Library", exact: true }).first().click();
  await page.getByRole("button", { name: "Example Remote A", exact: true }).click();
  const remoteWorks = await api("GET", "/api/remote-sources/2/works");
  // Catalog entries 2 and 3 share the translated title, so the card is located by the order the
  // source list itself reports instead of by an ambiguous accessible name.
  const catalogIndex = (remoteWorks.works as Array<{ catalogEntryId?: number }>).findIndex(
    (work) => work.catalogEntryId === 2,
  );
  expect(catalogIndex, "catalog entry 2 is listed under Example Remote A").toBeGreaterThanOrEqual(0);
  const cards = page.getByTestId("work-card");
  await expect(cards).toHaveCount(remoteWorks.works.length);
  await cards.nth(catalogIndex).click();
  await expect(page).toHaveURL(/\/catalog\/2$/);
  await expect(audio).toHaveJSProperty("paused", false);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(stream);
  expect(await queueState()).toEqual(queue);
  await expect
    .poll(async () => audio.evaluate((element: HTMLAudioElement) => element.currentTime))
    .toBeGreaterThan(startedAt);
  await expect(page.getByRole("region", { name: "Source catalog work", exact: true })).toContainText("無碼森林音樂");
  await expect(page.getByText("聽見風聲.wav", { exact: true }).first()).toBeVisible();
  await editor.getByLabel("Personal rating", { exact: true }).selectOption("5");
  await editor.getByLabel("Personal note", { exact: true }).fill("Example combined numberless note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  const registered = await api("GET", "/api/source-catalog/2");
  expect(registered.primaryCode).toBe("");
  expect(registered.personalRating).toBe(5);
  expect(registered.personalNote).toBe("Example combined numberless note");
  expect(registered.workId).toBeGreaterThan(0);
  expect((await api("GET", "/api/workflow-runs")).total).toBe(0);
  const preview = await api("POST", "/api/shared-associations/previews", {
    left: { kind: "catalog", id: 2 },
    right: { kind: "catalog", id: 3 },
  }, 201);
  const association = await api("POST", `/api/shared-associations/${preview.id}/start`, { version: preview.version });
  expect(association.status).toBe("completed");
  const alternate = await api("GET", "/api/source-catalog/3");
  // ADR 0002 keeps one edition per source: the association maps identities and shares the review,
  // it does not move the other catalog entry onto the first work id.
  expect(alternate).toMatchObject({
    personalRating: 5,
    personalNote: "Example combined numberless note",
  });
  expect(alternate.workId).toBeGreaterThan(0);
  expect(alternate.workId).not.toBe(registered.workId);
  const directories = await api("GET", `/api/works/${registered.workId}/catalog-directories`);
  const directoryIDs = (directories.directories as Array<{ catalogEntryId: number }>)
    .map((directory) => directory.catalogEntryId)
    .sort();
  expect(directoryIDs).toEqual([2, 3]);
  const media = await api("GET", `/api/works/${registered.workId}/media`);
  expect(media.mediaItems).toEqual([]);
  const resources = await api("GET", "/__fixture/resources");
  await testInfo.attach("combined-acceptance-observations", {
    body: Buffer.from(JSON.stringify({ observedRequests, queue, stream, startedAt, resources }, null, 2)),
    contentType: "application/json",
  });
});
