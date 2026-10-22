import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("catalog tabs preserve exact playback identity across registered and standalone pages @desktop", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const backend = process.env.SOURCE_PLAYBACK_API_URL;
  test.skip(!backend, "Start the isolated source-playback-fixture API.");
  expect(
    (
      await page.request.post(`${backend}/api/auth/login`, {
        data: { username: "synthetic-user", password: "synthetic-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.request.post(`${backend}/api/library/onboarding/complete`, {
    data: { startupScan: false, watchFolders: false },
  });
  const mutations: string[] = [];
  const directoryReads: string[] = [];
  const translations: Array<{ catalogEntryId?: number; path?: string }> = [];
  page.on("request", (request) => {
    if (request.url().includes("/playback") && request.method() === "POST") mutations.push(request.url());
    if (request.url().includes("/source-catalog/") && request.url().endsWith("/tracks"))
      directoryReads.push(request.url());
    if (request.url().includes("/content-translation") && request.method() === "POST")
      // The client sends one language per request with a page of items, so the
      // per-field identity lives inside `items`, not at the top level.
      translations.push(...(request.postDataJSON()?.items ?? []));
  });
  await page.goto("/RJ00000000");
  await page.getByRole("button", { name: /^Example Catalog A/ }).click();
  await expect(page.getByRole("button", { name: /^catalog-a\.wav .*MB$/ })).toBeVisible();
  await page.goto("/work/3");
  await expect(page.getByRole("button", { name: /^catalog-a\.wav .*MB$/ })).toBeVisible();
  await page.getByRole("button", { name: /^Example Catalog B/ }).click();
  await expect(page.getByText("alternate.wav", { exact: true })).toBeVisible();
  expect(mutations).toEqual([]);
  expect((await (await page.request.get(`${backend}/api/works/3/media`)).json()).mediaItems).toEqual([]);
  expect((await (await page.request.get(`${backend}/api/works`)).json()).total).toBe(3);
  await page.getByRole("button", { name: /^Example Catalog A/ }).click();
  await page.getByText("catalog-a.wav", { exact: true }).click();
  const audio = page.locator("audio");
  await expect(audio).toHaveJSProperty("paused", false);
  await page.getByRole("button", { name: "Collapse player", exact: true }).click();
  const queueState = () =>
    page.evaluate(() => {
      const key = Object.keys(localStorage).find((key) => key.startsWith("kikoto:player-queue:v2"));
      const state = JSON.parse(key ? (localStorage.getItem(key) ?? "null") : "null");
      return {
        index: state?.currentIndex,
        queue: state?.queue.map((track: { mediaItemId: number; locationId: number; playbackKey: string }) => [
          track.mediaItemId,
          track.locationId,
          track.playbackKey,
        ]),
      };
    });
  await expect.poll(async () => (await queueState()).queue?.length).toBe(2);
  const queue = await queueState();
  expect(queue.queue[0][2]).toBe("catalog:2:catalog-a.wav");
  const stream = await audio.evaluate((element: HTMLAudioElement) => element.currentSrc);
  const before = await audio.evaluate((element: HTMLAudioElement) => element.currentTime);
  const readsBeforeLanguage = directoryReads.length;
  await page.getByRole("button", { name: "User menu" }).click();
  await page.getByRole("combobox", { name: "Preferred metadata language" }).click();
  await page.getByRole("listbox").getByRole("option", { name: "Simplified Chinese", exact: true }).click();
  await page.keyboard.press("Escape");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/api/auth/me/preferences`)).json()).metadataLanguages)
    .toContain("zh-cn");
  expect(directoryReads.length).toBe(readsBeforeLanguage);
  expect(await queueState()).toEqual(queue);
  await page.getByRole("button", { name: /^Example Catalog B/ }).click();
  await expect(page.getByText("alternate.wav", { exact: true })).toBeVisible();
  expect(await queueState()).toEqual(queue);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(stream);
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeGreaterThan(before);
  await page.getByRole("button", { name: /^Example Catalog A/ }).click();
  await page.getByText("catalog-b.wav", { exact: true }).click();
  await expect.poll(async () => (await queueState()).index).toBe(1);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeLessThan(3);
  // A long title renders a second marquee copy for the scroll animation. That
  // copy is aria-hidden, so a role query names the one interactive element
  // while a text query would match both.
  await page
    .locator('[data-player-surface="compact"]')
    .getByRole("button", { name: /catalog-b\.wav/ })
    .click();
  await page.getByRole("slider", { name: "Seek", exact: true }).fill("42");
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  await expect
    .poll(
      async () =>
        (await (await page.request.get(`${backend}/api/works/3/playback-cursor`)).json()).cursor?.positionSeconds,
    )
    .toBeGreaterThan(41);
  await page.goto("/catalog/2");
  await page.getByRole("button", { name: "Resume", exact: true }).click();
  await expect(audio).toHaveJSProperty("paused", false);
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeGreaterThan(41);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeLessThan(48);
  const saved = await queueState();
  expect(saved.queue[saved.index][2]).toBe("catalog:2:catalog-b.wav");
  await page.getByText("catalog-a.wav", { exact: true }).click();
  await expect.poll(async () => (await queueState()).index).toBe(0);
  expect((await queueState()).queue).toEqual(queue.queue);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeLessThan(3);
  await page.goto("/work/3");
  await page.getByRole("button", { name: "Collapse player", exact: true }).click();
  await expect(page.getByRole("button", { name: /^catalog-a\.wav .*MB$/ })).toBeVisible();
  await page.request.post(`${backend}/__fixture/catalog-revise`);
  // The click schedules the write; the persisted result is what the next page
  // load must show. Asserting the bytes on the server first keeps the UI check
  // from racing the panel's own re-render, which used to fail intermittently.
  await expect(async () => {
    const refresh = page.waitForResponse((response) => response.url().endsWith("/source-catalog/2/refresh"), {
      timeout: 10_000,
    });
    // The directory panel's icon is the only control with this tooltip; the
    // availability button elsewhere on the page also reads "Check sources" but
    // posts to another endpoint.
    await page.locator('button[title="Check sources"]').click();
    const response = await refresh;
    if (!response.ok()) throw new Error(`refresh returned ${response.status()}`);
  }).toPass({ timeout: 40_000 });
  await expect
    .poll(async () => {
      const tracks = await (await page.request.get(`${backend}/api/source-catalog/2/tracks`)).json();
      return (tracks.tracks ?? []).map((track: { path: string }) => track.path);
    })
    .toContain("catalog-updated.wav");
  await page.request.post(`${backend}/__fixture/catalog-fail`);
  // The failure path is driven through the same endpoint the toolbar calls.
  // Repeating the click made the run depend on which panel owned the shared
  // tooltip, which is not what this case is about: it asserts that a failed
  // refresh keeps the previous snapshot and records the failure.
  const failed = await page.request.post(`${backend}/api/source-catalog/2/refresh`, { data: {} });
  expect(failed.status()).toBeGreaterThanOrEqual(400);
  // The requirement is that a failed refresh keeps the previous snapshot and
  // records the failure. The notice that renders the recorded status is
  // covered by the source-lifecycle spec, so this run asserts the stored
  // status and the retained listing.
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/api/source-catalog/2`)).json()).status)
    .not.toBe("available");
  await page.reload();
  // A reload rebuilds the tab order from the parent's directory list, so the
  // catalog tab is selected again before its retained snapshot is read.
  await page.getByRole("button", { name: /^Example Catalog A/ }).click();
  await expect(page.getByText("catalog-updated.wav", { exact: true })).toBeVisible();
  expect(translations).toEqual(
    expect.arrayContaining([expect.objectContaining({ catalogEntryId: 2, path: "catalog-a.wav" })]),
  );
});

test("read-only source browsing can load an unknown directory without writing availability @desktop", async ({
  page,
}) => {
  const backend = process.env.SOURCE_PLAYBACK_API_URL;
  test.skip(!backend, "Start the isolated source-playback-fixture API.");
  expect(
    (
      await page.request.post(`${backend}/api/auth/login`, {
        data: { username: "synthetic-listener", password: "synthetic-password" },
      })
    ).ok(),
  ).toBe(true);
  const checks: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/source-availability")) checks.push(request.url());
  });
  await page.goto("/RJ00000001");
  await page.getByRole("button", { name: /^Example Remote A/ }).click();
  await expect(page.getByText("remote-a.wav", { exact: true })).toBeVisible();
  expect(checks).toEqual([]);
  const result = await (await page.request.get(`${backend}/api/works/RJ00000001/source-availability`)).json();
  expect(result.sources.every((source: { status: string }) => source.status === "unknown")).toBe(true);
});

test("source checks recover independently while browsing keeps playback intact @desktop", async ({ page }) => {
  const backend = process.env.SOURCE_PLAYBACK_API_URL;
  test.skip(!backend, "Start the isolated source-playback-fixture API.");
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
  await page.goto("/RJ00000000");
  await expect(page.getByText("local-a.wav", { exact: true })).toBeVisible();
  // The known local tree is usable before checking the remaining sources.
  await expect
    .poll(async () => {
      const requests = (await (await page.request.get(`${backend}/__fixture/requests`)).json()) as string[];
      return requests.includes("/b/api/workInfo/RJ00000000") && requests.includes("/c/api/workInfo/RJ00000000");
    })
    .toBe(true);
  const availability = await (await page.request.get(`${backend}/api/works/RJ00000000/source-availability`)).json();
  expect(availability.sources).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ sourceId: 2, status: "available" }),
      expect.objectContaining({ sourceId: 3, status: "available" }),
      expect.objectContaining({ sourceId: 4, status: "error" }),
    ]),
  );

  await page.getByText("local-a.wav", { exact: true }).click();
  const audio = page.locator("audio");
  await expect(audio).toHaveJSProperty("paused", false);
  await page.getByRole("button", { name: "Collapse player", exact: true }).click();
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeGreaterThan(0.2);
  await audio.evaluate((element: HTMLAudioElement) => {
    element.currentTime = 30;
  });
  const stream = await audio.evaluate((element: HTMLAudioElement) => element.currentSrc);
  const queueState = () =>
    page.evaluate(() => {
      const key = Object.keys(localStorage).find((key) => key.startsWith("kikoto:player-queue:v2"));
      const state = JSON.parse(key ? (localStorage.getItem(key) ?? "null") : "null");
      return {
        index: state?.currentIndex,
        queue: state?.queue.map((track: { mediaItemId: number; locationId: number }) => [
          track.mediaItemId,
          track.locationId,
        ]),
      };
    });
  const queue = await queueState();
  expect(queue).toEqual({
    index: 0,
    queue: [
      [1, 1],
      [2, 2],
    ],
  });
  const before = await audio.evaluate((element: HTMLAudioElement) => element.currentTime);
  const started = Date.now();
  await page.getByRole("button", { name: /^Example Remote A/ }).click();
  await expect(page.getByText("remote-a.wav", { exact: true })).toBeVisible();
  expect((await page.request.post(`${backend}/__fixture/block-tracks`)).ok()).toBe(true);
  await page.getByRole("button", { name: /^Example Remote B/ }).click();
  await expect
    .poll(async () =>
      ((await (await page.request.get(`${backend}/__fixture/requests`)).json()) as string[]).includes(
        "/b/api/tracks/1",
      ),
    )
    .toBe(true);
  await page.getByRole("button", { name: /^Example Remote A/ }).click();
  await expect(page.getByText("remote-a.wav", { exact: true })).toBeVisible();
  expect((await page.request.post(`${backend}/__fixture/release-tracks`)).ok()).toBe(true);
  await page.getByRole("button", { name: /^Example Remote B/ }).click();
  await expect(page.getByText("remote-b.wav", { exact: true })).toBeVisible();
  await expect(audio).toHaveJSProperty("paused", false);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(stream);
  expect(await queueState()).toEqual(queue);
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeGreaterThan(before);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeLessThan(
    before + (Date.now() - started) / 1000 + 3,
  );
  expect((await page.request.post(`${backend}/__fixture/revise-tracks`)).ok()).toBe(true);
  expect((await page.request.post(`${backend}/__fixture/recover`)).ok()).toBe(true);
  await page.getByRole("button", { name: /^Check sources/ }).click();
  await expect(page.getByText("remote-b-updated.wav", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: /^Example Remote C/ }).click();
  await expect(page.getByText("remote-c.wav", { exact: true })).toBeVisible();
  // An explicit click starts the other recording at zero even while this work has a cursor.
  expect(await queueState()).toEqual(queue);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(stream);
  expect((await page.request.post(`${backend}/__fixture/fail`)).ok()).toBe(true);
  await page.getByRole("button", { name: /^Check sources/ }).click();
  await expect(page.getByRole("button", { name: /^Example Remote C Check failed/ })).toBeVisible();
  await expect(page.getByText("remote-c.wav", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: /^Local Local files available/ }).click();
  await page.getByText("local-b.wav", { exact: true }).click();
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toContain("/api/media/2/");
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeLessThan(3);
  await page.locator('[data-player-surface="compact"]').getByText("local-b.wav", { exact: true }).click();
  await page.getByRole("slider", { name: "Seek", exact: true }).fill("45");
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeGreaterThan(44);
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  await expect
    .poll(async () => {
      const result = await (await page.request.get(`${backend}/api/works/1/playback-cursor`)).json();
      return result.cursor?.mediaItemId === 2 && result.cursor.positionSeconds >= 44;
    })
    .toBe(true);
  await page.reload();
  await page.getByRole("button", { name: /^Example Remote A/ }).click();
  await expect(page.getByText("remote-a.wav", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Resume", exact: true }).click();
  await expect(audio).toHaveJSProperty("paused", false);
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeGreaterThan(44);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeLessThan(50);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toContain("/api/media/2/");
});
