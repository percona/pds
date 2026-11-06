import { CircleAlert, Info, TriangleAlert } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { BadgeProps } from "@/components/ui/badge";
import type { AlertSeverity } from "../types";

export const severityStyles: Record<
  AlertSeverity,
  { icon: LucideIcon; variant: BadgeProps["variant"]; label: string }
> = {
  critical: { icon: CircleAlert, variant: "destructive", label: "Critical" },
  warning: { icon: TriangleAlert, variant: "warning", label: "Warning" },
  info: { icon: Info, variant: "muted", label: "Info" },
};

/** A severity as an icon plus text, never color alone. */
export function AlertSeverityBadge({ severity }: { severity: AlertSeverity }) {
  const { icon: Icon, variant, label } = severityStyles[severity] ?? severityStyles.warning;

  return (
    <Badge variant={variant}>
      <Icon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
      {label}
    </Badge>
  );
}
