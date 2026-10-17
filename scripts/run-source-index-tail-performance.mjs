import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import os from "node:os";
import path from "node:path";

const root = fileURLToPath(new URL("../", import.meta.url));
const environment = { KIKOTO_SOURCE_INDEX_TAIL_PERF: "1" };
for (const name of [
  "KIKOTO_SOURCE_INDEX_TAIL_DB",
  "KIKOTO_SOURCE_INDEX_TAIL_QUERY",
  "KIKOTO_SOURCE_INDEX_TAIL_ROWS",
]) {
  if (process.env[name]) environment[name] = process.env[name];
}

const child = spawn(
  process.env.GO || "go",
  [
    "test",
    "./internal/httpapi",
    "-run",
    "^TestSourceCatalogPublishTailPerformance$",
    "-count=1",
    "-v",
    "-timeout=10m",
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
