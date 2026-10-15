import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("series and episode review targets save independently through the real API @desktop", async ({ page }) => {
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
  const mediaRequests: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.startsWith("/api/") && /\/media(?:$|\/)|\/fetch(?:$|\/)/.test(path)) mediaRequests.push(path);
  });
  await page.goto("/library/source/example_remote_a");
  await page.getByRole("heading", { name: "Example series", exact: true }).click();
  const seriesUrl = page.url();
  const seriesId = seriesUrl.split("/").pop();
  const series = page.getByRole("region", { name: "Series review", exact: true });
  await series.getByLabel("Personal rating", { exact: true }).selectOption("5");
  await series.getByLabel("Personal note", { exact: true }).fill("Series note");
  await series.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(series.getByRole("status")).toHaveText("Review saved.");
  const ids: string[] = [];
  for (let i = 0; i < 2; i++) {
    await page
      .getByRole("list", { name: "Episodes", exact: true })
      .getByRole("listitem")
      .nth(i)
      .getByRole("button", { name: "Open episode", exact: true })
      .click();
    await expect(page).toHaveURL(/\/catalog\/\d+$/);
    ids.push(page.url().split("/").pop()!);
    const episode = page.getByRole("region", { name: `Episode review: Episode ${i + 1}`, exact: true });
    await episode.getByLabel("Personal rating", { exact: true }).selectOption(String(i + 1));
    await episode.getByLabel("Personal note", { exact: true }).fill(`Episode ${i + 1} note`);
    await episode.getByRole("button", { name: "Save review", exact: true }).click();
    await expect(episode.getByRole("status")).toHaveText("Review saved.");
    await page.getByRole("navigation", { name: "Folders", exact: true }).getByRole("button", { name: /WAV/ }).click();
    await expect(episode.getByLabel("Personal note", { exact: true })).toHaveValue(`Episode ${i + 1} note`);
    await page.getByRole("button", { name: "Open series", exact: true }).click();
    await expect(page).toHaveURL(seriesUrl);
    await expect(series.getByLabel("Personal note", { exact: true })).toHaveValue("Series note");
  }
  await series.getByLabel("Personal note", { exact: true }).fill("Unsaved series revision");
  await series.getByRole("button", { name: "Clear rating", exact: true }).click();
  await expect(series.getByLabel("Personal rating", { exact: true })).toHaveValue("");
  await expect(series.getByLabel("Personal note", { exact: true })).toHaveValue("Unsaved series revision");
  await series.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(series.getByRole("status")).toHaveText("Review saved.");
  await page.reload();
  await expect(series.getByLabel("Personal note", { exact: true })).toHaveValue("Unsaved series revision");
  expect(await (await page.request.get(`${backend}/api/catalog-series/${seriesId}`)).json()).toMatchObject({
    personalRating: null,
    personalNote: "Unsaved series revision",
  });
  for (let i = 0; i < 2; i++) {
    expect(await (await page.request.get(`${backend}/api/source-catalog/${ids[i]}`)).json()).toMatchObject({
      personalRating: i + 1,
      personalNote: `Episode ${i + 1} note`,
    });
  }
  expect(await (await page.request.get(`${backend}/api/works`)).json()).toMatchObject({ total: 3 });
  expect(mediaRequests).toEqual([]);
});
