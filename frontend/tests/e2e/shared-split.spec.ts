import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("owners allocate changed reviews and reopen a pending split through the real API @desktop", async ({ page }) => {
  const backend = process.env.OPENLIST_API_URL;
  test.skip(!backend, "Start the synthetic OpenList fixture.");
  const login = async (username: string) =>
    expect(
      (
        await page.request.post(`${backend}/api/auth/login`, { data: { username, password: "synthetic-password" } })
      ).ok(),
    ).toBe(true);
  await login("synthetic-user");
  expect(
    (
      await page.request.post(`${backend}/api/library/onboarding/complete`, {
        data: { startupScan: false, watchFolders: false },
      })
    ).ok(),
  ).toBe(true);
  expect((await page.request.post(`${backend}/api/remote-sources/1/catalog/scan`, { data: {} })).ok()).toBe(true);
  const list = await (await page.request.get(`${backend}/api/remote-sources/1/works`)).json();
  const first = list.works.find((work: { title: string }) => work.title === "Example Work").catalogEntryId;
  const second = list.works.find((work: { primaryCode: string }) => work.primaryCode === "RJ00000001").catalogEntryId;
  expect(
    (
      await page.request.patch(`${backend}/api/source-catalog/${first}/user-state`, {
        data: { rating: 1, note: "Editor original" },
      })
    ).ok(),
  ).toBe(true);
  expect(
    (await page.request.patch(`${backend}/api/source-catalog/${second}/user-state`, { data: { note: "" } })).ok(),
  ).toBe(true);
  expect(
    (
      await page.request.post(`${backend}/api/users`, {
        data: {
          username: "synthetic-split-owner",
          displayName: "Synthetic owner",
          role: "user",
          enabled: true,
          password: "synthetic-password",
        },
      })
    ).ok(),
  ).toBe(true);
  await login("synthetic-split-owner");
  expect(
    (
      await page.request.patch(`${backend}/api/source-catalog/${second}/user-state`, {
        data: { rating: 2, note: "Listener original" },
      })
    ).ok(),
  ).toBe(true);
  await login("synthetic-user");
  const preview = await (
    await page.request.post(`${backend}/api/shared-associations/previews`, {
      data: { left: { kind: "catalog", id: first }, right: { kind: "catalog", id: second } },
    })
  ).json();
  expect(
    (
      await page.request.post(`${backend}/api/shared-associations/${preview.id}/start`, {
        data: { version: preview.version },
      })
    ).ok(),
  ).toBe(true);
  expect(
    (
      await page.request.patch(`${backend}/api/source-catalog/${second}/user-state`, {
        data: { rating: 4, note: "Editor changed" },
      })
    ).ok(),
  ).toBe(true);
  await login("synthetic-split-owner");
  expect(
    (
      await page.request.patch(`${backend}/api/source-catalog/${first}/user-state`, {
        data: { note: "Listener changed" },
      })
    ).ok(),
  ).toBe(true);
  await login("synthetic-user");
  await page.goto(`/catalog/${first}`);
  const association = page.getByRole("region", { name: "Shared association", exact: true });
  await association
    .getByRole("button", { name: /Open association/ })
    .first()
    .click();
  const split = page.getByRole("region", { name: "Split association", exact: true });
  await split.getByRole("button", { name: "Preview split", exact: true }).click();
  await expect(split.getByRole("region", { name: "First entry before association", exact: true })).toContainText(
    "Editor original",
  );
  await expect(split.getByRole("region", { name: "My current shared review", exact: true })).toContainText(
    "Editor changed",
  );
  await expect(split).not.toContainText("Listener changed");
  await expect(split).not.toContainText("Listener original");
  await split.getByRole("button", { name: "Start shared split", exact: true }).click();
  await expect(split.getByRole("status")).toHaveText("Waiting for record allocations");
  await expect(split.getByRole("button", { name: "Confirm my allocation", exact: true })).toBeDisabled();
  await split.getByLabel("Keep my changed review in", { exact: true }).selectOption("left");
  await split.getByRole("button", { name: "Confirm my allocation", exact: true }).click();
  await expect(split.getByRole("status")).toHaveText("Waiting for record allocations");
  await page.reload();
  await association
    .getByRole("button", { name: /Open association/ })
    .first()
    .click();
  await split
    .getByRole("button", { name: /Open split/ })
    .first()
    .click();
  await expect(split.getByLabel("Keep my changed review in", { exact: true })).toHaveValue("left");
  expect(await (await page.request.get(`${backend}/api/source-catalog/${second}`)).json()).toMatchObject({
    personalNote: "Editor changed",
  });
  await login("synthetic-split-owner");
  await page.reload();
  await association
    .getByRole("button", { name: /Open association/ })
    .first()
    .click();
  await split
    .getByRole("button", { name: /Open split/ })
    .first()
    .click();
  await expect(split.getByRole("button", { name: "Start shared split", exact: true })).toHaveCount(0);
  await expect(split).not.toContainText("Editor changed");
  await split.getByLabel("Keep my changed review in", { exact: true }).selectOption("both");
  await split.getByRole("button", { name: "Confirm my allocation", exact: true }).click();
  await expect(split.getByRole("status")).toHaveText("Split completed");
  for (const entry of [first, second])
    expect(await (await page.request.get(`${backend}/api/source-catalog/${entry}`)).json()).toMatchObject({
      personalRating: 2,
      personalNote: "Listener changed",
    });
  await login("synthetic-user");
  expect(await (await page.request.get(`${backend}/api/source-catalog/${first}`)).json()).toMatchObject({
    personalRating: 4,
    personalNote: "Editor changed",
  });
  expect(await (await page.request.get(`${backend}/api/source-catalog/${second}`)).json()).toMatchObject({
    personalRating: null,
    personalNote: "",
  });
});
