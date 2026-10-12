import { describe, expect, it } from "vitest";

import {
  createZonedDays,
  deviceTimeZone,
  listeningTimeZone,
  setListeningTimeZone,
  timeZoneNames,
  timeZoneOffset,
} from "./timeZone";

describe("time zone calendar days", () => {
  it("names the calendar day of an instant in a zone and when that day ends", () => {
    const tokyo = createZonedDays("Asia/Tokyo");
    // 14:59:30 UTC is 23:59:30 in Tokyo, thirty seconds before its midnight.
    const evening = Date.parse("2026-03-02T14:59:30.250Z");
    expect(tokyo(evening)).toMatchObject({ day: "2026-03-02", end: Date.parse("2026-03-02T15:00:00Z") });
    expect(tokyo(Date.parse("2026-03-02T15:00:00Z"))).toMatchObject({
      day: "2026-03-03",
      end: Date.parse("2026-03-03T15:00:00Z"),
    });
    const utc = createZonedDays("UTC");
    expect(utc(evening)).toMatchObject({ day: "2026-03-02", end: Date.parse("2026-03-03T00:00:00Z") });
  });

  it("ends a day early or late across a daylight saving change", () => {
    const berlin = createZonedDays("Europe/Berlin");
    // 2026-03-29 has 23 hours in Berlin: it starts at 23:00 UTC and ends at 22:00 UTC.
    expect(berlin(Date.parse("2026-03-29T12:00:00Z"))).toMatchObject({
      day: "2026-03-29",
      end: Date.parse("2026-03-29T22:00:00Z"),
    });
  });

  it("counts days in UTC for a zone the runtime does not know", () => {
    const unknown = createZonedDays("Mars/Olympus");
    expect(unknown(Date.parse("2026-03-02T23:30:00Z"))).toEqual({
      day: "2026-03-02",
      end: Date.parse("2026-03-03T00:00:00Z"),
    });
  });

  it("adopts the account's zone and keeps it when a response names none", () => {
    setListeningTimeZone("Asia/Tokyo");
    setListeningTimeZone(undefined);
    expect(listeningTimeZone()).toBe("Asia/Tokyo");
    setListeningTimeZone("UTC");
    expect(listeningTimeZone()).toBe("UTC");
  });

  it("follows this device when the account fixed no zone", () => {
    setListeningTimeZone("Asia/Tokyo");
    setListeningTimeZone(null);
    expect(listeningTimeZone()).toBe(deviceTimeZone() ?? "UTC");
    setListeningTimeZone("UTC");
  });

  it("offers UTC and every zone in use, and labels a zone with its offset", () => {
    const names = timeZoneNames("Asia/Tokyo", null, "Synthetic/Zone");
    expect(names).toContain("UTC");
    expect(names).toContain("Synthetic/Zone");
    expect(names.filter((name) => name === "Asia/Tokyo")).toHaveLength(1);
    expect(timeZoneOffset("Asia/Tokyo")).toBe("UTC+09:00");
    expect(timeZoneOffset("UTC")).toBe("UTC+00:00");
    expect(timeZoneOffset("Mars/Olympus")).toBe("");
  });
});
