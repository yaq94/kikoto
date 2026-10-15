import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });
test.beforeEach(async ({ page }) => {
  const backend = process.env.OPENLIST_API_URL;
  test.skip(!backend, "Start openlist-fixture with OPENLIST_SERIES_FIXTURE=1.");
  expect(
    (
      await page.request.post(`${backend}/api/auth/login`, {
        data: { username: "synthetic-user", password: "synthetic-password" },
      })
    ).ok(),
  ).toBe(true);
  expect(
    (
      await page.request.post(`${backend}/api/library/onboarding/complete`, {
        data: { startupScan: false, watchFolders: false },
      })
    ).ok(),
  ).toBe(true);
  expect(
    (await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: null } })).ok(),
  ).toBe(true);
  expect((await page.request.post(`${backend}/api/remote-sources/1/catalog/scan`, { data: {} })).ok()).toBe(true);
});

test("series card opens ordered independent episode directories and preserves playing audio @desktop", async ({
  page,
}) => {
  await page.goto("/library/source/example_remote_a");
  await expect(page.getByRole("heading", { name: "Example series", exact: true })).toHaveCount(1);
  await page.getByRole("heading", { name: "Example series", exact: true }).click();
  await expect(page).toHaveURL(/\/catalog-series\/\d+$/);
  const seriesUrl = page.url();
  const episodes = page.getByRole("list", { name: "Episodes", exact: true }).getByRole("listitem");
  await expect(episodes).toHaveCount(2);
  await expect(episodes.nth(0)).toContainText("Episode 1");
  await expect(episodes.nth(1)).toContainText("Episode 2");
  expect(
    (
      await page.request.patch(process.env.OPENLIST_API_URL + "/api/auth/me/preferences", {
        data: { metadataLanguages: ["en-us"] },
      })
    ).ok(),
  ).toBe(true);
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Series", exact: true }).getByRole("heading", { level: 1 }),
  ).toContainText("Translated: Example series");
  await expect(episodes.nth(0)).toContainText("Translated: Episode 1");
  await expect(episodes.nth(1)).toContainText("Translated: Episode 2");
  expect(
    (
      await page.request.patch(process.env.OPENLIST_API_URL + "/api/auth/me/preferences", {
        data: { metadataLanguages: null },
      })
    ).ok(),
  ).toBe(true);
  await page.reload();
  await episodes.nth(0).getByRole("button", { name: "Open episode", exact: true }).click();
  await expect(page).toHaveURL(/\/catalog\/\d+$/);
  const firstId = page.url().split("/").pop();
  await page.getByRole("navigation", { name: "Folders", exact: true }).getByRole("button", { name: /WAV/ }).click();
  await page.getByRole("button", { name: /^01 opening\.wav/ }).click();
  const audio = page.locator("audio").first();
  await expect.poll(() => audio.evaluate((node: HTMLAudioElement) => node.currentTime)).toBeGreaterThan(0.5);
  await page.getByRole("button", { name: "Collapse player", exact: true }).click();
  const src = await audio.evaluate((node: HTMLAudioElement) => node.currentSrc);
  expect(src).toContain(`/api/source-catalog/${firstId}/media`);
  const time = await audio.evaluate((node: HTMLAudioElement) => node.currentTime);
  await page.getByRole("button", { name: "Open series", exact: true }).click();
  await expect(page).toHaveURL(seriesUrl);
  await episodes.nth(1).getByRole("button", { name: "Open episode", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Episode 2", exact: true })).toHaveText("Episode 2");
  await expect.poll(() => audio.evaluate((node: HTMLAudioElement) => node.currentSrc)).toBe(src);
  await expect.poll(() => audio.evaluate((node: HTMLAudioElement) => node.currentTime)).toBeGreaterThan(time);
  await page.getByRole("navigation", { name: "Folders", exact: true }).getByRole("button", { name: /WAV/ }).click();
  await page.getByRole("button", { name: /^02 ending\.wav/ }).click();
  await expect.poll(() => audio.evaluate((node: HTMLAudioElement) => node.currentSrc)).not.toBe(src);
  expect(new URL(await audio.evaluate((node: HTMLAudioElement) => node.currentSrc)).searchParams.get("path")).toBe(
    "WAV/02 ending.wav",
  );
});

test("manual series preview survives reload and confirms shared grouping without recording a review @desktop", async ({
  page,
}) => {
  const backend = process.env.OPENLIST_API_URL;
  await page.goto("/library/source/example_remote_a");
  await page.getByRole("heading", { name: "Similar A", exact: true }).click();
  await page.getByRole("button", { name: "Group into a series", exact: true }).click();
  const editor = page.getByRole("region", { name: "Group into a series", exact: true });
  await editor.getByLabel("Series title", { exact: true }).fill("Manually confirmed series");
  await editor.getByRole("checkbox", { name: "Similar B", exact: true }).check();
  await editor.getByRole("button", { name: "Preview grouping", exact: true }).click();
  await expect(editor.getByRole("list", { name: "Preview grouping", exact: true })).toContainText("Similar A");
  expect(await (await page.request.get(`${backend}/api/remote-sources/1/works?q=Similar`)).json()).toMatchObject({
    total: 2,
  });
  await page.reload();
  await expect(editor.getByRole("list", { name: "Preview grouping", exact: true })).toContainText("Similar B");
  await editor.getByRole("button", { name: "Confirm grouping", exact: true }).click();
  await expect(page).toHaveURL(/\/catalog-series\/\d+$/);
  await expect(page.getByRole("heading", { level: 1, name: "Manually confirmed series", exact: true })).toHaveText(
    "Manually confirmed series",
  );
  const seriesId = page.url().split("/").pop();
  const shared = await (await page.request.get(`${backend}/api/catalog-series/${seriesId}`)).json();
  expect(shared.episodes).toHaveLength(2);
  expect(shared.episodes.every((episode: { workId: number | null }) => episode.workId === null)).toBe(true);
  expect(await (await page.request.get(`${backend}/api/works`)).json()).toMatchObject({ total: 0 });
  expect(await (await page.request.get(`${backend}/api/remote-sources/1/works?q=Similar`)).json()).toMatchObject({
    total: 1,
  });
});
