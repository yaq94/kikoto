import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import os from "node:os";
import path from "node:path";

const root = fileURLToPath(new URL("../", import.meta.url));
const args = process.argv.slice(2);
let cells;
for (let index = 0; index < args.length; index += 1) {
  if (args[index] !== "--cells" || index + 1 >= args.length)
    throw new Error("expected --cells <name-prefix>[,<name-prefix>]");
  cells = args[index + 1];
  index += 1;
}

const environment = { KIKOTO_SOURCE_INDEX_PERF: "1" };
for (const name of [
  "KIKOTO_SOURCE_INDEX_PERF_CELLS",
  "KIKOTO_SOURCE_INDEX_PERF_DIRS",
  "KIKOTO_SOURCE_INDEX_PERF_RTT_MS",
  "KIKOTO_SOURCE_INDEX_PERF_BUDGET_MS",
  "KIKOTO_SOURCE_INDEX_PERF_TOTAL_SECONDS",
]) {
  if (process.env[name]) environment[name] = process.env[name];
}
if (cells) environment.KIKOTO_SOURCE_INDEX_PERF_CELLS = cells;

const child = spawn(
  process.env.GO || "go",
  [
    "test",
    "./internal/httpapi",
    "-run",
    "^TestSourceCatalogIndexThroughputCalibration$",
    "-count=1",
    "-v",
    "-timeout=90m",
  ],
  {
    cwd: path.join(root, "backend"),
    env: { ...process.env, ...environment },
    stdio: "inherit",
    windowsHide: true,
  },
);
console.log(
  `hardware os=${os.platform()} ${os.release()} arch=${os.arch()} cpu=${os.cpus()[0]?.model} logical_cpus=${os.cpus().length} node=${process.version}`,
);
process.once("exit", () => child.kill());
process.once("SIGINT", () => {
  child.kill();
  process.exitCode = 130;
});
child.once("error", (error) => {
  console.error(error.message);
  process.exitCode = 1;
});
child.once("exit", (code) => {
  if (code !== 0) process.exitCode = code ?? 1;
});
