import { DatabaseBackup } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { cn } from "@/lib/utils";
import { backupStatus, canRestore, emptyBackups, formatBackupSize } from "../backups";
import type { Backup, BackupSchedule } from "../types";
import { StatusPill } from "./status-pill";

type BackupListProps = {
  /** Newest first, the order the adapter returns them in. */
  backups: Backup[];
  /** Absent when this database takes on-demand backups only. */
  schedule?: BackupSchedule;
  onBackUpNow: () => void;
  /** True while a backup is being started, which is a second or two. */
  backingUp?: boolean;
  /** False when the environment is offline and the hub cannot carry the request. */
  backUpEnabled?: boolean;
  /** Opens the restore dialog on one row. */
  onRestore?: (backup: Backup) => void;
  className?: string;
};

/**
 * The backup list. Five columns is the table budget, so the row says when it
 * ran, how big it is, how it got there, how it went, and offers the one action
 * a backup has.
 */
export function BackupList({
  backups,
  schedule,
  onBackUpNow,
  backingUp = false,
  backUpEnabled = true,
  onRestore,
  className,
}: BackupListProps) {
  if (backups.length === 0) {
    const empty = emptyBackups(schedule);

    return (
      <Card
        data-testid="backups-empty"
        className={cn("flex flex-col items-center gap-3 px-6 py-12 text-center", className)}
      >
        <DatabaseBackup
          className="size-5 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <div className="space-y-1">
          <p className="text-sm font-medium">{empty.title}</p>
          <p className="text-sm text-muted-foreground">{empty.detail}</p>
        </div>
        <Button loading={backingUp} disabled={!backUpEnabled} onClick={onBackUpNow}>
          {backingUp ? "Backing up..." : "Back up now"}
        </Button>
      </Card>
    );
  }

  return (
    <Card className={cn("overflow-hidden", className)}>
      <Table data-testid="backup-list">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Started</TableHead>
            <TableHead>Size</TableHead>
            <TableHead>Type</TableHead>
            <TableHead>Status</TableHead>
            <TableHead className="text-right">
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {backups.map((backup) => (
            <BackupRow key={backup.id} backup={backup} onRestore={onRestore} />
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

type BackupRowProps = {
  backup: Backup;
  onRestore?: (backup: Backup) => void;
};

function BackupRow({ backup, onRestore }: BackupRowProps) {
  const status = backupStatus(backup);
  const started = formatRelativeTime(backup.createdAt);
  const size = formatBackupSize(backup.sizeBytes);

  return (
    <TableRow data-testid={`backup-row-${backup.id}`}>
      <TableCell title={formatTimestamp(backup.createdAt) ?? undefined}>
        {started ?? "Not reported"}
      </TableCell>
      <TableCell className={cn(size ? "font-mono" : "text-muted-foreground")}>
        {size ?? "Not reported"}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {backup.scheduled ? "Scheduled" : "On demand"}
      </TableCell>
      <TableCell>
        <div className="space-y-1">
          <StatusPill kind={status.kind} label={status.label} />
          {backup.message ? (
            <p className="max-w-64 text-xs text-muted-foreground">{backup.message}</p>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="text-right">
        <Button
          variant="outline"
          size="sm"
          disabled={!onRestore || !canRestore(backup)}
          onClick={() => onRestore?.(backup)}
        >
          Restore
        </Button>
      </TableCell>
    </TableRow>
  );
}
