import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchAlerts } from "../api";
import type { AlertReport } from "../types";

/** How often alerts are read again. The sidebar count and the page share it. */
export const alertsPollMs = 30_000;

export const alertsKey = ["alerts"] as const;

/**
 * Every environment's alerts. One query serves the sidebar count and the page,
 * so a screen that shows both never asks twice.
 */
export function useAlerts() {
  return useQuery<AlertReport, ApiError>({
    queryKey: alertsKey,
    queryFn: ({ signal }) => fetchAlerts(signal),
    refetchInterval: alertsPollMs,
  });
}
