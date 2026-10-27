import { spawn, type ChildProcess } from "node:child_process";
import { once } from "node:events";
import { copyFile, mkdtemp, mkdir, rm } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

test.use({ screenshot: "off", trace: "off", video: "off" });
test.describe.configure({ mode: "serial" });
test.skip(!process.env.KIKOTO_E2E_BACKEND, "Set KIKOTO_E2E_BACKEND to the locally built application.");

const backendURL = process.env.KIKOTO_E2E_API_URL ?? "http://127.0.0.1:17611";
let backend: ChildProcess;
let directory: string;
let upstreamURL: string;
let upstreamStatus = 200;
let cancelledRequests = 0;
const upstreamRequests: Array<{ model: string; authorization: string }> = [];
const upstream = createServer(async (request, response) => {
  let body = "";
  for await (const chunk of request) body += chunk.toString();
  const payload = JSON.parse(body) as { model: string };
  upstreamRequests.push({ model: payload.model, authorization: request.headers.authorization ?? "" });
  if (upstreamStatus === 202) {
    response.on("close", () => cancelledRequests++);
    return;
  }
  response.writeHead(upstreamStatus, { "Content-Type": "application/json" });
  response.end(
    upstreamStatus === 200
      ? JSON.stringify({ choices: [{ message: { content: "今天天气真好。" }, finish_reason: "stop" }] })
      : JSON.stringify({ error: "synthetic upstream authentication error" }),
  );
});

test.beforeAll(async ({ request, baseURL }) => {
  directory = await mkdtemp(path.join(tmpdir(), "kikoto-translation-e2e-"));
  await mkdir(path.join(directory, "data"));
  await mkdir(path.join(directory, "cache"));
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const address = upstream.address();
  if (!address || typeof address === "string") throw new Error("No upstream listener");
  const endpoint = new URL("http://127.0.0.1/v1");
  endpoint.port = String(address.port);
  upstreamURL = endpoint.href;
  const executable =
    process.platform === "win32" ? path.join(directory, "kikoto.exe") : process.env.KIKOTO_E2E_BACKEND!;
  if (process.platform === "win32") await copyFile(process.env.KIKOTO_E2E_BACKEND!, executable);
  backend = spawn(executable, [], {
    windowsHide: true,
    stdio: "ignore",
    env: {
      ...process.env,
      KIKOTO_MODE: "production",
      KIKOTO_HTTP_ADDR: new URL(backendURL).host,
      KIKOTO_DB_PATH: path.join(directory, "app.db"),
      KIKOTO_DATA_ROOT: path.join(directory, "data"),
      KIKOTO_CACHE_ROOT: path.join(directory, "cache"),
      KIKOTO_ROOT_ACCOUNT_MODE: "environment",
      KIKOTO_ROOT_USERNAME: "root",
      KIKOTO_ROOT_PASSWORD: "synthetic-browser-password",
      KIKOTO_ALLOWED_ORIGINS: baseURL!,
      KIKOTO_STATIC_DIR: "",
    },
  });
  await expect
    .poll(async () => {
      if (backend.exitCode !== null) throw new Error(`Backend exited: ${backend.exitCode}`);
      return request.get(`${backendURL}/health`).then(
        (res) => res.status(),
        () => 0,
      );
    })
    .toBe(200);
  expect(
    (
      await request.post(`${backendURL}/api/auth/login`, {
        data: { username: "root", password: "synthetic-browser-password" },
      })
    ).ok(),
  ).toBe(true);
  expect((await request.put(`${backendURL}/api/library/layout`, { data: { mode: "standard" } })).ok()).toBe(true);
  expect(
    (
      await request.post(`${backendURL}/api/library/onboarding/complete`, {
        data: { startupScan: false, watchFolders: false },
      })
    ).ok(),
  ).toBe(true);
});

test.afterAll(async () => {
  if (backend && backend.exitCode === null && backend.signalCode === null) {
    const closed = once(backend, "close");
    backend.kill();
    await closed;
  }
  upstream.closeAllConnections();
  await new Promise<void>((resolve) => upstream.close(() => resolve()));
  if (
    directory &&
    path.dirname(directory) === path.resolve(tmpdir()) &&
    path.basename(directory).startsWith("kikoto-translation-e2e-")
  ) {
    await rm(directory, { recursive: true, force: true });
  }
});

test("administrator saves, reopens and tests translation without changing interface language @desktop", async ({
  page,
}) => {
  expect(
    (
      await page.request.post(`${backendURL}/api/auth/login`, {
        data: { username: "root", password: "synthetic-browser-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.request.patch(`${backendURL}/api/auth/me`, { data: { uiLocale: "en" } });
  await page.goto("/settings");
  await page.getByRole("tab", { name: "Translation", exact: true }).click();
  await expect(page.getByLabel("Model invocation name", { exact: true })).toHaveValue("Index-Translate-35B-A3B");
  await page.getByLabel("API base URL", { exact: true }).fill(upstreamURL);
  await page.getByLabel("API key", { exact: true }).fill("synthetic-browser-key");
  await page.getByLabel("Model invocation name", { exact: true }).fill("custom/Browser-Model");
  await page.getByRole("button", { name: "Save translation settings", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Translation settings saved.");
  await expect(page.getByLabel("API key", { exact: true })).toHaveValue("");
  const saved = await (await page.request.get(`${backendURL}/api/settings/translation`)).json();
  expect(saved).toMatchObject({ model: "custom/Browser-Model", hasApiKey: true, apiKeyMask: "********" });
  expect(saved).not.toHaveProperty("apiKey");
  await page.reload();
  await expect(page.getByLabel("Model invocation name", { exact: true })).toHaveValue("custom/Browser-Model");
  await page.getByLabel("Trial text", { exact: true }).fill("The weather is nice today.");
  await page.getByLabel("Translation target", { exact: true }).selectOption("zh-Hans");
  await page.getByRole("button", { name: "Test saved configuration", exact: true }).click();
  await expect(page.getByLabel("Translation result", { exact: true })).toHaveText("今天天气真好。");
  expect(upstreamRequests).toEqual([{ model: "custom/Browser-Model", authorization: "Bearer synthetic-browser-key" }]);
  expect((await (await page.request.get(`${backendURL}/api/auth/me`)).json()).user.uiLocale).toBe("en");
  upstreamStatus = 401;
  await page.getByRole("button", { name: "Test saved configuration", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("The translation service rejected its credentials.");
  await expect(page.getByLabel("Translation result", { exact: true })).toHaveCount(0);
  await page.getByRole("checkbox", { name: "Clear saved API key", exact: true }).check();
  await page.getByRole("button", { name: "Save translation settings", exact: true }).click();
  await expect
    .poll(async () => (await (await page.request.get(`${backendURL}/api/settings/translation`)).json()).hasApiKey)
    .toBe(false);
});

test("translation cancellation releases the request and ordinary users cannot open configuration @desktop", async ({
  page,
}) => {
  expect(
    (
      await page.request.post(`${backendURL}/api/auth/login`, {
        data: { username: "root", password: "synthetic-browser-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.goto("/settings?tab=translation");
  await page.getByLabel("Trial text", { exact: true }).fill("Cancel this trial.");
  upstreamStatus = 202;
  const count = upstreamRequests.length;
  await page.getByRole("button", { name: "Test saved configuration", exact: true }).click();
  await expect.poll(() => upstreamRequests.length).toBe(count + 1);
  await page.getByRole("button", { name: "Cancel trial", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText("The translation request was cancelled.");
  await expect.poll(() => cancelledRequests).toBe(1);
  upstreamStatus = 200;
  await page.getByRole("button", { name: "Test saved configuration", exact: true }).click();
  await expect(page.getByLabel("Translation result", { exact: true })).toHaveText("今天天气真好。");
  expect(
    (
      await page.request.post(`${backendURL}/api/users`, {
        data: { username: "listener", password: "synthetic-listener-password", role: "user" },
      })
    ).ok(),
  ).toBe(true);
  await page.request.post(`${backendURL}/api/auth/logout`);
  expect(
    (
      await page.request.post(`${backendURL}/api/auth/login`, {
        data: { username: "listener", password: "synthetic-listener-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.reload();
  await expect(page.getByRole("tab", { name: "Account", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("tab", { name: "Translation", exact: true })).toHaveCount(0);
  expect((await page.request.get(`${backendURL}/api/settings/translation`)).status()).toBe(403);
});

test("a trial keystroke past the input limit is refused with an inline note @desktop", async ({ page }) => {
  expect(
    (
      await page.request.post(`${backendURL}/api/auth/login`, {
        data: { username: "root", password: "synthetic-browser-password" },
      })
    ).ok(),
  ).toBe(true);
  await page.request.patch(`${backendURL}/api/auth/me`, { data: { uiLocale: "en" } });
  await page.goto("/settings?tab=translation");
  const field = page.getByLabel("Trial text", { exact: true });
  await field.fill("Short text.");
  await expect(page.locator("#translation-trial-bytes")).toHaveText("11 of 8192 bytes used");
  // One keystroke past 8 KiB must leave the accepted text in place and say so;
  // silently dropping the edit looks like the input was lost.
  await field.fill("x".repeat(8193));
  await expect(page.getByText("The input limit of 8192 bytes is reached; extra text was not added.")).toBeVisible();
  await expect(field).toHaveValue("Short text.");
  // The next accepted edit clears the note; the field is usable again.
  await field.fill("Short.");
  await expect(field).toHaveValue("Short.");
  await expect(page.getByText("The input limit of 8192 bytes is reached; extra text was not added.")).toHaveCount(0);
});
