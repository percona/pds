import type { StatusKind } from "@/features/databases";
import type { EnvironmentState, Lifecycle } from "./types";

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
