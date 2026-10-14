import { expect, test, type Page } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });
test.describe.configure({ mode: "serial" });
test.skip(!process.env.KIKOTO_CONTENT_TRANSLATION_URL, "Start the isolated content translation fixture.");
const backend = process.env.KIKOTO_CONTENT_TRANSLATION_URL ?? "http://127.0.0.1:17612";
test.beforeEach(async ({ request }) => {
  await request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
});

async function chooseLanguage(page: Page, name: string) {
  if (!(await page.getByRole("combobox", { name: "Preferred metadata language" }).isVisible()))
    await page.getByRole("button", { name: "User menu" }).click();
  await page.getByRole("combobox", { name: "Preferred metadata language" }).click();
  await page.getByRole("listbox").getByRole("option", { name, exact: true }).click();
  await page.keyboard.press("Escape");
}

test("display language translates public detail and dynamic directory while names and originals remain @desktop", async ({
  page,
}) => {
  expect(
    (
      await page.request.post(`${backend}/api/auth/login`, {
        data: { username: "synthetic-user", password: "synthetic-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.request.patch(`${backend}/api/auth/me`, { data: { uiLocale: "en" } });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: null } });
  await page.request.put(`${backend}/api/works/1/metadata-tags`, { data: { newTags: ["Example tag"] } });
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
  await page.goto("/RJ00000000");
  await page.getByRole("button", { name: "User menu" }).click();
  const language = page.getByRole("combobox", { name: "Preferred metadata language" });
  await language.click();
  await page.getByRole("listbox").getByRole("option", { name: "Simplified Chinese", exact: true }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("detail-hero")).toContainText("ZH: Example title by Example Voice");
  const description = page.getByRole("region", { name: "Description", exact: true });
  await description.scrollIntoViewIfNeeded();
  await expect(description).toContainText("ZH: Example description <script>Example text</script>");
  await expect(description.locator("script")).toHaveCount(0);
  await expect(page.getByTestId("detail-identity-metadata")).toContainText("ZH: Example tag");
  await page.getByTestId("detail-hero").getByRole("button", { name: "Show original", exact: true }).click();
  await expect(page.getByTestId("detail-hero")).not.toContainText("ZH: Example title by Example Voice");
  await page.getByTestId("detail-hero").getByRole("button", { name: "Show translation", exact: true }).click();
  await page.getByTestId("directory-file-row").scrollIntoViewIfNeeded();
  await expect(page.getByTestId("directory-file-row")).toContainText("ZH: Example track.wav");
  const state = await (await page.request.get(`${backend}/__fixture/state`)).json();
  expect(state.requests.length).toBeGreaterThan(2);
  expect(state.requests.join("\n")).not.toContain("Example Voice");
  expect((await (await page.request.get(`${backend}/api/auth/me`)).json()).user.uiLocale).toBe("en");
});

test("late and failed translations retain the current language and allow explicit retry @desktop", async ({ page }) => {
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: null } });
  const before = await (await page.request.get(`${backend}/__fixture/state`)).json();
  await page.request.patch(`${backend}/api/settings/translation`, {
    data: { model: `Example delayed model ${before.requests.length}` },
  });
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "hold" } });
  await page.goto("/RJ00000000");
  await chooseLanguage(page, "Simplified Chinese");
  const hero = page.getByTestId("detail-hero");
  await expect(hero).toContainText("Example title by Example Voice");
  await expect(hero).toContainText("Translating…");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/__fixture/state`)).json()).requests.length)
    .toBeGreaterThan(before.requests.length);
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await chooseLanguage(page, "Japanese");
  await expect(hero).toContainText("JA: Example title by Example Voice");
  await expect(hero).not.toContainText("ZH: Example title");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/__fixture/state`)).json()).cancelled)
    .toBeGreaterThan(before.cancelled);
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "fail" } });
  await chooseLanguage(page, "English");
  await expect(hero).toContainText("The translation service rate limit was reached.");
  await expect(hero).not.toContainText("JA: Example title");
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await hero.getByRole("button", { name: "Retry translation", exact: true }).click();
  await expect(hero).toContainText("EN: Example title by Example Voice");
  await page.request.patch(`${backend}/api/settings/translation`, {
    data: { model: `Example navigation model ${before.requests.length}` },
  });
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "hold" } });
  await chooseLanguage(page, "Simplified Chinese");
  await expect(hero).toContainText("Translating…");
  const holding = await (await page.request.get(`${backend}/__fixture/state`)).json();
  await page.getByRole("button", { name: "Settings", exact: true }).first().click();
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/api/settings/translation/runtime`)).json()).readers)
    .toBe(0);
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/__fixture/state`)).json()).cancelled)
    .toBeGreaterThanOrEqual(holding.cancelled);
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
});

test("new list pages and another account use their own persisted content language @desktop", async ({ page }) => {
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.goto("/library?sort=code&direction=asc&pageSize=24");
  await expect(page.getByTestId("work-card").first()).toContainText("ZH: Example title by Example Voice");
  await page.getByRole("button", { name: "Next page", exact: true }).first().click();
  await expect(page.getByTestId("work-card").first()).toContainText("ZH: Example page title 24");
  await chooseLanguage(page, "Japanese");
  await expect(page.getByTestId("work-card").first()).toContainText("JA: Example page title 24");
  await page.reload();
  await expect(page.getByTestId("work-card").first()).toContainText("JA: Example page title 24");
  await page.request.post(`${backend}/api/users`, {
    data: { username: "synthetic-listener", password: "synthetic-password", role: "user" },
  });
  await page.request.post(`${backend}/api/auth/logout`);
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-listener", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["en-us", "origin"] } });
  await page.request.patch(`${backend}/api/auth/me`, { data: { uiLocale: "ja" } });
  await page.goto("/library?sort=code&direction=asc&pageSize=24&page=2");
  await expect(page.getByTestId("work-card").first()).toContainText("EN: Example page title 24");
  expect((await (await page.request.get(`${backend}/api/auth/me`)).json()).user.uiLocale).toBe("ja");
  await page.request.post(`${backend}/api/auth/logout`);
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  expect((await (await page.request.get(`${backend}/api/auth/me/preferences`)).json()).metadataLanguages[0]).toBe(
    "ja-jp",
  );
  expect((await (await page.request.get(`${backend}/api/auth/me`)).json()).user.uiLocale).toBe("en");
});
