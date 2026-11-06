import { CircleAlert, ExternalLink, MessageSquare, PlugZap } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { useReplyToTicket } from "../hooks/use-reply-to-ticket";
import { useTicket } from "../hooks/use-ticket";
import { publicSupportPortalUrl } from "../portal";
import { autoCloseDays, describeTicketState } from "../ticket-state";
import type { SupportTicket, TicketComment } from "../types";

/** One ticket with its state and its public comments. */
export function TicketDetail({ ticketNumber }: { ticketNumber: string }) {
  const { data, error, isPending, isFetching, refetch } = useTicket(ticketNumber);

  if (isPending) {
    return <TicketSkeleton />;
  }

  if (error) {
    if (error.code === "ErrNotImplemented") {
      return (
        <EmptyCard
          title="Support is not connected"
          detail={error.message}
          icon={<PlugZap className="size-5 text-muted-foreground" strokeWidth={1.5} />}
          action={
            <Button variant="outline" asChild>
              <a href={publicSupportPortalUrl} target="_blank" rel="noreferrer">
                Open the support portal
              </a>
            </Button>
          }
        />
      );
    }

    return (
      <EmptyCard
        title="This ticket could not be read"
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

  const state = describeTicketState(data.state);
  const StateIcon = state.icon;
  const externalUrl = data.externalUrl;
  const ticketURL = externalUrl ?? data.portalUrl ?? publicSupportPortalUrl;
  const ticketLinkLabel = externalUrl ? "Open in ServiceNow" : "Open in the support portal";

  return (
    <div className="space-y-6" data-testid="ticket-detail">
      <Card className="space-y-4 px-5 py-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <p className="font-mono text-sm text-muted-foreground">{data.number ?? ticketNumber}</p>
            <h2 className="text-base font-semibold">{data.subject ?? "No subject"}</h2>
          </div>
          <Badge variant={state.variant} data-testid="ticket-state">
            <StateIcon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
            {state.label}
          </Badge>
        </div>

        {state.awaitingCustomer ? (
          <p data-testid="ticket-awaiting-customer" className="text-sm">
            Percona is waiting for your reply. A ticket with no reply closes after {autoCloseDays}{" "}
            days.
          </p>
        ) : null}

        <dl className="grid gap-4 sm:grid-cols-3">
          <Fact label="Opened" value={when(data.openedAt)} title={formatTimestamp(data.openedAt)} />
          <Fact
            label="Last update"
            value={when(data.updatedAt)}
            title={formatTimestamp(data.updatedAt)}
          />
          <Fact label="Priority" value={data.priority} />
          <Fact label="Account" value={data.account} />
          <Fact label="Contact" value={data.contact} />
          <Fact label="Technology" value={data.technology} />
          {data.resolvedAt ? (
            <Fact
              label="Resolved"
              value={when(data.resolvedAt)}
              title={formatTimestamp(data.resolvedAt)}
            />
          ) : null}
        </dl>

        <a
          href={ticketURL}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
        >
          {ticketLinkLabel}
          <ExternalLink className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
        </a>
      </Card>

      {data.description ? (
        <Card className="space-y-3 px-5 py-5">
          <h3 className="text-base font-semibold">What was reported</h3>
          <pre
            data-testid="ticket-description"
            className="overflow-x-auto font-mono text-xs whitespace-pre-wrap text-muted-foreground"
          >
            {data.description}
          </pre>
        </Card>
      ) : null}

      <ReplyComposer ticketNumber={data.number ?? ticketNumber} />
      <Comments ticket={data} />
    </div>
  );
}

function ReplyComposer({ ticketNumber }: { ticketNumber: string }) {
  const fieldId = useId();
  const [body, setBody] = useState("");
  const reply = useReplyToTicket(ticketNumber);
  const sendable = body.trim() !== "";

  return (
    <Card className="space-y-3 px-5 py-5">
      <div className="space-y-1">
        <h3 className="text-base font-semibold">Reply</h3>
        <p className="text-sm text-muted-foreground">
          Connection strings, passwords, and tokens are removed before the reply is sent.
        </p>
      </div>
      <form
        className="space-y-3"
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          reply.mutate(
            { body: body.trim() },
            {
              onSuccess: () => {
                setBody("");
                toast.success("Reply sent");
              },
            },
          );
        }}
      >
        <div className="space-y-1.5">
          <Label htmlFor={fieldId}>Reply</Label>
          <textarea
            id={fieldId}
            rows={5}
            value={body}
            onChange={(event) => setBody(event.target.value)}
            disabled={reply.isPending}
            className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-xs text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
          />
        </div>
        {reply.error ? (
          <div
            role="alert"
            data-testid="ticket-reply-error"
            className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
          >
            <CircleAlert
              className="mt-0.5 size-4 shrink-0 text-destructive"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <span>{reply.error.message} Your reply is still here, so send it again.</span>
          </div>
        ) : null}
        <Button type="submit" disabled={!sendable} loading={reply.isPending}>
          {reply.isPending ? "Sending..." : "Send reply"}
        </Button>
      </form>
    </Card>
  );
}

function Comments({ ticket }: { ticket: SupportTicket }) {
  const comments = ticket.comments ?? [];

  if (comments.length === 0) {
    return (
      <EmptyCard
        title="No comments yet"
        detail="Percona support posts replies here and in the portal."
        icon={<MessageSquare className="size-5 text-muted-foreground" strokeWidth={1.5} />}
      />
    );
  }

  return (
    <Card className="overflow-hidden">
      <h3 className="px-5 pt-5 pb-4 text-base font-semibold">Comments</h3>
      <ul className="divide-y divide-border border-t border-border" data-testid="ticket-comments">
        {comments.map((comment, index) => (
          <Comment key={`${comment.at ?? index}-${comment.author ?? ""}`} comment={comment} />
        ))}
      </ul>
    </Card>
  );
}

function Comment({ comment }: { comment: TicketComment }) {
  return (
    <li className="space-y-3 px-5 py-4" data-testid="ticket-comment">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-medium text-foreground">
            {comment.author ?? "Author unavailable"}
          </span>
          {comment.label ? (
            <span className="border-l border-border pl-2" data-testid="ticket-comment-label">
              {comment.label}
            </span>
          ) : null}
        </div>
        {comment.at ? (
          <time
            className="font-mono"
            dateTime={comment.at}
            title={formatTimestamp(comment.at) ?? undefined}
          >
            {when(comment.at)}
          </time>
        ) : null}
      </div>
      <p className="text-sm leading-6 whitespace-pre-wrap" data-testid="ticket-comment-body">
        {comment.body}
      </p>
    </li>
  );
}

type FactProps = {
  label: string;
  value: string | undefined;
  title?: string | null;
};

function Fact({ label, value, title }: FactProps) {
  return (
    <div className="space-y-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-sm" title={title ?? undefined}>
        {value ?? "Not reported"}
      </dd>
    </div>
  );
}

// when reads a timestamp the way the rest of the app does, and states plainly
// when there is none rather than rendering an empty cell.
function when(iso: string | undefined): string | undefined {
  return formatRelativeTime(iso) ?? undefined;
}

function TicketSkeleton() {
  return (
    <div className="space-y-6" data-testid="ticket-detail-loading">
      <Card className="space-y-4 px-5 py-5">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-2">
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-5 w-64" />
          </div>
          <Skeleton className="h-5 w-28 rounded-full" />
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          {[0, 1, 2, 3, 4, 5].map((cell) => (
            <div key={cell} className="space-y-2">
              <Skeleton className="h-3 w-16" />
              <Skeleton className="h-4 w-24" />
            </div>
          ))}
        </div>
      </Card>
      <Card className="space-y-3 px-5 py-5">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-3/4" />
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
