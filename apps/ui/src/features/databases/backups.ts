import type { DatabaseStatus } from "./status";
import {
  formatClock,
  isScheduleRunning,
  localTimeZone,
  scheduleTime,
  utcToLocal,
  weekdayNames,
} from "./schedule";
import type { WallTime } from "./schedule";
import type { Backup, BackupPhase, BackupSchedule, BackupSchedulePeriod } from "./types";

/**
 * Every backup state the adapter reports, mapped onto a shipped pill. The label
 * says which state it is in, so a pending backup does not read as a running one.
 */
const phaseStatus: Record<BackupPhase, DatabaseStatus> = {
  pending: { kind: "creating", label: "Pending" },
  running: { kind: "creating", label: "Running" },
  succeeded: { kind: "ready", label: "Succeeded" },
  failed: { kind: "failed", label: "Failed" },
  deleting: { kind: "creating", label: "Deleting" },
  unknown: { kind: "degraded", label: "Unknown" },
};

export function backupStatus(backup: Backup): DatabaseStatus {
  return phaseStatus[backup.status] ?? phaseStatus.unknown;
}

/** True while the operator is still working on this backup, which keeps a poll alive. */
export function isBackupActive(backup: Backup): boolean {
  return backup.status === "pending" || backup.status === "running" || backup.status === "deleting";
}

/** Only a finished backup has data to restore from. */
export function canRestore(backup: Backup): boolean {
  return backup.status === "succeeded";
}

const units = [
  { label: "TB", factor: 1024 ** 4 },
  { label: "GB", factor: 1024 ** 3 },
  { label: "MB", factor: 1024 ** 2 },
  { label: "KB", factor: 1024 },
];

/**
 * The size of a backup, in the unit a person would use. Null means the operator
 * has not reported one yet: a zero would read as an empty backup rather than a
 * measurement that has not happened.
 */
export function formatBackupSize(bytes: number): string | null {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return null;
  }

  for (const unit of units) {
    if (bytes >= unit.factor) {
      return `${trimDecimal(bytes / unit.factor)} ${unit.label}`;
    }
  }

  return `${Math.round(bytes)} bytes`;
}

function trimDecimal(value: number): string {
  const rounded = Math.round(value * 10) / 10;

  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
}

export type ScheduleSummary = {
  headline: string;
  detail: string;
};

const headlines = {
  hourly: "Hourly backups",
  daily: "Daily backups",
  weekly: "Weekly backups",
} as const;

/**
 * The schedule status above the backup list, in the viewer's time with the
 * stored UTC time beside it. It states what the environment actually runs, not the
 * profile default, because a spec edited outside the control plane still
 * decides when a backup happens.
 */
export function scheduleSummary(
  schedule: BackupSchedule | undefined,
  timeZone: string = localTimeZone(),
  now: Date = new Date(),
): ScheduleSummary {
  if (!schedule) {
    return {
      headline: "On-demand backups",
      detail: "No schedule runs for this database. Back up before a risky change.",
    };
  }
  if (!isScheduleRunning(schedule)) {
    return {
      headline: "Schedule off",
      detail: "No scheduled backups run. Backups already taken are kept.",
    };
  }

  const read = scheduleTime(schedule);
  const retention = schedule.retentionCopies
    ? ` The last ${schedule.retentionCopies} are kept.`
    : "";
  if (!read) {
    return { headline: "Scheduled backups", detail: `Runs on cron ${schedule.cron}.${retention}` };
  }

  return {
    headline: headlines[read.period],
    detail: `Runs ${runsAt(read.period, read.utc, timeZone, now)}.${retention}`,
  };
}

/**
 * When a schedule runs, as a phrase that follows "runs". A time in a zone
 * other than UTC is followed by the stored UTC time, so what the viewer sees
 * and what the environment reads can both be checked.
 */
function runsAt(period: BackupSchedulePeriod, utc: WallTime, timeZone: string, now: Date): string {
  const local = utcToLocal(utc, timeZone, now);
  if (period === "hourly") {
    // A half hour zone moves the minute, so the stored one is stated too.
    return local.minute === utc.minute
      ? `every hour at ${local.minute} minutes past`
      : `every hour at ${local.minute} minutes past (${utc.minute} past in UTC)`;
  }

  const localClock = formatClock(local.hour, local.minute);
  const utcClock = formatClock(utc.hour, utc.minute);
  const sameAsUtc = localClock === utcClock && local.weekday === utc.weekday;

  if (period === "daily") {
    return sameAsUtc ? `at ${utcClock} UTC` : `at ${localClock} (${utcClock} UTC)`;
  }

  const localDay = weekdayNames[local.weekday];
  if (sameAsUtc) {
    return `on ${localDay} at ${utcClock} UTC`;
  }

  return `on ${localDay} at ${localClock} (${weekdayNames[utc.weekday]} ${utcClock} UTC)`;
}

/**
 * The empty backup list. It only promises a scheduled backup when a schedule
 * will actually produce one.
 */
export function emptyBackups(
  schedule: BackupSchedule | undefined,
  timeZone: string = localTimeZone(),
  now: Date = new Date(),
): {
  title: string;
  detail: string;
} {
  if (!schedule) {
    return {
      title: "No backups yet",
      detail: "This database has no schedule, so back it up when you need one.",
    };
  }
  if (!isScheduleRunning(schedule)) {
    return {
      title: "No backups yet",
      detail: "The schedule is off, so back it up when you need one.",
    };
  }

  const read = scheduleTime(schedule);

  return {
    title: "No backups yet",
    detail: read
      ? `The first scheduled backup runs ${runsAt(read.period, read.utc, timeZone, now)}.`
      : "The first scheduled backup runs at the next time the schedule names.",
  };
}
