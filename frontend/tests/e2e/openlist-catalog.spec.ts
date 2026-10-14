import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("OpenList catalog opens numberless tracks and persists a private review through the real API @desktop", async ({
  page,
}) => {
  const backend = process.env.OPENLIST_API_URL;
  test.skip(!backend, "Start the isolated openlist-fixture and set OPENLIST_API_URL.");
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
  await page.getByRole("button", { name: "Scan configured directories", exact: true }).click();
  await expect(page.getByRole("status", { name: "Catalog scan" })).toHaveText("Configured directories scanned.");
  await page.getByRole("heading", { name: "Example Work", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Example Work", exact: true })).toBeVisible();
  await expect(page).toHaveURL(/\/catalog\/\d+$/);
  const catalogId = page.url().split("/").pop();
  expect(await (await page.request.get(`${backend}/api/source-catalog/${catalogId}`)).json()).toMatchObject({
    primaryCode: "",
    workId: null,
  });
  await page.getByRole("button", { name: /^01 opening\.wav/ }).click();
  await expect
    .poll(() =>
      page
        .locator("audio")
        .first()
        .evaluate((audio: HTMLAudioElement) => audio.currentTime),
    )
    .toBeGreaterThan(0.5);
  const mediaURL = await page
    .locator("audio")
    .first()
    .evaluate((audio: HTMLAudioElement) => audio.currentSrc);
  expect(mediaURL).toContain(`/api/source-catalog/${catalogId}/media`);
  expect(new URL(mediaURL).searchParams.get("path")).toBe("MP3/01 opening.wav");
  const editor = page.getByRole("region", { name: "Personal review", exact: true });
  await editor.getByLabel("Personal rating", { exact: true }).selectOption("0");
  await editor.getByLabel("Personal note", { exact: true }).fill("Example private catalog note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  const saved = await (await page.request.get(`${backend}/api/source-catalog/${catalogId}`)).json();
  expect(saved).toMatchObject({ primaryCode: "", personalRating: 0, personalNote: "Example private catalog note" });
  expect(saved.workId).toBeGreaterThan(0);
  expect(await (await page.request.get(`${backend}/api/workflow-runs`)).json()).toMatchObject({ total: 0 });
  await page.goto(`/work/${saved.workId}`);
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("0");
  await editor.getByRole("button", { name: "Clear rating", exact: true }).click();
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("Example private catalog note");
  await page.reload();
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("");
  expect(
    await (await page.request.get(`${backend}/api/works/${saved.workId}?includeMedia=false`)).json(),
  ).toMatchObject({ primaryCode: "", personalRating: null, personalNote: "Example private catalog note" });
});
