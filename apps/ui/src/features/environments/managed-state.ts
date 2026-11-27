import type { StatusKind } from "@/features/databases";
import type { Environment, EnvironmentState, Lifecycle } from "./types";

type ManagedPill = { kind: StatusKind; label: string };

/** The pill a managed environment shows for its state: an icon and words, never color alone. */
export function managedPill(state: EnvironmentState | undefined): ManagedPill {
  switch (state) {
    case "creating":
      return { kind: "creating", label: "Creating" };
    case "deleting":
      return { kind: "creating", label: "Deleting" };
    case "failed":
      return { kind: "failed", label: "Failed" };
    case "lost":
      return { kind: "failed", label: "Lost" };
    case "degraded":
      return { kind: "degraded", label: "Degraded" };
    default:
      return { kind: "ready", label: "Ready" };
  }
}

/**
 * A joined environment appears the moment its token is minted, so one that is
 * unreachable and has never reported is most likely still being built, not one
 * that failed.
 */
export function joiningEnvironment(environment: Environment): boolean {
  return (
    environment.origin !== "managed" &&
    environment.status === "unreachable" &&
    !environment.lastSeen
  );
}

/**
 * Whether there is a running environment to monitor or reach: a joined one
 * that has reported, or a managed one that PDS finished building.
 */
export function operableEnvironment(environment: Environment): boolean {
  if (environment.origin === "managed") {
    return environment.state === "ready";
  }

  return !joiningEnvironment(environment);
}

/** The pill an environment shows on the list and on its own page. */
export function environmentPill(environment: Environment): ManagedPill {
  if (environment.origin === "managed") {
    return managedPill(environment.state);
  }
  if (joiningEnvironment(environment)) {
    return { kind: "creating", label: "Joining" };
  }
  if (environment.status === "unreachable") {
    return { kind: "degraded", label: "Unreachable" };
  }

  return { kind: "ready", label: "Connected" };
}

export type LifecycleNote = { kind: "update" | "drift"; label: string };

/** What a managed environment's row says about its upgrades and drift, in words. */
export function lifecycleNotes(lifecycle: Lifecycle | undefined): LifecycleNote[] {
  if (!lifecycle) {
    return [];
  }
  const notes: LifecycleNote[] = [];
  if (
    lifecycle.pendingChartVersion ||
    lifecycle.templateOutOfDate ||
    lifecycle.kubernetesUpgradeAvailable
  ) {
    notes.push({ kind: "update", label: "Update available" });
  }
  if (lifecycle.drift.length > 0) {
    notes.push({ kind: "drift", label: "Changed outside PDS" });
  }

  return notes;
}
