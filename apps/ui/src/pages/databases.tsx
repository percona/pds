import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  EnvironmentBanner,
  EnvironmentNote,
  environmentCutAlerts,
  summarizeAlerts,
  useAlerts,
} from "@/features/alerts";
import {
  DisconnectDialog,
  LifecyclePanel,
  ManagedDeleteDialog,
  MonitoringPanel,
  UnreachableBanner,
  UnreachableEmpty,
  awsCleanupPath,
  disconnectEnvironment,
  environmentsKey,
  updatePath,
  useEnvironments,
} from "@/features/environments";
import { useIsAdmin } from "@/features/auth";
import { accountName, useAwsAccounts } from "@/features/aws-accounts";
import { SupportAccessPanel } from "@/features/support-access";
import { useFeature } from "@/features/flags";
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
  const unreachable = environment?.status === "unreachable";
  const managed = environment?.origin === "managed";
  const joining = Boolean(unreachable && !environment?.lastSeen);
  const supportAccessOn = useFeature("support-access");
  const isAdmin = useIsAdmin();
  const accounts = useAwsAccounts().data ?? [];
  const account = accounts.find((candidate) => candidate.id === environment?.connectionId);
  const [disconnecting, setDisconnecting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const cleanup = awsCleanupPath(environment?.name ?? "", environment?.namespace);
  const disconnect = useMutation({
    mutationFn: () => disconnectEnvironment(environment?.name ?? ""),
    onSuccess: async () => {
      toast.success(`${environment?.name ?? "Environment"} disconnected`, {
        action: { label: "View delete command", onClick: () => void navigate(cleanup) },
      });
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
      void navigate("/environments");
    },
    onError: (disconnectError: Error) => toast.error(disconnectError.message),
  });

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

  const description = `${engineChoicePhrase} on ${routeEnvironment}.`;

  // Only an environment that has never reported blocks a create. An offline environment
  // that has beaten before accepts the commit and applies it on reconnect.
  const createDisabled = unreachable && !environment?.lastSeen;
  const createTo = `/environments/${routeEnvironment}/new`;

  return (
    <div className="space-y-6">
      <PageHeader
        title={routeEnvironment}
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

      <Tabs defaultValue="databases">
        <TabsList>
          <TabsTrigger value="databases">Databases</TabsTrigger>
          <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
          {supportAccessOn && !managed ? (
            <TabsTrigger value="support">Support access</TabsTrigger>
          ) : null}
          {managed && environment?.lifecycle ? (
            <TabsTrigger value="updates">Updates</TabsTrigger>
          ) : null}
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>
        <TabsContent value="databases" className="space-y-6">
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
        </TabsContent>
        <TabsContent value="monitoring">
          <MonitoringPanel environment={routeEnvironment} />
        </TabsContent>
        {supportAccessOn && !managed ? (
          <TabsContent value="support">
            <SupportAccessPanel environment={routeEnvironment} canChange={isAdmin} />
          </TabsContent>
        ) : null}
        {managed && environment?.lifecycle ? (
          <TabsContent value="updates">
            <LifecyclePanel environment={routeEnvironment} lifecycle={environment.lifecycle} />
          </TabsContent>
        ) : null}
        <TabsContent value="settings" className="space-y-4">
          <p className="text-sm text-muted-foreground">
            Moving an environment to another team is not available.
          </p>
          {!managed && !joining ? (
            <Button asChild variant="outline">
              <Link to={updatePath(routeEnvironment, environment?.team)}>Update</Link>
            </Button>
          ) : null}
          {managed && environment?.state !== "lost" ? (
            <Button type="button" variant="outline" onClick={() => setDeleting(true)}>
              Delete
            </Button>
          ) : null}
          {!managed ? (
            <Button type="button" variant="outline" onClick={() => setDisconnecting(true)}>
              Disconnect
            </Button>
          ) : null}
        </TabsContent>
      </Tabs>
      {!managed ? (
        <DisconnectDialog
          name={routeEnvironment}
          namespace={environment?.namespace}
          open={disconnecting}
          pending={disconnect.isPending}
          onOpenChange={setDisconnecting}
          onConfirm={() => disconnect.mutate()}
        />
      ) : null}
      {managed && environment ? (
        <ManagedDeleteDialog
          environment={environment.name}
          account={account ? accountName(account) : (environment.connectionId ?? "")}
          accountUnreachable={account?.state === "unreachable"}
          lost={environment.state === "lost"}
          open={deleting}
          onOpenChange={setDeleting}
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
