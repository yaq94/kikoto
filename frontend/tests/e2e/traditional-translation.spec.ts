import { expect, test, type Page } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });
test.describe.configure({ mode: "serial" });
test.skip(!process.env.KIKOTO_CONTENT_TRANSLATION_URL, "Start the isolated content translation fixture.");
const backend = process.env.KIKOTO_CONTENT_TRANSLATION_URL ?? "http://127.0.0.1:17613";

async function chooseLanguage(page: Page, name: string) {
  await page.getByRole("button", { name: "User menu" }).click();
  await page.getByRole("combobox", { name: "Preferred metadata language" }).click();
  await page.getByRole("listbox").getByRole("option", { name, exact: true }).click();
  await page.keyboard.press("Escape");
}

test.beforeEach(async ({ page }) => {
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  expect(
    (
      await page.request.post(`${backend}/api/auth/login`, {
        data: { username: "synthetic-user", password: "synthetic-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.request.patch(`${backend}/api/auth/me`, { data: { uiLocale: "en" } });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: null } });
  const state = await (await page.request.get(`${backend}/__fixture/state`)).json();
  await page.request.patch(`${backend}/api/settings/translation`, {
    data: { model: `Example Traditional model ${state.requests.length}` },
  });
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
});

test("Simplified and Traditional reuse generation and preserve protected names, originals and file identity @desktop", async ({
  page,
}) => {
  const initial = await (await page.request.get(`${backend}/__fixture/state`)).json();
  await page.request.put(`${backend}/api/works/27/metadata-tags`, { data: { newTags: ["Example conversion tag"] } });
  await page.request.patch(`${backend}/api/works/27/user-state`, { data: { note: "个人备注听音乐" } });
  await page.goto("/RJ00000026");
  await chooseLanguage(page, "Traditional Chinese");
  const hero = page.getByTestId("detail-hero");
  const description = page.getByRole("region", { name: "Description", exact: true });
  const file = page.getByTestId("directory-file-row");
  await expect(hero).toContainText("聽見森林裏的音樂 示例声优 示例社团");
  await description.scrollIntoViewIfNeeded();
  await expect(description).toContainText("風吹過樹葉 <script>音樂</script>");
  await expect(description.locator("script")).toHaveCount(0);
  await expect(page.getByTestId("detail-identity-metadata")).toContainText("自然音樂");
  await file.scrollIntoViewIfNeeded();
  await expect(file).toContainText("聽見風聲.wav");
  const before = await (await page.request.get(`${backend}/__fixture/state`)).json();
  const contentRequests = before.requests
    .slice(initial.requests.length)
    .filter((value: string) => value.includes("Example conversion"));
  expect(contentRequests).toHaveLength(4);
  expect(contentRequests.join("\n")).not.toMatch(/示例声优|示例社团|个人备注/);
  await hero.getByRole("button", { name: "Show original", exact: true }).click();
  await expect(hero).toContainText("Example conversion title 示例声优 示例社团");
  await hero.getByRole("button", { name: "Show translation", exact: true }).click();
  await chooseLanguage(page, "Simplified Chinese");
  await expect(hero).toContainText("听见森林里的音乐 示例声优 示例社团");
  await description.scrollIntoViewIfNeeded();
  await expect(description).toContainText("风吹过树叶 <script>音乐</script>");
  await file.scrollIntoViewIfNeeded();
  await expect(file).toContainText("听见风声.wav");
  await chooseLanguage(page, "Traditional Chinese");
  await hero.scrollIntoViewIfNeeded();
  await expect(hero).toContainText("聽見森林裏的音樂 示例声优 示例社团");
  await file.scrollIntoViewIfNeeded();
  await expect(file).toContainText("聽見風聲.wav");
  const after = await (await page.request.get(`${backend}/__fixture/state`)).json();
  expect(after.requests.length).toBe(before.requests.length);
  const detail = await (await page.request.get(`${backend}/api/works/27`)).json();
  expect(detail.id).toBe(27);
  expect(detail.personalNote).toBe("个人备注听音乐");
  expect(detail.mediaItems[0].id).toBe(2);
  expect(detail.mediaItems[0].locations[0].path).toBe("RJ00000026/Example folder/Example conversion track.wav");
});

test("Traditional pending and failed generation keeps original, retries and cannot replace a later language @desktop", async ({
  page,
}) => {
  const before = await (await page.request.get(`${backend}/__fixture/state`)).json();
  await page.request.patch(`${backend}/api/settings/translation`, {
    data: { model: `Example Traditional delay ${before.requests.length}` },
  });
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "hold" } });
  await page.goto("/RJ00000026");
  await chooseLanguage(page, "Traditional Chinese");
  const hero = page.getByTestId("detail-hero");
  await expect(hero).toContainText("Example conversion title 示例声优 示例社团");
  await expect(hero).toContainText("Translating…");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/__fixture/state`)).json()).requests.length)
    .toBeGreaterThan(before.requests.length);
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await chooseLanguage(page, "English");
  await expect(hero).toContainText("EN: Example conversion title 示例声优 示例社团");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/__fixture/state`)).json()).cancelled)
    .toBeGreaterThan(before.cancelled);
  await expect(hero).not.toContainText("聽見森林");
  // The model is part of the translation cache identity. A queued generation can
  // finish after the pending page was cancelled and leave a cached translation,
  // so name a new model to force the failing attempt to reach the service.
  await page.request.patch(`${backend}/api/settings/translation`, {
    data: { model: `Example Traditional failure ${before.requests.length}` },
  });
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "fail" } });
  await chooseLanguage(page, "Traditional Chinese");
  await hero.scrollIntoViewIfNeeded();
  await expect(hero).toContainText("The translation service rate limit was reached.");
  await expect(hero).toContainText("Example conversion title 示例声优 示例社团");
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await hero.getByRole("button", { name: "Retry translation", exact: true }).click();
  await expect(hero).toContainText("聽見森林裏的音樂 示例声优 示例社团");
});
