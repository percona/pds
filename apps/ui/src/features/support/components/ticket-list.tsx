import { CircleAlert, LifeBuoy, PlugZap } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
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
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { useSupportTickets } from "../hooks/use-support-tickets";
import { publicSupportPortalUrl } from "../portal";
import { describeTicketState } from "../ticket-state";
import type { SupportTicket } from "../types";

/**
 * The account's tickets. Five columns is the table budget, so a row says which
 * ticket it is, what it is about, where it stands, and when it last moved.
 */
export function TicketList() {
  const { data, error, isPending, isFetching, refetch } = useSupportTickets();

  if (isPending) {
    return <TicketListSkeleton />;
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
        title="Tickets could not be read"
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

  if (data.tickets.length === 0) {
    return (
      <EmptyCard
        title="No tickets on this account"
        detail="Open one with Contact support, or from a database that is misbehaving."
        icon={<LifeBuoy className="size-5 text-muted-foreground" strokeWidth={1.5} />}
      />
    );
  }

  return (
    <Card className="overflow-hidden">
      <Table data-testid="ticket-list">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Ticket</TableHead>
            <TableHead>Subject</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Opened</TableHead>
            <TableHead>Last update</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data.tickets.map((ticket) => (
            <TicketRow key={ticket.number ?? ticket.id} ticket={ticket} />
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function TicketRow({ ticket }: { ticket: SupportTicket }) {
  const state = describeTicketState(ticket.state);
  const StateIcon = state.icon;

  return (
    <TableRow data-testid={`ticket-row-${ticket.number ?? ticket.id ?? "unknown"}`}>
      <TableCell className="font-mono">
        {ticket.number ? (
          <Link
            to={`/support/${encodeURIComponent(ticket.number)}`}
            className="font-medium text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
          >
            {ticket.number}
          </Link>
        ) : (
          <span className="text-muted-foreground">Not reported</span>
        )}
      </TableCell>
      <TableCell className="max-w-sm truncate">{ticket.subject ?? "No subject"}</TableCell>
      <TableCell>
        <Badge variant={state.variant} data-testid="ticket-state">
          <StateIcon className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
          {state.label}
        </Badge>
      </TableCell>
      <TableCell title={formatTimestamp(ticket.openedAt) ?? undefined}>
        {formatRelativeTime(ticket.openedAt) ?? "Not reported"}
      </TableCell>
      <TableCell title={formatTimestamp(ticket.updatedAt) ?? undefined}>
        {formatRelativeTime(ticket.updatedAt) ?? "Not reported"}
      </TableCell>
    </TableRow>
  );
}

// TicketListSkeleton matches the loaded row layout so the page does not shift.
function TicketListSkeleton() {
  return (
    <Card className="overflow-hidden" data-testid="ticket-list-loading">
      <div className="divide-y divide-border">
        {[0, 1, 2, 3].map((row) => (
          <div key={row} className="flex items-center gap-4 px-3 py-3">
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-4 flex-1" />
            <Skeleton className="h-5 w-24 rounded-full" />
            <Skeleton className="h-4 w-20" />
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
