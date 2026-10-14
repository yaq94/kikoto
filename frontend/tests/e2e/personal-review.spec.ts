import { expect, test } from "@playwright/test";
import { mockApplication, mockRemoteSource, work } from "./fixtures/player-library";
import { remoteWorkDetailFixture, workDetailFixture } from "./fixtures/api";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("personal review saves, retries and clears only the chosen field @desktop", async ({ page }) => {
  await mockApplication(page, undefined, false, 1, 0, [], undefined, { authenticated: true });
  let rating: number | null = null;
  let note = "";
  let failNext = false;
  const writes: Record<string, unknown>[] = [];
  await page.route(/\/api\/works\/(1|RJ00000000)(\?.*)?$/, (route) =>
    route.fulfill({ json: { ...workDetailFixture(work), personalRating: rating, personalNote: note } }),
  );
  await page.route("**/api/works/1/user-state", async (route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>;
    writes.push(body);
    if (failNext) {
      failNext = false;
      await route.fulfill({ status: 503, json: { error: "Synthetic temporary failure" } });
      return;
    }
    if ("rating" in body) rating = body.rating as number | null;
    if ("note" in body) note = body.note as string;
    await route.fulfill({
      json: { workId: 1, listeningStatus: "none", favorite: false, personalRating: rating, personalNote: note },
    });
  });
  await page.goto("/RJ00000000");
  const editor = page.getByRole("region", { name: "Personal review", exact: true });
  await editor.getByLabel("Personal rating", { exact: true }).selectOption("0");
  await editor.getByLabel("Personal note", { exact: true }).fill("Example private note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  expect(writes[0]).toEqual({ rating: 0, note: "Example private note" });
  await page.reload();
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("0");
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("Example private note");
  failNext = true;
  await editor.getByLabel("Personal note", { exact: true }).fill("Updated example note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("alert")).toBeVisible();
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("Updated example note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  expect(writes.at(-1)).toEqual({ note: "Updated example note" });
  await editor.getByRole("button", { name: "Clear rating", exact: true }).click();
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("");
  expect(writes.at(-1)).toEqual({ rating: null });
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("Updated example note");
  await editor.getByRole("button", { name: "Clear note", exact: true }).click();
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("");
  expect(writes.at(-1)).toEqual({ note: "" });
});

test("remote-only review registers metadata and reopens with the saved review @desktop", async ({ page }) => {
  await mockRemoteSource(page, () => undefined);
  let workId: number | null = null;
  let rating: number | null = null;
  let note = "";
  const writes: Record<string, unknown>[] = [];
  await page.route("**/api/remote-sources/1/works/RJ00000051", (route) =>
    route.fulfill({
      json: remoteWorkDetailFixture({ primaryCode: "RJ00000051", workId, personalRating: rating, personalNote: note }),
    }),
  );
  await page.route("**/api/remote-sources/1/works/RJ00000051/user-state", async (route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>;
    writes.push(body);
    workId = 91;
    if ("rating" in body) rating = body.rating as number | null;
    if ("note" in body) note = body.note as string;
    await route.fulfill({
      json: { workId, personalRating: rating, personalNote: note, listeningStatus: "none", favorite: false },
    });
  });
  await page.goto("/RJ00000051?source=1");
  const editor = page.getByRole("region", { name: "Personal review", exact: true });
  await editor.getByLabel("Personal rating", { exact: true }).selectOption("4");
  await editor.getByLabel("Personal note", { exact: true }).fill("Remote example note");
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  expect(writes[0]).toEqual({ rating: 4, note: "Remote example note" });
  await page.reload();
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("4");
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("Remote example note");
  await editor.getByLabel("Personal note", { exact: true }).fill("Unsaved next note");
  await editor.getByRole("button", { name: "Clear rating", exact: true }).click();
  await expect(editor.getByLabel("Personal rating", { exact: true })).toHaveValue("");
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("Unsaved next note");
  expect(writes.at(-1)).toEqual({ rating: null });
  await editor.getByRole("button", { name: "Save review", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("Review saved.");
  expect(writes.at(-1)).toEqual({ note: "Unsaved next note" });
  await editor.getByRole("button", { name: "Clear note", exact: true }).click();
  await expect(editor.getByLabel("Personal note", { exact: true })).toHaveValue("");
  expect(writes.at(-1)).toEqual({ note: "" });
});
