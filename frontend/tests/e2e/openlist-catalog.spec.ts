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
  expect(
    (await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: null } })).ok(),
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
  // Browsing loads one level at a time: the card root lists its MP3 folder,
  // and the track appears after that folder is opened.
  await page.getByRole("navigation", { name: "Folders", exact: true }).getByRole("button", { name: /^MP3/ }).click();
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

test("unregistered catalog translates titles and real-path directories without changing source identity @desktop", async ({
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
    // A display language different from the source text exercises the model
    // path; English content shown as English is a native row by design.
    (await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn"] } })).ok(),
  ).toBe(true);
  expect(
    (
      await page.request.post(`${backend}/api/library/onboarding/complete`, {
        data: { startupScan: false, watchFolders: false },
      })
    ).ok(),
  ).toBe(true);
  expect((await page.request.post(`${backend}/api/remote-sources/1/catalog/scan`, { data: {} })).ok()).toBe(true);
  await page.goto("/library/source/example_remote_a");
  const cardTitle = page.getByRole("heading").filter({ hasText: "Translation Example" });
  // Translation starts when the card becomes visible, so scroll to it instead
  // of relying on the card sitting above the fold.
  await cardTitle.scrollIntoViewIfNeeded();
  await expect(cardTitle).toContainText("Translated: Translation Example");
  await cardTitle.getByText("Translated: Translation Example", { exact: true }).click();
  await expect(page).toHaveURL(/\/catalog\/\d+$/);
  const catalogId = page.url().split("/").pop();
  const detail = page.getByRole("region", { name: "Source catalog work", exact: true });
  await expect(detail.getByRole("heading", { level: 1 })).toContainText("Translated: Translation Example");
  await expect(detail.getByRole("navigation", { name: "Folders", exact: true })).toContainText(
    "Translated: Example folder",
  );
  await detail
    .getByRole("navigation", { name: "Folders", exact: true })
    .getByRole("button", { name: /^Translated: Example folder/ })
    .click();
  await expect(detail.getByTestId("directory-file-row")).toContainText("Translated: 01 opening.wav");
  await expect(detail.getByTestId("directory-breadcrumb")).toContainText("Translated: Example folder");
  await detail.getByRole("heading", { level: 1 }).getByRole("button", { name: "Show original", exact: true }).click();
  await expect(detail.getByRole("heading", { level: 1 })).not.toContainText("Translated:");
  await expect(detail.getByTestId("directory-file-row")).not.toContainText("Translated:");
  await detail
    .getByRole("heading", { level: 1 })
    .getByRole("button", { name: "Show translation", exact: true })
    .click();
  await expect(detail.getByTestId("directory-file-row")).toContainText("Translated: 01 opening.wav");
  expect(await (await page.request.get(`${backend}/api/source-catalog/${catalogId}`)).json()).toMatchObject({
    title: "Translation Example",
    workId: null,
    primaryCode: "",
  });
  const tracks = await (await page.request.get(`${backend}/api/source-catalog/${catalogId}/tracks`)).json();
  expect(tracks.tracks).toContainEqual(
    expect.objectContaining({
      title: "Example folder",
      path: "Example folder",
      children: [expect.objectContaining({ title: "01 opening.wav", path: "Example folder/01 opening.wav" })],
    }),
  );
  expect(await (await page.request.get(`${backend}/api/workflow-runs`)).json()).toMatchObject({ total: 0 });
});
