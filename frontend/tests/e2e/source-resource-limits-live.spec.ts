import { expect, test } from "@playwright/test";

import { playerQueueStorageBaseKey, readScopedPlayerState } from "./fixtures/player-library";

test.use({ screenshot: "off", trace: "off", video: "off" });

// AC-25: the scan parks on a low disk while the listener keeps playing a real
// catalog track, keeps browsing, and keeps their own rating and note. Resuming
// the scan must not touch any of that.
test("playback and personal records survive a low-disk catalog pause @desktop", async ({ page }) => {
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

  await page.goto("/work/3");
  await page.getByRole("button", { name: /^Example Catalog A/ }).click();
  await expect(page.getByText("catalog-b.wav", { exact: true })).toBeVisible();
  await page.getByText("catalog-a.wav", { exact: true }).click();
  const audio = page.locator("audio");
  await expect(audio).toHaveJSProperty("paused", false);
  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime)).toBeGreaterThan(0.5);

  // The player queue is part of the listener's live state, not the scan's.
  const queueState = async () => {
    const state = await readScopedPlayerState(page, playerQueueStorageBaseKey, 1);
    return {
      at: state?.currentIndex,
      queue: (state?.queue ?? []).map((track: { mediaItemId: number; locationId: number; playbackKey: string }) => [
        track.mediaItemId,
        track.locationId,
        track.playbackKey,
      ]),
    };
  };
  // The first render persists the queue in more than one step. Wait for the
  // settled two-track baseline before comparing later states against it.
  await expect.poll(async () => (await queueState()).queue.length).toBe(2);
  await expect.poll(async () => (await queueState()).at).toBe(0);
  await page.waitForTimeout(300);
  const queue = await queueState();
  const stream = await audio.evaluate((element: HTMLAudioElement) => element.currentSrc);

  const runIndex = async (data: Record<string, unknown>) =>
    (
      await page.request.post(`${backend}/api/remote-sources/5/catalog/index`, {
        data,
      })
    ).json();
  const progress = async () => (await page.request.get(`${backend}/api/remote-sources/5/catalog/index`)).json();
  await runIndex({ intervalMs: 100, minFreeBytes: 1_099_511_627_776 });
  await expect.poll(async () => (await progress()).status).toBe("paused");
  const paused = await progress();
  expect(paused.pauseReason).toBe("low_disk_space");
  expect(paused.indexedEntries).toBe(0);
  const pausedAt = await audio.evaluate((element: HTMLAudioElement) => element.currentTime);
  expect(await queueState()).toEqual(queue);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(stream);

  // Browsing and personal state keep working while the background pass is
  // parked, and the player keeps its identity instead of restarting.
  await page.getByRole("button", { name: "Library" }).click();
  await expect(page.getByRole("button", { name: "Collapse player", exact: true })).toBeVisible();
  const browsed = await (await page.request.get(`${backend}/api/works?q=Example`)).json();
  expect(browsed.total).toBeGreaterThan(0);
  await page.request.patch(`${backend}/api/source-catalog/2/user-state`, {
    data: { rating: 4, note: "Example low disk note" },
  });
  const review = await (await page.request.get(`${backend}/api/source-catalog/2`)).json();
  expect(review.personalRating).toBe(4);
  expect(review.personalNote).toBe("Example low disk note");
  expect(await queueState()).toEqual(queue);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(stream);

  await expect.poll(() => audio.evaluate((element: HTMLAudioElement) => element.paused)).toBe(false);
  await expect
    .poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime))
    .toBeGreaterThan(pausedAt + 0.5);

  // Free space returns: the scan finishes instead of reporting the pause as
  // coverage, and the rating and note are still the listener's own.
  await runIndex({ minFreeBytes: 0 });
  await expect.poll(async () => (await progress()).status).toBe("complete");
  const resumed = await progress();
  expect(resumed.pauseReason ?? "").toBe("");
  expect(resumed.observedEntries).toBeGreaterThan(0);
  const kept = await (await page.request.get(`${backend}/api/source-catalog/2`)).json();
  expect(kept.personalRating).toBe(4);
  expect(kept.personalNote).toBe("Example low disk note");
  expect(await queueState()).toEqual(queue);
  await expect
    .poll(() => audio.evaluate((element: HTMLAudioElement) => element.currentTime))
    .toBeGreaterThan(pausedAt + 1);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(stream);

  // A local two-track queue is the listener's app state, not fixture data that
  // revalidation can rebuild. Pause it, reload the whole app, and require the
  // queue and cursor to come back without the player starting on its own.
  await page.goto("/RJ00000000");
  await expect(page.getByText("local-a.wav", { exact: true })).toBeVisible();
  await page.getByText("local-a.wav", { exact: true }).click();
  await expect(audio).toHaveJSProperty("paused", false);
  await expect.poll(async () => (await queueState()).queue.length).toBe(2);
  await expect.poll(async () => (await queueState()).at).toBe(0);
  await page.getByRole("button", { name: "Pause", exact: true }).click();
  const localQueue = await queueState();
  const localStream = await audio.evaluate((element: HTMLAudioElement) => element.currentSrc);
  await page.reload();
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  await expect.poll(async () => (await queueState()).queue.length).toBe(2);
  expect(await queueState()).toEqual(localQueue);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.currentSrc)).toBe(localStream);
  expect(await audio.evaluate((element: HTMLAudioElement) => element.paused)).toBe(true);
  // The persisted bytes are not the product: the restored queue panel must
  // render both tracks and keep the current one selected.
  await page.getByRole("button", { name: "Playback queue" }).click();
  await expect(page.getByRole("button", { name: /^Options for local-a\.wav/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Options for / })).toHaveCount(2);
  await page.keyboard.press("Escape");

  // The personal record also survives the app restart, without the player or
  // the index recreating it.
  await page.goto("/work/3");
  await page.reload();
  const reviewPanel = page.getByRole("region", { name: "Personal review", exact: true });
  await expect(reviewPanel.getByLabel("Personal rating")).toHaveValue("4");
  await expect(reviewPanel.getByLabel("Personal note")).toHaveValue("Example low disk note");
});
