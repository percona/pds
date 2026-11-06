import type { Database, DatabasePhase } from "./types";

/** The five pills the design system ships. */
export const statusKinds = ["ready", "creating", "degraded", "failed", "metrics-pending"] as const;

export type StatusKind = (typeof statusKinds)[number];

export type DatabaseStatus = {
  kind: StatusKind;
  label: string;
};

/**
 * Every phase the adapter can report, mapped onto a shipped pill. The kind
 * picks the pill; the label says which work is running, so a scale in flight
 * does not read as a create.
 */
const phaseStatus: Record<DatabasePhase, DatabaseStatus> = {
  pending: { kind: "creating", label: "Queued" },
  provisioning: { kind: "creating", label: "Creating volumes" },
  initializing: { kind: "creating", label: "Starting database" },
  ready: { kind: "ready", label: "Ready" },
  updating: { kind: "creating", label: "Updating" },
  deleting: { kind: "creating", label: "Deleting" },
  failed: { kind: "failed", label: "Failed" },
  restoring: { kind: "creating", label: "Restoring" },
  suspending: { kind: "degraded", label: "Suspending" },
  suspended: { kind: "degraded", label: "Suspended" },
  resuming: { kind: "creating", label: "Resuming" },
  unknown: { kind: "degraded", label: "Degraded" },
};

export function databaseStatus(database: Database): DatabaseStatus {
  if (database.status === "ready" && database.connection == null) {
    return { kind: "creating", label: "Publishing connection" };
  }

  return phaseStatus[database.status] ?? phaseStatus.unknown;
}

/** True while the operator is still working, which is what keeps a poll alive. */
export function isSettled(database: Database): boolean {
  if (database.status === "ready" && database.connection == null) {
    return false;
  }

  const { kind } = databaseStatus(database);

  return kind === "ready" || kind === "failed" || kind === "degraded";
}
