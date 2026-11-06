import { ApiError, requestJson } from "@/lib/api";
import { databaseResourcePath } from "@/features/databases";
import type { AlertReport, DatabaseAlertReport } from "./types";

/** Reads every environment's state and firing alerts, already sorted. */
export async function fetchAlerts(signal?: AbortSignal): Promise<AlertReport> {
  const payload = await requestJson<AlertReport>("/api/alerts", { signal });

  if (!payload || !Array.isArray(payload.environments) || !Array.isArray(payload.alerts)) {
    throw new ApiError("ErrInternal", "The alerts response could not be read.", 200);
  }

  return payload;
}

/** Reads one database's firing alerts and the state of its environment. */
export async function fetchDatabaseAlerts(
  databaseId: string,
  signal?: AbortSignal,
): Promise<DatabaseAlertReport> {
  const payload = await requestJson<DatabaseAlertReport>(
    `/api/databases/${databaseResourcePath(databaseId)}/alerts`,
    { signal },
  );

  if (!payload || !payload.environment || !Array.isArray(payload.alerts)) {
    throw new ApiError("ErrInternal", "The alerts response could not be read.", 200);
  }

  return payload;
}
