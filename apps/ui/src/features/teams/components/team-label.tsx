import { Users } from "lucide-react";
import { cn } from "@/lib/utils";

type TeamLabelProps = {
  name: string;
  testId?: string;
  className?: string;
};

/** Which Team a row belongs to, on the lists that cover every Team. */
export function TeamLabel({ name, testId, className }: TeamLabelProps) {
  return (
    <span
      title="Team"
      data-testid={testId}
      className={cn("flex items-center gap-1.5 text-xs text-muted-foreground", className)}
    >
      <Users className="size-3.5 shrink-0" strokeWidth={1.5} aria-hidden="true" />
      <span className="truncate">{name}</span>
    </span>
  );
}
