import { ChevronRight, CircleAlert, CircleArrowUp, Server, TriangleAlert } from "lucide-react";
import { Link } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  environmentPill,
  lifecycleNotes,
  useEnvironments,
  type Environment,
} from "@/features/environments";
import {
  accountName,
  AwsAccountsSection,
  useAwsAccounts,
  type AwsAccount,
} from "@/features/aws-accounts";
import { engineChoicePhrase, StatusPill, useDatabases } from "@/features/databases";
import { TeamLabel } from "@/features/teams";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";

export function EnvironmentsPage() {
  const { isPending, environments, error, resolved } = useEnvironments();
  const accounts = useAwsAccounts().data ?? [];
  const databases = useDatabases();
  const databaseCounts = new Map<string, number>();
  for (const database of databases.data ?? []) {
    databaseCounts.set(database.environment, (databaseCounts.get(database.environment) ?? 0) + 1);
  }

  // A failed first read is an error. A later blip keeps the last list.
  if (error && !resolved) {
    return <ReadFailed detail={error.message} />;
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Environments"
        description="Environments connected to this control plane."
        actions={
          <Button asChild>
            <Link to="/environments/new">Create an environment</Link>
          </Button>
        }
      />

      {isPending ? <ListSkeleton /> : null}

      {!isPending && environments.length === 0 ? <EmptyState /> : null}

      {!isPending && environments.length > 0 ? (
        <Card className="overflow-hidden">
          <ul className="divide-y divide-border">
            {environments.map((environment) => (
              <EnvironmentRow
                key={`${environment.team ?? ""}/${environment.name}`}
                environment={environment}
                account={accounts.find((candidate) => candidate.id === environment.connectionId)}
                databaseCount={
                  databases.data ? (databaseCounts.get(environment.name) ?? 0) : undefined
                }
              />
            ))}
          </ul>
        </Card>
      ) : null}

      <AwsAccountsSection />
    </div>
  );
}

/** A row only summarises. Everything an environment needs done is on its own page. */
function EnvironmentRow({
  environment,
  account,
  databaseCount,
}: {
  environment: Environment;
  account: AwsAccount | undefined;
  databaseCount?: number;
}) {
  const managed = environment.origin === "managed";
  const notes =
    managed && environment.state !== "lost" ? lifecycleNotes(environment.lifecycle) : [];
  const pill = environmentPill(environment);
  const lastSeen = formatRelativeTime(environment.lastSeen);

  return (
    <li>
      <Link
        to={`/environments/${encodeURIComponent(environment.name)}`}
        className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 px-5 py-4 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none sm:grid-cols-[minmax(0,2fr)_9rem_minmax(0,1fr)_minmax(0,1.2fr)_auto]"
      >
        <div className="min-w-0 space-y-1">
          <p className="truncate font-mono text-sm font-medium">{environment.name}</p>
          {environment.team ? <TeamLabel name={environment.team} /> : null}
          {managed ? (
            <p className="text-xs text-muted-foreground">
              Created by PDS in AWS account{" "}
              <span className="font-mono">
                {account ? accountName(account) : environment.connectionId}
              </span>
              {environment.region ? (
                <>
                  {" "}
                  in <span className="font-mono">{environment.region}</span>
                </>
              ) : null}
            </p>
          ) : null}
          {notes.length > 0 ? (
            <ul className="flex flex-wrap gap-x-4 gap-y-1">
              {notes.map((note) => (
                <li
                  key={note.kind}
                  className="flex items-center gap-1.5 text-xs text-muted-foreground"
                >
                  {note.kind === "update" ? (
                    <CircleArrowUp className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
                  ) : (
                    <TriangleAlert
                      className="size-3.5 text-warning"
                      strokeWidth={1.5}
                      aria-hidden="true"
                    />
                  )}
                  {note.label}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
        <span>
          <StatusPill kind={pill.kind} label={pill.label} />
        </span>
        <p className="hidden text-xs text-muted-foreground sm:block">
          {databaseCount !== undefined ? (
            <>
              <span className="font-mono">{databaseCount}</span>
              {databaseCount === 1 ? " database" : " databases"}
            </>
          ) : null}
        </p>
        <p
          className="hidden text-xs text-muted-foreground sm:block"
          title={formatTimestamp(environment.lastSeen) ?? undefined}
        >
          {lastSeen ? `Last seen ${lastSeen}` : managed ? "" : "No report yet"}
        </p>
        <ChevronRight
          className="hidden size-4 text-muted-foreground sm:block"
          strokeWidth={1.5}
          aria-hidden="true"
        />
      </Link>
    </li>
  );
}

function ReadFailed({ detail }: { detail: string }) {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Environments"
        description="Environments connected to this control plane."
      />
      <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
        <CircleAlert className="size-5 text-destructive" strokeWidth={1.5} aria-hidden="true" />
        <div className="space-y-1">
          <p className="text-sm font-medium">The environment list could not be read</p>
          <p className="text-sm text-muted-foreground">{detail}</p>
        </div>
      </Card>
    </div>
  );
}

function EmptyState() {
  return (
    <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
      <Server className="size-5 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
      <div className="space-y-1">
        <p className="text-sm font-medium">No environments yet</p>
        <p className="text-sm text-muted-foreground">
          Add an environment to run {engineChoicePhrase} on it from here. PDS builds an EKS cluster
          and its worker nodes in your AWS account, and AWS bills for them.
        </p>
      </div>
      <Button asChild>
        <Link to="/environments/new">Create an environment</Link>
      </Button>
    </Card>
  );
}

function ListSkeleton() {
  return (
    <Card data-testid="environments-loading" className="overflow-hidden">
      <ul className="divide-y divide-border">
        {[0, 1, 2].map((row) => (
          <li key={row} className="flex items-center justify-between gap-4 px-5 py-4">
            <Skeleton className="h-4 w-40" />
            <Skeleton className="h-5 w-24 rounded-full" />
          </li>
        ))}
      </ul>
    </Card>
  );
}
