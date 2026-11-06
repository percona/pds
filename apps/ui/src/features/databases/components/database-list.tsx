import { Database as DatabaseGlyph } from "lucide-react";
import type { AlertSummary } from "@/features/alerts";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { emptyListDescription } from "../engines";
import type { Database } from "../types";
import { DatabaseCard } from "./database-card";

type DatabaseListProps = {
  databases: Database[];
  /** CPU percentages per database id, once PMM reports the targets. */
  cpuSeries?: Record<string, number[]>;
  /** Firing alerts per database id. A database with none has no entry. */
  alerts?: Record<string, AlertSummary>;
  /** False when the environment has never reported, so a schedule cannot be saved. */
  scheduleEnabled?: boolean;
  className?: string;
};

/** The databases list, with the empty state it renders on day one. */
export function DatabaseList({
  databases,
  cpuSeries,
  alerts,
  scheduleEnabled = true,
  className,
}: DatabaseListProps) {
  if (databases.length === 0) {
    return (
      <Card className={cn("flex flex-col items-center gap-3 px-6 py-12 text-center", className)}>
        <DatabaseGlyph
          className="size-5 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <div className="space-y-1">
          <p className="text-sm font-medium">No databases yet</p>
          <p className="text-sm text-muted-foreground">{emptyListDescription}</p>
        </div>
      </Card>
    );
  }

  return (
    <Card className={cn("overflow-hidden", className)}>
      <ul className="divide-y divide-border">
        {databases.map((database) => (
          <DatabaseCard
            key={database.id}
            database={database}
            cpuSeries={cpuSeries?.[database.id]}
            alerts={alerts?.[database.id]}
            scheduleEnabled={scheduleEnabled}
          />
        ))}
      </ul>
    </Card>
  );
}
