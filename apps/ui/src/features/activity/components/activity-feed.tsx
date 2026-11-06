import { CircleAlert, PlugZap, ScrollText } from "lucide-react";
import type { MouseEvent, ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TeamLabel } from "@/features/teams";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { clientIcon, describeAction, describeOutcome, describeSource } from "../actions";
import { useActivity } from "../hooks/use-activity";
import type { ActivityEntry } from "../types";

type ActivityFeedProps = {
  /** Set to scope the feed to one database. Unset shows the whole environment. */
  databaseId?: string;
  live: boolean;
  onLiveChange: (live: boolean) => void;
};

/**
 * What the control plane did, newest first.
 *
 * Five columns is the table budget, so a row says when it happened, what was
 * asked for, which database or connection it touched, who asked, and how it
 * ended. The detail rides under the action rather than claiming a sixth column,
 * and the action itself opens the full timeline for that change.
 */
export function ActivityFeed({ databaseId, live, onLiveChange }: ActivityFeedProps) {
  const { entries, error, isPending, isFetching, retry } = useActivity({ databaseId, live });

  if (isPending) {
    return <ActivityFeedSkeleton />;
  }

  if (error) {
    if (error.code === "ErrNotImplemented") {
      return (
        <EmptyCard
          title="The activity log is not enabled"
          detail={error.message}
          icon={<PlugZap className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        />
      );
    }

    return (
      <EmptyCard
        title="Activity could not be read"
        detail={error.message}
        icon={<CircleAlert className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        action={
          <Button variant="outline" loading={isFetching} onClick={() => void retry()}>
            Try again
          </Button>
        }
      />
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-4">
        <p className="text-sm text-muted-foreground">
          {live ? "Updating as changes happen." : "Paused. New changes are not shown."}
        </p>
        <Button
          variant="outline"
          size="sm"
          aria-pressed={live}
          className={live ? "bg-accent" : undefined}
          onClick={() => onLiveChange(!live)}
        >
          Live
        </Button>
      </div>

      {entries.length === 0 ? (
        <EmptyCard
          title="Nothing has changed yet"
          detail={
            databaseId
              ? "Scaling, backing up, or restoring this database will show up here."
              : "Creating a database, backing one up, or changing a connection will show up here."
          }
          icon={<ScrollText className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        />
      ) : (
        <Card className="overflow-hidden">
          <div className="overflow-x-auto">
            <Table data-testid="activity-feed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>Time</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>Outcome</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {entries.map((entry) => (
                  <ActivityRow key={entry.id} entry={entry} />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </div>
  );
}

function ActivityRow({ entry }: { entry: ActivityEntry }) {
  const navigate = useNavigate();
  const action = describeAction(entry.action);
  const outcome = describeOutcome(entry.outcome, entry.action);
  const source = describeSource(entry.client, entry.agent);
  const SourceIcon = clientIcon(entry.client);
  const ActionIcon = action.icon;
  const OutcomeIcon = outcome.icon;

  // A failure explains itself under the action; a success shows what changed.
  const detail = entry.outcome === "failed" ? (entry.error ?? entry.detail) : entry.detail;

  /**
   * The whole row opens the change. The action stays a real link underneath,
   * so keyboard focus, middle click, and open in a new tab all still work;
   * this only widens the target, because a row that opens something is what a
   * person clicks first and the action label alone is a small thing to hit.
   */
  function openDetail(event: MouseEvent<HTMLTableRowElement>) {
    // The link inside the row navigates on its own.
    if (event.target instanceof Element && event.target.closest("a, button")) {
      return;
    }

    // Someone selecting a backup id to copy it is not asking to navigate.
    if (window.getSelection()?.toString()) {
      return;
    }

    void navigate(`/activity/${entry.id}`);
  }

  return (
    <TableRow
      data-testid="activity-row"
      data-outcome={entry.outcome}
      className="cursor-pointer"
      onClick={openDetail}
    >
      <TableCell
        className="whitespace-nowrap font-mono text-xs"
        title={formatTimestamp(entry.at) ?? undefined}
        data-testid="activity-time"
      >
        {formatRelativeTime(entry.at) ?? "Not reported"}
      </TableCell>
      <TableCell>
        {/*
          The row says a backup was asked for and accepted. Whether it then ran
          for four minutes and failed is only readable behind this link, which
          follows the object the action produced. The action is the link rather
          than a sixth column, because the table budget is five.
        */}
        <Link
          to={`/activity/${entry.id}`}
          className="flex items-center gap-2 rounded-md focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          data-testid="activity-detail-link"
        >
          <ActionIcon
            className="size-4 shrink-0 text-muted-foreground"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <span className="font-medium text-primary hover:underline">{action.label}</span>
        </Link>
        {detail ? (
          <p className="mt-1 max-w-md text-xs text-muted-foreground" data-testid="activity-detail">
            {detail}
          </p>
        ) : null}
      </TableCell>
      <TableCell className="space-y-1">
        <p className="font-mono text-xs">{entry.target}</p>
        {entry.team ? <TeamLabel name={entry.team} testId="activity-team" /> : null}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {entry.actor}
        {source ? (
          <p className="mt-1 flex items-center gap-1.5 text-xs" data-testid="activity-client">
            <SourceIcon className="size-3.5 shrink-0" strokeWidth={1.5} aria-hidden="true" />
            {source}
          </p>
        ) : null}
      </TableCell>
      <TableCell>
        <Badge variant={outcome.variant} data-testid="activity-outcome">
          <OutcomeIcon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
          {outcome.label}
        </Badge>
      </TableCell>
    </TableRow>
  );
}

// ActivityFeedSkeleton matches the loaded row layout so the page does not shift.
function ActivityFeedSkeleton() {
  return (
    <Card className="overflow-hidden" data-testid="activity-loading">
      <div className="divide-y divide-border">
        {[0, 1, 2, 3].map((row) => (
          <div key={row} className="flex items-center gap-4 px-3 py-3">
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-4 flex-1" />
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-5 w-24 rounded-full" />
          </div>
        ))}
      </div>
    </Card>
  );
}

type EmptyCardProps = {
  title: string;
  detail: string;
  icon: ReactNode;
  action?: ReactNode;
};

function EmptyCard({ title, detail, icon, action }: EmptyCardProps) {
  return (
    <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
      {icon}
      <div className="space-y-1">
        <p className="text-sm font-medium">{title}</p>
        <p className="max-w-md text-sm text-muted-foreground">{detail}</p>
      </div>
      {action}
    </Card>
  );
}
