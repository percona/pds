import type { BackupSchedule, BackupScheduleInput, BackupSchedulePeriod } from "./types";

/**
 * The schedule editor works in the viewer's time zone and the adapter stores
 * UTC. The conversion uses the offset in force today, which is why the help
 * text warns that a stored time can move by an hour across daylight saving.
 */

export type WallTime = {
  /** 0 is Sunday, the way cron counts. */
  weekday: number;
  hour: number;
  minute: number;
};

export const minRetentionCopies = 1;
export const maxRetentionCopies = 30;
export const defaultRetentionCopies = 7;

/** The schedule an empty request turns on: daily at 02:00 UTC. */
const defaultUtc: WallTime = { weekday: 1, hour: 2, minute: 0 };

export const weekdayNames = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
] as const;

/** The viewer's IANA time zone, for example Europe/Berlin. */
export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

type ZoneParts = WallTime & { year: number; month: number; day: number };

const weekdayIndex: Record<string, number> = {
  Sun: 0,
  Mon: 1,
  Tue: 2,
  Wed: 3,
  Thu: 4,
  Fri: 5,
  Sat: 6,
};

function partsIn(date: Date, timeZone: string): ZoneParts {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    hourCycle: "h23",
    weekday: "short",
    year: "numeric",
    month: "numeric",
    day: "numeric",
    hour: "numeric",
    minute: "numeric",
  }).formatToParts(date);
  const read = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((part) => part.type === type)?.value ?? "";

  return {
    year: Number(read("year")),
    month: Number(read("month")),
    day: Number(read("day")),
    weekday: weekdayIndex[read("weekday")] ?? 0,
    hour: Number(read("hour")) % 24,
    minute: Number(read("minute")),
  };
}

/** Minutes the zone is ahead of UTC at that instant. */
function offsetMinutes(date: Date, timeZone: string): number {
  const p = partsIn(date, timeZone);
  const asUtc = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute);

  return Math.round((asUtc - Math.floor(date.getTime() / 60000) * 60000) / 60000);
}

/** A stored UTC time as the viewer's clock reads it this week. */
export function utcToLocal(utc: WallTime, timeZone: string, now: Date = new Date()): WallTime {
  const shift = (utc.weekday - now.getUTCDay() + 7) % 7;
  const at = new Date(
    Date.UTC(
      now.getUTCFullYear(),
      now.getUTCMonth(),
      now.getUTCDate() + shift,
      utc.hour,
      utc.minute,
    ),
  );
  const { weekday, hour, minute } = partsIn(at, timeZone);

  return { weekday, hour, minute };
}

/** A time on the viewer's clock as UTC, at the offset in force this week. */
export function localToUtc(local: WallTime, timeZone: string, now: Date = new Date()): WallTime {
  const today = partsIn(now, timeZone);
  const shift = (local.weekday - today.weekday + 7) % 7;
  const naive = Date.UTC(today.year, today.month - 1, today.day + shift, local.hour, local.minute);

  let at = new Date(naive - offsetMinutes(new Date(naive), timeZone) * 60000);
  const settled = offsetMinutes(at, timeZone);
  at = new Date(naive - settled * 60000);

  return { weekday: at.getUTCDay(), hour: at.getUTCHours(), minute: at.getUTCMinutes() };
}

/** True when the schedule will take backups. An adapter older than `enabled` only reported running ones. */
export function isScheduleRunning(schedule: BackupSchedule | undefined): boolean {
  return schedule != null && schedule.enabled !== false;
}

/**
 * The schedule as period and UTC time. An adapter older than these fields
 * sent the cron alone, so a daily cron is read here too. Null is a cron this
 * cannot phrase.
 */
export function scheduleTime(
  schedule: BackupSchedule,
): { period: BackupSchedulePeriod; utc: WallTime } | null {
  if (schedule.period) {
    return {
      period: schedule.period,
      utc: {
        weekday: schedule.weekday ?? defaultUtc.weekday,
        hour: schedule.hour ?? 0,
        minute: schedule.minute ?? 0,
      },
    };
  }

  const match = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(schedule.cron.trim());
  if (!match) {
    return null;
  }
  const minute = Number(match[1]);
  const hour = Number(match[2]);
  // Out of range is a cron the environment refuses, not one to phrase as a time.
  if (minute > 59 || hour > 23) {
    return null;
  }

  return { period: "daily", utc: { weekday: defaultUtc.weekday, hour, minute } };
}

/**
 * What the Frequency control holds. Custom is a running cron this page cannot
 * phrase, set outside it. It is only ever the starting value: saving means
 * picking one of the others, which replaces it.
 */
export type ScheduleFormPeriod = "off" | "custom" | BackupSchedulePeriod;

/** The editor's fields, as strings the inputs hold, in the viewer's time. */
export type ScheduleFormValues = {
  period: ScheduleFormPeriod;
  /** Minutes past the hour, for an hourly schedule. */
  minute: string;
  /** HH:MM, for a daily or weekly schedule. */
  time: string;
  weekday: string;
  retentionCopies: string;
};

export function formatClock(hour: number, minute: number): string {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

/**
 * The editor's starting values. A schedule turned off opens on "off" with the
 * settings it had, so choosing its period again restores them. A running cron
 * this cannot phrase opens on "custom" rather than on "off", which would state
 * that nothing runs.
 */
export function scheduleFormFrom(
  schedule: BackupSchedule | undefined,
  timeZone: string,
  now: Date = new Date(),
): ScheduleFormValues {
  const read = schedule ? scheduleTime(schedule) : null;
  const utc = read?.utc ?? defaultUtc;
  const local = utcToLocal(utc, timeZone, now);
  const running = isScheduleRunning(schedule);

  return {
    period: !running ? "off" : read ? read.period : "custom",
    minute: String(local.minute),
    time: formatClock(local.hour, local.minute),
    weekday: String(local.weekday),
    retentionCopies: String(schedule?.retentionCopies ?? defaultRetentionCopies),
  };
}

/** The request body for the editor's values, with every time moved to UTC. */
export function scheduleRequestFrom(
  values: ScheduleFormValues,
  timeZone: string,
  now: Date = new Date(),
): BackupScheduleInput {
  if (values.period === "off") {
    return { enabled: false };
  }
  if (values.period === "custom") {
    throw new Error("A custom schedule is replaced by picking a frequency, not saved as is.");
  }

  const retentionCopies = Number(values.retentionCopies);

  if (values.period === "hourly") {
    // Only the minute matters, and a zone offset of a whole number of hours
    // leaves it alone. Half hour zones do move it.
    const utc = localToUtc({ weekday: 0, hour: 12, minute: Number(values.minute) }, timeZone, now);

    return { enabled: true, period: "hourly", minute: utc.minute, retentionCopies };
  }

  const [hour = 0, minute = 0] = values.time.split(":").map(Number);
  const local: WallTime = { weekday: Number(values.weekday), hour, minute };
  const utc = localToUtc(local, timeZone, now);

  if (values.period === "daily") {
    return { enabled: true, period: "daily", minute: utc.minute, hour: utc.hour, retentionCopies };
  }

  return {
    enabled: true,
    period: "weekly",
    minute: utc.minute,
    hour: utc.hour,
    weekday: utc.weekday,
    retentionCopies,
  };
}
