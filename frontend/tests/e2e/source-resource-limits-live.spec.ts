import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

// AC-25: the scan parks on a low disk while the listener keeps playing a real
// catalog track, keeps browsing, and keeps their own rating and note. Resuming
// the scan must not touch any of that.
test("playback and personal records survive a low-disk catalog pause @desktop", async ({
  page,
}) => {
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

  await page.goto("/RJ00000000");
  await page.getByRole("button", { name: /^Example Catalog A/ }).click();
  await page.getByText("catalog-a.wav", { exact: true }).click();
  const audio = page.locator("audio");
  await expect(audio).toHaveJSProperty("paused", false);
  await expect
    .poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime))
    .toBeGreaterThan(0.5);

  const index = async (data: Record<string, unknown>) =>
    (
      await page.request.post(`${backend}/api/remote-sources/5/catalog/index`, {
        data,
      })
    ).json();
  const progress = async () =>
    (
      await page.request.get(`${backend}/api/remote-sources/5/catalog/index`)
    ).json();
  await index({ intervalMs: 100, minFreeBytes: 1_099_511_627_776 });
  await expect.poll(async () => (await progress()).status).toBe("paused");
  const paused = await progress();
  expect(paused.pauseReason).toBe("low_disk_space");
  expect(paused.indexedEntries).toBe(0);
  const pausedAt = await audio.evaluate(
    (element: HTMLAudioElement) => element.currentTime,
  );

  // Browsing and personal state keep working while the background pass is
  // parked, and the player keeps its identity instead of restarting.
  await page.getByRole("button", { name: "Library" }).click();
  await expect(
    page.getByRole("button", { name: "Collapse player", exact: true }),
  ).toBeVisible();
  const browsed = await (
    await page.request.get(`${backend}/api/works?q=Example`)
  ).json();
  expect(browsed.total).toBeGreaterThan(0);
  await page.request.patch(`${backend}/api/source-catalog/1/user-state`, {
    data: { rating: 4, note: "Example low disk note" },
  });
  const review = await (
    await page.request.get(`${backend}/api/source-catalog/1`)
  ).json();
  expect(review.personalRating).toBe(4);
  expect(review.personalNote).toBe("Example low disk note");

  await expect
    .poll(() => audio.evaluate((element: HTMLAudioElement) => element.paused))
    .toBe(false);
  await expect
    .poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime))
    .toBeGreaterThan(pausedAt + 0.5);

  // Free space returns: the scan finishes instead of reporting the pause as
  // coverage, and the rating and note are still the listener's own.
  await index({ minFreeBytes: 0 });
  await expect.poll(async () => (await progress()).status).toBe("complete");
  const resumed = await progress();
  expect(resumed.pauseReason ?? "").toBe("");
  expect(resumed.observedEntries).toBeGreaterThan(0);
  const kept = await (
    await page.request.get(`${backend}/api/source-catalog/1`)
  ).json();
  expect(kept.personalRating).toBe(4);
  expect(kept.personalNote).toBe("Example low disk note");
  await expect
    .poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime))
    .toBeGreaterThan(pausedAt + 1);
});
