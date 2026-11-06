import { CircleAlert, Plus } from "lucide-react";
import { useMemo } from "react";
import { Link, useParams } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  EnvironmentBanner,
  EnvironmentNote,
  environmentCutAlerts,
  summarizeAlerts,
  useAlerts,
} from "@/features/alerts";
import { UnreachableBanner, UnreachableEmpty, useEnvironments } from "@/features/environments";
import { DatabaseList, engineChoicePhrase, useDatabases } from "@/features/databases";
import { useDatabasesCpu } from "@/features/monitoring";
import { formatTimestamp } from "@/lib/format";

/** The databases on one environment. */
export function DatabasesPage() {
  const { name: routeEnvironment } = useParams();
  const { data, error, isPending, isFetching, refetch } = useDatabases(routeEnvironment);
  const { environments, error: environmentsError, resolved } = useEnvironments();
  const environment = routeEnvironment
    ? environments.find((item) => item.name === routeEnvironment)
    : undefined;
  const unknownEnvironment = Boolean(routeEnvironment && resolved && !environment);

  // Only a running database has a CPU line to draw, and asking for the rest
  // would spend an adapter call per row on a pending badge. The hook stays
  // above the returns so a pending environment read and a resolved one declare
  // the same hooks.
  const cpuSeries = useDatabasesCpu(
    (data ?? []).filter((database) => database.status === "ready").map((database) => database.id),
  );

  // The list shares the sidebar's alerts query, so a quiet read costs no
  // extra request. A failed read shows no mark rather than a wrong one, and
  // says so below, because rows with no mark would read as healthy.
  const { data: alertReport, error: alertsError } = useAlerts();
  const alerts = useMemo(() => summarizeAlerts(alertReport?.alerts ?? []), [alertReport]);
  const alertEnvironment = alertReport?.environments.find((item) => item.name === routeEnvironment);

  // A failed first read is an error. A later blip keeps the last list, the same
  // way the environments page does, so a poll that misses does not blank the rows.
  if (environmentsError && !resolved) {
    return <EnvironmentReadFailed detail={environmentsError.message} />;
  }

  if (!resolved) {
    return <ListSkeleton />;
  }

  if (!routeEnvironment || unknownEnvironment) {
    return <NoSuchEnvironment name={routeEnvironment ?? ""} />;
  }

  const unreachable = environment?.status === "unreachable";
  const description = `${engineChoicePhrase} on ${routeEnvironment}.`;

  // Only an environment that has never reported blocks a create. An offline environment
  // that has beaten before accepts the commit and applies it on reconnect.
  const createDisabled = unreachable && !environment?.lastSeen;
  const createTo = `/environments/${routeEnvironment}/new`;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Databases"
        description={description}
        actions={
          createDisabled ? (
            <Button disabled>
              <Plus strokeWidth={1.5} aria-hidden="true" />
              Create database
            </Button>
          ) : (
            <Button asChild>
              <Link to={createTo}>
                <Plus strokeWidth={1.5} aria-hidden="true" />
                Create database
              </Link>
            </Button>
          )
        }
      />

      {unreachable ? (
        <UnreachableBanner lastSeen={formatTimestamp(environment?.lastSeen) ?? undefined} />
      ) : null}

      {/* The unreachable banner already says the environment is not reporting, which is all a stale one adds. An off one still owes the setup action. */}
      {alertEnvironment &&
      alertEnvironment.state !== "live" &&
      !(unreachable && alertEnvironment.state === "stale") ? (
        <EnvironmentBanner environment={alertEnvironment} />
      ) : null}

      {alertsError && !alertReport ? (
        <EnvironmentNote
          role="status"
          note="Alerts could not be read. The list shows no alert marks until they can be."
        />
      ) : null}

      {alertEnvironment?.note && environmentCutAlerts(alertEnvironment) ? (
        <EnvironmentNote note={alertEnvironment.note} />
      ) : null}

      {isPending ? <ListSkeleton /> : null}

      {error ? (
        <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
          <CircleAlert
            className="size-5 text-muted-foreground"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <div className="space-y-1">
            <p className="text-sm font-medium">Databases could not be read</p>
            <p className="text-sm text-muted-foreground">{error.message}</p>
          </div>
          <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
            Try again
          </Button>
        </Card>
      ) : null}

      {!isPending && !error && unreachable && (!data || data.length === 0) ? (
        <UnreachableEmpty />
      ) : null}

      {data && !(unreachable && data.length === 0) ? (
        <DatabaseList
          databases={data}
          cpuSeries={cpuSeries}
          alerts={alerts}
          scheduleEnabled={!unreachable || Boolean(environment?.lastSeen)}
        />
      ) : null}
    </div>
  );
}

function EnvironmentReadFailed({ detail }: { detail: string }) {
  return (
    <div className="space-y-6">
      <PageHeader title="Databases" description="The environment list could not be read." />
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

export function NoSuchEnvironment({ name }: { name: string }) {
  return (
    <div className="space-y-6">
      <PageHeader title="Databases" description="This environment is not on the list." />
      <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
        <CircleAlert
          className="size-5 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <div className="space-y-1">
          <p className="text-sm font-medium">
            No environment named <span className="font-mono">{name}</span>
          </p>
          <p className="text-sm text-muted-foreground">
            It may have been removed, or the link may be out of date.
          </p>
        </div>
        <Button asChild>
          <Link to="/environments">See the environments</Link>
        </Button>
      </Card>
    </div>
  );
}

function ListSkeleton() {
  return (
    <Card data-testid="databases-loading" className="overflow-hidden">
      <ul className="divide-y divide-border">
        {[0, 1, 2].map((row) => (
          <li
            key={row}
            className="grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-4 px-5 py-4"
          >
            <div className="space-y-2">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-3 w-20" />
            </div>
            <Skeleton className="h-5 w-32 rounded-full" />
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-10 w-24" />
          </li>
        ))}
      </ul>
    </Card>
  );
}
