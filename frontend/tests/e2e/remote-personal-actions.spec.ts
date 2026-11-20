import { expect, test } from "@playwright/test";
import {
  favoriteListFixture,
  librarySourceFixture,
  remoteTrackFixture,
  remoteWorkDetailFixture,
  sourceAvailabilitySourceFixture,
} from "./fixtures/api";
import { mockRemoteSource } from "./fixtures/player-library";
import { syntheticWorkCode } from "../../src/test-support/workCode";

test.use({ screenshot: "off", trace: "off", video: "off" });

for (const backupIdentity of [
  "known",
  "resolved after saving",
  "refresh failed",
  "refresh timed out",
  "failed then changes edition",
  "pending then changes edition",
] as const) {
  test(`remote detail shares a review saved during a source switch (identity ${backupIdentity})`, async ({ page }) => {
    const code = syntheticWorkCode("RJ", 51);
    const editionCode = syntheticWorkCode("RJ", 52);
    const endpoint = `/api/remote-sources/1/works/${code}`;
    let listeningStatus = "listening";
    let favorite = false;
    let personalRating: number | null = 3;
    let personalNote = "Example note";
    let noteRequested = false;
    let backupMetadataReads = 0;
    let downloadRequests = 0;
    const backupNeedsIdentityCheck = backupIdentity !== "known";
    const switchesEdition = backupIdentity.endsWith("changes edition");
    const refreshPending = backupIdentity === "pending then changes edition";
    const identityRefreshFails =
      backupIdentity === "refresh failed" || backupIdentity === "refresh timed out" || switchesEdition;
    let editionNote = "Example edition note";
    let failIdentityRefresh = identityRefreshFails;
    let releaseDelayedRefresh!: () => void;
    const delayedRefresh = new Promise<void>((resolve) => {
      releaseDelayedRefresh = resolve;
    });
    let finishNoteSave!: () => void;
    const noteCanSave = new Promise<void>((resolve) => {
      finishNoteSave = resolve;
    });
    const list = favoriteListFixture({ id: 2, name: "Example list", kind: "user" });
    await mockRemoteSource(page, () => {});
    await page.route("**/api/library-sources", (route) =>
      route.fulfill({
        json: [
          librarySourceFixture({ id: 1, code: "example_remote", displayName: "Example Remote" }),
          librarySourceFixture({ id: 2, code: "example_backup", displayName: "Example Backup" }),
        ],
      }),
    );
    await page.route(`**/api/works/${code}/source-availability`, (route) =>
      route.fulfill({
        json: {
          workCode: code,
          checkedAt: "2026-01-01T00:00:00Z",
          sources: [
            sourceAvailabilitySourceFixture({
              sourceId: 1,
              status: "available",
              primaryCode: code,
              hasRemote: true,
              workId: 91,
            }),
            sourceAvailabilitySourceFixture({
              sourceId: 2,
              status: "available",
              primaryCode: code,
              hasRemote: true,
              workId: backupNeedsIdentityCheck ? null : 91,
            }),
          ],
        },
      }),
    );
    await page.route(`**/api/remote-sources/2/works/${code}/tracks`, (route) =>
      route.fulfill({ json: { tracks: [remoteTrackFixture({ title: "Example alternate track.mp3" })] } }),
    );
    await page.route(`**/api/remote-sources/*/works/${code}`, async (route) => {
      const secondSource = new URL(route.request().url()).pathname.startsWith("/api/remote-sources/2/");
      if (secondSource) backupMetadataReads += 1;
      if (secondSource && personalNote === "Example revised note" && failIdentityRefresh) {
        failIdentityRefresh = false;
        if (backupIdentity === "refresh timed out" || refreshPending) await delayedRefresh;
        return route.fulfill({ status: 503, json: { error: "unavailable" } });
      }
      const backupUnresolved = secondSource && backupNeedsIdentityCheck && personalNote === "Example note";
      return route.fulfill({
        json: {
          ...remoteWorkDetailFixture({
            primaryCode: code,
            workId: backupUnresolved ? null : 91,
            title: "Remote Japanese work",
            sourceId: secondSource ? 2 : 1,
            languageEditions: switchesEdition
              ? [
                  {
                    remoteCode: code,
                    language: "JPN",
                    label: "Japanese",
                    displayOrder: 1,
                    current: true,
                    origin: true,
                  },
                  {
                    remoteCode: editionCode,
                    language: "ENG",
                    label: "English",
                    displayOrder: 2,
                    current: false,
                    origin: false,
                  },
                ]
              : [],
          }),
          listeningStatus: backupUnresolved ? "unmarked" : listeningStatus,
          favorite: !backupUnresolved && favorite,
          personalRating: backupUnresolved ? null : personalRating,
          personalNote: backupUnresolved ? "" : personalNote,
        },
      });
    });
    await page.route(`**/api/remote-sources/2/works/${editionCode}`, (route) =>
      route.fulfill({
        json: {
          ...remoteWorkDetailFixture({ primaryCode: editionCode, workId: 92, sourceId: 2 }),
          personalNote: editionNote,
          personalRating: 2,
        },
      }),
    );
    await page.route(`**/api/remote-sources/2/works/${editionCode}/tracks`, (route) =>
      route.fulfill({ json: { tracks: [remoteTrackFixture({ title: "Example edition track.mp3" })] } }),
    );
    await page.route(`**/api/remote-sources/2/works/${editionCode}/user-state`, (route) => {
      editionNote = route.request().postDataJSON().note;
      return route.fulfill({
        json: {
          workId: 92,
          listeningStatus: "unmarked",
          favorite: false,
          personalRating: 2,
          personalNote: editionNote,
        },
      });
    });
    await page.route("**/api/shared-associations?kind=work&id=92", (route) => route.fulfill({ json: [] }));
    await page.route(`**/api/remote-sources/*/works/${code}/{fetch,track,sync}`, (route) => {
      downloadRequests += 1;
      return route.abort();
    });
    await page.route(`**${endpoint}/user-state`, async (route) => {
      const patch = route.request().postDataJSON();
      if ("note" in patch) {
        noteRequested = true;
        await noteCanSave;
      }
      if ("rating" in patch) personalRating = patch.rating;
      if ("note" in patch) personalNote = patch.note;
      return route.fulfill({ json: { workId: 91, listeningStatus, favorite, personalRating, personalNote } });
    });
    await page.route("**/api/shared-associations?kind=work&id=91", (route) => route.fulfill({ json: [] }));
    await page.route("**/api/works/91/user-state", (route) => {
      listeningStatus = route.request().postDataJSON().listeningStatus;
      return route.fulfill({ json: { workId: 91, listeningStatus, favorite, personalRating, personalNote } });
    });
    await page.route("**/api/favorite-lists", (route) => route.fulfill({ json: [list] }));
    await page.route("**/api/works/91/favorite-lists", (route) => {
      if (route.request().method() === "GET") return route.fulfill({ json: [{ ...list, selected: favorite }] });
      favorite = route.request().postDataJSON().listIds.includes(list.id);
      return route.fulfill({ json: { workId: 91, favorite, lists: [{ ...list, selected: favorite }] } });
    });
    await page.goto("/");
    await page.getByRole("button", { name: "Example Remote", exact: true }).click();
    const sourceLibraryURL = page.url();
    await page.getByText("Remote Japanese work", { exact: true }).click();
    const detailHistoryLength = await page.evaluate(() => window.history.length);

    await expect(page.getByRole("button", { name: "Mark: Listening", exact: true })).toBeVisible({ timeout: 2000 });
    await page.getByRole("button", { name: "Mark: Listening", exact: true }).click();
    await page.getByRole("button", { name: "Unmarked", exact: true }).click();
    await expect(page.getByRole("button", { name: "Mark: Unmarked", exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Add to list", exact: true }).click();
    await page.getByRole("checkbox", { name: "Add to Example list", exact: true }).click();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("button", { name: "Favorite lists", exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Info", exact: true }).click();
    await expect(page.getByRole("region", { name: "Shared association", exact: true })).toBeVisible({ timeout: 1500 });
    const note = page.getByRole("textbox", { name: "Personal note", exact: true });
    await expect(note).toHaveValue("Example note");
    await note.fill("Example revised note");
    await page.getByRole("button", { name: "Clear rating", exact: true }).click();
    await expect.poll(() => personalRating).toBe(null);
    await expect(note).toHaveValue("Example revised note");
    expect(personalNote).toBe("Example note");
    await page.getByRole("region", { name: "Personal review" }).getByRole("button", { name: "Save review" }).click();
    const rating = page.getByRole("combobox", { name: "Personal rating", exact: true });
    const saveReview = page.getByRole("button", { name: "Save review", exact: true });
    let readsBeforeSave = 0;
    try {
      await expect.poll(() => noteRequested).toBe(true);
      await page.getByRole("button", { name: "Directory", exact: true }).click();
      await page.getByRole("button", { name: /Example Backup/ }).click();
      await expect(page.getByText("Example alternate track.mp3", { exact: true })).toBeVisible();
      expect(new URL(page.url()).searchParams.get("source")).toBe("2");
      expect(await page.evaluate(() => window.history.length)).toBe(detailHistoryLength);
      if (!backupNeedsIdentityCheck) {
        await expect(page.getByRole("button", { name: "Mark: Unmarked", exact: true })).toBeVisible();
        await expect(page.getByRole("button", { name: "Favorite lists", exact: true })).toBeVisible();
      }
      await page.getByRole("button", { name: "Info", exact: true }).click();
      await expect(note).toHaveValue(backupNeedsIdentityCheck ? "" : "Example note");
      readsBeforeSave = backupMetadataReads;
      await rating.selectOption("4");
      await expect(saveReview).toBeDisabled({ timeout: 1000 });
      if (backupIdentity === "refresh timed out") await page.clock.install();
    } finally {
      finishNoteSave();
    }
    await expect.poll(() => personalNote).toBe("Example revised note");
    if (identityRefreshFails) {
      await expect.poll(() => backupMetadataReads - readsBeforeSave).toBe(1);
      if (backupIdentity === "refresh timed out") await page.clock.fastForward(21_000);
      try {
        const error = page.getByRole("alert", { name: "Personal review", exact: true });
        if (!refreshPending) await expect(error).toBeVisible({ timeout: 2500 });
        await expect(note).toHaveValue("");
        await expect(rating).toHaveValue("4");
        await expect(saveReview).toBeDisabled();
        if (switchesEdition) {
          await page.getByRole("button", { name: "Directory edition", exact: true }).click();
          await page
            .getByRole("dialog", { name: "Directory edition", exact: true })
            .getByRole("menuitemradio")
            .filter({ hasText: editionCode })
            .click();
          await expect(note).toHaveValue("Example edition note");
          await expect(rating).toHaveValue("2");
          releaseDelayedRefresh();
          await note.fill("Example edition draft");
          await expect(saveReview).toBeEnabled({ timeout: 1500 });
          await expect(error).toBeHidden();
          await saveReview.click();
          await expect.poll(() => editionNote).toBe("Example edition draft");
          await expect(note).toHaveValue("Example edition draft");
          expect(personalNote).toBe("Example revised note");
          expect(downloadRequests).toBe(0);
          return;
        }
        await page.getByRole("button", { name: "Directory", exact: true }).click();
        await expect(page.getByText("Example alternate track.mp3", { exact: true })).toBeVisible();
        await page.getByRole("button", { name: "Info", exact: true }).click();
        await expect(error).toBeVisible();
        await error.getByRole("button", { name: "Retry", exact: true }).click();
        await expect(error).toBeHidden();
      } finally {
        releaseDelayedRefresh();
      }
    }
    await expect(note).toHaveValue("Example revised note", { timeout: 2500 });
    await expect(rating).toHaveValue("4");
    await expect(saveReview).toBeEnabled();
    expect(backupMetadataReads - readsBeforeSave).toBe(identityRefreshFails ? 2 : backupNeedsIdentityCheck ? 1 : 0);
    expect(downloadRequests).toBe(0);
    await page.reload();
    await page.getByRole("button", { name: "Info", exact: true }).click();
    await expect(note).toHaveValue("Example revised note");
    await expect(rating).toHaveValue("");
    await page.goBack();
    await expect(page).toHaveURL(sourceLibraryURL);
    await expect(page.getByText("Remote Japanese work", { exact: true })).toBeVisible();
  });
}
