import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Activity, CircleAlert, Cloud, Plus, Unplug } from "lucide-react";
import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
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
  LostPanel,
  ManagedDeleteDialog,
  MonitoringPanel,
  ProvisioningPanel,
  UnreachableBanner,
  UnreachableEmpty,
  awsCleanupPath,
  disconnectEnvironment,
  environmentPill,
  environmentsKey,
  joiningEnvironment,
  operableEnvironment,
  updatePath,
  useEnvironments,
  type Environment,
} from "@/features/environments";
import { useIsAdmin } from "@/features/auth";
import { accountName, useAwsAccounts, type AwsAccount } from "@/features/aws-accounts";
import { SupportAccessPanel, SupportAccessStatus } from "@/features/support-access";
import { useFeature } from "@/features/flags";
import { DatabaseList, StatusPill, engineChoicePhrase, useDatabases } from "@/features/databases";
import { useDatabasesCpu } from "@/features/monitoring";
import { TeamLabel } from "@/features/teams";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";

type EnvironmentTab = "databases" | "monitoring" | "support" | "updates" | "settings";

const linkClassName =
  "rounded-md font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none";

/** One environment: its databases, monitoring, support access, updates and settings. */
export function DatabasesPage() {
  const { name: routeEnvironment } = useParams();
  const [params, setParams] = useSearchParams();
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
  const joining = environment ? joiningEnvironment(environment) : false;
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

  if (!environment) {
    return <NoSuchEnvironment name={routeEnvironment} />;
  }

  // Monitoring and support access need a running environment: a joined one that
  // has reported, or a managed one PDS finished building. Support access opens
  // no window on a managed environment.
  const operable = operableEnvironment(environment);
  const lost = managed && environment.state === "lost";
  const available: EnvironmentTab[] = [
    "databases",
    ...(operable ? (["monitoring"] as const) : []),
    ...(supportAccessOn && operable && !managed ? (["support"] as const) : []),
    ...(managed && operable && environment.lifecycle ? (["updates"] as const) : []),
    "settings",
  ];
  const asked = params.get("tab");
  const tab = available.find((value) => value === asked) ?? "databases";

  function chooseTab(value: string) {
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        if (value === "databases") {
          next.delete("tab");
        } else {
          next.set("tab", value);
        }

        return next;
      },
      { replace: true },
    );
  }

  // A database needs a running environment to land on. An offline joined
  // environment that has reported before accepts the commit and applies it on
  // reconnect.
  const createDisabled = joining || (managed && !operable && environment.state !== "degraded");
  const createTo = `/environments/${routeEnvironment}/new`;
  const accountLabel = account ? accountName(account) : (environment.connectionId ?? "");

  return (
    <div className="space-y-6">
      <PageHeader
        title={routeEnvironment}
        monoTitle
        meta={<EnvironmentFacts environment={environment} accountLabel={accountLabel} />}
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

      {lost && environment.lifecycle ? (
        <LostPanel
          environment={environment.name}
          lifecycle={environment.lifecycle}
          onRemove={() => setDeleting(true)}
        />
      ) : null}

      {managed && account?.state === "unreachable" ? (
        <AccountUnreachable account={account} />
      ) : null}

      {managed && environment.provisioning && environment.provisioning.progress !== "ready" ? (
        <div className="rounded-lg border border-border bg-card px-5 py-5">
          <ProvisioningPanel
            environment={environment.name}
            provisioning={environment.provisioning}
          />
        </div>
      ) : null}

      {unreachable ? (
        <UnreachableBanner lastSeen={formatTimestamp(environment.lastSeen) ?? undefined} />
      ) : null}

      {/* The unreachable banner already says the environment is not reporting, which is all a stale one adds. An off one still owes the setup action. */}
      {alertEnvironment &&
      alertEnvironment.state !== "live" &&
      !(unreachable && alertEnvironment.state === "stale") ? (
        <EnvironmentBanner environment={alertEnvironment} />
      ) : null}

      <Tabs value={tab} onValueChange={chooseTab}>
        <div className="overflow-x-auto">
          <TabsList aria-label="Environment sections" className="w-max min-w-full">
            <TabsTrigger value="databases">Databases</TabsTrigger>
            {available.includes("monitoring") ? (
              <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
            ) : null}
            {available.includes("support") ? (
              <TabsTrigger value="support">Support access</TabsTrigger>
            ) : null}
            {available.includes("updates") ? (
              <TabsTrigger value="updates">Updates</TabsTrigger>
            ) : null}
            <TabsTrigger value="settings">Settings</TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="databases" className="space-y-6">
          <p className="text-sm text-muted-foreground">
            {engineChoicePhrase} on <span className="font-mono">{routeEnvironment}</span>.
          </p>

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
              scheduleEnabled={!unreachable || Boolean(environment.lastSeen)}
            />
          ) : null}
        </TabsContent>
        {available.includes("monitoring") ? (
          <TabsContent value="monitoring">
            <MonitoringPanel environment={routeEnvironment} />
          </TabsContent>
        ) : null}
        {available.includes("support") ? (
          <TabsContent value="support" className="space-y-4">
            {environment.supportAccess ? (
              <SupportAccessStatus summary={environment.supportAccess} />
            ) : null}
            <SupportAccessPanel environment={routeEnvironment} canChange={isAdmin} />
          </TabsContent>
        ) : null}
        {available.includes("updates") && environment.lifecycle ? (
          <TabsContent value="updates">
            <LifecyclePanel environment={routeEnvironment} lifecycle={environment.lifecycle} />
          </TabsContent>
        ) : null}
        <TabsContent value="settings">
          <Card className="divide-y divide-border">
            {managed ? (
              <SettingsSection
                title="AWS account"
                description={
                  <>
                    PDS built <span className="font-mono">{routeEnvironment}</span> in AWS account{" "}
                    <span className="font-mono">{accountLabel}</span>
                    {environment.region ? (
                      <>
                        , region <span className="font-mono">{environment.region}</span>
                      </>
                    ) : null}
                    .
                  </>
                }
                action={
                  <Button asChild variant="outline">
                    <Link to="/environments#aws-accounts">View AWS accounts</Link>
                  </Button>
                }
              />
            ) : null}
            {!managed && !joining ? (
              <SettingsSection
                title="Update"
                description={
                  <>
                    Shows the command that updates what PDS runs on the Kubernetes cluster behind{" "}
                    <span className="font-mono">{routeEnvironment}</span>.
                  </>
                }
                action={
                  <Button asChild variant="outline">
                    <Link to={updatePath(routeEnvironment, environment.team)}>Update</Link>
                  </Button>
                }
              />
            ) : null}
            <SettingsSection
              title="Team"
              description={
                <>
                  {environment.team ? (
                    <>
                      Members of{" "}
                      <span className="font-medium text-foreground">{environment.team}</span> manage
                      this environment and its databases.{" "}
                    </>
                  ) : null}
                  Moving an environment to another Team is not available.
                </>
              }
            />
            {managed ? (
              <SettingsSection
                title={lost ? "Remove from PDS only" : "Delete"}
                description={
                  lost ? (
                    "Forgets the environment and deletes nothing in AWS. What is left there keeps billing until you delete it."
                  ) : (
                    <>
                      Deletes <span className="font-mono">{routeEnvironment}</span>, its databases,
                      and the network and Kubernetes cluster PDS built for it, including the backup
                      bucket and every backup in it. There is no undo.
                    </>
                  )
                }
                action={
                  <Button type="button" variant="outline" onClick={() => setDeleting(true)}>
                    {lost ? "Remove from PDS only" : "Delete"}
                  </Button>
                }
              />
            ) : (
              <SettingsSection
                title="Disconnect"
                description="Removes the environment from PDS and revokes its token. The Kubernetes cluster and its databases keep running, and nothing in AWS is deleted."
                action={
                  <Button type="button" variant="outline" onClick={() => setDisconnecting(true)}>
                    Disconnect
                  </Button>
                }
              />
            )}
          </Card>
        </TabsContent>
      </Tabs>
      {!managed ? (
        <DisconnectDialog
          name={routeEnvironment}
          namespace={environment.namespace}
          open={disconnecting}
          pending={disconnect.isPending}
          onOpenChange={setDisconnecting}
          onConfirm={() => disconnect.mutate()}
        />
      ) : null}
      {managed ? (
        <ManagedDeleteDialog
          environment={environment.name}
          account={accountLabel}
          accountUnreachable={account?.state === "unreachable"}
          lost={lost}
          open={deleting}
          onOpenChange={setDeleting}
        />
      ) : null}
    </div>
  );
}

/** Status, Team, last report and, for a managed environment, where PDS built it. */
function EnvironmentFacts({
  environment,
  accountLabel,
}: {
  environment: Environment;
  accountLabel: string;
}) {
  const pill = environmentPill(environment);
  const lastSeen = formatRelativeTime(environment.lastSeen);

  return (
    <>
      <StatusPill kind={pill.kind} label={pill.label} />
      {environment.team ? <TeamLabel name={environment.team} /> : null}
      <span
        className="flex items-center gap-1.5"
        title={formatTimestamp(environment.lastSeen) ?? undefined}
      >
        <Activity className="size-3.5 shrink-0" strokeWidth={1.5} aria-hidden="true" />
        {lastSeen ? `Last seen ${lastSeen}` : "No report yet"}
      </span>
      {environment.origin === "managed" ? (
        <span className="flex items-center gap-1.5">
          <Cloud className="size-3.5 shrink-0" strokeWidth={1.5} aria-hidden="true" />
          <span>
            Managed by PDS in AWS account <span className="font-mono">{accountLabel}</span>
            {environment.region ? (
              <>
                , <span className="font-mono">{environment.region}</span>
              </>
            ) : null}
          </span>
        </span>
      ) : null}
    </>
  );
}

function AccountUnreachable({ account }: { account: AwsAccount }) {
  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-border bg-card px-4 py-3 text-sm"
    >
      <Unplug className="size-4 shrink-0 text-warning" strokeWidth={1.5} aria-hidden="true" />
      <p className="min-w-0 flex-1">
        PDS cannot reach AWS account <span className="font-mono">{accountName(account)}</span>.
      </p>
      <Link
        to={`/environments?fix=${encodeURIComponent(account.id)}#aws-accounts`}
        className={linkClassName}
      >
        Fix the AWS account
      </Link>
    </div>
  );
}

function SettingsSection({
  title,
  description,
  action,
}: {
  title: string;
  description: ReactNode;
  action?: ReactNode;
}) {
  return (
    <section className="grid gap-4 px-5 py-5 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <div className="space-y-1">
        <h2 className="text-base font-semibold">{title}</h2>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      {action ? <div>{action}</div> : null}
    </section>
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
