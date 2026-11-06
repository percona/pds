const minute = 60_000;
const hour = 60 * minute;
const day = 24 * hour;

/**
 * How long ago something happened, in the words a person would use. Past 30
 * days the count stops being useful, so it becomes a date instead.
 */
export function formatRelativeTime(iso: string | undefined, now: Date = new Date()): string | null {
  const at = parse(iso);
  if (!at) {
    return null;
  }

  // A clock skew between the environment and the browser must not print a future.
  const elapsed = Math.max(0, now.getTime() - at.getTime());

  if (elapsed < minute) {
    return "Just now";
  }

  if (elapsed < hour) {
    return count(Math.floor(elapsed / minute), "minute");
  }

  if (elapsed < day) {
    return count(Math.floor(elapsed / hour), "hour");
  }

  if (elapsed < 30 * day) {
    return count(Math.floor(elapsed / day), "day");
  }

  return at.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

/** The full timestamp, for the title of a relative time. */
export function formatTimestamp(iso: string | undefined): string | null {
  const at = parse(iso);

  return at ? at.toLocaleString() : null;
}

function parse(iso: string | undefined): Date | null {
  if (!iso) {
    return null;
  }

  const at = new Date(iso);

  return Number.isNaN(at.getTime()) ? null : at;
}

function count(value: number, unit: string): string {
  return `${value} ${unit}${value === 1 ? "" : "s"} ago`;
}
