import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("shared association requires each owner's UI confirmation through the real API @desktop", async ({ page }) => {
  const backend = process.env.OPENLIST_API_URL;
  test.skip(!backend, "Start the synthetic OpenList fixture.");
  const login = async (username: string) => {
    expect(
      (
        await page.request.post(`${backend}/api/auth/login`, { data: { username, password: "synthetic-password" } })
      ).ok(),
    ).toBe(true);
  };
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
  const first = list.works.find((work: { title: string }) => work.title === "Example Work");
  const second = list.works.find((work: { primaryCode: string }) => work.primaryCode === "RJ00000001");
  for (const entry of [first, second]) {
    expect(
      (
        await page.request.patch(`${backend}/api/source-catalog/${entry.catalogEntryId}/user-state`, {
          data: { note: "" },
        })
      ).ok(),
    ).toBe(true);
  }
  for (const username of ["synthetic-association-a", "synthetic-association-b"]) {
    expect(
      (
        await page.request.post(`${backend}/api/users`, {
          data: { username, displayName: username, role: "user", password: "synthetic-password", enabled: true },
        })
      ).ok(),
    ).toBe(true);
  }
  for (const [owner, username] of [
    ["A", "synthetic-association-a"],
    ["B", "synthetic-association-b"],
  ]) {
    await login(username);
    expect(
      (
        await page.request.patch(`${backend}/api/source-catalog/${first.catalogEntryId}/user-state`, {
          data: { rating: 1, note: `${owner} first private note` },
        })
      ).ok(),
    ).toBe(true);
    expect(
      (
        await page.request.patch(`${backend}/api/source-catalog/${second.catalogEntryId}/user-state`, {
          data: { rating: 4, note: `${owner} second private note` },
        })
      ).ok(),
    ).toBe(true);
  }
  await login("synthetic-user");
  await page.goto(`/catalog/${first.catalogEntryId}`);
  const panel = page.getByRole("region", { name: "Shared association", exact: true });
  await panel.getByLabel("Link to the other entry", { exact: true }).fill(`/catalog/${second.catalogEntryId}`);
  await panel.getByRole("button", { name: "Preview association", exact: true }).click();
  await expect(panel.getByRole("status")).toHaveText("Preview ready");
  await expect(panel).not.toContainText("first private note");
  expect(
    (
      await page.request.patch(`${backend}/api/source-catalog/${first.catalogEntryId}/user-state`, {
        data: { note: "Editor updated note" },
      })
    ).ok(),
  ).toBe(true);
  await panel.getByRole("button", { name: "Start shared association", exact: true }).click();
  await expect(panel.getByRole("alert")).toBeVisible();
  await panel.getByRole("button", { name: "Refresh association", exact: true }).click();
  await expect(panel.getByRole("status")).toHaveText("Preview expired: related data changed");
  await panel.getByRole("button", { name: "Create a fresh preview", exact: true }).click();
  await expect(panel.getByRole("status")).toHaveText("Preview ready");
  await panel.getByRole("button", { name: "Start shared association", exact: true }).click();
  await expect(panel.getByRole("status")).toHaveText("Waiting for record owners");
  await expect(panel.getByRole("button", { name: "Confirm my review", exact: true })).toHaveCount(0);
  for (const [owner, username] of [
    ["A", "synthetic-association-a"],
    ["B", "synthetic-association-b"],
  ]) {
    await login(username);
    await page.reload();
    await panel
      .getByRole("button", { name: /Open association/ })
      .first()
      .click();
    await expect(panel.getByRole("button", { name: "Start shared association", exact: true })).toHaveCount(0);
    await expect(panel).toContainText(`${owner} first private note`);
    await expect(panel).not.toContainText(`${owner === "A" ? "B" : "A"} first private note`);
    await panel.getByLabel("Final personal rating", { exact: true }).selectOption(owner === "A" ? "4" : "2");
    await panel.getByLabel("Final personal note", { exact: true }).fill(`${owner} chosen note`);
    await panel.getByRole("button", { name: "Confirm my review", exact: true }).click();
    await expect(panel.getByRole("status")).toHaveText(
      owner === "A" ? "Waiting for record owners" : "Association completed",
    );
    if (owner === "A") {
      expect(
        await (await page.request.get(`${backend}/api/source-catalog/${first.catalogEntryId}`)).json(),
      ).toMatchObject({ personalRating: 1, personalNote: "A first private note" });
      await page.reload();
      await panel
        .getByRole("button", { name: /Open association/ })
        .first()
        .click();
      await expect(panel.getByLabel("Final personal note", { exact: true })).toHaveValue("A chosen note");
    }
  }
  for (const entry of [first, second]) {
    expect(
      await (await page.request.get(`${backend}/api/source-catalog/${entry.catalogEntryId}`)).json(),
    ).toMatchObject({ personalRating: 2, personalNote: "B chosen note" });
  }
  await panel
    .getByRole("region", { name: "Second entry", exact: true })
    .getByRole("button", { name: /Example Remote A/ })
    .click();
  await expect(page).toHaveURL(new RegExp(`/catalog/${second.catalogEntryId}$`));
  await expect(page.getByLabel("Personal note", { exact: true })).toHaveValue("B chosen note");
  await panel.getByLabel("Link to the other entry", { exact: true }).fill("/RJ00000001");
  const submitted = page.waitForRequest(
    (request) => request.url().endsWith("/api/shared-associations/previews") && request.method() === "POST",
  );
  await panel.getByRole("button", { name: "Preview association", exact: true }).click();
  expect((await submitted).postDataJSON().right.kind).toBe("work");
  await expect(panel.getByRole("alert")).toBeVisible();
});
