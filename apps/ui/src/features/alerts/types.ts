export type AlertSeverity = "critical" | "warning" | "info";

/** What the console offers beside an alert or an unavailable environment. */
export type AlertActionKind =
  | "open_database"
  | "view_environment"
  | "scale_storage"
  | "view_metrics"
  | "scale_resources"
  | "retry_backup"
  | "set_up_monitoring"
  | "open_monitoring"
  | "update_connection";

export type AlertAction = {
  kind: AlertActionKind;
  label: string;
};

/** One firing alert, as `GET /api/alerts` returns it. */
export type Alert = {
  id: string;
  environment: string;
  /** Absent for an alert that maps to no database. */
  databaseId?: string;
  rule: string;
  severity: AlertSeverity;
  summary: string;
  startsAt: string;
  labels: Record<string, string>;
  action: AlertAction;
};

/**
 * Only live means monitoring was read on the newest report, so a live
 * environment with no alerts is the one case where nothing is wrong.
 */
export type AlertEnvironmentState = "live" | "stale" | "off";

export type AlertEnvironment = {
  name: string;
  state: AlertEnvironmentState;
  pending?: string;
  /** Set whenever the state is not live. */
  reason?: string;
  action?: AlertAction;
  /** Null when the environment never reported. */
  readAt: string | null;
  total: number;
  shown: number;
  /** Says what was cut when total is above shown. */
  note?: string;
};

export type AlertReport = {
  environments: AlertEnvironment[];
  alerts: Alert[];
  /** Firing critical alerts, which feeds the sidebar. */
  criticals: number;
};

/** `GET /api/databases/{environment}/{namespace}.{name}/alerts`. */
export type DatabaseAlertReport = {
  databaseId: string;
  /** The same entry `GET /api/alerts` carries for the database's environment. */
  environment: AlertEnvironment;
  alerts: Alert[];
};
