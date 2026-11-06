import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchDatabaseAlerts } from "../api";
import type { DatabaseAlertReport } from "../types";
import { alertsKey, alertsPollMs } from "./use-alerts";

/** One database's firing alerts, read again on the same beat as the page. */
export function useDatabaseAlerts(databaseId: string) {
  return useQuery<DatabaseAlertReport, ApiError>({
    queryKey: [...alertsKey, "database", databaseId],
    queryFn: ({ signal }) => fetchDatabaseAlerts(databaseId, signal),
    refetchInterval: alertsPollMs,
    enabled: databaseId !== "",
  });
}
