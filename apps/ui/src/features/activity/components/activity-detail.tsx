import { CircleAlert, Info, PlugZap } from "lucide-react";
import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBackupSize, StatusPill } from "@/features/databases";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { clientIcon, describeAction, describeClient, describeOutcome } from "../actions";
import { useActivityDetail } from "../hooks/use-activity-detail";
import {
  describeOperationState,
  describeResourceKind,
  describeTimelineSource,
  isOperationActive,
  isWarning,
} from "../operation";
import type { TimelineEntry } from "../types";

/**
 * One recorded action and everything that happened after it.
 *
 * The feed can only say a backup was asked for and accepted. This is where a
 * backup that ran for four minutes and then died says so, and where a stuck
 * provision names the claim that never bound.
 */
export function ActivityDetailView({ id }: { id: string }) {
  const { data, error, isPending, isFetching, refetch } = useActivityDetail(id);

  if (isPending) {
    return <DetailSkeleton />;
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
        title="This entry could not be read"
        detail={error.message}
        icon={<CircleAlert className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        action={
          <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
            Try again
          </Button>
        }
      />
    );
  }

  const { event, resource, timeline, notes, truncated } = data;
  const action = describeAction(event.action);
  const outcome = describeOutcome(event.outcome, event.action);
  const client = describeClient(event.client);
  const ClientIcon = clientIcon(event.client);
  const ActionIcon = action.icon;
  const OutcomeIcon = outcome.icon;
  const running = isOperationActive(resource);

  return (
    <div className="space-y-6" data-testid="activity-detail">
      <Card className="space-y-4 px-5 py-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <p className="flex items-center gap-2 text-lg font-medium">
              <ActionIcon
                className="size-5 text-muted-foreground"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              {action.label}
            </p>
            <p className="font-mono text-sm text-muted-foreground">{event.target}</p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {resource ? <StatusPill {...describeOperationState(resource.state)} /> : null}
            <Badge variant={outcome.variant} data-testid="detail-outcome">
              <OutcomeIcon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
              Request {outcome.label.toLowerCase()}
            </Badge>
          </div>
        </div>

        <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Fact label="Requested">
            <span title={formatTimestamp(event.at) ?? undefined}>
              {formatRelativeTime(event.at) ?? "Not reported"}
            </span>
          </Fact>
          <Fact label="Requested by">{event.actor}</Fact>
          {client ? (
            <Fact label="Client">
              <span className="inline-flex items-center gap-1.5">
                <ClientIcon className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
                {client}
              </span>
            </Fact>
          ) : null}
          {event.agent ? <Fact label="Agent">{event.agent}</Fact> : null}
          {resource ? (
            <Fact label={describeResourceKind(resource.kind)}>{resource.name}</Fact>
          ) : null}
          {resource?.sizeBytes ? (
            <Fact label="Size">{formatBackupSize(resource.sizeBytes) ?? "Not reported"}</Fact>
          ) : null}
          {event.requestId ? <Fact label="Request id">{event.requestId}</Fact> : null}
        </dl>

        {resource?.message ? (
          <p
            className="rounded-md bg-muted px-3 py-2 font-mono text-xs"
            data-testid="detail-message"
          >
            {resource.message}
          </p>
        ) : null}
      </Card>

      {notes?.length ? (
        <div className="space-y-2">
          {notes.map((note) => (
            <p
              key={note}
              className="flex items-start gap-2 text-sm text-muted-foreground"
              data-testid="detail-note"
            >
              <Info className="mt-0.5 size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
              {note}
            </p>
          ))}
        </div>
      ) : null}

      <section className="space-y-3">
        <div className="flex items-center justify-between gap-4">
          <h2 className="text-sm font-medium">What happened</h2>
          <p className="text-sm text-muted-foreground">
            {running ? "Updating while this runs." : "This operation has finished."}
          </p>
        </div>

        <Card className="overflow-hidden">
          <ol className="divide-y divide-border" data-testid="activity-timeline">
            {timeline.map((entry, index) => (
              <TimelineRow key={`${entry.at}-${index}`} entry={entry} />
            ))}
          </ol>
        </Card>

        {truncated ? (
          <p className="text-sm text-muted-foreground">
            Older lines were dropped. This shows the most recent of a busy window.
          </p>
        ) : null}
      </section>
    </div>
  );
}

function TimelineRow({ entry }: { entry: TimelineEntry }) {
  const warning = isWarning(entry.type);

  return (
    <li
      className="flex flex-col gap-1 px-4 py-3 sm:flex-row sm:gap-4"
      data-testid="timeline-row"
      data-type={entry.type}
      data-source={entry.source}
    >
      <span
        className="w-40 shrink-0 font-mono text-xs text-muted-foreground"
        title={formatTimestamp(entry.at) ?? undefined}
      >
        {formatTimestamp(entry.at) ?? "Not reported"}
      </span>
      <div className="min-w-0 space-y-1">
        <p className="flex flex-wrap items-center gap-2 text-sm">
          {warning ? (
            <CircleAlert
              className="size-4 shrink-0 text-destructive"
              strokeWidth={1.5}
              aria-hidden="true"
            />
          ) : null}
          <span className="font-medium">
            {entry.reason || describeTimelineSource(entry.source)}
          </span>
          <Badge variant="outline">{describeTimelineSource(entry.source)}</Badge>
          {entry.count && entry.count > 1 ? (
            <Badge variant="muted" data-testid="timeline-count">
              <span className="font-mono">{entry.count}</span> times
            </Badge>
          ) : null}
        </p>
        {entry.message ? (
          <p className="font-mono text-xs break-words text-muted-foreground">{entry.message}</p>
        ) : null}
      </div>
    </li>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="font-mono text-sm break-words">{children}</dd>
    </div>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-6" data-testid="activity-detail-loading">
      <Card className="space-y-4 px-5 py-5">
        <Skeleton className="h-6 w-48" />
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {[0, 1, 2, 3].map((cell) => (
            <Skeleton key={cell} className="h-10" />
          ))}
        </div>
      </Card>
      <Card className="overflow-hidden">
        <div className="divide-y divide-border">
          {[0, 1, 2, 3].map((row) => (
            <div key={row} className="flex items-center gap-4 px-4 py-3">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-4 flex-1" />
            </div>
          ))}
        </div>
      </Card>
    </div>
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
