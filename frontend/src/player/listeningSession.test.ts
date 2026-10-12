import { describe, expect, it } from "vitest";
import type { DatedListeningReport } from "@/lib/playbackReportApi";
import { isMediaAdvancing, ListeningClock } from "./listeningSession";

const target = { activationKey: "synthetic-queue-a", workId: 1 };
const wall = Date.parse("2026-01-01T23:59:50Z");
function harness() {
  const reports: DatedListeningReport[] = [];
  let id = 0;
  const clock = new ListeningClock(
    () => `synthetic-session-${++id}`,
    (r) => reports.push(r),
  );
  const update = (now: number, advancing: boolean, generation: number | null = 0, next = target) =>
    clock.update(next, generation, advancing, now, wall + now);
  const observe = (now: number) => clock.observe(now, wall + now);
  return { clock, reports, update, observe };
}

describe("listening measurement", () => {
  it("credits elapsed wall time across rates and excludes pause, waiting and seeking", () => {
    const h = harness();
    h.update(0, true);
    h.observe(10_000); // Media could move 5s or 20s; neither changes this 10s.
    h.update(10_000, false);
    h.observe(50_000);
    h.update(50_000, true);
    h.observe(55_000);
    expect(h.reports[h.reports.length - 1]?.listenedSeconds).toBe(15);
    expect(new Set(h.reports.map((r) => r.sessionId)).size).toBe(1);
    for (const state of [{ paused: true }, { seeking: true }, { ended: true }, { readyState: 2 }])
      expect(isMediaAdvancing({ paused: false, seeking: false, ended: false, readyState: 4, ...state })).toBe(false);
  });

  it("persists occurrence UTC buckets without opening a second session at midnight", () => {
    const h = harness();
    h.update(0, true);
    h.observe(30_000);
    expect(h.reports[h.reports.length - 1]).toMatchObject({
      listenedSeconds: 30,
      startedAt: "2026-01-01T23:59:50.000Z",
      days: [
        { day: "2026-01-01", listenedSeconds: 10 },
        { day: "2026-01-02", listenedSeconds: 20 },
      ],
    });
  });

  it("buckets by the account's time zone and opens a new session when the zone changes", () => {
    const reports: DatedListeningReport[] = [];
    let id = 0;
    let zone = "Asia/Tokyo";
    const clock = new ListeningClock(
      () => `synthetic-session-${++id}`,
      (r) => reports.push(r),
      () => zone,
    );
    // 23:59:50 UTC on January 1 is the morning of January 2 in Tokyo, far from its midnight.
    clock.update(target, 0, true, 0, wall);
    clock.observe(30_000, wall + 30_000);
    expect(reports[reports.length - 1]).toMatchObject({
      listenedSeconds: 30,
      days: [{ day: "2026-01-02", listenedSeconds: 30 }],
    });
    zone = "UTC";
    clock.observe(40_000, wall + 40_000);
    const last = reports[reports.length - 1];
    expect(last.sessionId).not.toBe(reports[0].sessionId);
    expect(last.days).toEqual([{ day: "2026-01-02", listenedSeconds: 10 }]);
  });

  it("drops suspended and changed-clock gaps and resumes from the next observation", () => {
    const h = harness();
    h.update(0, true);
    h.observe(120_000);
    h.observe(125_000);
    expect(h.reports[h.reports.length - 1]?.listenedSeconds).toBe(5);
    h.clock.observe(126_000, wall + 500_000);
    expect(h.reports[h.reports.length - 1]?.listenedSeconds).toBe(5);
  });

  it("never measures before a generation is known and never carries pre-clear time forward", () => {
    const h = harness();
    h.update(0, true, null);
    h.observe(5_000);
    expect(h.reports).toEqual([]);
    h.update(5_000, true);
    h.observe(10_000);
    h.update(10_000, true, 1);
    h.observe(15_000);
    expect(h.reports[h.reports.length - 1]).toMatchObject({ generation: 1, listenedSeconds: 5 });
    expect(h.reports[0].sessionId).not.toBe(h.reports[h.reports.length - 1]?.sessionId);
  });

  it("opens fresh activations for track switches and replays and rolls over the one-day limit", () => {
    const h = harness();
    h.update(0, true);
    h.observe(1_000);
    h.clock.end();
    h.update(1_000, true);
    h.observe(2_000);
    expect(new Set(h.reports.map((r) => r.sessionId)).size).toBe(2);
    h.update(2_000, true, 0, { activationKey: "synthetic-queue-b", workId: 2 });
    for (let now = 62_000; now <= 86_402_000; now += 60_000) h.observe(now);
    h.observe(86_403_000);
    expect(h.reports[h.reports.length - 1]).toMatchObject({ workId: 2, listenedSeconds: 1 });
    expect(h.reports.some((r) => r.listenedSeconds === 86400)).toBe(true);
  });
});
