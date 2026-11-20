import { expect, test, type Page } from "@playwright/test";
import { mockApplication, work } from "./fixtures/player-library";
import type { ApiResponse } from "./fixtures/api";

const longTitle = "Example title for the first work with enough words to take more than two lines on a narrow card";
const secondTitle = "Example native episode";
const version = "synthetic-translation-version";

async function mockTranslations(page: Page, isNative: (text: string) => boolean, onText?: (text: string) => void) {
  await page.route("**/api/auth/me/preferences", (route) =>
    route.fulfill({ json: { metadataLanguages: ["en", "origin"] } }),
  );
  await page.route("**/api/content-translations/version", (route) => route.fulfill({ json: { version } }));
  await page.route("**/api/content-translations/batch", (route) => {
    const { items } = route.request().postDataJSON() as { items: Array<{ key: string; text: string }> };
    items.forEach(({ text }) => onText?.(text));
    return route.fulfill({
      json: {
        items: items.map(({ key, text }) => ({
          key,
          original: text,
          text: isNative(text) ? text : `Translated: ${text}`,
          language: "en",
          version,
          status: isNative(text) ? "native" : "translated",
        })),
      },
    });
  });
}

test("a long card title and its original control remain independent keyboard targets", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await mockApplication(page, undefined, false, 1, 0, [], undefined, {
    authenticated: true,
    work: { ...work, title: longTitle },
  });
  await mockTranslations(page, () => false);
  await page.goto("/library");
  const card = page.getByTestId("work-card");
  const heading = card.getByRole("heading", { name: `Translated: ${longTitle}`, exact: true });
  await expect(heading).toBeVisible();
  const titleButton = heading.getByRole("button");
  await titleButton.press("Tab");
  await page.keyboard.press("Shift+Tab");
  await expect(titleButton).toBeFocused();
  await expect(titleButton).toHaveCSS("outline-style", "solid");
  const toggle = card.getByRole("button", { name: "Show original", exact: true });
  await page.keyboard.press("Tab");
  await expect(toggle).toBeFocused();
  await toggle.press("Enter");
  await expect(card.getByRole("heading", { name: longTitle, exact: true })).toBeVisible();
  await expect(page).toHaveURL(/\/library/);
  await card.getByRole("button", { name: longTitle, exact: true }).press("Enter");
  await expect(page).toHaveURL(/RJ00000000/);
});

test("a series episode controls its own translation and native text has no toggle", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await mockApplication(page, undefined, false, 0, 0, [], undefined, { authenticated: true });
  await mockTranslations(page, (text) => text === secondTitle);
  await page.route("**/api/catalog-series/1", (route) =>
    route.fulfill({
      json: {
        seriesId: 1,
        title: "Example series heading",
        workId: null,
        personalRating: null,
        personalNote: "",
        episodes: [
          {
            catalogEntryId: 21,
            workId: null,
            sourceId: 1,
            primaryCode: "",
            position: 1,
            title: longTitle,
            sourceName: "Example source",
          },
          {
            catalogEntryId: 22,
            workId: null,
            sourceId: 1,
            primaryCode: "",
            position: 2,
            title: secondTitle,
            sourceName: "Example source",
          },
        ],
      } satisfies ApiResponse<"getCatalogSeries">,
    }),
  );
  await page.goto("/catalog-series/1");
  const heading = page.getByRole("heading", { level: 1 });
  const rows = page.getByRole("list", { name: "Episodes", exact: true }).getByRole("listitem");
  const first = rows.nth(0);
  const second = rows.nth(1);
  await expect(heading).toContainText("Translated: Example series heading");
  await expect(first).toContainText(`Translated: ${longTitle}`);
  const original = first.getByRole("button", { name: "Show original", exact: true });
  const open = first.getByRole("button", { name: "Open episode", exact: true });
  await expect(original).toBeVisible();
  await expect(open).toBeEnabled();
  await original.tap();
  await expect(first).not.toContainText("Translated:");
  await expect(heading).toContainText("Translated: Example series heading");
  await expect(second).toHaveText(new RegExp(secondTitle));
  await expect(second.getByRole("button", { name: /Show original|Retry translation/ })).toHaveCount(0);
});

test("mobile directory hides review controls and preserves an unfinished review", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 740 });
  await mockApplication(page, undefined, false, 1, 0, [], undefined, { authenticated: true });
  await page.goto("/RJ00000000");
  const info = page.getByRole("button", { name: "Info", exact: true });
  const directory = page.getByRole("button", { name: "Directory", exact: true });
  const note = page.getByRole("region", { name: "Personal review", exact: true }).getByLabel("Personal note");
  await info.click();
  await note.fill("Example unfinished note");
  await directory.press("Enter");
  await expect(directory).toHaveAttribute("aria-pressed", "true");
  await expect(note).not.toBeVisible();
  await info.press("Space");
  await expect(info).toHaveAttribute("aria-pressed", "true");
  await expect(note).toHaveValue("Example unfinished note");
});

test("successful title retry checks the current result, and Mini keeps translation controls accessible @desktop", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await mockApplication(page, undefined, false, 1, 0, [], undefined, {
    authenticated: true,
    work: { ...work, title: longTitle },
  });
  let titleRequests = 0;
  await mockTranslations(
    page,
    () => false,
    (text) => {
      if (text === longTitle) titleRequests++;
    },
  );
  await page.goto("/RJ00000000");
  const hero = page.getByTestId("detail-hero");
  const title = "Translated: " + longTitle;
  const detailTitle = hero.getByRole("heading", { name: title, exact: true });
  await expect(detailTitle).toBeVisible();
  await hero.getByRole("button", { name: "Retry translation", exact: true }).press("Enter");
  await expect.poll(() => titleRequests).toBe(2);
  await expect(detailTitle).toBeVisible();
  await expect(page).toHaveURL(/RJ00000000/);

  await page.goto("/library");
  const card = page.getByTestId("work-card");
  const heading = card.getByRole("heading", { name: title, exact: true });
  const retry = card.getByRole("button", { name: "Retry translation", exact: true });
  await expect(retry).toBeVisible();
  await page.getByRole("button", { name: /^Display options/ }).click();
  await page.getByRole("radio", { name: /^Mini cards/ }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Display options", exact: true })).not.toBeVisible();
  await expect(heading).toBeVisible();
  const options = card.getByRole("button", { name: "Translation options", exact: true });
  await options.press("Enter");
  const actions = page.getByRole("dialog", { name: "Translation options", exact: true });
  await expect(actions.getByRole("button", { name: "Retry translation", exact: true })).toBeVisible();
  const showOriginal = actions.getByRole("button", { name: "Show original", exact: true });
  await expect(showOriginal).toBeFocused();
  await showOriginal.press("Enter");
  await expect(card.getByRole("heading", { name: longTitle, exact: true })).toBeVisible();
  await expect(actions.getByRole("button", { name: "Show translation", exact: true })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(actions).not.toBeVisible();
  await expect(options).toBeFocused();
  await expect(page).toHaveURL(/\/library/);
});

test("work-wide original control appears only when a translated field can change", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await mockApplication(page, undefined, false, 1, 0, [], undefined, {
    authenticated: true,
    work: { ...work, title: secondTitle, tags: [] },
    detailMetadataPresentation: {
      defaultVariantKey: "original",
      variants: [
        {
          key: "original",
          language: "ja-jp",
          title: secondTitle,
          description: "Example translated introduction",
          tags: [],
          origin: true,
        },
      ],
    },
  });
  await mockTranslations(page, (text) => text !== "Example translated introduction");
  await page.goto("/RJ00000000");
  const title = page.getByRole("heading", { name: secondTitle, exact: true });
  await expect(title).toBeVisible();
  const toggle = page.getByRole("button", { name: "Show original text for all fields", exact: true });
  await expect(toggle).toHaveCount(0);
  await page.getByRole("button", { name: "Info", exact: true }).click();
  await expect(page.getByText("Translated: Example translated introduction", { exact: true })).toBeVisible();
  await toggle.press("Enter");
  await expect(page.getByText("Example translated introduction", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Show translations for all fields", exact: true }).press("Enter");
  await expect(page.getByText("Translated: Example translated introduction", { exact: true })).toBeVisible();
});

test("a detail title can show its original without changing the translated introduction @desktop", async ({ page }) => {
  const description = "Example independently translated introduction";
  await mockApplication(page, undefined, false, 1, 0, [], undefined, {
    authenticated: true,
    work: { ...work, title: longTitle, tags: [] },
    detailMetadataPresentation: {
      defaultVariantKey: "original",
      variants: [{ key: "original", language: "ja-jp", title: longTitle, description, tags: [], origin: true }],
    },
  });
  await mockTranslations(page, () => false);
  await page.goto("/RJ00000000");
  const hero = page.getByTestId("detail-hero");
  await expect(hero.getByRole("heading", { name: `Translated: ${longTitle}`, exact: true })).toBeVisible();
  await expect(page.getByText(`Translated: ${description}`, { exact: true })).toBeVisible();
  await hero.getByRole("button", { name: "Show original", exact: true }).press("Enter");
  await expect(hero.getByRole("heading", { name: longTitle, exact: true })).toBeVisible();
  await expect(page.getByText(`Translated: ${description}`, { exact: true })).toBeVisible();
  await hero.getByRole("button", { name: "Show original text for all fields", exact: true }).press("Enter");
  await expect(page.getByText(description, { exact: true })).toBeVisible();
});

test("a Mini card keeps retry available after a failed translation on a narrow screen", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await mockApplication(page, undefined, false, 1, 0, [], undefined, {
    authenticated: true,
    work: { ...work, title: longTitle },
  });
  await mockTranslations(page, () => false);
  let available = false;
  await page.route("**/api/content-translations/batch", async (route) => {
    if (available) return route.fallback();
    const { items } = route.request().postDataJSON() as { items: Array<{ key: string; text: string }> };
    return route.fulfill({
      json: {
        items: items.map(({ key, text }) => ({
          key,
          original: text,
          text,
          language: "en",
          version,
          status: "failed",
          code: "translation_unavailable",
        })),
      },
    });
  });
  await page.goto("/library");
  await page.getByRole("button", { name: /^Display options/ }).click();
  await page.getByRole("radio", { name: /^Mini cards/ }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Display options", exact: true })).not.toBeVisible();
  const card = page.getByTestId("work-card");
  await expect(card.getByRole("heading", { name: longTitle, exact: true })).toBeVisible();
  const options = card.getByRole("button", { name: "Translation options", exact: true });
  await options.tap();
  const actions = page.getByRole("dialog", { name: "Translation options", exact: true });
  await expect(actions.getByRole("button", { name: "Show original", exact: true })).toHaveCount(0);
  available = true;
  await actions.getByRole("button", { name: "Retry translation", exact: true }).tap();
  await expect(card.getByRole("heading", { name: `Translated: ${longTitle}`, exact: true })).toBeVisible();
  await expect(actions.getByRole("button", { name: "Show original", exact: true })).toBeVisible();
});

for (const width of [320, 390]) {
  test(`at ${width}px, tapping the edge of a Mini title opens the work`, async ({ page }) => {
    await page.setViewportSize({ width, height: 740 });
    await mockApplication(page, undefined, false, 1, 0, [], undefined, {
      authenticated: true,
      work: { ...work, title: longTitle },
    });
    await mockTranslations(page, () => false);
    await page.goto("/library");
    await page.getByRole("button", { name: /^Display options/ }).click();
    await page.getByRole("radio", { name: /^Mini cards/ }).click();
    await page.keyboard.press("Escape");

    const card = page.getByTestId("work-card");
    const title = card.getByRole("button", { name: `Translated: ${longTitle}`, exact: true });
    const options = card.getByRole("button", { name: "Translation options", exact: true });
    const actions = page.getByRole("dialog", { name: "Translation options", exact: true });
    await options.tap();
    await expect(actions.getByRole("button", { name: "Show original", exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(actions).toBeHidden();

    await title.scrollIntoViewIfNeeded();
    const area = await title.boundingBox();
    if (!area) throw new Error("Mini title is not visible");
    // Exercise the boundary where the adjacent icon extends its touch target.
    await page.touchscreen.tap(area.x + area.width - 1, area.y + area.height / 2);
    await expect(actions).toBeHidden();
    await expect(page).toHaveURL(/\/RJ00000000(?:[/?#]|$)/);
  });
}

test("Mini continues keyboard focus when a delayed native title makes actions unnecessary @desktop", async ({
  page,
}) => {
  await mockApplication(page, undefined, false, 1, 0, [], undefined, {
    authenticated: true,
    work: { ...work, title: secondTitle },
  });
  let release!: () => void;
  const reply = new Promise<void>((resolve) => {
    release = resolve;
  });
  await mockTranslations(page, () => true);
  await page.route("**/api/content-translations/batch", async (route) => {
    await reply;
    await route.fallback();
  });
  try {
    await page.goto("/library");
    await page.getByRole("button", { name: /^Display options/ }).click();
    await page.getByRole("radio", { name: /^Mini cards/ }).click();
    await page.keyboard.press("Escape");
    const card = page.getByTestId("work-card");
    const title = card.getByRole("heading").getByRole("button");
    const options = card.getByRole("button", { name: "Translation options", exact: true });
    await options.press("Enter");
    const actions = page.getByRole("dialog", { name: "Translation options", exact: true });
    await expect(actions).toBeVisible();
    await expect.poll(() => actions.evaluate((el) => el.contains(document.activeElement))).toBe(true);
    release();
    await expect(actions).not.toBeVisible();
    await expect(title).toBeFocused();
  } finally {
    release();
  }
});
