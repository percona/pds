import { CircleAlert, PlugZap, ScrollText } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useId, type MouseEvent, type ReactNode } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
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
import {
  clientIcon,
  describeAction,
  describeOutcome,
  describeSource,
  shownOutcome,
} from "../actions";
import { activityKey, useActivity } from "../hooks/use-activity";
import type { ActivityEntry, ActivityPage } from "../types";

type ActivityFeedProps = {
  /** Set to scope the feed to one database. Unset shows the whole environment. */
  databaseId?: string;
  live: boolean;
  onLiveChange: (live: boolean) => void;
  /**
   * Shows the action, outcome, made by and database filters, kept in the URL.
   * Only the database filter is a query; the others narrow the loaded rows,
   * because the control plane filters by nothing else.
   */
  filterable?: boolean;
};

type Filters = { action: string; outcome: string; by: string; database: string };

const filterKeys = ["action", "outcome", "by", "database"] as const;

const outcomeOptions = [
  { value: "succeeded", label: "Succeeded" },
  { value: "accepted", label: "Accepted" },
  { value: "failed", label: "Failed" },
];

const madeByOptions = [
  { value: "people", label: "People in the console" },
  { value: "chat", label: "Chat" },
  { value: "mcp", label: "AI agents" },
];

function matches(entry: ActivityEntry, filters: Filters): boolean {
  if (filters.action && entry.action !== filters.action) {
    return false;
  }
  if (filters.outcome && shownOutcome(entry.outcome, entry.action) !== filters.outcome) {
    return false;
  }
  if (filters.by === "people") {
    return !entry.client;
  }

  return !filters.by || entry.client === filters.by;
}

/**
 * What the control plane did, newest first.
 *
 * Five columns is the table budget, so a row says when it happened, what was
 * asked for, which database or connection it touched, who asked, and how it
 * ended. The detail rides under the action rather than claiming a sixth column,
 * and the action itself opens the full timeline for that change.
 */
export function ActivityFeed({
  databaseId,
  live,
  onLiveChange,
  filterable = false,
}: ActivityFeedProps) {
  const queryClient = useQueryClient();
  const [params, setParams] = useSearchParams();
  const filters: Filters = {
    action: filterable ? (params.get("action") ?? "") : "",
    outcome: filterable ? (params.get("outcome") ?? "") : "",
    by: filterable ? (params.get("by") ?? "") : "",
    database: filterable ? (params.get("database") ?? "") : "",
  };
  const { entries, error, isPending, isFetching, retry } = useActivity({
    databaseId: databaseId ?? (filters.database || undefined),
    live,
  });

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

  const filtered = filterKeys.some((key) => filters[key] !== "");
  const shown = entries.filter((entry) => matches(entry, filters));
  // A database filter narrows the read itself, so the choices also come from
  // the unfiltered read kept in the cache: picking one database must not make
  // every other one disappear from the list.
  const databaseOptions = filterable
    ? unique([
        ...(queryClient.getQueryData<ActivityPage>(activityKey())?.entries ?? []).map(
          (entry) => entry.databaseId,
        ),
        ...entries.map((entry) => entry.databaseId),
        filters.database,
      ])
    : [];

  function setFilter(key: keyof Filters, value: string) {
    const next = new URLSearchParams(params);
    if (value) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    setParams(next);
  }

  function clearFilters() {
    const next = new URLSearchParams(params);
    for (const key of filterKeys) {
      next.delete(key);
    }
    setParams(next);
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

      {filterable && (entries.length > 0 || filtered) ? (
        <FilterBar
          filters={filters}
          actions={unique([...entries.map((entry) => entry.action), filters.action])}
          databases={databaseOptions}
          onChange={setFilter}
          onClear={filtered ? clearFilters : undefined}
        />
      ) : null}

      {entries.length === 0 && !filtered ? (
        <EmptyCard
          title="Nothing has changed yet"
          detail={
            databaseId
              ? "Scaling, backing up, or restoring this database will show up here."
              : "Creating a database, backing one up, or changing a connection will show up here."
          }
          icon={<ScrollText className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        />
      ) : shown.length === 0 ? (
        <EmptyCard
          title="No changes match these filters"
          detail="Change a filter to see the rest."
          icon={<ScrollText className="size-5 text-muted-foreground" strokeWidth={1.5} />}
          action={
            <Button variant="outline" size="sm" onClick={clearFilters}>
              Clear filters
            </Button>
          }
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
                {shown.map((entry) => (
                  <ActivityRow key={entry.id} entry={entry} />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}

      {filterable && entries.length > 0 ? (
        <p className="text-xs text-muted-foreground" data-testid="activity-count">
          Showing <span className="font-mono">{shown.length}</span> of{" "}
          <span className="font-mono">{entries.length}</span> loaded changes. The database filter
          asks the control plane. The others narrow what is already loaded.
        </p>
      ) : null}
    </div>
  );
}

type FilterBarProps = {
  filters: Filters;
  actions: string[];
  databases: string[];
  onChange: (key: keyof Filters, value: string) => void;
  onClear?: () => void;
};

function FilterBar({ filters, actions, databases, onChange, onClear }: FilterBarProps) {
  return (
    <div className="flex flex-wrap items-end gap-4" data-testid="activity-filters">
      <Filter label="Action" value={filters.action} onChange={(v) => onChange("action", v)}>
        <option value="">All actions</option>
        {actions.map((action) => (
          <option key={action} value={action}>
            {describeAction(action).label}
          </option>
        ))}
      </Filter>
      <Filter label="Outcome" value={filters.outcome} onChange={(v) => onChange("outcome", v)}>
        <option value="">Any outcome</option>
        {outcomeOptions.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </Filter>
      <Filter label="Made by" value={filters.by} onChange={(v) => onChange("by", v)}>
        <option value="">Anyone</option>
        {madeByOptions.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </Filter>
      <Filter label="Database" value={filters.database} onChange={(v) => onChange("database", v)}>
        <option value="">All databases</option>
        {databases.map((database) => (
          <option key={database} value={database}>
            {database}
          </option>
        ))}
      </Filter>
      {onClear ? (
        <Button variant="ghost" size="sm" onClick={onClear}>
          Clear filters
        </Button>
      ) : null}
    </div>
  );
}

type FilterProps = {
  label: string;
  value: string;
  onChange: (value: string) => void;
  children: ReactNode;
};

function Filter({ label, value, onChange, children }: FilterProps) {
  const id = useId();

  // The label names the control and nothing else. Wrapping the select would
  // fold every option into the accessible name, so "Database" also matched
  // an action whose label contains that word.
  return (
    <div className="space-y-1.5 text-sm font-medium">
      <label htmlFor={id} className="block">
        {label}
      </label>
      <NativeSelect id={id} value={value} onChange={(event) => onChange(event.target.value)}>
        {children}
      </NativeSelect>
    </div>
  );
}

function unique(values: (string | undefined)[]): string[] {
  return [...new Set(values.filter((value): value is string => Boolean(value)))];
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
