import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });
test.describe.configure({ mode: "serial" });
test.skip(!process.env.KIKOTO_CONTENT_TRANSLATION_URL, "Start the isolated content translation fixture.");
const backend = process.env.KIKOTO_CONTENT_TRANSLATION_URL ?? "http://127.0.0.1:17612";

test("cached detail observes a model change through settings in the same SPA @desktop", async ({ page }) => {
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await page.request.post(`${backend}/api/auth/login`, { data: { username: "synthetic-user", password: "synthetic-password" } });
  await page.request.patch(`${backend}/api/auth/me`, { data: { uiLocale: "en" } });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.post(`${backend}/api/library/onboarding/complete`, { data: { startupScan: false, watchFolders: false } });
  await page.request.patch(`${backend}/api/settings/translation`, { data: { model: "Review Model A" } });
  const versions: string[] = [];
  page.on("response", async (response) => {
    if (!response.url().endsWith("/api/content-translations/batch") || response.status() !== 200) return;
    const result = await response.json().catch(() => ({ items: [] }));
    for (const item of result.items) {
      if (item.original === "Example title by Example Voice") versions.push(item.version);
    }
  });
  await page.goto("/RJ00000000");
  const hero = page.getByTestId("detail-hero");
  await expect(hero).toContainText("ZH: Example title by Example Voice");
  await expect.poll(() => versions.some((version) => version.startsWith("Review Model A|"))).toBe(true);
  await page.getByRole("button", { name: "Settings", exact: true }).first().click();
  await page.getByRole("tab", { name: "Translation", exact: true }).click();
  await page.getByLabel("Model invocation name", { exact: true }).fill("Review Model B");
  await page.getByRole("button", { name: "Save translation settings", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Translation settings saved.");
  // Browser history returns to the retained detail; no reload, language change
  // or new account can invalidate the cache on this path.
  await page.goto("/RJ00000000");
  await expect(page.getByTestId("detail-hero")).toContainText("ZH: Example title by Example Voice");
  const current = await (await page.request.get(`${backend}/api/content-translations/version`)).json();
  await expect.poll(() => versions.includes(current.version)).toBe(true);
});
