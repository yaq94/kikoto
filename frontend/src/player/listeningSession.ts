import type { DatedListeningReport } from "@/lib/playbackReportApi";
import { createZonedDays } from "@/lib/timeZone";

export const MAX_LISTENING_SESSION_SECONDS = 86_400;
export const LISTENING_MAX_OBSERVATION_GAP_MS = 60_000;
const MAX_SESSION_DAYS = 32;
// A report rolls over before its hour buckets outgrow one request.
const MAX_SESSION_HOURS = 48;

let hourReports = false;

/** Whether the server takes each day's listening split by hour of the day. */
export function reportsListeningHours() {
  return hourReports;
}

export function setReportsListeningHours(enabled: boolean) {
  hourReports = enabled;
}
export type ListeningTarget = { activationKey: string; workId: number };

/**
 * Measures actual advancing wall time and credits it to calendar days, and
 * their hours, of the account's time zone. It owns neither storage nor
 * networking.
 */
export class ListeningClock {
  private target: ListeningTarget | null = null;
  private generation: number | null = null;
  private report: DatedListeningReport | null = null;
  private days = new Map<string, number>();
  // Milliseconds per `day hour`; reported only to a server that counts hours.
  private hours = new Map<string, number>();
  private runningSince: number | null = null;
  private wallSince = 0;
  private publishedSeconds = 0;
  private zone = "UTC";
  private dayAt = createZonedDays("UTC");

  constructor(
    private readonly createId: () => string,
    private readonly checkpoint: (report: DatedListeningReport) => void,
    /** The time zone whose calendar days listening is credited to. */
    private readonly timeZone: () => string = () => "UTC",
    /** Whether reports split each day by hour. */
    private readonly countsHours: () => boolean = () => false,
  ) {}

  update(target: ListeningTarget | null, generation: number | null, advancing: boolean, now: number, wall: number) {
    if (generation !== this.generation) {
      this.report = null;
      this.days.clear();
      this.hours.clear();
      this.publishedSeconds = 0;
      this.runningSince = null;
      this.generation = generation;
    }
    this.observe(now, wall);
    if (!advancing) this.publish(true);
    if (target?.activationKey !== this.target?.activationKey || target?.workId !== this.target?.workId) {
      this.end();
      this.target = target;
    }
    this.runningSince = advancing && target && generation !== null ? now : null;
    this.wallSince = wall;
  }

  observe(now: number, wall: number) {
    if (this.runningSince === null || !this.target || this.generation === null) return;
    const elapsed = now - this.runningSince;
    const wallElapsed = wall - this.wallSince;
    this.runningSince = now;
    this.wallSince = wall;
    // Frozen pages, device sleep, or a changed wall clock cannot invent time.
    if (Math.abs(elapsed - wallElapsed) > 2000) {
      this.closeReport();
      return;
    }
    if (elapsed <= 0 || elapsed > LISTENING_MAX_OBSERVATION_GAP_MS) return;
    const zone = this.timeZone();
    if (zone !== this.zone) {
      // One report keeps one zone's days.
      this.closeReport();
      this.zone = zone;
      this.dayAt = createZonedDays(zone);
    }
    let cursor = wall - elapsed;
    while (cursor < wall) {
      const { day, hour, hourEnd } = this.dayAt(cursor);
      const hourKey = `${day} ${hour}`;
      if (
        (this.days.size >= MAX_SESSION_DAYS && !this.days.has(day)) ||
        (this.hours.size >= MAX_SESSION_HOURS && !this.hours.has(hourKey))
      )
        this.closeReport();
      if (!this.report)
        this.report = {
          generation: this.generation,
          sessionId: this.createId(),
          workId: this.target.workId,
          listenedSeconds: 0,
          startedAt: new Date(cursor).toISOString(),
          lastListenedAt: new Date(cursor).toISOString(),
          days: [],
        };
      const remaining = MAX_LISTENING_SESSION_SECONDS * 1000 - this.report.listenedSeconds * 1000;
      // An hour never outlasts its day, so a step also stops at midnight.
      const amount = Math.min(wall - cursor, Math.max(1, hourEnd - cursor), remaining);
      this.days.set(day, (this.days.get(day) ?? 0) + amount);
      this.hours.set(hourKey, (this.hours.get(hourKey) ?? 0) + amount);
      cursor += amount;
      this.report.listenedSeconds = [...this.days.values()].reduce((sum, value) => sum + value, 0) / 1000;
      this.report.lastListenedAt = new Date(cursor).toISOString();
      const countHours = this.countsHours();
      this.report.days = [...this.days]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([day, ms]) => ({
          day,
          listenedSeconds: ms / 1000,
          ...(countHours ? { hours: this.hoursOf(day) } : {}),
        }));
      this.publish();
      if (this.report.listenedSeconds >= MAX_LISTENING_SESSION_SECONDS) this.closeReport();
    }
  }

  end() {
    this.closeReport();
    this.runningSince = null;
  }
  private hoursOf(day: string) {
    return [...this.hours]
      .filter(([key]) => key.startsWith(`${day} `))
      .map(([key, ms]) => ({ hour: Number(key.slice(day.length + 1)), listenedSeconds: ms / 1000 }))
      .sort((left, right) => left.hour - right.hour);
  }
  private publish(force = false) {
    if (
      !this.report ||
      this.report.listenedSeconds < 1 ||
      (!force && Math.floor(this.report.listenedSeconds) <= Math.floor(this.publishedSeconds))
    )
      return;
    this.publishedSeconds = this.report.listenedSeconds;
    this.checkpoint(structuredClone(this.report));
  }
  private closeReport() {
    this.publish(true);
    this.report = null;
    this.days.clear();
    this.hours.clear();
    this.publishedSeconds = 0;
  }
}

export function isMediaAdvancing(
  element: Pick<HTMLMediaElement, "paused" | "ended" | "seeking" | "readyState"> | null,
) {
  return Boolean(element && !element.paused && !element.ended && !element.seeking && element.readyState >= 3);
}
