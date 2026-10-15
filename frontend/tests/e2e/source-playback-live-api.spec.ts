import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

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
