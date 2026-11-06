import { BellOff, Clock } from "lucide-react";
import { Link } from "react-router";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatRelativeTime } from "@/lib/format";
import { useNow } from "@/lib/use-now";
import { alertActionTarget } from "../actions";
import { useEnvironmentTeam } from "../use-environment-team";
import type { AlertEnvironment } from "../types";

/**
 * One line for an environment with no current alerts reading. It says why and
 * offers the one action that fixes it, so an environment nobody can read never
 * looks like one with nothing wrong.
 */
export function EnvironmentBanner({ environment }: { environment: AlertEnvironment }) {
  const stale = environment.state === "stale";
  const Icon = stale ? Clock : BellOff;
  const { action } = environment;
  const team = useEnvironmentTeam(environment.name);
  const lastRead = formatRelativeTime(environment.readAt ?? undefined, useNow());

  return (
    <div
      role="status"
      className="flex items-start justify-between gap-4 rounded-lg border border-border bg-card px-4 py-3"
    >
      <div className="flex min-w-0 items-start gap-3">
        <Icon
          className="mt-0.5 size-4 shrink-0 text-warning"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-medium">{environment.name}</span>
            <Badge variant="warning">{stale ? "Stale" : "Off"}</Badge>
            {lastRead ? (
              <span className="text-xs text-muted-foreground">Last read {lastRead}</span>
            ) : null}
          </div>
          <p className="text-sm text-muted-foreground">{environment.reason}</p>
        </div>
      </div>
      {action ? (
        <Button asChild variant="outline" size="sm">
          <Link to={alertActionTarget(action, environment.name, undefined, team).to}>
            {action.label}
          </Link>
        </Button>
      ) : null}
    </div>
  );
}
