import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("administrator configures, cancels and resumes the full source index through the real API @desktop", async ({
  page,
}) => {
  const backend = process.env.OPENLIST_API_URL;
  test.skip(!backend, "Start the isolated OpenList fixture and set OPENLIST_API_URL.");
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
  await page.goto("/library/source/example_remote_a");
  await page.getByText("Index limits", { exact: true }).click();
  await page.getByLabel("Metadata request concurrency", { exact: true }).fill("2");
  await page.getByLabel("Request interval (ms)", { exact: true }).fill("100");
  await page.getByRole("button", { name: "Index all anonymous audio", exact: true }).click();
  const region = page.getByRole("region", { name: "Full source index", exact: true });
  await region.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Cancelled");
  const cancelled = await (await page.request.get(`${backend}/api/remote-sources/1/catalog/index`)).json();
  expect(cancelled).toMatchObject({ status: "cancelled", options: { concurrency: 2, intervalMs: 100 } });
  await page.reload();
  await expect(region.getByRole("status")).toContainText("Cancelled");
  await region.getByRole("button", { name: "Resume index", exact: true }).click();
  await expect(region.getByRole("status")).toContainText("Complete", { timeout: 45_000 });
  const completed = await (await page.request.get(`${backend}/api/remote-sources/1/catalog/index`)).json();
  expect(completed).toMatchObject({
    runId: cancelled.runId,
    status: "complete",
    indexedEntries: 3,
    failedDirectories: 0,
    restrictedDirectories: 0,
  });
  await expect(region).toContainText("/Example Work");
  await expect(page.getByRole("heading", { name: "Example Work", exact: true })).toBeVisible();
  expect(await (await page.request.get(`${backend}/api/works`)).json()).toMatchObject({ total: 0 });
});
