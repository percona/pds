import { BellRing, CircleCheck, Info } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useDatabaseAlerts } from "../hooks/use-database-alerts";
import { AlertRow } from "./alert-row";
import { EmptyCard } from "./empty-card";
import { environmentCutAlerts } from "../summary";
import { EnvironmentBanner } from "./environment-banner";
import { EnvironmentNote } from "./environment-note";

/**
 * One database's firing alerts. When the environment has no current reading,
 * the banner says why and the empty state says the alerts are unknown, so a
 * database nobody can read never looks healthy. The same holds when the
 * environment cut its list, since this database's alert may be one left out.
 */
export function DatabaseAlertsTab({ databaseId }: { databaseId: string }) {
  const { data, error, isPending, refetch } = useDatabaseAlerts(databaseId);

  if (isPending) {
    return (
      <div
        data-testid="database-alerts-loading"
        className="divide-y divide-border rounded-lg border border-border bg-card"
      >
        {[0, 1].map((row) => (
          <div key={row} className="space-y-2 px-4 py-3">
            <Skeleton className="h-4 w-64" />
            <Skeleton className="h-3 w-96" />
          </div>
        ))}
      </div>
    );
  }

  if (!data) {
    return (
      <div role="alert" className="space-y-3 rounded-lg border border-border bg-card px-4 py-4">
        <p className="text-sm font-medium">Alerts could not be read</p>
        <p className="text-sm text-muted-foreground">
          {error?.message ?? "The alerts response was empty."}
        </p>
        <Button variant="outline" size="sm" onClick={() => void refetch()}>
          Try again
        </Button>
      </div>
    );
  }

  const { environment, alerts } = data;
  const live = environment.state === "live";
  const cut = environmentCutAlerts(environment);

  return (
    <div className="space-y-6">
      {error ? (
        <EnvironmentNote
          role="status"
          note="Alerts could not be refreshed. The list shows the last reading."
        />
      ) : null}

      {live ? null : <EnvironmentBanner environment={environment} />}

      {cut && environment.note ? <EnvironmentNote note={environment.note} /> : null}

      {alerts.length > 0 ? (
        <ul className="divide-y divide-border rounded-lg border border-border bg-card">
          {alerts.map((alert) => (
            <AlertRow key={alert.id} alert={alert} readAt={environment.readAt} />
          ))}
        </ul>
      ) : live && cut ? (
        <EmptyCard
          icon={Info}
          title="Alerts for this database may be incomplete"
          body="Its environment has more firing alerts than the list holds, so an alert for this database may be among those left out."
        />
      ) : live && error ? (
        <EmptyCard
          icon={Info}
          title="No alerts were firing at the last reading"
          body="The latest read failed, so this may be out of date."
        />
      ) : live ? (
        <EmptyCard
          icon={CircleCheck}
          title="No alerts are firing for this database"
          body="Its environment reported its monitoring recently."
        />
      ) : (
        <EmptyCard
          icon={BellRing}
          title="Alerts for this database are unknown"
          body="Its environment has no current reading, so nothing can be said about its alerts yet."
        />
      )}
    </div>
  );
}
