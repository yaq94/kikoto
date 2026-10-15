import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("work matching explains automatic and uncertain candidates through the real API @desktop", async ({ page }) => {
  const backend = process.env.OPENLIST_API_URL;
  test.skip(!backend || process.env.OPENLIST_MATCHING_FIXTURE !== "1", "Start the synthetic matching fixture.");
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
      await page.request.patch(`${backend}/api/source-catalog/901/user-state`, {
        data: { rating: 4, note: "Example matching review" },
      })
    ).ok(),
  ).toBe(true);
  await page.goto("/catalog/901");
  let matching = page.getByRole("region", { name: "Work matching", exact: true });
  await matching.getByRole("button", { name: "Check work matches", exact: true }).click();
  await expect(matching).toContainText("Association completed");
  await expect(matching).toContainText("Original title, voice actors and release date");
  expect(await (await page.request.get(`${backend}/api/source-catalog/902`)).json()).toMatchObject({
    personalRating: 4,
    personalNote: "Example matching review",
  });
  await matching.getByRole("button", { name: /Open association/ }).click();
  await expect(page.getByRole("region", { name: "Shared association", exact: true }).getByRole("status")).toHaveText(
    "Association completed",
  );
  await page.goto("/catalog/903");
  matching = page.getByRole("region", { name: "Work matching", exact: true });
  await matching.getByRole("button", { name: "Check work matches", exact: true }).click();
  await expect(matching).toContainText("Confirmation required");
  await expect(matching).toContainText("Not enough independent evidence");
  await matching.getByRole("button", { name: "Review candidate: Example Uncertain Work", exact: true }).click();
  await expect(page.getByRole("region", { name: "Shared association", exact: true }).getByRole("status")).toHaveText(
    "Preview ready",
  );
});
