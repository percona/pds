import { CircleAlert } from "lucide-react";
import { PageHeader } from "@/components/layout/page-header";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAlerts } from "@/features/alerts";
import { useSession } from "@/features/auth";
import { useAwsAccounts, type AwsAccount } from "@/features/aws-accounts";
import { useDatabases, type Database } from "@/features/databases";
import { useEnvironments, type Environment } from "@/features/environments";
import { useFeature } from "@/features/flags";
import {
  CountTiles,
  CreateDatabaseButton,
  NeedsAttention,
  RecentActivity,
  SetupChecklist,
} from "@/features/home";

/**
 * The page people land on. Until a database exists it is a setup checklist.
 * After that it is the counts, what needs attention, and recent activity.
 */
export function HomePage() {
  const environmentsOn = useFeature("clusters");
  const environments = useEnvironments();
  const databases = useDatabases();
  const accounts = useAwsAccounts({ enabled: environmentsOn });
  const organization = useSession().data?.organization;

  if (environments.error && !environments.resolved) {
    return (
      <ReadFailed
        title="The environment list could not be read"
        detail={environments.error.message}
      />
    );
  }

  if (databases.error && !databases.data) {
    return (
      <ReadFailed title="The database list could not be read" detail={databases.error.message} />
    );
  }

  if (!environments.resolved || databases.isPending) {
    return <HomeSkeleton />;
  }

  const databaseList = databases.data ?? [];
  const accountList = accounts.data ?? [];

  if (databaseList.length === 0) {
    return (
      <div className="space-y-6">
        <PageHeader
          title="Home"
          description="Two steps take a new organization to a database your team can use."
        />
        <SetupChecklist
          organization={organization}
          environments={environments.environments}
          accounts={accountList}
        />
      </div>
    );
  }

  return (
    <Fleet
      organization={organization}
      environments={environments.environments}
      databases={databaseList}
      accounts={accountList}
    />
  );
}

function Fleet({
  organization,
  environments,
  databases,
  accounts,
}: {
  organization?: string;
  environments: Environment[];
  databases: Database[];
  accounts: AwsAccount[];
}) {
  const alerts = useAlerts();
  const activityOn = useFeature("activity");
  const databaseCounts = new Map<string, number>();
  for (const database of databases) {
    databaseCounts.set(database.environment, (databaseCounts.get(database.environment) ?? 0) + 1);
  }
  const across = `${environments.length} ${environments.length === 1 ? "environment" : "environments"}`;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Home"
        description={
          organization
            ? `Everything you can see in ${organization}, across ${across}.`
            : `Everything you can see, across ${across}.`
        }
        actions={
          <CreateDatabaseButton environments={environments} databaseCounts={databaseCounts} />
        }
      />
      <CountTiles environments={environments} databases={databases} alerts={alerts.data} />
      <div className="grid gap-6 lg:grid-cols-2">
        <NeedsAttention
          environments={environments}
          databases={databases}
          accounts={accounts}
          alerts={alerts.data?.alerts ?? []}
        />
        {activityOn ? <RecentActivity /> : null}
      </div>
    </div>
  );
}

function ReadFailed({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="space-y-6">
      <PageHeader title="Home" description="What is running, and what needs you." />
      <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
        <CircleAlert className="size-5 text-destructive" strokeWidth={1.5} aria-hidden="true" />
        <div className="space-y-1">
          <p className="text-sm font-medium">{title}</p>
          <p className="text-sm text-muted-foreground">{detail}</p>
        </div>
      </Card>
    </div>
  );
}

function HomeSkeleton() {
  return (
    <div className="space-y-6" data-testid="home-loading">
      <Skeleton className="h-8 w-32" />
      <Skeleton className="h-28 rounded-lg" />
      <div className="grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-48 rounded-lg" />
        <Skeleton className="h-48 rounded-lg" />
      </div>
    </div>
  );
}
