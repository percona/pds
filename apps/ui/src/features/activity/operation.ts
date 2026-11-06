import type { DatabaseStatus } from "@/features/databases";
import type { ActivityResource, TimelineSource, TimelineType } from "./types";

/**
 * States that mean the operation is still going. A detail polls while it is
 * in one of them and stops when it is not, so a finished backup costs nothing
 * to leave open on screen.
 */
const activeStates = new Set([
  "pending",
  "provisioning",
  "initializing",
  "updating",
  "deleting",
  "restoring",
  "resuming",
  "suspending",
  "running",
]);

export function isOperationActive(resource: ActivityResource | undefined): boolean {
  if (!resource) {
    return false;
  }

  return activeStates.has(resource.state);
}

/**
 * The pill for one operation state. Instance phases and backup states share
 * this map because they name the same lifecycle, and a reader should not have
 * to learn two vocabularies to read one timeline.
 */
const stateStatus: Record<string, DatabaseStatus> = {
  pending: { kind: "creating", label: "Queued" },
  provisioning: { kind: "creating", label: "Creating volumes" },
  initializing: { kind: "creating", label: "Starting database" },
  running: { kind: "creating", label: "Running" },
  updating: { kind: "creating", label: "Updating" },
  deleting: { kind: "creating", label: "Deleting" },
  restoring: { kind: "creating", label: "Restoring" },
  resuming: { kind: "creating", label: "Resuming" },
  suspending: { kind: "degraded", label: "Suspending" },
  suspended: { kind: "degraded", label: "Suspended" },
  ready: { kind: "ready", label: "Ready" },
  succeeded: { kind: "ready", label: "Succeeded" },
  failed: { kind: "failed", label: "Failed" },
  unknown: { kind: "degraded", label: "Unknown" },
};

export function describeOperationState(state: string): DatabaseStatus {
  return stateStatus[state] ?? { kind: "degraded", label: state || "Unknown" };
}

/** What the object is called on screen. */
const resourceLabels: Record<string, string> = {
  instance: "Database",
  backup: "Backup",
  restore: "Restore",
};

export function describeResourceKind(kind: string): string {
  return resourceLabels[kind] ?? kind;
}

/**
 * The label on a timeline line. Naming the source matters because the three
 * fail differently: the request is what we recorded, the resource is what the
 * platform reported about the object, and the cluster source is what Kubernetes saw.
 */
const sourceLabels: Record<TimelineSource, string> = {
  request: "Request",
  resource: "Platform",
  cluster: "Kubernetes",
};

export function describeTimelineSource(source: string): string {
  return sourceLabels[source as TimelineSource] ?? source;
}

export function isWarning(type: string): type is TimelineType {
  return type === "warning";
}
