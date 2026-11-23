import { Clock, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { MONGODB_BACKUPS_DELETED, POSTGRESQL_BACKUPS_STAY } from "../confirm";
import type { Database } from "../types";

type SettingsTabProps = {
  database: Database;
  /** Opens the delete dialog, which the page owns. */
  onDelete: () => void;
};

/**
 * The Settings tab. Editing the name, the profile, and the backup destination is
 * not built, so the tab says so rather than offering a field that cannot be
 * saved. Delete is built, and it sits at the bottom behind its own heading: it
 * is never the button next to the one someone meant to press.
 */
export function SettingsTab({ database, onDelete }: SettingsTabProps) {
  return (
    <div className="space-y-6">
      <Card
        data-testid="settings-not-editable"
        className="flex flex-col items-center gap-3 px-6 py-12 text-center"
      >
        <Clock className="size-5 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
        <div className="space-y-1">
          <p className="text-sm font-medium">
            Name, profile, and backup destination are not editable yet.
          </p>
          <p className="text-sm text-muted-foreground">
            Scale changes CPU, memory, storage, and members today. The rest lands in a later
            release.
          </p>
        </div>
      </Card>

      <Card data-testid="danger-zone" className="border-destructive">
        <CardHeader>
          <CardTitle>Danger zone</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap items-center justify-between gap-4 pt-0">
          <p className="text-sm text-muted-foreground">
            Deleting <span className="font-mono">{database.name}</span> takes its data
            {database.engine === "postgresql" ? "" : " and its backups"} with it.
            {database.engine === "postgresql" ? ` ${POSTGRESQL_BACKUPS_STAY}` : ""}
            {database.engine === "mongodb" ? ` ${MONGODB_BACKUPS_DELETED}` : ""}
          </p>
          <Button
            variant="outline"
            className="text-destructive hover:text-destructive"
            onClick={onDelete}
          >
            <Trash2 strokeWidth={1.5} aria-hidden="true" />
            Delete
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
