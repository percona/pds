import { Link } from "react-router";
import { Badge } from "@/components/ui/badge";
import { DatabaseAlertIndicator } from "@/features/alerts";
import type { AlertSummary } from "@/features/alerts";
import { TeamLabel } from "@/features/teams";
import { findProfile } from "@/features/profiles";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { databaseHref } from "../api";
import { databaseStatus } from "../status";
import type { Database } from "../types";
import { CpuSparkline } from "./cpu-sparkline";
import { EngineIcon, engineLabel } from "./engine-icon";
import { ScheduleBackupButton } from "./schedule-backup-button";
import { MetricsPendingPill, StatusPill } from "./status-pill";

type DatabaseCardProps = {
  database: Database;
  /** CPU percentages, oldest first, once PMM reports the target. */
  cpuSeries?: number[];
  /** The database's firing alerts, absent when it has none. */
  alerts?: AlertSummary;
  /** False when the environment has never reported, so a schedule cannot be saved. */
  scheduleEnabled?: boolean;
};

/**
 * One row of the databases list. The name and the readings are the link.
 * Schedule backup sits beside that link, because a button cannot live inside it.
 */
export function DatabaseCard({
  database,
  cpuSeries,
  alerts,
  scheduleEnabled = true,
}: DatabaseCardProps) {
  const status = databaseStatus(database);
  const profile = findProfile(database.profile);
  const lastBackup = formatRelativeTime(database.lastBackupAt);

  return (
    <li
      data-testid={`database-row-${database.id}`}
      className="grid grid-cols-[1fr_auto] items-center gap-x-4 gap-y-2 px-5 py-4 hover:bg-accent sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto_auto]"
    >
      <Link to={databaseHref(database.id)} className="contents">
        <div className="min-w-0 space-y-1">
          <p className="truncate font-mono text-sm font-medium">{database.name}</p>
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <EngineIcon engine={database.engine} className="size-3.5" />
            {engineLabel(database.engine, database.version)}
          </span>
          {database.team ? <TeamLabel name={database.team} /> : null}
          {alerts ? (
            <div>
              <DatabaseAlertIndicator summary={alerts} />
            </div>
          ) : null}
        </div>

        <div className="flex items-center gap-2">
          <StatusPill kind={status.kind} label={status.label} />
          <Badge variant="outline">{profile.label}</Badge>
        </div>

        <div className="space-y-0.5">
          <p className="text-xs text-muted-foreground">Last backup</p>
          <p className="text-xs" title={formatTimestamp(database.lastBackupAt) ?? undefined}>
            {lastBackup ?? "No backups yet"}
          </p>
        </div>

        {cpuSeries && cpuSeries.length > 1 ? (
          <CpuSparkline points={cpuSeries} />
        ) : (
          <MetricsPendingPill />
        )}
      </Link>
      <ScheduleBackupButton database={database} enabled={scheduleEnabled} />
    </li>
  );
}
