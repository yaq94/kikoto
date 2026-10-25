import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

// Opt-in driver for TestCatalog42KPerformance. It writes the JSON report and
// leaves the threshold judgement to the reader: the test never fails on p95.
//
//   node scripts/run-catalog-42k-perf.mjs --cards 2000 --samples 10 \
//     --out ../.kikoto-work/reports/catalog-42k-perf-local-2000.json
//
//   node scripts/run-catalog-42k-perf.mjs --binary /root/kikoto/bench42k/httpapi.test \
//     --tmp /root/kikoto/bench42k/tmp --out /root/kikoto/bench42k/report.json
//
// Without --binary the script runs `go test` in backend/.

const root = fileURLToPath(new URL("../", import.meta.url));
const backend = path.join(root, "backend");
const args = process.argv.slice(2);
function option(name, fallback = "") {
  const index = args.indexOf(`--${name}`);
  if (index === -1) return fallback;
  const value = args[index + 1];
  if (value === undefined || value.startsWith("--")) {
    throw new Error(`--${name} needs a value`);
  }
  return value;
}

const cards = option("cards", process.env.KIKOTO_CATALOG_42K_CARDS || "42624");
const samples = option(
  "samples",
  process.env.KIKOTO_CATALOG_42K_SAMPLES || "30",
);
const warmup = option("warmup", process.env.KIKOTO_CATALOG_42K_WARMUP || "5");
const out = option("out", process.env.KIKOTO_CATALOG_42K_OUT || "");
const tmp = option("tmp", process.env.KIKOTO_CATALOG_42K_TMP || "");
const binary = option("binary", "");
const timeout = option("timeout", "90m");

const env = {
  ...process.env,
  KIKOTO_CATALOG_42K_PERF: "1",
  KIKOTO_CATALOG_42K_CARDS: cards,
  KIKOTO_CATALOG_42K_SAMPLES: samples,
  KIKOTO_CATALOG_42K_WARMUP: warmup,
};
if (out) env.KIKOTO_CATALOG_42K_OUT = path.resolve(out);
if (tmp) env.KIKOTO_CATALOG_42K_TMP = path.resolve(tmp);

const command = binary || "go";
const commandArgs = binary
  ? [
      "-test.run",
      "^TestCatalog42KPerformance$",
      "-test.v",
      "-test.timeout",
      timeout,
    ]
  : [
      "test",
      "./internal/httpapi",
      "-run",
      "^TestCatalog42KPerformance$",
      "-count=1",
      "-v",
      "-timeout",
      timeout,
    ];

const child = spawn(command, commandArgs, {
  cwd: binary ? path.dirname(path.resolve(binary)) : backend,
  env,
  stdio: "inherit",
  windowsHide: true,
});
child.once("error", (error) => {
  console.error(error.message);
  process.exitCode = 1;
});
child.once("exit", (code) => {
  process.exitCode = code === 0 ? 0 : 1;
});
