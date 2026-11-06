import type { Alert, AlertEnvironment, AlertSeverity } from "./types";

/** What a databases list row says about one database's firing alerts. */
export type AlertSummary = {
  total: number;
  /** The highest severity firing on the database. */
  worst: AlertSeverity;
  /** How many alerts have that severity. */
  worstCount: number;
};

const rank: Record<AlertSeverity, number> = { critical: 0, warning: 1, info: 2 };

/** Groups firing alerts by database. A database with none has no entry. */
export function summarizeAlerts(alerts: Alert[]): Record<string, AlertSummary> {
  const summaries: Record<string, AlertSummary> = {};

  for (const alert of alerts) {
    if (!alert.databaseId) {
      continue;
    }

    const current = summaries[alert.databaseId];
    if (!current) {
      summaries[alert.databaseId] = { total: 1, worst: alert.severity, worstCount: 1 };
    } else if (rank[alert.severity] < rank[current.worst]) {
      summaries[alert.databaseId] = {
        total: current.total + 1,
        worst: alert.severity,
        worstCount: 1,
      };
    } else {
      summaries[alert.databaseId] = {
        ...current,
        total: current.total + 1,
        worstCount: current.worstCount + (alert.severity === current.worst ? 1 : 0),
      };
    }
  }

  return summaries;
}

/** True when the environment relayed fewer alerts than are firing in it. */
export function environmentCutAlerts(environment: AlertEnvironment | undefined): boolean {
  return environment !== undefined && environment.total > environment.shown;
}
