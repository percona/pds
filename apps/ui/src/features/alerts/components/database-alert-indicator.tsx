import { Badge } from "@/components/ui/badge";
import type { AlertSummary } from "../summary";
import { severityStyles } from "./alert-severity";

/**
 * The databases list's mark for a database with firing alerts: the highest
 * severity as an icon plus its count, and how many others are firing. A quiet
 * database gets nothing.
 */
export function DatabaseAlertIndicator({ summary }: { summary: AlertSummary | undefined }) {
  if (!summary) {
    return null;
  }

  const worst = severityStyles[summary.worst];
  const others = summary.total - summary.worstCount;

  return (
    <Badge variant={worst.variant}>
      <worst.icon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
      {summary.worstCount} {summary.worst}
      {others > 0 ? `, ${others} more` : ""}
    </Badge>
  );
}
