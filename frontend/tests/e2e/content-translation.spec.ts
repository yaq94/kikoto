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
  // The panel opens the only folder automatically. Step back to the work
  // root and expand it again so the translated text is asserted after a real
  // navigation, not only on first paint.
  await page.getByRole("button", { name: "Parent folder", exact: true }).click();
  const folder = page.getByRole("button", { name: "ZH: Example folder 1 audio", exact: true });
  await expect(folder).toBeVisible();
  await folder.click();
  await expect(page.getByTestId("directory-file-row").filter({ hasText: "ZH: Example track.wav" })).toBeVisible();
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
  await chooseLanguage(page, "Japanese");
  const hero = page.getByTestId("detail-hero");
  await expect(hero).toContainText("Example title by Example Voice");
  await expect(hero).toContainText("Translating…");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/__fixture/state`)).json()).requests.length)
    .toBeGreaterThan(before.requests.length);
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await chooseLanguage(page, "Traditional Chinese");
  await expect(hero).toContainText("ZH: Example title by Example Voice");
  await expect(hero).not.toContainText("JA: Example title");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/__fixture/state`)).json()).cancelled)
    .toBeGreaterThan(before.cancelled);
  // Korean has not been generated for this title, so a refused model call is
  // the visible state and the previous script must not remain on screen.
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "fail" } });
  await chooseLanguage(page, "Korean");
  await expect(hero).toContainText("The translation service rate limit was reached.");
  await expect(hero).not.toContainText("ZH: Example title");
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await hero.getByRole("button", { name: "Retry translation", exact: true }).click();
  await expect(hero).toContainText("ZH: Example title by Example Voice");
  await page.request.patch(`${backend}/api/settings/translation`, {
    data: { model: `Example navigation model ${before.requests.length}` },
  });
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "hold" } });
  await chooseLanguage(page, "Japanese");
  await expect(hero).toContainText("Translating…");
  const holding = await (await page.request.get(`${backend}/__fixture/state`)).json();
  await page.getByRole("button", { name: "Settings", exact: true }).first().click();
  await expect
    .poll(
      async () => (await (await page.request.get(`${backend}/api/settings/translation/runtime`)).json()).queue.readers,
    )
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
  const requestsBeforeSecondAccount = (await (await page.request.get(`${backend}/__fixture/state`)).json()).requests
    .length;
  await page.goto("/library?sort=code&direction=asc&pageSize=24&page=2");
  // The second account reads the same English titles. English is already the
  // display language, so the card shows the source text and the title never
  // reaches the model.
  await expect(page.getByTestId("work-card").first()).toContainText("Example page title 24");
  await expect(page.getByTestId("work-card").first()).not.toContainText("EN:");
  const secondAccountPrompts: string[] = (
    await (await page.request.get(`${backend}/__fixture/state`)).json()
  ).requests.slice(requestsBeforeSecondAccount);
  expect(secondAccountPrompts.filter((prompt) => prompt.includes("Example page title 24"))).toEqual([]);
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

test("library search matches the displayed language and keeps untranslated text out @desktop", async ({ page }) => {
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me`, { data: { uiLocale: "en" } });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
  // Reading the work in Simplified Chinese stores its translation row, which is
  // the only corpus a display-language search may consult.
  await page.goto("/RJ00000026");
  await chooseLanguage(page, "Simplified Chinese");
  await expect(page.getByTestId("detail-hero")).toContainText("听见森林里的音乐");

  await page.goto("/library?scope=local&sort=code&direction=asc&pageSize=24&q=%E5%90%AC%E8%A7%81");
  await expect(page.getByTestId("work-card")).toHaveCount(1);
  await expect(page.getByTestId("work-card").first()).toContainText("听见森林里的音乐");

  // A source title that has no row for this language stays invisible to the
  // display-language search even though the origin text contains it.
  await page.goto("/library?scope=local&sort=code&direction=asc&pageSize=24&q=Another");
  await expect(page.getByTestId("work-card")).toHaveCount(0);
});

test("traditional search reaches the simplified row and protects published names @desktop", async ({ page }) => {
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
  await page.goto("/RJ00000026");
  await chooseLanguage(page, "Simplified Chinese");
  await expect(page.getByTestId("detail-hero")).toContainText("听见森林里的音乐");
  // The protected circle and cast names survive the script conversion.
  const hero = page.getByTestId("detail-hero");
  await expect(hero).toContainText("示例声优");
  await expect(hero).toContainText("示例社团");

  await chooseLanguage(page, "Traditional Chinese");
  await expect(hero).toContainText("聽見森林裏的音樂");
  await expect(hero).toContainText("示例声优");

  await page.goto("/library?scope=local&sort=code&direction=asc&pageSize=24&q=%E8%81%BD%E8%A6%8B");
  await expect(page.getByTestId("work-card").first()).toContainText("聽見森林裏的音樂");
});

test("a second visit reuses the browser translation cache without new requests @desktop", async ({ page }) => {
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
  let requests = 0;
  page.on("request", (request) => {
    // Only generation calls count. The version read is a cache identity check
    // and must happen on every visit; it never asks the model for text.
    if (request.method() === "POST" && request.url().includes("/api/content-translations")) requests += 1;
  });
  await page.goto("/RJ00000026");
  await chooseLanguage(page, "Simplified Chinese");
  await expect(page.getByTestId("detail-hero")).toContainText("听见森林里的音乐");
  const first = requests;
  expect(first).toBeGreaterThan(0);

  await page.reload();
  await expect(page.getByTestId("detail-hero")).toContainText("听见森林里的音乐");
  // The row also exists server-side, so the durable browser cache is proven by
  // the page painting the translated title before any translation call.
  expect(requests).toBe(first);
});
