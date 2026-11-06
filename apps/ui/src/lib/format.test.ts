import { describe, expect, it } from "vitest";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";

const now = new Date("2026-08-25T12:00:00Z");

function ago(ms: number): string {
  return new Date(now.getTime() - ms).toISOString();
}

const minute = 60_000;
const hour = 60 * minute;
const day = 24 * hour;

describe("formatRelativeTime", () => {
  it("reads the last minute as just now", () => {
    expect(formatRelativeTime(ago(20_000), now)).toBe("Just now");
  });

  it("counts minutes, hours, and days", () => {
    expect(formatRelativeTime(ago(minute), now)).toBe("1 minute ago");
    expect(formatRelativeTime(ago(5 * minute), now)).toBe("5 minutes ago");
    expect(formatRelativeTime(ago(hour), now)).toBe("1 hour ago");
    expect(formatRelativeTime(ago(3 * hour), now)).toBe("3 hours ago");
    expect(formatRelativeTime(ago(day), now)).toBe("1 day ago");
    expect(formatRelativeTime(ago(2 * day), now)).toBe("2 days ago");
  });

  it("falls back to a date once relative time stops being useful", () => {
    expect(formatRelativeTime(ago(45 * day), now)).toContain("2026");
  });

  it("returns null for a missing or unreadable timestamp", () => {
    expect(formatRelativeTime(undefined, now)).toBeNull();
    expect(formatRelativeTime("", now)).toBeNull();
    expect(formatRelativeTime("not a date", now)).toBeNull();
  });

  it("does not invent a future", () => {
    expect(formatRelativeTime(new Date(now.getTime() + hour).toISOString(), now)).toBe("Just now");
  });
});

describe("formatTimestamp", () => {
  it("renders a readable absolute time", () => {
    expect(formatTimestamp("2026-08-25T12:00:00Z")).toContain("2026");
  });

  it("returns null for a missing or unreadable timestamp", () => {
    expect(formatTimestamp(undefined)).toBeNull();
    expect(formatTimestamp("not a date")).toBeNull();
  });
});
