import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("personal transfer shows review details and imports through the production API @desktop", async ({ page }) => {
  const backend = process.env.PERSONAL_REVIEW_API_URL;
  test.skip(!backend, "Start the isolated personal-review fixture and set PERSONAL_REVIEW_API_URL.");
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
    (
      await page.request.patch(`${backend}/api/works/1/user-state`, {
        data: { rating: 2, note: "Example existing review" },
      })
    ).ok(),
  ).toBe(true);
  const backup = await (await page.request.get(`${backend}/api/user-data/export`)).json();
  expect(backup.version).toBe(2);
  backup.works[0].rating = 5;
  backup.works[0].note = "Example incoming review";
  await page.goto("/settings?tab=data");
  await page.getByLabel("JSON file", { exact: true }).setInputFiles({
    name: "example-backup.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(backup)),
  });
  const preview = page.getByRole("region", { name: "Preview", exact: true });
  await expect(preview).toBeVisible();
  await preview.getByText("Review import details (1)", { exact: true }).click();
  await expect(preview.getByText("Your existing review: 2 · Example existing review", { exact: true })).toBeVisible();
  await expect(preview.getByText("Incoming review: 5 · Example incoming review", { exact: true })).toBeVisible();
  await page.getByRole("radio", { name: /Overwrite/ }).check();
  await expect(page.getByRole("button", { name: "Import", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await expect(
    page
      .getByText("Imported 1 works, 0 lists, and 0 tags. Skipped 0 works and 0 progress entries.", { exact: true })
      .first(),
  ).toBeVisible();
  expect(await (await page.request.get(`${backend}/api/works/1?includeMedia=false`)).json()).toMatchObject({
    personalRating: 5,
    personalNote: "Example incoming review",
  });
  await page.reload();
  expect((await (await page.request.get(`${backend}/api/user-data/export`)).json()).works[0]).toMatchObject({
    rating: 5,
    note: "Example incoming review",
  });
});
