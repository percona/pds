import { CircleAlert, CircleCheck, CircleDashed, CircleX, Loader } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { BadgeProps } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { StatusKind } from "../status";

type PillStyle = {
  icon: LucideIcon;
  variant: BadgeProps["variant"];
  /** The soft pulse is the only perpetual animation in the product. */
  pulse?: boolean;
};

const pillStyles: Record<StatusKind, PillStyle> = {
  ready: { icon: CircleCheck, variant: "success" },
  creating: { icon: Loader, variant: "muted", pulse: true },
  degraded: { icon: CircleAlert, variant: "warning" },
  failed: { icon: CircleX, variant: "destructive" },
  "metrics-pending": { icon: CircleDashed, variant: "muted" },
};

type StatusPillProps = {
  kind: StatusKind;
  label: string;
  className?: string;
};

/**
 * A status pill. Always icon plus text, so state never lands on color alone.
 * The pulse collapses to a still icon under prefers-reduced-motion through the
 * global rule in index.css.
 */
export function StatusPill({ kind, label, className }: StatusPillProps) {
  const style = pillStyles[kind];
  const Icon = style.icon;

  return (
    <Badge
      variant={style.variant}
      data-status={kind}
      data-testid="status-pill"
      className={cn("shrink-0", style.pulse && "animate-soft-pulse", className)}
    >
      <Icon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
      {label}
    </Badge>
  );
}

/** The pill that stands where a chart will be until PMM reports a target. */
export function MetricsPendingPill({ className }: { className?: string }) {
  return <StatusPill kind="metrics-pending" label="Metrics pending" className={className} />;
}
