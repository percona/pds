import { useEffect, useState } from "react";

/**
 * The current time, refreshed on an interval, so a relative time ("5 minutes
 * ago") keeps moving while the data it was computed from has not changed.
 */
export function useNow(intervalMs = 30_000): Date {
  const [now, setNow] = useState(() => new Date());

  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs);

    return () => clearInterval(timer);
  }, [intervalMs]);

  return now;
}
