import { describe, expect, it } from "vitest";
import {
  backupStatus,
  canRestore,
  emptyBackups,
  formatBackupSize,
  isBackupActive,
  scheduleSummary,
} from "./backups";
import type { Backup, BackupSchedule } from "./types";

function backup(overrides?: Partial<Backup>): Backup {
  return {
    id: "orders-dev-20260825020000",
    status: "succeeded",
    sizeBytes: 2147483648,
    scheduled: true,
    createdAt: "2026-08-25T02:00:00Z",
    ...overrides,
  };
}

const daily: BackupSchedule = { name: "daily", cron: "0 2 * * *", retentionCopies: 7 };

describe("backupStatus", () => {
  it("maps every state the adapter reports onto a shipped pill", () => {
    expect(backupStatus(backup({ status: "pending" }))).toEqual({
      kind: "creating",
      label: "Pending",
    });
    expect(backupStatus(backup({ status: "running" }))).toEqual({
      kind: "creating",
      label: "Running",
    });
    expect(backupStatus(backup({ status: "succeeded" }))).toEqual({
      kind: "ready",
      label: "Succeeded",
    });
    expect(backupStatus(backup({ status: "failed" }))).toEqual({ kind: "failed", label: "Failed" });
    expect(backupStatus(backup({ status: "deleting" }))).toEqual({
      kind: "creating",
      label: "Deleting",
    });
    expect(backupStatus(backup({ status: "unknown" }))).toEqual({
      kind: "degraded",
      label: "Unknown",
    });
  });
});

describe("isBackupActive", () => {
  it("is true while the operator still has work to do", () => {
    expect(isBackupActive(backup({ status: "pending" }))).toBe(true);
    expect(isBackupActive(backup({ status: "running" }))).toBe(true);
    expect(isBackupActive(backup({ status: "deleting" }))).toBe(true);
  });

  it("is false once the backup has an outcome", () => {
    expect(isBackupActive(backup({ status: "succeeded" }))).toBe(false);
    expect(isBackupActive(backup({ status: "failed" }))).toBe(false);
    expect(isBackupActive(backup({ status: "unknown" }))).toBe(false);
  });
});

describe("canRestore", () => {
  it("only offers a restore from a backup that finished", () => {
    expect(canRestore(backup({ status: "succeeded" }))).toBe(true);
    expect(canRestore(backup({ status: "running" }))).toBe(false);
    expect(canRestore(backup({ status: "failed" }))).toBe(false);
  });
});

describe("formatBackupSize", () => {
  it("reads the size in the unit a person would use", () => {
    expect(formatBackupSize(512)).toBe("512 bytes");
    expect(formatBackupSize(1024)).toBe("1 KB");
    expect(formatBackupSize(1289748)).toBe("1.2 MB");
    expect(formatBackupSize(2147483648)).toBe("2 GB");
    expect(formatBackupSize(1099511627776)).toBe("1 TB");
  });

  // A backup that has not finished has no size, and a zero would read as an
  // empty backup rather than a missing measurement.
  it("has nothing to report before the backup finishes", () => {
    expect(formatBackupSize(0)).toBeNull();
  });
});

// Mid July, when Berlin is two hours ahead of UTC.
const july = new Date("2026-07-15T12:00:00Z");

describe("scheduleSummary", () => {
  it("states the daily schedule and how many copies it keeps", () => {
    expect(scheduleSummary(daily, "UTC", july)).toEqual({
      headline: "Daily backups",
      detail: "Runs at 02:00 UTC. The last 7 are kept.",
    });
  });

  it("states the time in the viewer's zone beside the stored UTC time", () => {
    expect(scheduleSummary(daily, "Europe/Berlin", july)).toEqual({
      headline: "Daily backups",
      detail: "Runs at 04:00 (02:00 UTC). The last 7 are kept.",
    });
  });

  it("states an hourly schedule by the minute", () => {
    const hourly: BackupSchedule = {
      name: "daily",
      enabled: true,
      cron: "15 * * * *",
      period: "hourly",
      minute: 15,
      retentionCopies: 24,
    };

    expect(scheduleSummary(hourly, "Europe/Berlin", july)).toEqual({
      headline: "Hourly backups",
      detail: "Runs every hour at 15 minutes past. The last 24 are kept.",
    });
  });

  it("states the stored minute too when the zone moves it", () => {
    const hourly: BackupSchedule = {
      name: "daily",
      enabled: true,
      cron: "45 * * * *",
      period: "hourly",
      minute: 45,
      retentionCopies: 24,
    };

    expect(scheduleSummary(hourly, "Asia/Kolkata", july).detail).toBe(
      "Runs every hour at 15 minutes past (45 past in UTC). The last 24 are kept.",
    );
  });

  it("states a weekly schedule with the day in both clocks", () => {
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

    expect(scheduleSummary(weekly, "Europe/Berlin", july)).toEqual({
      headline: "Weekly backups",
      detail: "Runs on Monday at 01:30 (Sunday 23:30 UTC). The last 4 are kept.",
    });
    expect(scheduleSummary(weekly, "UTC", july).detail).toBe(
      "Runs on Sunday at 23:30 UTC. The last 4 are kept.",
    );
  });

  it("says a schedule turned off runs nothing and keeps what it took", () => {
    expect(scheduleSummary({ ...daily, enabled: false }, "UTC", july)).toEqual({
      headline: "Schedule off",
      detail: "No scheduled backups run. Backups already taken are kept.",
    });
  });

  it("prints a schedule it cannot phrase as the cron the environment runs", () => {
    expect(
      scheduleSummary({ name: "weekly", cron: "15 3 * * 1-5", retentionCopies: 4 }, "UTC", july),
    ).toEqual({
      headline: "Scheduled backups",
      detail: "Runs on cron 15 3 * * 1-5. The last 4 are kept.",
    });
  });

  it("leaves out retention the schedule does not set", () => {
    expect(scheduleSummary({ name: "daily", cron: "0 2 * * *" }, "UTC", july).detail).toBe(
      "Runs at 02:00 UTC.",
    );
  });

  it("says on demand when there is no schedule", () => {
    expect(scheduleSummary(undefined)).toEqual({
      headline: "On-demand backups",
      detail: "No schedule runs for this database. Back up before a risky change.",
    });
  });
});

describe("emptyBackups", () => {
  it("says when the first scheduled backup runs", () => {
    expect(emptyBackups(daily, "Europe/Berlin", july)).toEqual({
      title: "No backups yet",
      detail: "The first scheduled backup runs at 04:00 (02:00 UTC).",
    });
  });

  it("does not promise a backup that is never coming", () => {
    expect(emptyBackups(undefined)).toEqual({
      title: "No backups yet",
      detail: "This database has no schedule, so back it up when you need one.",
    });
    expect(emptyBackups({ ...daily, enabled: false })).toEqual({
      title: "No backups yet",
      detail: "The schedule is off, so back it up when you need one.",
    });
  });
});
