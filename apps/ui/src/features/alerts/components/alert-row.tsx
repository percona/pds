import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { useNow } from "@/lib/use-now";
import { alertTarget } from "../actions";
import { useEnvironmentTeam } from "../use-environment-team";
import type { Alert } from "../types";
import { AlertSeverityBadge } from "./alert-severity";

type AlertRowProps = {
  alert: Alert;
  /** When monitoring was last read for the alert's environment. */
  readAt?: string | null;
};

/** One firing alert with the action its rule names. */
export function AlertRow({ alert, readAt }: AlertRowProps) {
  const team = useEnvironmentTeam(alert.environment);
  const { to, state } = alertTarget(alert, team);
  const now = useNow();
  const firing = formatRelativeTime(alert.startsAt, now);
  const read = formatRelativeTime(readAt ?? undefined, now);

  return (
    <li className="flex items-start justify-between gap-4 px-4 py-3">
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium">{alert.summary}</p>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <AlertSeverityBadge severity={alert.severity} />
          <span className="font-mono text-xs text-muted-foreground">{alert.environment}</span>
          <span className="font-mono text-xs text-muted-foreground">{alert.rule}</span>
          <span
            className="text-xs text-muted-foreground"
            title={formatTimestamp(alert.startsAt) ?? undefined}
          >
            Firing since {firing ?? "an unknown time"}
          </span>
          <span
            className="text-xs text-muted-foreground"
            title={formatTimestamp(readAt ?? undefined) ?? undefined}
          >
            {read ? `Read ${read}` : "Not read yet"}
          </span>
        </div>
      </div>
      <Button asChild variant="outline" size="sm">
        <Link to={to} state={state}>
          {alert.action.label}
        </Link>
      </Button>
    </li>
  );
}
