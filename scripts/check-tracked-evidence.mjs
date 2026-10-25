import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDirectory, "..");
const allowlistFile = "scripts/tracked-evidence-allowlist.json";
const evidencePrefix = ".scratch/kikoto-next/evidence/";
const topLevelEvidencePattern = /^\.scratch\/kikoto-next\/[^/]+\.md$/u;

export const RULE = Object.freeze({
  PRODUCTION_PATH: "production-path",
  RELEASE_NUMBER: "release-number",
  PRODUCTION_RECORD_ID: "production-record-id",
  DEPLOYMENT_DETAIL: "deployment-detail",
  STALE_ALLOWLIST: "stale-allowlist-entry",
});

const productionPathPattern =
  /(?:\/srv\/|\/root\/|\/mnt\/|(?<![A-Za-z0-9])[A-Za-z]:[\\/]|\bnext-build\b)/iu;
const pathToKikotoPattern = /\/path\/to\/kikoto\b/iu;
const releaseNumberPattern = /\b(?:RJ|VJ)\d{4,}\b/giu;
const productionRecordIdPattern =
  /\b(?:entry[_-]?id|source[_-]?id)\b\s*[:=]\s*(\d{3,})\b/giu;
const syntheticContextPattern = /(?:fixture|synthetic|合成)/iu;
const dbByteCountPattern =
  /\bkikoto[A-Za-z0-9._-]*\.db\b[^\n]{0,120}\b\d[\d,]*\s*(?:B|bytes?|字节)/iu;
const sha256Pattern = /\bsha(?:-|\s)?256\b[^\n]{0,80}\b[0-9a-f]{64}\b/iu;
const dockerLogsRedirectPattern =
  /\bdocker\s+logs\b[^\n]*?(?:>>?|tee\s+(?:-a\s+)?)\s*(?:["'`])?([^\s"'`|;&]+)/giu;
const syntheticAllowlistPattern =
  /(?:(?:fixture|synthetic|合成)[-_ ]?[A-Za-z0-9]|\bRJ0{6}\d{2}\b|\b(?:entry[_-]?id|source[_-]?id)\s*[:=]\s*[0-3]\b)/iu;
const genericTemplateMarkerPattern =
  /(?:generic command template|通用命令模板)/iu;
const fencePattern = /^\s*(`{3,}|~{3,})(.*)$/u;
const genericPlaceholderPrefixPattern =
  /^(?:\/path\/to\/|<[A-Za-z][A-Za-z0-9_-]*>)/u;

function normalizeRepositoryPath(file) {
  return file.replaceAll("\\", "/");
}

export function isTrackedEvidenceFile(file) {
  const normalized = normalizeRepositoryPath(file);
  return (
    (normalized.startsWith(evidencePrefix) &&
      normalized.length > evidencePrefix.length &&
      !normalized.endsWith("/")) ||
    topLevelEvidencePattern.test(normalized)
  );
}

export function isReservedReleaseNumber(value) {
  const match = /^RJ(\d+)$/iu.exec(value);
  return match !== null && /^0{6}\d{2}$/u.test(match[1]);
}

function hasDisallowedDockerLogTarget(line) {
  dockerLogsRedirectPattern.lastIndex = 0;
  for (const match of line.matchAll(dockerLogsRedirectPattern)) {
    const target = match[1];
    if (
      !target ||
      target.startsWith("<") ||
      target.startsWith("$") ||
      target.includes("<") ||
      target.includes(">") ||
      target === "/dev/null" ||
      target === "/dev/stdout" ||
      target === "/dev/stderr"
    ) {
      continue;
    }
    if (
      target.includes("/") ||
      target.includes("\\") ||
      /\.(?:log|txt)$/iu.test(target)
    ) {
      return true;
    }
  }
  return false;
}

function isUnsafeAllowlistPattern(pattern, allowPathToKikoto = false) {
  if (
    productionPathPattern.test(pattern) ||
    (!allowPathToKikoto && pathToKikotoPattern.test(pattern))
  ) {
    return true;
  }
  releaseNumberPattern.lastIndex = 0;
  for (const match of pattern.matchAll(releaseNumberPattern)) {
    if (!isReservedReleaseNumber(match[0])) return true;
  }
  return /\b[0-9a-f]{64}\b/iu.test(pattern);
}

function residualHasRule(finding, pattern) {
  const residual = finding.text.split(pattern).join("");
  const rules = new Set();

  if (
    productionPathPattern.test(residual) ||
    pathToKikotoPattern.test(residual)
  ) {
    rules.add(RULE.PRODUCTION_PATH);
  }

  releaseNumberPattern.lastIndex = 0;
  for (const match of residual.matchAll(releaseNumberPattern)) {
    if (!isReservedReleaseNumber(match[0])) {
      rules.add(RULE.RELEASE_NUMBER);
      break;
    }
  }

  if (!syntheticContextPattern.test(residual)) {
    productionRecordIdPattern.lastIndex = 0;
    for (const match of residual.matchAll(productionRecordIdPattern)) {
      if (Number(match[1]) > 3) {
        rules.add(RULE.PRODUCTION_RECORD_ID);
        break;
      }
    }
  }

  if (
    dbByteCountPattern.test(residual) ||
    sha256Pattern.test(residual) ||
    hasDisallowedDockerLogTarget(residual)
  ) {
    rules.add(RULE.DEPLOYMENT_DETAIL);
  }

  return rules.has(finding.rule);
}

export function scanEvidenceText(file, text) {
  const findings = [];
  const lines = text.split(/\r?\n/u);
  let fenceMarker = null;
  let genericTemplateFence = false;
  let fenceHasContent = false;

  for (const [index, line] of lines.entries()) {
    const lineNumber = index + 1;
    const rules = new Set();
    // A digest or a byte count is often wrapped after a short label. Include
    // one preceding line so the rule still fires without flagging the label.
    const previous = index === 0 ? "" : lines[index - 1];
    const splitLabel =
      (/\bsha256\b/iu.test(previous) && /\b[0-9a-f]{64}\b/iu.test(line)) ||
      (/\bkikoto\.db\b/iu.test(previous) &&
        /\b\d[\d,]*\s*(?:B|bytes?|字节)/iu.test(line));
    const fence = line.match(fencePattern);
    if (fence) {
      const marker = fence[1][0];
      if (fenceMarker === null) {
        fenceMarker = marker;
        genericTemplateFence = genericTemplateMarkerPattern.test(fence[2]);
        fenceHasContent = false;
      } else if (fenceMarker === marker) {
        fenceMarker = null;
        genericTemplateFence = false;
        fenceHasContent = false;
      }
      continue;
    }
    if (
      fenceMarker !== null &&
      !genericTemplateFence &&
      !fenceHasContent &&
      line.trim() !== ""
    ) {
      fenceHasContent = true;
      if (genericTemplateMarkerPattern.test(line)) {
        genericTemplateFence = true;
      }
    }

    if (
      productionPathPattern.test(line) ||
      (!genericTemplateFence && pathToKikotoPattern.test(line))
    ) {
      rules.add(RULE.PRODUCTION_PATH);
    }

    releaseNumberPattern.lastIndex = 0;
    for (const match of line.matchAll(releaseNumberPattern)) {
      if (!isReservedReleaseNumber(match[0])) {
        rules.add(RULE.RELEASE_NUMBER);
        break;
      }
    }

    if (!syntheticContextPattern.test(line)) {
      productionRecordIdPattern.lastIndex = 0;
      for (const match of line.matchAll(productionRecordIdPattern)) {
        if (Number(match[1]) > 3) {
          rules.add(RULE.PRODUCTION_RECORD_ID);
          break;
        }
      }
    }

    if (
      dbByteCountPattern.test(line) ||
      sha256Pattern.test(line) ||
      splitLabel ||
      hasDisallowedDockerLogTarget(line)
    ) {
      rules.add(RULE.DEPLOYMENT_DETAIL);
    }

    for (const rule of rules) {
      findings.push({ file, line: lineNumber, rule, text: line });
    }
  }

  return findings;
}

function validateAllowlistFile(file, entryIndex) {
  if (typeof file !== "string" || file.trim() === "") {
    throw new Error(`allowlist entry ${entryIndex} has an invalid file`);
  }
  const normalized = normalizeRepositoryPath(file);
  if (
    normalized !== file ||
    path.posix.isAbsolute(normalized) ||
    /^[A-Za-z]:\//u.test(normalized) ||
    normalized === ".." ||
    normalized.startsWith("../") ||
    normalized.includes("/../") ||
    !isTrackedEvidenceFile(normalized)
  ) {
    throw new Error(
      `allowlist entry ${entryIndex} must reference a tracked evidence file`,
    );
  }
  return normalized;
}

export function parseAllowlist(contents) {
  let parsed;
  try {
    parsed = JSON.parse(contents);
  } catch (error) {
    throw new Error(
      `invalid JSON: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
  if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object") {
    throw new Error("root must be an object");
  }
  const rootKeys = Object.keys(parsed).sort();
  if (
    rootKeys.join(",") !== "entries,version" ||
    parsed.version !== 1 ||
    !Array.isArray(parsed.entries)
  ) {
    throw new Error("root must contain only version 1 and an entries array");
  }

  const entries = [];
  const seen = new Set();
  for (const [index, entry] of parsed.entries.entries()) {
    const entryIndex = index + 1;
    if (entry === null || Array.isArray(entry) || typeof entry !== "object") {
      throw new Error(`allowlist entry ${entryIndex} must be an object`);
    }
    const entryKeys = Object.keys(entry).sort();
    if (entryKeys.join(",") !== "file,kind,pattern,reason,rule") {
      throw new Error(
        `allowlist entry ${entryIndex} must contain only file, rule, pattern, kind, and reason`,
      );
    }
    const file = validateAllowlistFile(entry.file, entryIndex);
    if (
      ![
        RULE.PRODUCTION_PATH,
        RULE.RELEASE_NUMBER,
        RULE.PRODUCTION_RECORD_ID,
        RULE.DEPLOYMENT_DETAIL,
      ].includes(entry.rule)
    ) {
      throw new Error(`allowlist entry ${entryIndex} has an invalid rule`);
    }
    if (typeof entry.pattern !== "string" || entry.pattern.trim() === "") {
      throw new Error(`allowlist entry ${entryIndex} has an invalid pattern`);
    }
    if (
      entry.kind !== "synthetic-placeholder" &&
      entry.kind !== "generic-template-prefix"
    ) {
      throw new Error(`allowlist entry ${entryIndex} has an invalid kind`);
    }
    if (typeof entry.reason !== "string" || entry.reason.trim() === "") {
      throw new Error(`allowlist entry ${entryIndex} must have a reason`);
    }
    if (
      entry.kind === "synthetic-placeholder" &&
      (!syntheticAllowlistPattern.test(entry.pattern) ||
        isUnsafeAllowlistPattern(entry.pattern))
    ) {
      throw new Error(
        `allowlist entry ${entryIndex} synthetic pattern must name a fixture, synthetic value, or id 0-3`,
      );
    }
    if (
      entry.kind === "generic-template-prefix" &&
      (!genericPlaceholderPrefixPattern.test(entry.pattern) ||
        isUnsafeAllowlistPattern(entry.pattern, true))
    ) {
      throw new Error(
        `allowlist entry ${entryIndex} generic template prefix must use /path/to/ or an angle-bracket placeholder`,
      );
    }
    const key = `${file}\0${entry.rule}\0${entry.pattern}`;
    if (seen.has(key)) {
      throw new Error(
        `allowlist entry ${entryIndex} duplicates an earlier entry`,
      );
    }
    seen.add(key);
    entries.push({
      file,
      rule: entry.rule,
      pattern: entry.pattern,
      kind: entry.kind,
      reason: entry.reason,
    });
  }
  return entries;
}

export function applyAllowlist(findings, entries) {
  const used = new Set();
  const kept = [];

  for (const finding of findings) {
    let suppressed = false;
    for (const [index, entry] of entries.entries()) {
      if (
        entry.file !== finding.file ||
        entry.rule !== finding.rule ||
        !finding.text.includes(entry.pattern) ||
        residualHasRule(finding, entry.pattern)
      ) {
        continue;
      }
      used.add(index);
      suppressed = true;
      break;
    }
    if (!suppressed) kept.push(finding);
  }

  const stale = entries.flatMap((entry, index) =>
    used.has(index)
      ? []
      : [
          {
            file: allowlistFile,
            line: 0,
            rule: RULE.STALE_ALLOWLIST,
            text: `unused allowlist entry ${index + 1}: ${entry.pattern}`,
          },
        ],
  );

  return { findings: kept, stale, used };
}

export function listTrackedEvidenceFiles(root = repositoryRoot) {
  const result = spawnSync("git", ["ls-files", "--cached", "-z"], {
    cwd: root,
    encoding: "buffer",
  });
  if (result.error) {
    throw new Error(`could not run git: ${result.error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(
      `git ls-files --cached -z failed: ${String(result.stderr).trim()}`,
    );
  }
  return result.stdout
    .toString("utf8")
    .split("\0")
    .filter(Boolean)
    .filter(isTrackedEvidenceFile)
    .sort();
}

function loadAllowlist(root = repositoryRoot) {
  const fullPath = path.resolve(root, allowlistFile);
  try {
    return parseAllowlist(fs.readFileSync(fullPath, "utf8"));
  } catch (error) {
    throw new Error(
      `${allowlistFile}: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
}

export function checkEvidenceFiles(files, entries, root = repositoryRoot) {
  const findings = [];
  for (const file of files) {
    const fullPath = path.resolve(root, file);
    if (!fs.existsSync(fullPath)) {
      throw new Error(`tracked evidence file is missing: ${file}`);
    }
    const text = fs.readFileSync(fullPath, "utf8");
    findings.push(...scanEvidenceText(file, text));
  }
  return applyAllowlist(findings, entries);
}

function formatFinding(finding) {
  return `${finding.file}:${finding.line ?? 0}:${finding.rule}`;
}

export function runTrackedEvidenceCheck(options = {}) {
  const root = options.root ?? repositoryRoot;
  const files = options.files ?? listTrackedEvidenceFiles(root);

  if (options.listOnly) {
    (options.stdout ?? console.log)(
      `Scanned ${files.length} tracked evidence file(s).`,
    );
    return 0;
  }

  const entries = options.entries ?? loadAllowlist(root);
  const result = checkEvidenceFiles(files, entries, root);
  const allFindings = [...result.findings, ...result.stale];

  if (allFindings.length > 0) {
    (options.stderr ?? console.error)(
      `Tracked-evidence scan found ${allFindings.length} item(s):`,
    );
    for (const finding of allFindings) {
      (options.stderr ?? console.error)(formatFinding(finding));
    }
    if (result.stale.length > 0) {
      (options.stderr ?? console.error)(
        "Remove unused allowlist entries; every entry must match a finding.",
      );
    }
    return 1;
  }

  (options.stdout ?? console.log)(
    `Tracked-evidence scan passed for ${files.length} file(s).`,
  );
  return 0;
}

function main() {
  const args = process.argv.slice(2);
  const allowed = new Set(["--list", "--help"]);
  if (
    args.some((argument) => !allowed.has(argument)) ||
    args.filter((argument) => argument === "--list").length > 1
  ) {
    console.error("Usage: node scripts/check-tracked-evidence.mjs [--list]");
    process.exitCode = 2;
    return;
  }
  if (args.includes("--help")) {
    console.log(
      "Scan tracked .scratch/kikoto-next evidence for deployment details. Use --list to print the scanned file count.",
    );
    return;
  }
  try {
    process.exitCode = runTrackedEvidenceCheck({
      listOnly: args.includes("--list"),
    });
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 2;
  }
}

if (
  process.argv[1] !== undefined &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  main();
}
