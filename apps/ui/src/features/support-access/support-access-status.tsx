import { Loader, ShieldCheck } from "lucide-react";
import { formatTimeLeft, useNow } from "./countdown";
import { levelName } from "./copy";
import type { SupportSummary } from "@/features/environments";

/**
 * The one line under an environment's row while a window is opening or open:
 * an icon and text, so the state never rests on color. The countdown ticks
 * silently, because a live region that spoke every second would be noise.
 */
export function SupportAccessStatus({ summary }: { summary: SupportSummary }) {
  const now = useNow();
  const opening = summary.state === "opening";
  const Icon = opening ? Loader : ShieldCheck;

  return (
    <p
      role="group"
      aria-label="Support access status"
      className="flex items-center gap-2 px-5 pb-3 text-xs text-muted-foreground"
    >
      <Icon
        className={opening ? "size-4 shrink-0 animate-pulse" : "size-4 shrink-0 text-warning"}
        strokeWidth={1.5}
        aria-hidden="true"
      />
      {opening ? (
        <span>Support access starting</span>
      ) : (
        <span>
          Support access on, {levelName[summary.level].toLowerCase()},{" "}
          <span className="font-mono">{formatTimeLeft(summary.expiresAt, now)}</span> left
        </span>
      )}
    </p>
  );
}
