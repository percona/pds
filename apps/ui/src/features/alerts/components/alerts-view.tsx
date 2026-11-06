import { BellRing, CircleCheck, Server } from "lucide-react";
import { Link, useSearchParams } from "react-router";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Skeleton } from "@/components/ui/skeleton";
import { useAlerts } from "../hooks/use-alerts";
import type { AlertEnvironment, AlertReport, AlertSeverity } from "../types";
import { AlertRow } from "./alert-row";
import { EmptyCard } from "./empty-card";
import { EnvironmentBanner } from "./environment-banner";
import { EnvironmentNote } from "./environment-note";

const severities: { value: AlertSeverity; label: string }[] = [
  { value: "critical", label: "Critical" },
  { value: "warning", label: "Warning" },
  { value: "info", label: "Info" },
];

/**
 * Every environment's firing alerts, with a line for each environment that has
 * no current reading. The filters live in the URL so a link or a reload keeps
 * them.
 */
export function AlertsView() {
  const { data, error, isPending, refetch } = useAlerts();
  const [params, setParams] = useSearchParams();
  const severity = params.get("severity") ?? "";
  const requested = params.get("environment") ?? "";

  if (isPending) {
    return <AlertsSkeleton />;
  }

  if (error || !data) {
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

  const setFilter = (key: "severity" | "environment", value: string) => {
    const next = new URLSearchParams(params);
    if (value) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    setParams(next);
  };

  // A link can name an environment that is gone, and a filter nobody can see
  // or clear must not hide every row.
  const filtered = data.environments.find((env) => env.name === requested);
  const environment = filtered ? requested : "";
  const readAt = new Map(data.environments.map((env) => [env.name, env.readAt]));
  const shown = data.alerts.filter(
    (alert) =>
      (!severity || alert.severity === severity) &&
      (!environment || alert.environment === environment),
  );
  const unavailable = data.environments.filter((env) => env.state !== "live");
  const notes = data.environments.filter((env) => env.note);

  return (
    <div className="space-y-6">
      {unavailable.length > 0 ? (
        <div className="space-y-2">
          {unavailable.map((env) => (
            <EnvironmentBanner key={env.name} environment={env} />
          ))}
        </div>
      ) : null}

      {data.environments.length > 0 ? (
        <div className="flex flex-wrap items-end gap-4">
          <label className="space-y-1.5 text-sm font-medium">
            <span className="block">Severity</span>
            <NativeSelect value={severity} onChange={(e) => setFilter("severity", e.target.value)}>
              <option value="">All severities</option>
              {severities.map((item) => (
                <option key={item.value} value={item.value}>
                  {item.label}
                </option>
              ))}
            </NativeSelect>
          </label>
          <label className="space-y-1.5 text-sm font-medium">
            <span className="block">Environment</span>
            <NativeSelect
              value={environment}
              onChange={(e) => setFilter("environment", e.target.value)}
            >
              <option value="">All environments</option>
              {data.environments.map((env) => (
                <option key={env.name} value={env.name}>
                  {env.name}
                </option>
              ))}
            </NativeSelect>
          </label>
        </div>
      ) : null}

      {notes.map((env) => (
        <EnvironmentNote key={env.name} note={env.note ?? ""} />
      ))}

      {shown.length > 0 ? (
        <ul className="divide-y divide-border rounded-lg border border-border bg-card">
          {shown.map((alert) => (
            <AlertRow key={alert.id} alert={alert} readAt={readAt.get(alert.environment)} />
          ))}
        </ul>
      ) : (
        <AlertsEmpty
          report={data}
          environment={filtered}
          filtered={data.alerts.length > 0}
          onClear={() => setParams(new URLSearchParams())}
        />
      )}
    </div>
  );
}

type AlertsEmptyProps = {
  report: AlertReport;
  /** The environment the list is narrowed to, when it is. */
  environment?: AlertEnvironment;
  /** True when alerts exist but the filters hide all of them. */
  filtered: boolean;
  onClear: () => void;
};

function AlertsEmpty({ report, environment, filtered, onClear }: AlertsEmptyProps) {
  if (environment && environment.state !== "live") {
    return (
      <EmptyCard
        icon={BellRing}
        title={`${environment.name} has no current reading`}
        body="Its alerts are unknown until monitoring is read again. The line above says why."
      >
        <Button variant="outline" size="sm" onClick={onClear}>
          Clear filters
        </Button>
      </EmptyCard>
    );
  }

  if (filtered) {
    return (
      <EmptyCard icon={BellRing} title="No alerts match these filters">
        <Button variant="outline" size="sm" onClick={onClear}>
          Clear filters
        </Button>
      </EmptyCard>
    );
  }

  if (report.environments.length === 0) {
    return (
      <EmptyCard
        icon={Server}
        title="No environments yet"
        body="Alerts appear here once an environment is connected."
      >
        <Button asChild size="sm">
          <Link to="/environments/new">Create an environment</Link>
        </Button>
      </EmptyCard>
    );
  }

  // Nothing firing is only good news when every environment could be read.
  if (report.environments.every((env) => env.state === "live")) {
    return (
      <EmptyCard
        icon={CircleCheck}
        title="No alerts are firing"
        body="Every environment reported its monitoring recently."
      />
    );
  }

  return (
    <EmptyCard
      icon={BellRing}
      title="No firing alerts in the environments that reported"
      body="The environments above have no current reading, so their alerts are unknown."
    />
  );
}

function AlertsSkeleton() {
  return (
    <div
      data-testid="alerts-loading"
      className="divide-y divide-border rounded-lg border border-border bg-card"
    >
      {[0, 1, 2].map((row) => (
        <div key={row} className="space-y-2 px-4 py-3">
          <Skeleton className="h-4 w-64" />
          <Skeleton className="h-3 w-96" />
        </div>
      ))}
    </div>
  );
}
