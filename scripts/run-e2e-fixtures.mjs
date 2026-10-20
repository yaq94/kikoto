#!/usr/bin/env node
// Runs the live-backend Playwright suites against disposable fixtures.
//
// Vite pins its development proxy to 127.0.0.1:7659, so the page is pointed at
// its fixture through VITE_API_BASE_URL instead. Every spec file gets its own
// fixture process, database, Vite server and port pair: catalog-series and
// catalog-series-review write incompatible state, and the OpenList fixture
// selects mutually exclusive roots through OPENLIST_SERIES_FIXTURE.
import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, "..");
const backendDir = path.join(repoRoot, "backend");
const frontendDir = path.join(repoRoot, "frontend");
// A per-process directory keeps two local runs from overwriting each other's
// binaries while Go's build cache still makes the rebuild cheap.
const fixtureBinDir = path.join(
  os.tmpdir(),
  "kikoto-e2e-fixtures",
  String(process.pid),
);
const playwrightCli = path.join(
  frontendDir,
  "node_modules",
  "@playwright",
  "test",
  "cli.js",
);
const viteCli = path.join(
  frontendDir,
  "node_modules",
  "vite",
  "bin",
  "vite.js",
);
const tscCli = path.join(
  frontendDir,
  "node_modules",
  "typescript",
  "bin",
  "tsc",
);

// One entry per spec file. `fixture` names the Go main package; a null fixture
// means the spec starts the real application binary itself (translation-settings).
export const groups = [
  {
    name: "openlist-catalog",
    fixture: "openlist-fixture",
    spec: "openlist-catalog",
    env: { OPENLIST_API_URL: "{fixture}" },
  },
  {
    name: "openlist-proxy",
    fixture: "openlist-fixture",
    spec: "openlist-proxy",
    env: { OPENLIST_API_URL: "{fixture}" },
  },
  {
    name: "source-catalog-index",
    fixture: "openlist-fixture",
    spec: "source-catalog-index",
    env: { OPENLIST_API_URL: "{fixture}" },
  },
  {
    name: "catalog-series",
    fixture: "openlist-fixture",
    spec: "catalog-series",
    env: { OPENLIST_API_URL: "{fixture}" },
    fixtureEnv: { OPENLIST_SERIES_FIXTURE: "1" },
  },
  {
    name: "catalog-series-review",
    fixture: "openlist-fixture",
    spec: "catalog-series-review",
    env: { OPENLIST_API_URL: "{fixture}" },
    fixtureEnv: { OPENLIST_SERIES_FIXTURE: "1" },
  },
  {
    name: "work-matching",
    fixture: "openlist-fixture",
    spec: "work-matching",
    env: { OPENLIST_API_URL: "{fixture}", OPENLIST_MATCHING_FIXTURE: "1" },
    fixtureEnv: { OPENLIST_MATCHING_FIXTURE: "1" },
  },
  {
    name: "shared-association",
    fixture: "openlist-fixture",
    spec: "shared-association",
    env: { OPENLIST_API_URL: "{fixture}", OPENLIST_MATCHING_FIXTURE: "1" },
    fixtureEnv: { OPENLIST_MATCHING_FIXTURE: "1" },
  },
  {
    name: "shared-split",
    fixture: "openlist-fixture",
    spec: "shared-split",
    env: { OPENLIST_API_URL: "{fixture}", OPENLIST_MATCHING_FIXTURE: "1" },
    fixtureEnv: { OPENLIST_MATCHING_FIXTURE: "1" },
  },
  {
    name: "personal-review-live-api",
    fixture: "personal-review-fixture",
    spec: "personal-review-live-api",
    env: { PERSONAL_REVIEW_API_URL: "{fixture}" },
  },
  {
    name: "personal-transfer",
    fixture: "personal-review-fixture",
    spec: "personal-transfer",
    env: { PERSONAL_REVIEW_API_URL: "{fixture}" },
  },
  {
    name: "source-lifecycle",
    fixture: "source-playback-fixture",
    spec: "source-lifecycle",
    env: { SOURCE_PLAYBACK_API_URL: "{fixture}" },
  },
  {
    name: "source-playback-live-api",
    fixture: "source-playback-fixture",
    spec: "source-playback-live-api",
    env: { SOURCE_PLAYBACK_API_URL: "{fixture}" },
  },
  {
    name: "content-translation",
    fixture: "content-translation-fixture",
    spec: "content-translation",
    env: { KIKOTO_CONTENT_TRANSLATION_URL: "{fixture}" },
  },
  {
    name: "traditional-translation",
    fixture: "content-translation-fixture",
    spec: "traditional-translation",
    env: { KIKOTO_CONTENT_TRANSLATION_URL: "{fixture}" },
  },
  {
    // The fixture does not serve static files, so this group builds the
    // production bundle with the fixture URL baked in and runs `vite preview`.
    name: "integrated-acceptance",
    fixture: "integrated-acceptance-fixture",
    spec: "integrated-acceptance",
    env: { INTEGRATED_ACCEPTANCE_URL: "{fixture}" },
    preview: true,
  },
  {
    // translation-settings.spec.ts spawns backend/bin/kikoto itself, so this
    // group builds the application binary instead of starting a fixture.
    name: "translation-settings",
    fixture: null,
    spec: "translation-settings",
    env: {
      KIKOTO_E2E_BACKEND: "{backend}",
      KIKOTO_E2E_API_URL: "http://127.0.0.1:{port}",
    },
    backendBinary: true,
  },
  {
    // Ticket 19's mobile skeleton, favorites navigation and cover-touch flows
    // are mobile-only. The mocked shard runs the desktop project, so these
    // tests need their own bounded Playwright project. AC-28 admits only
    // text, operations, requests and persisted state here; the pre-existing
    // visual definitions stay in their specs but are not this gate.
    name: "mobile-shell",
    fixture: null,
    specs: ["work-detail", "favorites-state", "player"],
    grep: "mobile footer Library and Favorites|returning through the mobile footer keeps the Sales sort|single touch on the full player cover|double-tapping the cover opens the work|shows a directory skeleton while media loads",
    project: "mobile-chromium",
  },
  {
    // Everything else is a mocked-API browser test. It stays in this runner so
    // moving the fixture suites into CI does not drop the existing coverage.
    name: "mocked",
    fixture: null,
    specs: "mocked",
    playwrightShard: true,
  },
];

// User-facing group names expand to the spec groups they contain. Each member
// still gets its own fixture process, database and port pair.
export const bundles = {
  openlist: ["openlist-catalog", "openlist-proxy", "source-catalog-index"],
  series: ["catalog-series", "catalog-series-review"],
  matching: ["work-matching", "shared-association", "shared-split"],
  personal: ["personal-review-live-api", "personal-transfer"],
  "source-playback": ["source-lifecycle", "source-playback-live-api"],
  "content-translation": ["content-translation", "traditional-translation"],
  "integrated-acceptance": ["integrated-acceptance"],
  "translation-settings": ["translation-settings"],
  mocked: ["mocked"],
  "mobile-shell": ["mobile-shell"],
};

// Opt-in performance suites need their own harness and environment.
const optInSpecs = new Set([
  "browse-performance",
  "browse-production-performance",
  "playback-production-performance",
]);

const fixturePackages = {
  "openlist-fixture": {
    env: { OPENLIST_HTTP_ADDR: "127.0.0.1:{port}" },
  },
  "personal-review-fixture": {
    env: { PERSONAL_REVIEW_FIXTURE_ADDR: "127.0.0.1:{port}" },
  },
  "source-playback-fixture": {
    env: {
      SOURCE_PLAYBACK_FIXTURE_ADDR: "127.0.0.1:{port}",
      SOURCE_PLAYBACK_FIXTURE_ORIGIN: "{page}",
    },
  },
  "content-translation-fixture": {
    env: { CONTENT_TRANSLATION_FIXTURE_ADDR: "127.0.0.1:{port}" },
  },
  "integrated-acceptance-fixture": {
    env: { INTEGRATED_ACCEPTANCE_ADDR: "127.0.0.1:{port}" },
  },
};

export function parseArgs(argv) {
  const options = { groups: null, shard: null, list: false, logDir: null };
  for (const arg of argv) {
    if (arg === "--list") options.list = true;
    else if (arg.startsWith("--group="))
      options.groups = arg.slice("--group=".length).split(",").filter(Boolean);
    else if (arg.startsWith("--shard="))
      options.shard = arg.slice("--shard=".length);
    else if (arg.startsWith("--log-dir="))
      options.logDir = path.resolve(arg.slice("--log-dir=".length));
    else throw new Error(`Unknown argument: ${arg}`);
  }
  return options;
}

function log(message) {
  process.stdout.write(`[e2e-fixtures] ${message}\n`);
}

export function parseShard(value) {
  const match = /^(\d+)\/(\d+)$/.exec(value ?? "");
  if (!match) throw new Error(`--shard must look like N/M, received ${value}`);
  const index = Number(match[1]);
  const count = Number(match[2]);
  if (index < 1 || index > count || count < 1)
    throw new Error(`Invalid shard ${value}`);
  return { index, count };
}

export async function discoverMockedSpecs() {
  const fixtureSpecs = new Set(
    groups
      .filter((group) => group.fixture !== null || group.backendBinary)
      .map((group) => group.spec),
  );
  const entries = await readdir(path.join(frontendDir, "tests", "e2e"));
  return entries
    .filter((name) => name.endsWith(".spec.ts"))
    .map((name) => name.slice(0, -".spec.ts".length))
    .filter((name) => !fixtureSpecs.has(name) && !optInSpecs.has(name))
    .sort();
}

function isPortFree(port) {
  return new Promise((resolve) => {
    const server = net.createServer();
    server.unref();
    server.on("error", () => resolve(false));
    server.listen({ host: "127.0.0.1", port, exclusive: true }, () => {
      server.close(() => resolve(true));
    });
  });
}

async function allocatePorts(used) {
  const base = Number(process.env.KIKOTO_E2E_FIXTURE_PORT_BASE ?? 17700);
  if (!Number.isInteger(base) || base < 1024 || base > 65000) {
    throw new Error(
      `KIKOTO_E2E_FIXTURE_PORT_BASE must be an integer between 1024 and 65000, received ${process.env.KIKOTO_E2E_FIXTURE_PORT_BASE}`,
    );
  }
  for (let candidate = base; candidate <= base + 290; candidate += 10) {
    const ports = [candidate, candidate + 1];
    if (ports.some((port) => used.has(port))) continue;
    if ((await Promise.all(ports.map(isPortFree))).every(Boolean)) {
      for (const port of ports) used.add(port);
      return ports;
    }
  }
  throw new Error(
    `No free fixture/Vite port pair between ${base} and ${base + 290}`,
  );
}

function run(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      ...options,
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      const text = chunk.toString();
      stdout += text;
      options.onOutput?.(text, false);
    });
    child.stderr.on("data", (chunk) => {
      const text = chunk.toString();
      stderr += text;
      options.onOutput?.(text, true);
    });
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
}

function executableName(name) {
  return process.platform === "win32" ? `${name}.exe` : name;
}

async function buildFixtures(logDir, names) {
  const logPath = path.join(logDir, "build-fixtures.log");
  const stream = createWriteStream(logPath);
  log(`building ${names.length} fixture binaries`);
  const result = await run(
    "go",
    ["build", "-o", fixtureBinDir, ...names.map((name) => `./tests/${name}`)],
    {
      cwd: backendDir,
      env: process.env,
      onOutput: (text) => stream.write(text),
    },
  );
  stream.end();
  if (result.code !== 0) {
    throw new Error(
      `go build failed (${result.code}); see ${logPath}\n${result.stderr}`,
    );
  }
}

async function buildBackendBinary(logDir) {
  const logPath = path.join(logDir, "build-backend.log");
  const stream = createWriteStream(logPath);
  const output = path.join(fixtureBinDir, executableName("kikoto"));
  log("building the application binary for translation-settings");
  const result = await run("go", ["build", "-o", output, "./cmd/kikoto"], {
    cwd: backendDir,
    env: process.env,
    onOutput: (text) => stream.write(text),
  });
  stream.end();
  if (result.code !== 0) {
    throw new Error(
      `go build ./cmd/kikoto failed (${result.code}); see ${logPath}`,
    );
  }
  return output;
}

async function buildFrontend(fixtureURL, logPath) {
  const stream = createWriteStream(logPath);
  log("building the production frontend for integrated-acceptance");
  for (const args of [
    [tscCli, "-b"],
    [viteCli, "build"],
  ]) {
    const result = await run(process.execPath, args, {
      cwd: frontendDir,
      env: { ...process.env, VITE_API_BASE_URL: fixtureURL },
      onOutput: (text) => stream.write(text),
    });
    if (result.code !== 0) {
      stream.end();
      throw new Error(`Frontend build failed (${result.code}); see ${logPath}`);
    }
  }
  stream.end();
}

async function waitForHTTP(url, label, logPath, timeoutMs, child) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (child && child.exitCode !== null) {
      throw new Error(
        `${label} exited with ${child.exitCode} before becoming ready; see ${logPath}`,
      );
    }
    try {
      const response = await fetch(url, { redirect: "error" });
      if (response.ok) return;
    } catch {
      // The listener is not up yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`${label} at ${url} did not become ready; see ${logPath}`);
}

async function stopFixture(url) {
  try {
    await fetch(`${url}/__fixture/stop`, {
      method: "POST",
      signal: AbortSignal.timeout(5_000),
    });
  } catch {
    // The process may already have exited; the kill below is the backstop.
  }
}

async function stopProcess(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  if (process.platform === "win32" && child.pid) {
    // Vite starts esbuild as a child process; terminate the tree so the port
    // is released before the next group starts.
    await run("taskkill", ["/PID", String(child.pid), "/T", "/F"], {
      env: process.env,
    });
    return;
  }
  const closed = new Promise((resolve) => child.once("close", resolve));
  child.kill("SIGTERM");
  const timer = setTimeout(() => {
    if (child.exitCode === null && child.signalCode === null)
      child.kill("SIGKILL");
  }, 5_000);
  await Promise.race([
    closed,
    new Promise((resolve) => setTimeout(resolve, 10_000)),
  ]);
  clearTimeout(timer);
}

async function printLogTail(label, logPath, lines = 40) {
  try {
    const content = await readFile(logPath, "utf8");
    const tail = content.trimEnd().split(/\r?\n/).slice(-lines).join("\n");
    if (tail) {
      process.stderr.write(`[e2e-fixtures] ${label} (${logPath}):\n${tail}\n`);
    }
  } catch {
    // The log was never created.
  }
}

function closeStream(stream) {
  return new Promise((resolve) => stream.end(resolve));
}

export function countResults(output) {
  const count = (word) => {
    const matches = [...output.matchAll(new RegExp(`(\\d+) ${word}`, "g"))];
    return matches.reduce((total, match) => total + Number(match[1]), 0);
  };
  return {
    passed: count("passed"),
    failed: count("failed"),
    skipped: count("skipped"),
    didNotRun: count("did not run"),
  };
}

async function runGroup(group, options, usedPorts, backendBinary) {
  const logDir = path.join(options.logDir, group.name);
  await mkdir(logDir, { recursive: true });
  const [fixturePort, vitePort] = await allocatePorts(usedPorts);
  const fixtureURL = `http://127.0.0.1:${fixturePort}`;
  const pageURL = `http://127.0.0.1:${vitePort}`;
  const fixtureLogPath = path.join(logDir, "fixture.log");
  const viteLogPath = path.join(logDir, "vite.log");
  const playwrightLogPath = path.join(logDir, "playwright.log");
  const fixtureLog = createWriteStream(fixtureLogPath);
  const viteLog = createWriteStream(viteLogPath);
  const playwrightLog = createWriteStream(playwrightLogPath);
  let fixture;
  let vite;
  const started = Date.now();
  let succeeded = false;

  try {
    if (group.fixture) {
      const packageConfig = fixturePackages[group.fixture];
      const fixtureEnv = {
        ...process.env,
        PLAYWRIGHT_BASE_URL: pageURL,
        OPENLIST_FRONTEND_ORIGIN: pageURL,
        ...Object.fromEntries(
          Object.entries(packageConfig.env).map(([key, value]) => [
            key,
            value
              .replace("{port}", String(fixturePort))
              .replace("{page}", pageURL),
          ]),
        ),
        ...group.fixtureEnv,
      };
      log(`${group.name}: ${group.fixture} on ${fixturePort}`);
      fixture = spawn(
        path.join(fixtureBinDir, executableName(group.fixture)),
        [],
        {
          cwd: backendDir,
          env: fixtureEnv,
          stdio: ["ignore", "pipe", "pipe"],
          windowsHide: true,
        },
      );
      fixture.stdout.on("data", (chunk) => fixtureLog.write(chunk));
      fixture.stderr.on("data", (chunk) => fixtureLog.write(chunk));
      await waitForHTTP(
        `${fixtureURL}/health`,
        "fixture",
        fixtureLogPath,
        30_000,
        fixture,
      );
    }

    if (group.playwrightShard) {
      // Mocked specs intercept the API; a server is only needed to serve the app.
      log(`${group.name}: Vite on ${vitePort}`);
      vite = spawn(
        process.execPath,
        [
          viteCli,
          "--host",
          "127.0.0.1",
          "--port",
          String(vitePort),
          "--strictPort",
        ],
        {
          cwd: frontendDir,
          env: { ...process.env },
          stdio: ["ignore", "pipe", "pipe"],
          windowsHide: true,
        },
      );
    } else if (group.preview) {
      await buildFrontend(fixtureURL, viteLogPath);
      log(`${group.name}: vite preview on ${vitePort}`);
      vite = spawn(
        process.execPath,
        [
          viteCli,
          "preview",
          "--host",
          "127.0.0.1",
          "--port",
          String(vitePort),
          "--strictPort",
        ],
        {
          cwd: frontendDir,
          env: { ...process.env, VITE_API_BASE_URL: fixtureURL },
          stdio: ["ignore", "pipe", "pipe"],
          windowsHide: true,
        },
      );
    } else {
      log(`${group.name}: Vite on ${vitePort} -> ${fixtureURL}`);
      vite = spawn(
        process.execPath,
        [
          viteCli,
          "--host",
          "127.0.0.1",
          "--port",
          String(vitePort),
          "--strictPort",
        ],
        {
          cwd: frontendDir,
          env: { ...process.env, VITE_API_BASE_URL: fixtureURL },
          stdio: ["ignore", "pipe", "pipe"],
          windowsHide: true,
        },
      );
    }
    vite.stdout.on("data", (chunk) => viteLog.write(chunk));
    vite.stderr.on("data", (chunk) => viteLog.write(chunk));
    await waitForHTTP(pageURL, "Vite", viteLogPath, 60_000, vite);

    const env = {
      ...process.env,
      PLAYWRIGHT_BASE_URL: pageURL,
      VITE_API_BASE_URL: fixtureURL,
      // The current acceptance boundary is functional: no screenshots,
      // traces or pixel/layout sign-off. Existing suites keep their own
      // definitions; this only controls the artifacts this runner captures.
      E2E_NO_ARTIFACTS: "1",
      ...Object.fromEntries(
        Object.entries(group.env ?? {}).map(([key, value]) => [
          key,
          value
            .replace("{fixture}", fixtureURL)
            .replace("{port}", String(fixturePort))
            .replace("{backend}", backendBinary ?? ""),
        ]),
      ),
    };
    const specs = group.playwrightShard
      ? await discoverMockedSpecs()
      : (group.specs ?? [group.spec]);
    log(`${group.name}: playwright ${specs.join(", ")}`);
    const shardArgs =
      group.playwrightShard && options.shard
        ? [`--shard=${options.shard}`]
        : [];
    const result = await run(
      process.execPath,
      [
        playwrightCli,
        "test",
        ...specs.map((name) => `tests/e2e/${name}.spec.ts`),
        `--project=${group.project ?? "desktop-chromium"}`,
        ...(group.grep ? [`--grep=${group.grep}`] : []),
        // Keep traces and screenshots per group; concurrent local runs of this
        // script must not share Playwright's artifact directory.
        `--output=${path.join(logDir, "artifacts")}`,
        ...shardArgs,
        ...(group.playwrightShard ? [] : ["--workers=1"]),
        "--retries=0",
      ],
      {
        cwd: frontendDir,
        env,
        onOutput: (text) => {
          playwrightLog.write(text);
          process.stdout.write(text);
        },
      },
    );
    const counts = countResults(result.stdout);
    const summary = {
      group: group.name,
      specs,
      exitCode: result.code,
      durationMs: Date.now() - started,
      fixturePort: group.fixture ? fixturePort : null,
      vitePort,
      ...counts,
    };
    await writeFile(
      path.join(logDir, "summary.json"),
      `${JSON.stringify(summary, null, 2)}\n`,
    );
    if (result.code !== 0) {
      throw new Error(
        `${group.name}: Playwright exited ${result.code}; see ${playwrightLogPath}`,
      );
    }
    if (counts.skipped > 0 || counts.didNotRun > 0) {
      throw new Error(
        `${group.name}: ${counts.skipped} skipped and ${counts.didNotRun} did not run; see ${playwrightLogPath}`,
      );
    }
    if (counts.passed === 0) {
      throw new Error(
        `${group.name}: Playwright reported no passing tests; see ${playwrightLogPath}`,
      );
    }
    log(
      `${group.name}: ${counts.passed} passed in ${Math.round((Date.now() - started) / 1000)}s`,
    );
    succeeded = true;
    return summary;
  } finally {
    if (group.fixture) await stopFixture(fixtureURL);
    await stopProcess(vite);
    await stopProcess(fixture);
    await closeStream(fixtureLog);
    await closeStream(viteLog);
    await closeStream(playwrightLog);
    if (!succeeded) {
      await printLogTail(`${group.name} fixture`, fixtureLogPath);
      await printLogTail(`${group.name} Vite`, viteLogPath);
    }
  }
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  if (options.list) {
    for (const group of groups) {
      process.stdout.write(
        `${group.name}\t${group.fixture ?? "application"}\t${group.playwrightShard ? "(all mocked specs)" : (group.specs ?? [group.spec]).join(", ")}\n`,
      );
    }
    process.stdout.write(`\nbundles:\n`);
    for (const [name, members] of Object.entries(bundles)) {
      process.stdout.write(`${name}\t${members.join(",")}\n`);
    }
    return 0;
  }

  let selected = groups;
  if (options.groups) {
    const names = new Set(
      options.groups.flatMap((name) => bundles[name] ?? [name]),
    );
    const unknown = [...names].filter(
      (name) => !groups.some((group) => group.name === name),
    );
    if (unknown.length > 0)
      throw new Error(`Unknown group(s): ${unknown.join(", ")}; use --list`);
    selected = groups.filter((group) => names.has(group.name));
  } else if (options.shard) {
    // Keep the old `frontend-e2e` contract: a bare shard runs the mocked suite.
    parseShard(options.shard);
    selected = groups.filter((group) => group.playwrightShard);
  }

  // Default logs stay inside the ignored Playwright artifact directory so CI
  // uploads them on failure. Local runs can pass --log-dir elsewhere.
  options.logDir ??= process.env.KIKOTO_E2E_FIXTURE_LOGS
    ? path.resolve(process.env.KIKOTO_E2E_FIXTURE_LOGS)
    : path.join(frontendDir, "test-results", "e2e-fixtures");
  await mkdir(options.logDir, { recursive: true });
  await mkdir(fixtureBinDir, { recursive: true });
  const neededFixtures = [
    ...new Set(selected.map((group) => group.fixture).filter(Boolean)),
  ];
  if (neededFixtures.length > 0) {
    await buildFixtures(options.logDir, neededFixtures);
  }
  const backendBinary = selected.some((group) => group.backendBinary)
    ? await buildBackendBinary(options.logDir)
    : null;

  const usedPorts = new Set();
  const summaries = [];
  const failures = [];
  for (const group of selected) {
    try {
      summaries.push(await runGroup(group, options, usedPorts, backendBinary));
    } catch (error) {
      failures.push(`${group.name}: ${error.message}`);
      summaries.push({
        group: group.name,
        specs: group.playwrightShard
          ? "(all mocked specs)"
          : (group.specs ?? [group.spec]),
        exitCode: 1,
        error: error.message,
      });
      process.stderr.write(`[e2e-fixtures] ${error.message}\n`);
    }
  }
  await writeFile(
    path.join(options.logDir, "summary.json"),
    `${JSON.stringify({ groups: summaries, failures }, null, 2)}\n`,
  );
  await rm(fixtureBinDir, { recursive: true, force: true });
  log(`logs: ${options.logDir}`);
  if (failures.length > 0) {
    process.stderr.write(`[e2e-fixtures] ${failures.length} group(s) failed\n`);
    return 1;
  }
  log(`${selected.length} group(s) passed`);
  return 0;
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  main().then(
    (code) => {
      process.exitCode = code;
    },
    (error) => {
      process.stderr.write(`${error.stack ?? error}\n`);
      process.exitCode = 1;
    },
  );
}
