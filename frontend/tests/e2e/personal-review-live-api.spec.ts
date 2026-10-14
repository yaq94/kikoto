import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("personal reviews persist through the real production API from local and remote pages @desktop", async ({
  page,
}) => {
  const backend = process.env.PERSONAL_REVIEW_API_URL;
  test.skip(!backend, "Start the isolated personal-review-fixture API and set PERSONAL_REVIEW_API_URL.");
  const login = await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  expect(login.ok()).toBe(true);
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
        data: { rating: null, note: "" },
      })
    ).ok(),
  ).toBe(true);
  await page.goto("/RJ00000000");
  const editor = page.getByRole("region", { name: "Personal review", exact: true });
  await editor.getByLabel("Personal rating", { exact: true }).selectOption("0");
  await editor.getByLabel("Personal note", { exact: true }).fill("Example local note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  const local = await page.request.get(`${backend}/api/works/1?includeMedia=false`);
  expect(await local.json()).toMatchObject({ personalRating: 0, personalNote: "Example local note", rating: 4.25 });
  await page.reload();
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("0");
  await editor.getByRole("button", { name: "Clear rating", exact: true }).click();
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("");
  expect(await (await page.request.get(`${backend}/api/works/1?includeMedia=false`)).json()).toMatchObject({
    personalRating: null,
    personalNote: "Example local note",
  });

  await page.goto("/RJ00000001?source=1");
  await editor.getByLabel("Personal rating", { exact: true }).selectOption("5");
  await editor.getByLabel("Personal note", { exact: true }).fill("Example remote note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  const remote = await page.request.get(`${backend}/api/remote-sources/2/works/RJ00000001`);
  expect(await remote.json()).toMatchObject({ personalRating: 5, personalNote: "Example remote note" });
  await page.goto("/RJ00000001?source=2");
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("5");
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("Example remote note");
  await editor.getByRole("button", { name: "Clear note", exact: true }).click();
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("");
  expect(await (await page.request.get(`${backend}/api/works/RJ00000001?includeMedia=false`)).json()).toMatchObject({
    personalRating: 5,
    personalNote: "",
  });
  expect(await (await page.request.get(`${backend}/api/workflow-runs`)).json()).toMatchObject({ total: 0 });
});
