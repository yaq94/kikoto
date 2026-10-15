import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("unavailable catalog keeps its review and defaults to an available directory @desktop", async ({ page }) => {
  const backend = process.env.SOURCE_PLAYBACK_API_URL;
  test.skip(!backend, "Start the isolated source-playback-fixture API.");
  expect(
    (
      await page.request.post(`${backend}/api/auth/login`, {
        data: { username: "synthetic-user", password: "synthetic-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
  expect(
    (
      await page.request.patch(`${backend}/api/source-catalog/2/user-state`, {
        data: { rating: 4, note: "Example retained note" },
      })
    ).ok(),
  ).toBe(true);
  const sources = await (await page.request.get(`${backend}/api/file-sources`)).json();
  const source = sources.find((item: { id: number }) => item.id === 5);
  expect(source).toBeDefined();
  expect(
    (await page.request.patch(`${backend}/api/file-sources/5`, { data: { ...source, enabled: false } })).ok(),
  ).toBe(true);
  try {
    await page.goto("/work/3");
    await expect(page.getByText("alternate.wav", { exact: true })).toBeVisible();
    await page.goto("/catalog/2");
    await expect(page.getByRole("status", { name: "Source status" })).toContainText("Source disabled");
    await expect(page.getByLabel("Personal note", { exact: true })).toHaveValue("Example retained note");
    await page.getByLabel("Personal note", { exact: true }).fill("Example edited retained note");
    await page.getByRole("button", { name: "Save review", exact: true }).click();
    await expect(page.getByText("Review saved.", { exact: true })).toBeVisible();
    expect(await (await page.request.get(`${backend}/api/source-catalog/2`)).json()).toMatchObject({
      status: "disabled",
      personalRating: 4,
      personalNote: "Example edited retained note",
    });
  } finally {
    expect((await page.request.patch(`${backend}/api/file-sources/5`, { data: source })).ok()).toBe(true);
  }
});

test("unconfirmed relocation remains a separate candidate with the original review @desktop", async ({ page }) => {
  const backend = process.env.SOURCE_PLAYBACK_API_URL;
  test.skip(!backend, "Start the isolated source-playback-fixture API.");
  expect(
    (
      await page.request.post(`${backend}/api/auth/login`, {
        data: { username: "synthetic-user", password: "synthetic-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.route("**/api/source-catalog/2", async (route) => {
    const response = await route.fetch();
    const detail = await response.json();
    await route.fulfill({
      json: {
        ...detail,
        status: "missing",
        relocationCandidates: [
          { catalogEntryId: 3, title: "Example moved candidate", reason: "insufficient_evidence" },
        ],
      },
    });
  });
  await page.goto("/catalog/2");
  await expect(page.getByRole("status", { name: "Source status" })).toContainText("Source content missing");
  await expect(
    page.getByText("Possible moved directories require confirmation. Your review stays with this work."),
  ).toBeVisible();
  await page.getByRole("button", { name: "Example moved candidate", exact: true }).click();
  await expect(page).toHaveURL(/\/catalog\/3$/);
  await expect(page.getByRole("heading", { name: "Example Catalog Alternate", exact: true })).toBeVisible();
});
