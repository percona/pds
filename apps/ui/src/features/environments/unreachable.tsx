import { CloudOff, Unplug } from "lucide-react";

/**
 * Shown when an environment has stopped answering.
 *
 * The two halves of the write path behave differently here and the copy has to
 * say which is which. Create, scale, backup schedule and delete are commits to
 * the hub's Git tree: they succeed now and Flux applies them when the spoke
 * comes back. Backup and restore ride the heartbeat, so there is nothing to
 * carry them and they are refused.
 *
 * An environment with no heartbeat at all is a different case. It is still being
 * built, nothing has ever pulled from its tree, so nothing can be saved for it
 * either.
 */
export function UnreachableBanner({ lastSeen }: { lastSeen?: string }) {
  return (
    <div
      role="status"
      className="flex items-start gap-3 rounded-lg border border-border bg-card px-4 py-3"
    >
      <CloudOff className="mt-0.5 size-4 text-muted-foreground" strokeWidth={1.5} aria-hidden />
      <div className="space-y-1">
        <p className="text-sm font-medium">This environment is unreachable</p>
        {lastSeen ? (
          <p className="text-sm text-muted-foreground">
            Last heartbeat {lastSeen}. Creates, scale changes, backup schedules and deletes are
            saved now and applied when it reconnects. Backups and restores need the environment, so
            they cannot run until it does.
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">
            No heartbeat yet, so this environment is still being set up. Nothing can be saved for it
            until it reports in.
          </p>
        )}
      </div>
    </div>
  );
}

export function UnreachableEmpty() {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-border bg-card px-6 py-12 text-center">
      <Unplug className="size-5 text-muted-foreground" strokeWidth={1.5} aria-hidden />
      <div className="space-y-1">
        <p className="text-sm font-medium">This environment is not reporting</p>
        <p className="text-sm text-muted-foreground">
          It has stopped reporting in, so its databases cannot be listed. They are still running.
          Contact Percona support if it does not come back.
        </p>
      </div>
    </div>
  );
}
