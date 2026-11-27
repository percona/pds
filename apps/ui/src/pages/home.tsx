import { CircleAlert, CircleCheck } from "lucide-react";
import { Link } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAlerts } from "@/features/alerts";
import { describeAction } from "@/features/activity";
import { useActivity } from "@/features/activity";
import { useAwsAccounts } from "@/features/aws-accounts";
import { useConnections } from "@/features/connections";
import { useDatabases } from "@/features/databases";
import { useEnvironments } from "@/features/environments";
import { formatRelativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

/**
 * The page people land on. Until a database exists it is a setup checklist.
 * After that it is the counts, what needs attention, and recent activity.
 */
export function HomePage() {
  const environments = useEnvironments();
  const databases = useDatabases();
  const accounts = useAwsAccounts();
  const alerts = useAlerts();
  const connections = useConnections();
  const activity = useActivity({ live: false });

  if (environments.error && !environments.resolved) {
    return (
      <div className="space-y-6">
        <PageHeader title="Home" description="What is running, and what needs you." />
        <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
          <CircleAlert className="size-5 text-destructive" strokeWidth={1.5} aria-hidden="true" />
          <div className="space-y-1">
            <p className="text-sm font-medium">The environment list could not be read</p>
            <p className="text-sm text-muted-foreground">{environments.error.message}</p>
          </div>
        </Card>
      </div>
    );
  }

  if (!environments.resolved || databases.isPending) {
    return <HomeSkeleton />;
  }

  const databaseCount = databases.data?.length ?? 0;
  const hasEnvironment = environments.environments.length > 0;
  const hasAccount = (accounts.data ?? []).some((account) => account.state === "ready");
  const agentReady = Boolean(
    connections.data?.assistant.configured || connections.data?.cursor.configured,
  );

  if (databaseCount === 0) {
    return <Checklist placeReady={hasEnvironment || hasAccount} agentReady={agentReady} />;
  }

  const criticals = alerts.data?.criticals ?? 0;
  const attention = environments.environments.filter(
    (environment) =>
      environment.state === "lost" ||
      environment.state === "failed" ||
      environment.status === "unreachable",
  );

  return (
    <div className="space-y-6">
      <PageHeader title="Home" description="What is running, and what needs you." />

      <div className="grid gap-4 sm:grid-cols-3">
        <Count label="Environments" value={environments.environments.length} to="/environments" />
        <Count label="Databases" value={databaseCount} to="/environments" />
        <Count label="Open alerts" value={alerts.data?.alerts.length ?? 0} to="/alerts" />
      </div>

      <section className="space-y-3" aria-labelledby="attention-heading">
        <h2 id="attention-heading" className="text-base font-semibold">
          Needs attention
        </h2>
        {criticals === 0 && attention.length === 0 ? (
          <Card className="flex items-center gap-2 px-4 py-3">
            <CircleCheck className="size-4 text-success" strokeWidth={1.5} aria-hidden="true" />
            <p className="text-sm">Nothing needs you right now.</p>
          </Card>
        ) : (
          <Card>
            <ul className="divide-y divide-border">
              {criticals > 0 ? (
                <li>
                  <Link
                    to="/alerts?severity=critical"
                    className="flex items-center gap-2 px-4 py-3 text-sm hover:bg-accent"
                  >
                    <CircleAlert
                      className="size-4 text-destructive"
                      strokeWidth={1.5}
                      aria-hidden="true"
                    />
                    <span className="font-mono">{criticals}</span>
                    {criticals === 1 ? " critical alert firing" : " critical alerts firing"}
                  </Link>
                </li>
              ) : null}
              {attention.map((environment) => (
                <li key={environment.name}>
                  <Link
                    to={`/environments/${encodeURIComponent(environment.name)}`}
                    className="flex items-center gap-2 px-4 py-3 text-sm hover:bg-accent"
                  >
                    <CircleAlert
                      className="size-4 text-warning"
                      strokeWidth={1.5}
                      aria-hidden="true"
                    />
                    <span className="font-mono">{environment.name}</span>
                    <span className="text-muted-foreground">
                      {environment.state === "lost"
                        ? "is lost"
                        : environment.state === "failed"
                          ? "could not be created"
                          : "is unreachable"}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          </Card>
        )}
      </section>

      {activity.error?.status === 501 ? null : (
        <section className="space-y-3" aria-labelledby="activity-heading">
          <div className="flex items-center justify-between gap-3">
            <h2 id="activity-heading" className="text-base font-semibold">
              Recent activity
            </h2>
            <Link
              to="/activity"
              className="rounded-md text-sm font-medium text-primary hover:underline"
            >
              View activity
            </Link>
          </div>
          {activity.isPending ? <Skeleton className="h-24 rounded-lg" /> : null}
          {!activity.isPending && activity.entries.length === 0 ? (
            <p className="text-sm text-muted-foreground">No activity yet.</p>
          ) : null}
          {activity.entries.length > 0 ? (
            <Card>
              <ul className="divide-y divide-border">
                {activity.entries.slice(0, 5).map((entry) => (
                  <li
                    key={entry.id}
                    className="flex items-center justify-between gap-3 px-4 py-3 text-sm"
                  >
                    <span>{describeAction(entry.action).label}</span>
                    <span className="text-xs text-muted-foreground">
                      {formatRelativeTime(entry.at) ?? ""}
                    </span>
                  </li>
                ))}
              </ul>
            </Card>
          ) : null}
        </section>
      )}
    </div>
  );
}

function Checklist({ placeReady, agentReady }: { placeReady: boolean; agentReady: boolean }) {
  const steps = [
    {
      label: "Connect an AWS account or create an environment",
      done: placeReady,
      to: placeReady ? "/environments" : "/environments/new",
    },
    { label: "Create a database", done: false, to: "/environments" },
    { label: "Connect to it", done: false, to: "/environments" },
    { label: "Set up an AI agent", done: agentReady, to: "/agents", optional: true },
  ];
  const done = steps.filter((step) => step.done).length;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Home"
        description="Four steps to a database you can connect to. The last one is optional."
      />
      <div className="space-y-2">
        <div
          role="progressbar"
          aria-label="Setup progress"
          aria-valuemin={0}
          aria-valuemax={steps.length}
          aria-valuenow={done}
          className="h-2 overflow-hidden rounded-full bg-muted"
        >
          <div className="h-full bg-primary" style={{ width: `${(done / steps.length) * 100}%` }} />
        </div>
        <p className="font-mono text-xs text-muted-foreground">
          {done} of {steps.length}
        </p>
      </div>
      <Card>
        <ol className="divide-y divide-border">
          {steps.map((step, index) => (
            <li key={step.label}>
              <Link
                to={step.to}
                className="flex items-center gap-3 px-4 py-3 text-sm hover:bg-accent"
              >
                {step.done ? (
                  <CircleCheck
                    className="size-4 text-success"
                    strokeWidth={1.5}
                    aria-hidden="true"
                  />
                ) : (
                  <span className="flex size-4 items-center justify-center font-mono text-xs text-muted-foreground">
                    {index + 1}
                  </span>
                )}
                <span className={cn(step.done && "text-muted-foreground")}>{step.label}</span>
                {step.optional ? (
                  <span className="text-xs text-muted-foreground">Optional</span>
                ) : null}
              </Link>
            </li>
          ))}
        </ol>
      </Card>
    </div>
  );
}

function Count({ label, value, to }: { label: string; value: number; to: string }) {
  return (
    <Link to={to} className="rounded-lg border border-border bg-card px-4 py-4 hover:bg-accent">
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="font-mono text-2xl font-semibold tracking-tight">{value}</p>
    </Link>
  );
}

function HomeSkeleton() {
  return (
    <div className="space-y-6" data-testid="home-loading">
      <Skeleton className="h-8 w-32" />
      <Skeleton className="h-24 rounded-lg" />
    </div>
  );
}
