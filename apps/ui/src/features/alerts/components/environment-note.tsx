import { Info } from "lucide-react";

/**
 * A quiet line above an alert list: what an environment cut from it, so a short
 * list is not read as the whole, or that the latest read failed (role "status").
 */
export function EnvironmentNote({ note, role }: { note: string; role?: "status" }) {
  return (
    <p role={role} className="flex items-start gap-2 text-sm text-muted-foreground">
      <Info className="mt-0.5 size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
      {note}
    </p>
  );
}
