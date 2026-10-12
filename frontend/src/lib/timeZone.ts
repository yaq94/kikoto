/** The calendar day an instant falls on in a time zone, and the instant that day ends. */
export type ZonedDay = { day: string; end: number };

function utcDay(time: number): ZonedDay {
  const day = new Date(time).toISOString().slice(0, 10);
  return { day, end: Date.parse(`${day}T00:00:00Z`) + 86_400_000 };
}

/**
 * Returns a lookup from an instant to its calendar day (YYYY-MM-DD) in an
 * IANA time zone. Consecutive lookups inside one day reuse the last answer. A
 * zone this runtime does not know counts days in UTC.
 */
export function createZonedDays(timeZone: string) {
  let format: Intl.DateTimeFormat | null = null;
  try {
    format = new Intl.DateTimeFormat("en-CA", {
      timeZone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    });
  } catch {
    format = null;
  }
  let cached: (ZonedDay & { start: number }) | null = null;
  return (time: number): ZonedDay => {
    if (cached && time >= cached.start && time < cached.end) return cached;
    if (!format) return utcDay(time);
    const parts: Partial<Record<Intl.DateTimeFormatPartTypes, string>> = {};
    for (const part of format.formatToParts(time)) parts[part.type] = part.value;
    const seconds = (Number(parts.hour) % 24) * 3600 + Number(parts.minute) * 60 + Number(parts.second);
    const wholeSecond = time - (((time % 1000) + 1000) % 1000);
    cached = {
      day: `${parts.year}-${parts.month}-${parts.day}`,
      start: time,
      end: wholeSecond + (86_400 - seconds) * 1000,
    };
    return cached;
  };
}

let listeningZone = "UTC";

/** The time zone whose calendar days this client credits listening to. */
export function listeningTimeZone() {
  return listeningZone;
}

/**
 * Adopts the account's time zone as the server reports it: a name is the zone
 * the account fixed, null follows this device, and a response that names
 * nothing keeps the current zone.
 */
export function setListeningTimeZone(timeZone: string | null | undefined) {
  if (timeZone === undefined) return;
  listeningZone = timeZone || deviceTimeZone() || "UTC";
}

/** The zone this device runs in, or null when the runtime does not say. */
export function deviceTimeZone() {
  try {
    return new Intl.DateTimeFormat().resolvedOptions().timeZone || null;
  } catch {
    return null;
  }
}

/**
 * IANA zones a time zone setting offers, in name order: every zone this
 * runtime knows, UTC, and each zone already in use.
 */
export function timeZoneNames(...inUse: Array<string | null | undefined>) {
  let known: string[] = [];
  try {
    // Absent from the compile target's library, and from older WebViews.
    const intl = Intl as typeof Intl & { supportedValuesOf?: (key: "timeZone") => string[] };
    known = intl.supportedValuesOf?.("timeZone") ?? [];
  } catch {
    known = [];
  }
  const names = new Set<string>(["UTC", ...known]);
  for (const name of inUse) if (name) names.add(name);
  return [...names].sort((left, right) => left.localeCompare(right));
}

/** A zone's current offset as `UTC+08:00`, or an empty string when the runtime cannot tell. */
export function timeZoneOffset(timeZone: string, now = new Date()) {
  try {
    const name = new Intl.DateTimeFormat("en", { timeZone, timeZoneName: "longOffset" })
      .formatToParts(now)
      .find((part) => part.type === "timeZoneName")?.value;
    if (!name) return "";
    return name === "GMT" ? "UTC+00:00" : name.replace("GMT", "UTC");
  } catch {
    return "";
  }
}
