import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  RULE,
  applyAllowlist,
  isReservedReleaseNumber,
  isTrackedEvidenceFile,
  parseAllowlist,
  runTrackedEvidenceCheck,
  scanEvidenceText,
} from "./check-tracked-evidence.mjs";

const fixtureFile = ".scratch/kikoto-next/evidence/fixture.md";

function rulesFor(text) {
  return scanEvidenceText(fixtureFile, text).map(
    (finding) => `${finding.line}:${finding.rule}`,
  );
}

function allowlistEntry(overrides = {}) {
  return {
    file: fixtureFile,
    rule: RULE.RELEASE_NUMBER,
    pattern: "fixture release RJ00000042",
    kind: "synthetic-placeholder",
    reason: "Synthetic fixture release code.",
    ...overrides,
  };
}

test("flags production and local absolute paths", () => {
  const text = [
    "srv: /srv/kikoto/config",
    "root: /root/private",
    "mnt: /mnt/data",
    "windows: E:/workspace/example",
    String.raw`windowsBackslash: C:\work\example`,
    "tag: next-build",
    "generic outside a template: /path/to/kikoto/work",
  ].join("\n");

  assert.deepEqual(rulesFor(text), [
    `1:${RULE.PRODUCTION_PATH}`,
    `2:${RULE.PRODUCTION_PATH}`,
    `3:${RULE.PRODUCTION_PATH}`,
    `4:${RULE.PRODUCTION_PATH}`,
    `5:${RULE.PRODUCTION_PATH}`,
    `6:${RULE.PRODUCTION_PATH}`,
    `7:${RULE.PRODUCTION_PATH}`,
  ]);
});

test("allows /path/to/kikoto only inside a marked generic command template", () => {
  const text = [
    "```sh",
    "generic command template",
    "cd /path/to/kikoto/backend",
    "```",
    "",
    "```sh",
    "cd /path/to/kikoto/backend",
    "```",
  ].join("\n");

  assert.deepEqual(rulesFor(text), [`7:${RULE.PRODUCTION_PATH}`]);
});

test("flags real release numbers but not the reserved test range", () => {
  const text = [
    "RJ123456",
    "VJ654321",
    "RJ00000042",
    "RJ00000099",
    "RJ00000100",
    "rj00000042",
  ].join("\n");

  assert.deepEqual(rulesFor(text), [
    `1:${RULE.RELEASE_NUMBER}`,
    `2:${RULE.RELEASE_NUMBER}`,
    `5:${RULE.RELEASE_NUMBER}`,
  ]);
  assert.equal(isReservedReleaseNumber("RJ00000042"), true);
  assert.equal(isReservedReleaseNumber("RJ00000099"), true);
  assert.equal(isReservedReleaseNumber("RJ00000100"), false);
});

test("flags production record ids but allows small and synthetic ids", () => {
  const text = [
    "entryId: 1234",
    "source_id = 98765",
    "entryId: 3",
    "sourceId: 003",
    "sourceId: 1234 // synthetic fixture",
    "entry_id=4567 合成",
  ].join("\n");

  assert.deepEqual(rulesFor(text), [
    `1:${RULE.PRODUCTION_RECORD_ID}`,
    `2:${RULE.PRODUCTION_RECORD_ID}`,
  ]);
});

test("flags deployment details but allows placeholders and common sinks", () => {
  const text = [
    "kikoto.db 387,837,952 B",
    "sha256 2be092c0cd05cb1a5a2897f70d66f3032812cd0519d9b522cead9a15f79f8e01",
    "docker logs --since 3h kikoto > logs/kikoto-3h.txt",
    "kikoto.db 约 34 MB",
    "docker logs --since 3h kikoto > <logs-dir>/kikoto-3h.txt",
    "docker logs --since 3h kikoto > /dev/null",
    "docker logs --since 3h kikoto 2>&1",
  ].join("\n");

  assert.deepEqual(rulesFor(text), [
    `1:${RULE.DEPLOYMENT_DETAIL}`,
    `2:${RULE.DEPLOYMENT_DETAIL}`,
    `3:${RULE.DEPLOYMENT_DETAIL}`,
  ]);
});

test("flags a sha256 digest split across two lines", () => {
  const text = [
    "镜像 ID",
    "`sha256:25e5467ede40342fad8ca772b95d931343a87e3c5239bd535bbc0330777b1a99`。",
  ].join("\n");

  assert.deepEqual(rulesFor(text), [`2:${RULE.DEPLOYMENT_DETAIL}`]);
});

test("suppresses an allowlisted synthetic finding and marks the entry used", () => {
  const findings = scanEvidenceText(fixtureFile, "cd /path/to/kikoto/work");
  const entries = parseAllowlist(
    JSON.stringify({
      version: 1,
      entries: [
        allowlistEntry({
          rule: RULE.PRODUCTION_PATH,
          pattern: "/path/to/kikoto/work",
          kind: "generic-template-prefix",
          reason: "Documented generic repository template.",
        }),
      ],
    }),
  );

  const result = applyAllowlist(findings, entries);
  assert.deepEqual(result.findings, []);
  assert.equal(result.used.size, 1);
  assert.deepEqual(result.stale, []);
});

test("reports an allowlist entry that matches nothing", () => {
  const entries = parseAllowlist(
    JSON.stringify({ version: 1, entries: [allowlistEntry()] }),
  );

  const result = applyAllowlist([], entries);
  assert.deepEqual(result.findings, []);
  assert.equal(result.stale.length, 1);
  assert.equal(result.stale[0].rule, RULE.STALE_ALLOWLIST);
});

test("rejects allowlist entries without a reason or with a non-placeholder pattern", () => {
  const missingReason = allowlistEntry();
  delete missingReason.reason;
  assert.throws(
    () =>
      parseAllowlist(JSON.stringify({ version: 1, entries: [missingReason] })),
    /reason/u,
  );

  assert.throws(
    () =>
      parseAllowlist(
        JSON.stringify({
          version: 1,
          entries: [
            allowlistEntry({
              rule: RULE.PRODUCTION_PATH,
              pattern: "/srv/kikoto",
              kind: "generic-template-prefix",
              reason: "Not a generic placeholder.",
            }),
          ],
        }),
      ),
    /generic template prefix/u,
  );
});

test("rejects a synthetic allowlist entry that names a real path", () => {
  assert.throws(
    () =>
      parseAllowlist(
        JSON.stringify({
          version: 1,
          entries: [
            allowlistEntry({
              rule: RULE.PRODUCTION_PATH,
              pattern: "/srv/kikoto",
              kind: "synthetic-placeholder",
              reason: "Not synthetic.",
            }),
          ],
        }),
      ),
    /synthetic pattern/u,
  );
});

test("rejects an allowlist entry that contains a real release number", () => {
  assert.throws(
    () =>
      parseAllowlist(
        JSON.stringify({
          version: 1,
          entries: [
            allowlistEntry({
              pattern: "synthetic fixture RJ123456",
              reason: "A real-looking number is not a synthetic placeholder.",
            }),
          ],
        }),
      ),
    /synthetic pattern/u,
  );
});

test("does not suppress a line that still contains another unsafe value", () => {
  const findings = scanEvidenceText(
    fixtureFile,
    "fixture RJ00000042 and /srv/private",
  );
  const entries = parseAllowlist(
    JSON.stringify({
      version: 1,
      entries: [
        allowlistEntry({
          rule: RULE.PRODUCTION_PATH,
          pattern: "fixture RJ00000042",
          reason: "Synthetic prefix only.",
        }),
      ],
    }),
  );

  const result = applyAllowlist(findings, entries);
  assert.deepEqual(
    result.findings.map((finding) => finding.rule),
    [RULE.PRODUCTION_PATH],
  );
  assert.equal(result.used.size, 0);
  assert.equal(result.stale.length, 1);
});

test("selects tracked evidence and top-level markdown only", () => {
  assert.equal(
    isTrackedEvidenceFile(".scratch/kikoto-next/evidence/21.md"),
    true,
  );
  assert.equal(
    isTrackedEvidenceFile(".scratch/kikoto-next/evidence/nested/report.txt"),
    true,
  );
  assert.equal(isTrackedEvidenceFile(".scratch/kikoto-next/spec.md"), true);
  assert.equal(
    isTrackedEvidenceFile(".scratch/kikoto-next/issues/23.md"),
    false,
  );
  assert.equal(isTrackedEvidenceFile(".scratch/kikoto-next/evidence/"), false);
  assert.equal(isTrackedEvidenceFile("docs/readme.md"), false);
});

test("exits 1 with file:line:rule findings and exits 0 when clean", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "tracked-evidence-"));
  try {
    const evidenceDirectory = path.join(
      root,
      ".scratch",
      "kikoto-next",
      "evidence",
    );
    fs.mkdirSync(evidenceDirectory, { recursive: true });
    fs.mkdirSync(path.join(root, "scripts"), { recursive: true });
    fs.writeFileSync(
      path.join(root, "scripts", "tracked-evidence-allowlist.json"),
      JSON.stringify({ version: 1, entries: [] }),
    );
    const files = [".scratch/kikoto-next/evidence/report.md"];
    fs.writeFileSync(
      path.join(root, files[0]),
      [
        "path /srv/private",
        "release RJ123456",
        "entryId: 98765",
        `sha256 ${"a".repeat(64)}`,
      ].join("\n"),
    );
    const errors = [];
    const exitCode = runTrackedEvidenceCheck({
      root,
      files,
      stdout: () => {},
      stderr: (line) => errors.push(line),
    });
    assert.equal(exitCode, 1);
    assert.deepEqual(errors.slice(1), [
      `${files[0]}:1:${RULE.PRODUCTION_PATH}`,
      `${files[0]}:2:${RULE.RELEASE_NUMBER}`,
      `${files[0]}:3:${RULE.PRODUCTION_RECORD_ID}`,
      `${files[0]}:4:${RULE.DEPLOYMENT_DETAIL}`,
    ]);

    fs.writeFileSync(
      path.join(root, files[0]),
      "fixture counts only; RJ00000042 is reserved.",
    );
    const messages = [];
    const cleanCode = runTrackedEvidenceCheck({
      root,
      files,
      stdout: (line) => messages.push(line),
      stderr: () => {},
    });
    assert.equal(cleanCode, 0);
    assert.match(messages.join("\n"), /passed for 1 file/u);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
