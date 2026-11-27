import { DatabaseBackup, LifeBuoy, Scaling, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Feature } from "@/features/flags";
import { findProfile } from "@/features/profiles";
import { databaseStatus } from "../status";
import type { Database } from "../types";
import { EngineIcon, engineLabel } from "./engine-icon";
import { StatusPill } from "./status-pill";

type DatabaseHeaderProps = {
  database: Database;
  onScale: () => void;
  onBackUpNow: () => void;
  /** True while a backup is being started, which is a second or two. */
  backingUp: boolean;
  /** False when the environment is offline and the hub cannot carry the request. */
  backupEnabled?: boolean;
  /** Opens the delete dialog. */
  onDelete: () => void;
  /**
   * Opens the support dialog. Absent when this environment has no ServiceNow
   * Secret, so the header does not offer a button that cannot send.
   */
  onContactSupport?: () => void;
};

/**
 * The detail header. Connect is not here: it leads the Overview. Delete is
 * last and quiet, red in its label only, because the red fill belongs to the
 * confirm button inside the dialog it opens.
 */
export function DatabaseHeader({
  database,
  onScale,
  onBackUpNow,
  backingUp,
  backupEnabled = true,
  onDelete,
  onContactSupport,
}: DatabaseHeaderProps) {
  const status = databaseStatus(database);
  const profile = findProfile(database.profile);

  return (
    <header
      data-testid="database-header"
      className="flex flex-wrap items-start justify-between gap-4"
    >
      <div className="space-y-2">
        <div className="flex items-center gap-2">
          <EngineIcon engine={database.engine} className="size-5" />
          <h1 className="font-mono text-2xl font-semibold tracking-tight">{database.name}</h1>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm text-muted-foreground">
            {engineLabel(database.engine, database.version)}
          </span>
          <StatusPill kind={status.kind} label={status.label} />
          <Badge variant="outline">{profile.label}</Badge>
        </div>
      </div>

      <div data-testid="database-actions" className="flex flex-wrap items-center gap-2">
        <Button variant="outline" onClick={onScale}>
          <Scaling strokeWidth={1.5} aria-hidden="true" />
          Scale
        </Button>
        <Feature name="backups">
          <Button
            variant="outline"
            loading={backingUp}
            disabled={!backupEnabled}
            onClick={onBackUpNow}
          >
            {backingUp ? null : <DatabaseBackup strokeWidth={1.5} aria-hidden="true" />}
            {backingUp ? "Backing up..." : "Back up now"}
          </Button>
        </Feature>
        {onContactSupport ? (
          <Button variant="outline" onClick={onContactSupport}>
            <LifeBuoy strokeWidth={1.5} aria-hidden="true" />
            Contact support
          </Button>
        ) : null}
        <Button
          variant="ghost"
          className="text-destructive hover:text-destructive"
          onClick={onDelete}
        >
          <Trash2 strokeWidth={1.5} aria-hidden="true" />
          Delete
        </Button>
      </div>
    </header>
  );
}
