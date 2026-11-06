import { useEffect, useState } from "react";

/** The time left until `expiresAt`, as "3h 12m" or, in the last hour, "4m 05s". */
export function formatTimeLeft(expiresAt: string, now: number): string {
  const total = Math.max(0, Math.floor((new Date(expiresAt).getTime() - now) / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (hours > 0) {
    return `${hours}h ${String(minutes).padStart(2, "0")}m`;
  }

  return `${minutes}m ${String(total % 60).padStart(2, "0")}s`;
}

/** The current time, refreshed every second while something shows a countdown. */
export function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);

    return () => window.clearInterval(id);
  }, []);

  return now;
}
