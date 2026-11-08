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
  await page.request.post(`${backend}/api/auth/login`, {
    data: { username: "synthetic-user", password: "synthetic-password" },
  });
  await page.request.patch(`${backend}/api/auth/me/preferences`, { data: { metadataLanguages: ["zh-cn", "origin"] } });
  await page.request.patch(`${backend}/api/settings/translation`, { data: { model: "Review slow A" } });
  // Delay an already received response at the browser transport boundary.
  // Navigation can cancel an intercepted network route before route.fulfill;
  // this completed response must still reach the stale-result check.
  await page.addInitScript(() => {
    const originalFetch = window.fetch.bind(window);
    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const state = { titleHeld: false, titleReleased: false, release };
    Object.assign(window, { translationResponseHold: state });
    window.fetch = async (...args) => {
      const response = await originalFetch(...args);
      if (!response.url.endsWith("/api/content-translations/batch") || response.status !== 200) return response;
      const text = await response.text();
      const body = JSON.parse(text);
      const oldTitle = body.items.some(
        (item: { original: string; version?: string }) =>
          item.original === "Example title by Example Voice" && item.version?.startsWith("Review slow A|"),
      );
      if (oldTitle) {
        state.titleHeld = true;
        await held;
      }
      if (oldTitle) state.titleReleased = true;
      return new Response(text, { status: response.status, headers: response.headers });
    };
  });
  let oldReleased = false;
  let announcedB = false;
  let rewound = false;
  page.on("response", async (response) => {
    if (!response.url().endsWith("/api/content-translations/version") || response.status() !== 200) return;
    const { version } = await response.json();
    if (version.startsWith("Review slow B|")) announcedB = true;
    if (oldReleased && announcedB && version.startsWith("Review slow A|")) rewound = true;
  });
  type ResponseHold = { titleHeld: boolean; titleReleased: boolean; release: () => void };
  const holdState = () =>
    page.evaluate(() => {
      const hold = (window as unknown as { translationResponseHold: ResponseHold }).translationResponseHold;
      return { titleHeld: hold.titleHeld, titleReleased: hold.titleReleased };
    });
  const release = () =>
    page.evaluate(() => {
      (window as unknown as { translationResponseHold: ResponseHold }).translationResponseHold.release();
    });
  const titleVersions = () =>
    page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve, reject) => {
        const r = indexedDB.open("kikoto-translations");
        r.onsuccess = () => resolve(r.result);
        r.onerror = () => reject(r.error);
      });
      try {
        const rows = await new Promise<Array<{ original: string; version?: string }>>((resolve, reject) => {
          const r = db.transaction("entries").objectStore("entries").getAll();
          r.onsuccess = () => resolve(r.result);
          r.onerror = () => reject(r.error);
        });
        return rows.filter((row) => row.original === "Example title by Example Voice").map((row) => row.version);
      } finally {
        db.close();
      }
    });
  try {
    await page.goto("/RJ00000000");
    await expect.poll(async () => (await holdState()).titleHeld).toBe(true);
    await page.request.patch(`${backend}/api/settings/translation`, { data: { model: "Review slow B" } });
    await page.getByRole("button", { name: "Settings", exact: true }).first().click();
    await expect.poll(() => announcedB).toBe(true);
    await page.goBack();
    const hero = page.getByTestId("detail-hero");
    await expect(hero).toContainText("ZH: Example title by Example Voice");
    const current = await (await page.request.get(`${backend}/api/content-translations/version`)).json();
    // Commit B before releasing A, so B cannot hide a stale write by simply
    // arriving last. Painting the title alone does not prove its IDB commit.
    await expect.poll(titleVersions).toEqual([current.version]);
    oldReleased = true;
    await release();
    await expect.poll(async () => (await holdState()).titleReleased).toBe(true);
    await page.removeAllListeners("response", { behavior: "wait" });
    const models = await titleVersions();
    expect(models.length).toBeGreaterThan(0);
    expect(models.every((version) => version?.startsWith("Review slow B|"))).toBe(true);
    await expect(hero).toContainText("ZH: Example title by Example Voice");
    expect(rewound).toBe(false);
  } finally {
    await release();
    await page.removeAllListeners("response", { behavior: "wait" });
  }
});
