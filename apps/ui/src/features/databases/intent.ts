import type { MetricKind } from "@/features/monitoring";
import type { ScaleField } from "./scale";

/**
 * What an alert or a chat action asks the database page to do.
 *
 * Schedule is absent on purpose: chat performs that write itself, because it
 * has its own loading and error state.
 */
export type NavigableIntent =
  | { kind: "scale"; field: ScaleField }
  | { kind: "backup" }
  | { kind: "backups" }
  | { kind: "metrics"; metric: MetricKind }
  | { kind: "status" }
  | { kind: "support"; subject?: string; description?: string };
