import { Loader2 } from "lucide-react";

/** Shown while the browser leaves for the app that asked to sign in. */
export function ReturningStatus() {
  return (
    <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 className="size-4 animate-spin" strokeWidth={1.5} aria-hidden="true" />
      Returning to the app
    </p>
  );
}
