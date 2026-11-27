import { ScrollText } from "lucide-react";
import { Link } from "react-router";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  describeAction,
  describeOutcome,
  useActivity,
  type ActivityEntry,
} from "@/features/activity";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";

const shown = 5;

/**
 * The last few recorded changes. A control plane built without the activity
 * log answers 501, and then there is nothing to show and nowhere to link, so
 * the section is left out.
 */
export function RecentActivity() {
  const activity = useActivity({ live: false });

  if (activity.error?.status === 501) {
    return null;
  }

  return (
    <Card className="overflow-hidden" role="region" aria-labelledby="activity-heading">
      <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-4">
        <h2 id="activity-heading" className="text-base font-semibold">
          Recent activity
        </h2>
        <Link
          to="/activity"
          className="rounded-md text-xs font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          View all
        </Link>
      </div>
      {activity.isPending ? (
        <div className="space-y-3 px-5 py-4" data-testid="activity-loading">
          {[0, 1, 2].map((row) => (
            <Skeleton key={row} className="h-4 w-full" />
          ))}
        </div>
      ) : activity.error ? (
        <p className="px-5 py-4 text-sm text-muted-foreground">
          Activity could not be read. {activity.error.message}
        </p>
      ) : activity.entries.length === 0 ? (
        <div className="flex flex-col items-center gap-2 px-5 py-10 text-center">
          <ScrollText
            className="size-5 text-muted-foreground"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <p className="text-sm text-muted-foreground">Nothing has changed yet.</p>
        </div>
      ) : (
        <ul className="divide-y divide-border">
          {activity.entries.slice(0, shown).map((entry) => (
            <ActivityLine key={entry.id} entry={entry} />
          ))}
        </ul>
      )}
    </Card>
  );
}

/** The environment a database id starts with, when the entry names a database. */
function environmentOf(entry: ActivityEntry): string | undefined {
  const slash = entry.databaseId?.indexOf("/") ?? -1;

  return slash > 0 ? entry.databaseId?.slice(0, slash) : undefined;
}

function ActivityLine({ entry }: { entry: ActivityEntry }) {
  const action = describeAction(entry.action);
  const outcome = describeOutcome(entry.outcome, entry.action);
  const environment = environmentOf(entry);
  const who = entry.agent ? `${entry.agent} for ${entry.actor}` : entry.actor;
  const when = formatRelativeTime(entry.at);

  return (
    <li className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-3 px-5 py-3">
      <action.icon
        className="mt-0.5 size-4 text-muted-foreground"
        strokeWidth={1.5}
        aria-hidden="true"
      />
      <div className="min-w-0 space-y-0.5">
        <p className="text-sm break-words">
          <Link
            to={`/activity/${entry.id}`}
            className="rounded-md font-medium hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >
            {action.label}
          </Link>{" "}
          <span className="font-mono">{entry.target}</span>
          {environment && environment !== entry.target ? (
            <span className="text-muted-foreground">
              {" "}
              on <span className="font-mono">{environment}</span>
            </span>
          ) : null}
        </p>
        <p className="text-xs text-muted-foreground">{who}</p>
      </div>
      <div className="flex flex-col items-end gap-1">
        <span
          className="font-mono text-xs whitespace-nowrap text-muted-foreground"
          title={formatTimestamp(entry.at) ?? undefined}
        >
          {when ?? ""}
        </span>
        {entry.outcome === "failed" ? (
          <Badge variant={outcome.variant}>
            <outcome.icon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
            {outcome.label}
          </Badge>
        ) : null}
      </div>
    </li>
  );
}
