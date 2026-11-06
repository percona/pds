import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ActivityFeed } from "@/features/activity";
import { DatabaseAlertsTab } from "@/features/alerts";
import { HistoryTab } from "@/features/history";
import { useFeature } from "@/features/flags";
import type { FeatureFlagName } from "@/features/flags";
import { MetricsTab, OverviewMetrics } from "@/features/monitoring";
import type { MetricKind } from "@/features/monitoring";
import { findProfile } from "@/features/profiles";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import type { Backup, Database } from "../types";
import { BackupsTab } from "./backups-tab";
import { ConnectCard } from "./connect-card";
import { SettingsTab } from "./settings-tab";

/**
 * The tabs, in the order the detail page fixes. Overview, History and
 * Settings are the database itself, so they have no surface of their own to
 * turn off; the other three each name one.
 */
const tabs = [
  { value: "overview", label: "Overview" },
  { value: "backups", label: "Backups", surface: "backups" },
  { value: "metrics", label: "Metrics", surface: "metrics" },
  { value: "activity", label: "Activity", surface: "activity" },
  { value: "alerts", label: "Alerts" },
  { value: "history", label: "History" },
  { value: "settings", label: "Settings" },
] as const satisfies readonly { value: string; label: string; surface?: FeatureFlagName }[];

export type DatabaseTab = (typeof tabs)[number]["value"];

type DatabaseTabsProps = {
  database: Database;
  tab: DatabaseTab;
  onTabChange: (tab: DatabaseTab) => void;
  /** True once this developer has copied the connection string before. */
  connectionCopied: boolean;
  /** Opens the connect dialog, which the page owns. */
  onConnect: () => void;
  onBackUpNow: () => void;
  /** True while a backup is being started, which is a second or two. */
  backingUp: boolean;
  /** False when the environment is offline and the hub cannot carry the request. */
  backupEnabled?: boolean;
  /** Opens the restore dialog on one backup. */
  onRestore?: (backup: Backup) => void;
  /** Opens the delete dialog from the Settings danger zone. */
  onDelete: () => void;
  /** The chart the Metrics tab opens on, set by a chat action. */
  metricsFocus?: MetricKind;
};

export function DatabaseTabs({
  database,
  tab,
  onTabChange,
  connectionCopied,
  onConnect,
  onBackUpNow,
  backingUp,
  backupEnabled = true,
  onRestore,
  onDelete,
  metricsFocus,
}: DatabaseTabsProps) {
  const backupsOn = useFeature("backups");
  const metricsOn = useFeature("metrics");
  const activityOn = useFeature("activity");

  const surfaceOn: Partial<Record<FeatureFlagName, boolean>> = {
    backups: backupsOn,
    metrics: metricsOn,
    activity: activityOn,
  };
  const visible = tabs.filter((entry) => !("surface" in entry) || surfaceOn[entry.surface]);
  // The tab the page asks for can be one that is turned off, and a value with
  // no trigger leaves the strip with nothing selected.
  const reachable = visible.some((entry) => entry.value === tab);
  const current = reachable ? tab : "overview";

  // The page owns the tab, and an action that asks for a tab that is off has
  // to land somewhere rather than appear to do nothing.
  useEffect(() => {
    if (!reachable) {
      onTabChange("overview");
    }
  }, [reachable, onTabChange]);

  return (
    <Tabs value={current} onValueChange={(value) => onTabChange(value as DatabaseTab)}>
      <TabsList>
        {visible.map((entry) => (
          <TabsTrigger key={entry.value} value={entry.value}>
            {entry.label}
          </TabsTrigger>
        ))}
      </TabsList>

      <TabsContent value="overview">
        <Overview database={database} connectionCopied={connectionCopied} onConnect={onConnect} />
      </TabsContent>

      <TabsContent value="backups">
        <BackupsTab
          database={database}
          onBackUpNow={onBackUpNow}
          backingUp={backingUp}
          backupEnabled={backupEnabled}
          onRestore={onRestore}
        />
      </TabsContent>

      <TabsContent value="metrics">
        <MetricsTab databaseId={database.id} focusKind={metricsFocus} />
      </TabsContent>

      <TabsContent value="activity">
        <DatabaseActivity databaseId={database.id} />
      </TabsContent>

      <TabsContent value="alerts">
        <DatabaseAlertsTab databaseId={database.id} />
      </TabsContent>

      <TabsContent value="history">
        <HistoryTab databaseId={database.id} />
      </TabsContent>

      <TabsContent value="settings">
        <SettingsTab database={database} onDelete={onDelete} />
      </TabsContent>
    </Tabs>
  );
}

// The Live toggle is per tab state rather than page state: it belongs to the
// feed the reader is looking at, and leaving the tab is a fine time to forget
// they paused it.
function DatabaseActivity({ databaseId }: { databaseId: string }) {
  const [live, setLive] = useState(true);

  return <ActivityFeed databaseId={databaseId} live={live} onLiveChange={setLive} />;
}

type OverviewProps = {
  database: Database;
  connectionCopied: boolean;
  onConnect: () => void;
};

/**
 * The overview: the connection card, then the details, then the metric minis.
 * The connection card leads until the developer has copied the string once;
 * after that the metrics take the top spot, because the question changes from
 * "how do I connect" to "how is it doing".
 */
function Overview({ database, connectionCopied, onConnect }: OverviewProps) {
  const metricsOn = useFeature("metrics");

  const connect = (
    <div key="connect" data-block="connect">
      <ConnectCard database={database} onConnect={onConnect} />
    </div>
  );

  const metrics = metricsOn ? (
    <section key="metrics" data-block="metrics">
      <OverviewMetrics databaseId={database.id} />
    </section>
  ) : null;

  const details = <Details key="details" database={database} />;

  const blocks = connectionCopied ? [metrics, connect, details] : [connect, details, metrics];

  return (
    <div data-testid="overview-blocks" className="space-y-6">
      {blocks}
    </div>
  );
}

function Details({ database }: { database: Database }) {
  const profile = findProfile(database.profile);
  const created = formatRelativeTime(database.createdAt);
  const lastBackup = formatRelativeTime(database.lastBackupAt);

  return (
    <section data-block="details">
      <Card>
        <CardHeader>
          <CardTitle>Details</CardTitle>
        </CardHeader>
        <CardContent className="pt-0">
          <dl className="grid gap-4 sm:grid-cols-3">
            <Detail label="Profile">
              {profile.label}
              <span className="text-muted-foreground"> ({profile.consequence})</span>
            </Detail>
            <Detail label="Namespace">
              <span className="font-mono">{database.namespace}</span>
            </Detail>
            <Detail label="Members">
              <span className="font-mono">{database.members}</span>
            </Detail>
            <Detail label="Size">
              <span className="font-mono">
                {database.cpu} CPU, {database.memoryGb} GB memory, {database.storageGb} GB storage
              </span>
            </Detail>
            <Detail label="Created" title={formatTimestamp(database.createdAt)}>
              {created ?? "Not reported"}
            </Detail>
            <Detail label="Last backup" title={formatTimestamp(database.lastBackupAt)}>
              {lastBackup ?? "No backups yet"}
            </Detail>
          </dl>
        </CardContent>
      </Card>
    </section>
  );
}

type DetailProps = {
  label: string;
  title?: string | null;
  children: ReactNode;
};

function Detail({ label, title, children }: DetailProps) {
  return (
    <div className="space-y-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-sm" title={title ?? undefined}>
        {children}
      </dd>
    </div>
  );
}
