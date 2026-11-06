import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Feature } from "@/features/flags";
import type { Database } from "../types";
import { BackupScheduleEditor } from "./backup-schedule-editor";

type ScheduleBackupButtonProps = {
  database: Database;
  /** False when the environment has never reported, so a schedule cannot be saved. */
  enabled?: boolean;
};

/** Opens the backup schedule editor from a database row. */
export function ScheduleBackupButton({ database, enabled = true }: ScheduleBackupButtonProps) {
  const [open, setOpen] = useState(false);

  return (
    <Feature name="backups">
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="whitespace-nowrap"
        disabled={!enabled}
        onClick={() => setOpen(true)}
      >
        Schedule backup
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>Schedule backup</DialogTitle>
            <DialogDescription>
              Choose how often <span className="font-mono">{database.name}</span> is backed up.
            </DialogDescription>
          </DialogHeader>
          <BackupScheduleEditor database={database} enabled={enabled} />
        </DialogContent>
      </Dialog>
    </Feature>
  );
}
