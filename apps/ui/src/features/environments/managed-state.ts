import type { StatusKind } from "@/features/databases";
import type { EnvironmentState } from "./types";

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
