import { describe, expect, it } from "vitest";
import {
  isScheduleRunning,
  localToUtc,
  scheduleFormFrom,
  scheduleRequestFrom,
  utcToLocal,
} from "./schedule";
import type { BackupSchedule } from "./types";

// Mid July: Berlin is on summer time, two hours ahead of UTC, and Los Angeles
// is seven hours behind.
const july = new Date("2026-07-15T12:00:00Z");
// Mid January: Berlin is one hour ahead.
const january = new Date("2026-01-15T12:00:00Z");

describe("utcToLocal and localToUtc", () => {
  it.each([
    {
      zone: "UTC",
      now: july,
      utc: { weekday: 1, hour: 2, minute: 0 },
      local: { weekday: 1, hour: 2, minute: 0 },
    },
    {
      zone: "Europe/Berlin",
      now: july,
      utc: { weekday: 1, hour: 2, minute: 0 },
      local: { weekday: 1, hour: 4, minute: 0 },
    },
    {
      zone: "Europe/Berlin",
      now: january,
      utc: { weekday: 1, hour: 2, minute: 0 },
      local: { weekday: 1, hour: 3, minute: 0 },
    },
    {
      zone: "Europe/Berlin",
      now: july,
      utc: { weekday: 0, hour: 23, minute: 30 },
      local: { weekday: 1, hour: 1, minute: 30 },
    },
    {
      zone: "America/Los_Angeles",
      now: july,
      utc: { weekday: 0, hour: 2, minute: 0 },
      local: { weekday: 6, hour: 19, minute: 0 },
    },
    {
      zone: "Asia/Kolkata",
      now: july,
      utc: { weekday: 3, hour: 20, minute: 45 },
      local: { weekday: 4, hour: 2, minute: 15 },
    },
  ])("maps $utc.hour:$utc.minute UTC to $zone and back", ({ zone, now, utc, local }) => {
    expect(utcToLocal(utc, zone, now)).toEqual(local);
    expect(localToUtc(local, zone, now)).toEqual(utc);
  });
});

describe("isScheduleRunning", () => {
  it("reads an adapter older than the enabled field as running", () => {
    expect(isScheduleRunning({ name: "daily", cron: "0 2 * * *" })).toBe(true);
  });

  it("reads a schedule turned off as not running", () => {
    expect(isScheduleRunning({ name: "daily", cron: "0 2 * * *", enabled: false })).toBe(false);
  });

  it("reads no schedule as not running", () => {
    expect(isScheduleRunning(undefined)).toBe(false);
  });
});

describe("scheduleFormFrom", () => {
  it("starts off with the default daily settings when there is no schedule", () => {
    expect(scheduleFormFrom(undefined, "Europe/Berlin", july)).toEqual({
      period: "off",
      minute: "0",
      time: "04:00",
      weekday: "1",
      retentionCopies: "7",
    });
  });

  it("prefills a weekly schedule in local time", () => {
    const weekly: BackupSchedule = {
      name: "daily",
      enabled: true,
      cron: "30 23 * * 0",
      period: "weekly",
      minute: 30,
      hour: 23,
      weekday: 0,
      retentionCopies: 4,
    };

    expect(scheduleFormFrom(weekly, "Europe/Berlin", july)).toEqual({
      period: "weekly",
      minute: "30",
      time: "01:30",
      weekday: "1",
      retentionCopies: "4",
    });
  });

  it("keeps the settings of a schedule turned off so turning it on restores them", () => {
    const off: BackupSchedule = {
      name: "daily",
      enabled: false,
      cron: "15 * * * *",
      period: "hourly",
      minute: 15,
      retentionCopies: 12,
    };

    expect(scheduleFormFrom(off, "UTC", july)).toMatchObject({
      period: "off",
      minute: "15",
      retentionCopies: "12",
    });
  });

  it("opens a running cron it cannot phrase on custom, not off", () => {
    expect(
      scheduleFormFrom({ name: "nightly", enabled: true, cron: "0 3 */2 * *" }, "UTC", july),
    ).toMatchObject({ period: "custom" });
  });

  it("opens an out of range cron on custom rather than as a time", () => {
    expect(
      scheduleFormFrom({ name: "daily", enabled: true, cron: "60 2 * * *" }, "UTC", july),
    ).toMatchObject({ period: "custom" });
    expect(
      scheduleFormFrom({ name: "daily", enabled: true, cron: "0 24 * * *" }, "UTC", july),
    ).toMatchObject({ period: "custom" });
  });

  it("reads an older adapter's daily cron", () => {
    expect(scheduleFormFrom({ name: "daily", cron: "0 2 * * *" }, "UTC", july)).toMatchObject({
      period: "daily",
      time: "02:00",
      retentionCopies: "7",
    });
  });
});

describe("scheduleRequestFrom", () => {
  const base = { minute: "0", time: "04:00", weekday: "1", retentionCopies: "7" };

  it("sends only enabled false when turned off", () => {
    expect(scheduleRequestFrom({ ...base, period: "off" }, "Europe/Berlin", july)).toEqual({
      enabled: false,
    });
  });

  it("sends an hourly schedule's minute in UTC", () => {
    expect(
      scheduleRequestFrom(
        { ...base, period: "hourly", minute: "15", retentionCopies: "24" },
        "Asia/Kolkata",
        july,
      ),
    ).toEqual({ enabled: true, period: "hourly", minute: 45, retentionCopies: 24 });
  });

  it("sends a daily schedule in UTC", () => {
    expect(scheduleRequestFrom({ ...base, period: "daily" }, "Europe/Berlin", july)).toEqual({
      enabled: true,
      period: "daily",
      minute: 0,
      hour: 2,
      retentionCopies: 7,
    });
  });

  it("moves a weekly schedule to the UTC weekday", () => {
    expect(
      scheduleRequestFrom(
        { ...base, period: "weekly", time: "01:30", weekday: "1" },
        "Europe/Berlin",
        july,
      ),
    ).toEqual({
      enabled: true,
      period: "weekly",
      minute: 30,
      hour: 23,
      weekday: 0,
      retentionCopies: 7,
    });
  });
});
