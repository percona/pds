import { CalendarClock, CircleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { scheduleSummary } from "../backups";
import { useBackups } from "../hooks/use-backups";
import type { Backup, Database } from "../types";
import { BackupList } from "./backup-list";
import { BackupScheduleEditor } from "./backup-schedule-editor";

type BackupsTabProps = {
  database: Database;
  onBackUpNow: () => void;
  /** True while a backup is being started, which is a second or two. */
  backingUp: boolean;
  /** False when the environment is offline and the hub cannot carry the request. */
  backupEnabled?: boolean;
  /** Opens the restore dialog on one row. */
  onRestore?: (backup: Backup) => void;
};

/**
 * The Backups tab: what the schedule will do and the editor that changes it,
 * then what it has done. Back up now lives in the page header, which stays on
 * screen above every tab, beside the editor, and in the empty state, which is
 * where a first backup is asked for.
 */
export function BackupsTab({
  database,
  onBackUpNow,
  backingUp,
  backupEnabled = true,
  onRestore,
}: BackupsTabProps) {
  const { data, error, isPending, isFetching, refetch } = useBackups(database.id, {
    enabled: true,
  });
  const schedule = scheduleSummary(database.backupSchedule);
  // The empty list asks for a first backup itself, so the button joins the
  // editor only once there is a list for it to add to.
  const hasBackups = (data?.length ?? 0) > 0;

  return (
    <div className="space-y-6">
      <Card className="space-y-4 px-5 py-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div data-testid="backup-schedule" className="flex items-start gap-2">
            <CalendarClock
              className="mt-0.5 size-4 shrink-0 text-muted-foreground"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <p className="text-sm">
              {schedule.headline}
              <span className="text-muted-foreground"> {schedule.detail}</span>
            </p>
          </div>
          {hasBackups ? (
            <Button
              variant="outline"
              loading={backingUp}
              disabled={!backupEnabled}
              onClick={onBackUpNow}
            >
              {backingUp ? "Backing up..." : "Back up now"}
            </Button>
          ) : null}
        </div>
        <BackupScheduleEditor database={database} enabled={backupEnabled} />
      </Card>

      {error ? (
        <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
          <CircleAlert
            className="size-5 text-muted-foreground"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <div className="space-y-1">
            <p className="text-sm font-medium">Backups could not be read</p>
            <p className="text-sm text-muted-foreground">{error.message}</p>
          </div>
          <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
            Try again
          </Button>
        </Card>
      ) : null}

      {isPending && !error ? <BackupsSkeleton /> : null}

      {data ? (
        <BackupList
          backups={data}
          schedule={database.backupSchedule}
          onBackUpNow={onBackUpNow}
          backingUp={backingUp}
          backUpEnabled={backupEnabled}
          onRestore={onRestore}
        />
      ) : null}
    </div>
  );
}

/** Rows the shape of the loaded ones, so the table does not jump when it arrives. */
function BackupsSkeleton() {
  return (
    <Card data-testid="backups-loading" className="space-y-3 px-5 py-4">
      {[0, 1, 2].map((row) => (
        <Skeleton key={row} className="h-8 w-full" />
      ))}
    </Card>
  );
}
