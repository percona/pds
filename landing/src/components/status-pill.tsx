import { CircleCheck, CircleDashed, Loader } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export type StatusKind = "ready" | "creating" | "pending";

const pillStyles: Record<
  StatusKind,
  { icon: LucideIcon; variant: BadgeProps["variant"]; pulse?: boolean }
> = {
  ready: { icon: CircleCheck, variant: "success" },
  creating: { icon: Loader, variant: "muted", pulse: true },
  pending: { icon: CircleDashed, variant: "muted" },
};

/** The console's status pill: always icon plus text, never color alone. */
export function StatusPill({
  kind,
  label,
  className,
}: {
  kind: StatusKind;
  label: string;
  className?: string;
}) {
  const style = pillStyles[kind];
  const Icon = style.icon;

  return (
    <Badge
      variant={style.variant}
      className={cn("shrink-0", style.pulse && "animate-soft-pulse", className)}
    >
      <Icon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
      {label}
    </Badge>
  );
}
