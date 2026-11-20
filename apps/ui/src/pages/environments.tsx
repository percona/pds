import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, Server } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { toast } from "sonner";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  awsCleanupPath,
  environmentsKey,
  disconnectEnvironment,
  managedPill,
  ManagedDeleteDialog,
  MonitoringPanel,
  openEverestPath,
  ProvisioningPanel,
  updatePath,
  useEnvironments,
  type Environment,
} from "@/features/environments";
import { ConnectionsPanel } from "@/features/connections";
import {
  accountName,
  AwsAccountsSection,
  useAwsAccounts,
  type AwsAccount,
} from "@/features/aws-accounts";
import { engineChoicePhrase, StatusPill } from "@/features/databases";
import { useIsAdmin } from "@/features/auth";
import { SupportAccessPanel, SupportAccessStatus } from "@/features/support-access";
import { TeamLabel } from "@/features/teams";
import { Feature, useFeature } from "@/features/flags";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { cn } from "@/lib/utils";

export function EnvironmentsPage() {
  const { isPending, environments, error, resolved } = useEnvironments();
  const accounts = useAwsAccounts().data ?? [];

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
                key={environment.name}
                environment={environment}
                account={accounts.find((candidate) => candidate.id === environment.connectionId)}
              />
            ))}
          </ul>
        </Card>
      ) : null}

      <AwsAccountsSection />

      <Feature name="connections">
        <ConnectionsPanel />
      </Feature>
    </div>
  );
}

function EnvironmentRow({
  environment,
  account,
}: {
  environment: Environment;
  account: AwsAccount | undefined;
}) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [monitoringOpen, setMonitoringOpen] = useState(false);
  const [supportOpen, setSupportOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const isAdmin = useIsAdmin();
  const supportAccess = useFeature("support-access");
  const cleanup = awsCleanupPath(environment.name, environment.namespace);
  const handOver = openEverestPath(environment.name, environment.namespace);
  const disconnect = useMutation({
    mutationFn: () => disconnectEnvironment(environment.name),
    onSuccess: async () => {
      // The row, and the link in it, is gone once the list refreshes.
      toast.success(`${environment.name} disconnected`, {
        action: { label: "View delete command", onClick: () => void navigate(cleanup) },
      });
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
    },
    onError: (error: Error) => toast.error(error.message),
  });

  const managed = environment.origin === "managed";
  const operable = !managed || environment.state === "ready";
  const pill = managedPill(environment.state);
  const unreachable = !managed && environment.status === "unreachable";
  // A row appears the moment a token is minted, so most of the time an
  // unreachable environment with no heartbeat is one still being built, not one
  // that failed. The red pill said otherwise for the whole 15 minutes.
  const joining = unreachable && !environment.lastSeen;
  const lastSeen = formatRelativeTime(environment.lastSeen);

  return (
    <li>
      {/*
       * The actions are centred on the summary line alone. Centred on the
       * whole row, they slid down over the confirmation text once it opened.
       */}
      <div className="relative">
        <Link
          to={`/environments/${encodeURIComponent(environment.name)}`}
          className={cn(
            "grid grid-cols-[1fr_auto] items-center gap-x-4 gap-y-2 px-5 py-4 hover:bg-accent sm:grid-cols-[minmax(0,2fr)_auto_minmax(0,1fr)]",
            confirming ? "pr-52" : joining ? "pr-80" : "pr-[28rem]",
          )}
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
          </div>
          {managed ? (
            <StatusPill kind={pill.kind} label={pill.label} />
          ) : joining ? (
            <StatusPill kind="creating" label="Joining" />
          ) : unreachable ? (
            <StatusPill kind="degraded" label="Unreachable" />
          ) : (
            <StatusPill kind="ready" label="Connected" />
          )}
          <p
            className="text-xs text-muted-foreground sm:text-right"
            title={formatTimestamp(environment.lastSeen) ?? undefined}
          >
            {managed && !lastSeen ? "" : lastSeen ? `Last seen ${lastSeen}` : "No heartbeat yet"}
          </p>
        </Link>
        <div className="absolute top-1/2 right-5 -translate-y-1/2">
          {confirming ? (
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={disconnect.isPending}
                onClick={() => setConfirming(false)}
              >
                Cancel
              </Button>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                loading={disconnect.isPending}
                onClick={() => disconnect.mutate()}
              >
                Remove
              </Button>
            </div>
          ) : (
            <div className="flex items-center gap-1">
              {operable ? (
                <>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-expanded={monitoringOpen}
                    onClick={() => setMonitoringOpen((open) => !open)}
                  >
                    Monitoring
                  </Button>
                  {supportAccess ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      aria-expanded={supportOpen}
                      onClick={() => setSupportOpen((open) => !open)}
                    >
                      Support access
                    </Button>
                  ) : null}
                </>
              ) : null}
              {joining || managed ? null : (
                <Button asChild variant="ghost" size="sm">
                  <Link to={updatePath(environment.name, environment.team)}>Update</Link>
                </Button>
              )}
              {managed ? (
                <Button type="button" variant="ghost" size="sm" onClick={() => setDeleting(true)}>
                  Delete
                </Button>
              ) : (
                <Button type="button" variant="ghost" size="sm" onClick={() => setConfirming(true)}>
                  Disconnect
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
      {managed && account?.state === "unreachable" ? (
        <p className="border-t border-border px-5 py-3 text-sm text-muted-foreground">
          PDS cannot reach AWS account <span className="font-mono">{accountName(account)}</span>.{" "}
          <Link
            to={`/environments?fix=${encodeURIComponent(account.id)}#aws-accounts`}
            className="rounded-md font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >
            Fix the AWS account
          </Link>
        </p>
      ) : null}
      {managed && environment.provisioning && environment.provisioning.progress !== "ready" ? (
        <div className="border-t border-border px-5 py-5">
          <ProvisioningPanel
            environment={environment.name}
            provisioning={environment.provisioning}
          />
        </div>
      ) : null}
      {managed ? (
        <ManagedDeleteDialog
          environment={environment.name}
          account={account ? accountName(account) : (environment.connectionId ?? "")}
          accountUnreachable={account?.state === "unreachable"}
          open={deleting}
          onOpenChange={setDeleting}
        />
      ) : null}
      {supportAccess && environment.supportAccess && !confirming ? (
        <SupportAccessStatus summary={environment.supportAccess} />
      ) : null}
      {supportAccess && supportOpen && !confirming ? (
        <div className="border-t border-border px-5 py-5">
          <SupportAccessPanel environment={environment.name} canChange={isAdmin} />
        </div>
      ) : null}
      {monitoringOpen && !confirming ? (
        <div className="border-t border-border px-5 py-5">
          <MonitoringPanel environment={environment.name} />
        </div>
      ) : null}
      {confirming ? (
        <div className="space-y-3 px-5 pb-4">
          <p className="text-sm font-medium">
            Disconnect <span className="font-mono">{environment.name}</span>?
          </p>
          <p className="text-xs text-muted-foreground">
            This removes <span className="font-mono">{environment.name}</span> from this control
            plane and revokes its token. Its saved configuration is kept. The Kubernetes cluster and
            its databases keep running. Nothing in AWS is deleted. If the join created an EKS
            cluster, its nodes and its backup bucket stay in your account and keep billing. One
            command deletes them.
          </p>
          <div className="flex items-center gap-4">
            <Link
              to={cleanup}
              className="rounded-md text-xs font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            >
              View delete command
            </Link>
            <Link
              to={handOver}
              className="rounded-md text-xs font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            >
              Continue with OpenEverest
            </Link>
          </div>
        </div>
      ) : null}
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
          Add an environment to run {engineChoicePhrase} on it from here. The command creates an EKS
          cluster and its worker nodes, which cost money, or joins a Kubernetes cluster you already
          run.
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
