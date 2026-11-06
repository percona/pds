import { Ban, Check, CircleAlert, Info, Plug, ShieldCheck, TriangleAlert } from "lucide-react";
import { useId, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Notice } from "@/components/ui/notice";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useIsAdmin } from "@/features/auth";
import { accessLabels } from "@/features/organization-ai";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { useNow } from "@/lib/use-now";
import { useAgentSessions } from "../hooks/use-agent-sessions";
import type { AgentSession } from "../types";
import { RevokeAgentDialog } from "./revoke-agent-dialog";

function Line({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <p className="flex items-center gap-1.5 text-xs text-muted-foreground [&_svg]:size-3.5 [&_svg]:shrink-0">
      {icon}
      <span>{children}</span>
    </p>
  );
}

/**
 * Who the app says it is. The name is the app's own claim, so it is labelled as
 * one, and the address it proved comes beside it in mono.
 */
function AgentName({ agent }: { agent: AgentSession }) {
  return (
    <div className="space-y-1">
      <p className="font-medium">{agent.name}</p>
      {agent.kind === "local" ? (
        <Line icon={<ShieldCheck strokeWidth={1.5} aria-hidden="true" />}>
          Built into this control plane
        </Line>
      ) : (
        <>
          <Badge variant="muted">
            <Info className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
            Name chosen by the app
          </Badge>
          {agent.host ? (
            <p className="font-mono text-xs text-muted-foreground">{agent.host}</p>
          ) : (
            <Line icon={<TriangleAlert strokeWidth={1.5} aria-hidden="true" />}>
              No address to check
            </Line>
          )}
        </>
      )}
    </div>
  );
}

function Allowed({ agent }: { agent: AgentSession }) {
  return (
    <div className="space-y-1">
      <p>{accessLabels[agent.access] ?? agent.access}</p>
      {agent.passwords ? (
        <Line icon={<Check strokeWidth={1.5} aria-hidden="true" />}>
          Fetches database passwords
        </Line>
      ) : null}
      {agent.sqlWrites ? (
        <Line icon={<Check strokeWidth={1.5} aria-hidden="true" />}>Changes data with SQL</Line>
      ) : null}
    </div>
  );
}

/**
 * The agents signed in to the control plane: the person's own, or every
 * member's for an organization admin. Revoking one ends it at once. It shows
 * nothing when the control plane answers "not found", which is how one that
 * does not serve agents answers.
 */
export function ConnectedAgents() {
  const titleId = useId();
  const admin = useIsAdmin();
  const now = useNow();
  const { data, error, isPending, isFetching, refetch } = useAgentSessions();
  const [revoking, setRevoking] = useState<AgentSession | null>(null);

  if (error?.status === 404) {
    return null;
  }

  return (
    <section aria-labelledby={titleId} className="space-y-3">
      <div className="space-y-1">
        <h2 id={titleId} className="text-base font-semibold">
          Connected agents
        </h2>
        <p className="text-sm text-muted-foreground">
          {admin
            ? "Agents signed in by people in your organization. Revoke one to end its access."
            : "Agents signed in as you. Revoke one to end its access."}
        </p>
      </div>

      {isPending ? (
        <Card className="overflow-hidden" data-testid="connected-agents-loading">
          <div className="divide-y divide-border">
            {[0, 1].map((row) => (
              <div key={row} className="flex items-center gap-4 px-3 py-3">
                <Skeleton className="h-4 flex-1" />
                <Skeleton className="h-4 w-24" />
                <Skeleton className="h-4 w-16" />
              </div>
            ))}
          </div>
        </Card>
      ) : error ? (
        <Notice
          icon={<CircleAlert className="size-5 text-muted-foreground" strokeWidth={1.5} />}
          title="Connected agents could not be read"
          detail={error.message}
          action={
            <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
              Try again
            </Button>
          }
        />
      ) : data.length === 0 ? (
        <Notice
          icon={<Plug className="size-5 text-muted-foreground" strokeWidth={1.5} />}
          title="No agents connected"
          detail="An agent that signs in shows here, with what it may do, until you revoke it or its sign in ends."
        />
      ) : (
        <Card className="overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Agent</TableHead>
                <TableHead>May do</TableHead>
                <TableHead>Team</TableHead>
                <TableHead>Last connected</TableHead>
                <TableHead className="w-28">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map((agent) => (
                <TableRow key={agent.id}>
                  <TableCell>
                    <AgentName agent={agent} />
                  </TableCell>
                  <TableCell>
                    <Allowed agent={agent} />
                  </TableCell>
                  <TableCell>
                    <p>{agent.team}</p>
                    <p className="text-xs text-muted-foreground">
                      {agent.mine ? "You" : agent.member}
                    </p>
                  </TableCell>
                  <TableCell>
                    <span
                      className="whitespace-nowrap font-mono"
                      title={formatTimestamp(agent.lastUsedAt) ?? ""}
                    >
                      {formatRelativeTime(agent.lastUsedAt, now) ?? "Unknown"}
                    </span>
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end">
                      <Button
                        variant="ghost"
                        aria-label={`Revoke ${agent.name}${agent.mine ? "" : ` for ${agent.member}`}`}
                        onClick={() => setRevoking(agent)}
                      >
                        <Ban className="size-4" strokeWidth={1.5} aria-hidden="true" />
                        Revoke
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <RevokeAgentDialog agent={revoking} onOpenChange={(open) => !open && setRevoking(null)} />
    </section>
  );
}
