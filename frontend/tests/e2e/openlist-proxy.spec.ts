import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });

test("administrator saves an OpenList source proxy override through Settings @desktop", async ({ page }) => {
  const backend = process.env.OPENLIST_API_URL;
  test.skip(!backend, "Start the isolated openlist-fixture and set OPENLIST_API_URL.");
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
  const initial = await (await page.request.get(`${backend}/api/settings`)).json();
  expect(
    (
      await page.request.patch(`${backend}/api/settings`, {
        data: {
          proxy: {
            ...initial.proxy,
            routes: { ...initial.proxy.routes, sources: {} },
            proxies: [
              { id: "example", name: "Example Proxy", kind: "custom", scheme: "http", host: "192.0.2.10", port: 8080 },
            ],
          },
        },
      })
    ).ok(),
  ).toBe(true);
  await page.goto("/settings?tab=proxy");
  const selection = page.getByRole("combobox", { name: "Proxy for Example Remote A", exact: true });
  await expect(selection).toHaveValue("inherit");
  await selection.selectOption("proxy:example");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/api/settings`)).json()).proxy.routes.sources["1"])
    .toEqual({ mode: "proxy", proxyIds: ["example"] });
  await page.reload();
  await expect(selection).toHaveValue("proxy:example");
  await selection.selectOption("direct");
  await expect
    .poll(async () => (await (await page.request.get(`${backend}/api/settings`)).json()).proxy.routes.sources["1"])
    .toEqual({ mode: "direct", proxyIds: [] });
});
