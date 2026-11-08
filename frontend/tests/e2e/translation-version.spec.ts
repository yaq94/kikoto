import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });
test.describe.configure({ mode: "serial" });
test.skip(!process.env.KIKOTO_CONTENT_TRANSLATION_URL, "Start the isolated content translation fixture.");
const backend = process.env.KIKOTO_CONTENT_TRANSLATION_URL ?? "http://127.0.0.1:17612";

test("cached detail observes a model change through settings in the same SPA @desktop", async ({ page }) => {
  await page.request.post(`${backend}/__fixture/mode`, { data: { mode: "ok" } });
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me`, { data: { uiLocale: "en" } });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
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
  await page.goBack();
  await expect(page.getByTestId("detail-hero")).toContainText("ZH: Example title by Example Voice");
  const current = await (await page.request.get(`${backend}/api/content-translations/version`)).json();
  await expect.poll(() => versions.includes(current.version)).toBe(true);
});

test("another administrator's configuration is observed on SPA return @desktop", async ({ page }) => {
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.patch(`${backend}/api/settings/translation`, { data: { model: "Review external A" } });
  await page.goto("/RJ00000000");
  await expect(page.getByTestId("detail-hero")).toContainText("ZH: Example title by Example Voice");
  await page.getByRole("button", { name: "Settings", exact: true }).first().click();
  await expect(page.getByRole("tab", { name: "Translation", exact: true })).toBeVisible();
  await page.request.patch(`${backend}/api/settings/translation`, { data: { model: "Review external B" } });
  const response = page.waitForResponse(async (res) => {
    if (!res.url().endsWith("/api/content-translations/batch") || res.status() !== 200) return false;
    const body = await res.json();
    return body.items.some((item: { version?: string }) => item.version?.startsWith("Review external B|"));
  });
  await page.goBack();
  await response;
  await expect(page.getByTestId("detail-hero")).toContainText("ZH: Example title by Example Voice");
});

test("a delayed old-model response cannot rewind an observed newer version @desktop", async ({ page }) => {
  const title = "Example title by Example Voice";
  await page.request.post(backend + "/api/auth/login", {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(backend + "/api/auth/me/preferences", { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.patch(backend + "/api/settings/translation", { data: { model: "Review slow A" } });
  const oldResponse = page.waitForResponse(async (response) => {
    if (!response.url().endsWith("/api/content-translations/batch") || response.status() !== 200) return false;
    const result = await response.json();
    return result.items.some((item: { original: string }) => item.original === title);
  });
  await page.goto("/RJ00000000");
  const oldBody = await (await oldResponse).json();
  const oldTitle = oldBody.items.find((item: { original: string }) => item.original === title);
  expect(oldTitle.version).toMatch(/^Review slow A\|/);
  await expect(page.getByTestId("detail-hero")).toContainText("ZH: " + title);

  await page.request.patch(backend + "/api/settings/translation", { data: { model: "Review slow B" } });
  const versionB = page.waitForResponse(async (response) => {
    if (!response.url().endsWith("/api/content-translations/version") || response.status() !== 200) return false;
    return (await response.json()).version.startsWith("Review slow B|");
  });
  await page.getByRole("button", { name: "Settings", exact: true }).first().click();
  const current = await (await versionB).json();

  let injected = false;
  let refreshesAfterStale = 0;
  const rewoundRequests: string[] = [];
  page.on("request", (request) => {
    if (!injected) return;
    if (request.url().endsWith("/api/content-translations/version")) refreshesAfterStale += 1;
    if (!request.url().endsWith("/api/content-translations/batch")) return;
    for (const item of request.postDataJSON().items) {
      if (item.key.includes("Review slow A|")) rewoundRequests.push(item.key);
    }
  });
  // Replay a real A result to the active B request, preserving its correlation
  // key. Unlike a cancelled A subscription, this response reaches the component.
  await page.route("**/api/content-translations/batch", async (route) => {
    const input = route.request().postDataJSON();
    if (injected || !input.items.some((item: { text: string }) => item.text === title)) {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    const body = await response.json();
    expect(body.items.find((item: { original: string }) => item.original === title).version).toBe(current.version);
    body.items = body.items.map((item: { key: string; original: string }) =>
      item.original === title ? { ...oldTitle, key: item.key } : item,
    );
    injected = true;
    await route.fulfill({ response, json: body });
  });
  const titleVersions = () =>
    page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve, reject) => {
        const request = indexedDB.open("kikoto-translations");
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      try {
        const rows = await new Promise<Array<{ original: string; version?: string }>>((resolve, reject) => {
          const request = db.transaction("entries").objectStore("entries").getAll();
          request.onsuccess = () => resolve(request.result);
          request.onerror = () => reject(request.error);
        });
        return rows.filter((row) => row.original === "Example title by Example Voice").map((row) => row.version);
      } finally {
        db.close();
      }
    });
  try {
    await page.goBack();
    await expect.poll(() => injected).toBe(true);
    // This new request is caused by consuming the stale result, rather than by
    // navigation. Waiting for it proves the active version check actually ran.
    await expect.poll(() => refreshesAfterStale).toBeGreaterThan(0);
    await expect(page.getByTestId("detail-hero")).not.toContainText("ZH: " + title);
    expect(await titleVersions()).not.toContain(oldTitle.version);
    expect(rewoundRequests).toEqual([]);

    // Reopen only after the stale result was handled; a fresh B response must
    // paint and persist normally, without any request using the A cache scope.
    await page.getByRole("button", { name: "Settings", exact: true }).first().click();
    await expect(page.getByRole("tab", { name: "Translation", exact: true })).toBeVisible();
    await page.goBack();
    await expect(page.getByTestId("detail-hero")).toContainText("ZH: " + title);
    await expect.poll(titleVersions).toEqual([current.version]);
    expect(rewoundRequests).toEqual([]);
  } finally {
    await page.unrouteAll({ behavior: "wait" });
    page.removeAllListeners("request");
  }
});
