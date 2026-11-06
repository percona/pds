import { CircleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { MetricsPendingPill } from "@/features/databases";
import { cn } from "@/lib/utils";
import { formatMetricValue, metricLabel, trendLabel } from "../format";
import { useDatabaseMetrics } from "../hooks/use-database-metrics";
import type { MetricKind, MetricSeries, MetricWindow } from "../types";
import { allMetricKinds, metricWindows } from "../types";
import { MetricChart } from "./metric-chart";

export type MetricsTabProps = {
  databaseId: string;
  /**
   * The chart to open on. It is how an alert or a chat action lands on the
   * series it is about rather than on a grid of five the developer has to scan.
   */
  focusKind?: MetricKind;
};

/** The Metrics tab: five charts and the window toggle. */
export function MetricsTab({ databaseId, focusKind }: MetricsTabProps) {
  const [window, setWindow] = useState<MetricWindow>("1h");
  const { data, error, isPending, isFetching, refetch } = useDatabaseMetrics(databaseId, {
    window,
    kinds: allMetricKinds,
  });

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-4">
        <p className="text-sm text-muted-foreground">
          {data?.pending ? data.pendingReason : windowDescription(window)}
        </p>
        <WindowToggle window={window} onChange={setWindow} />
      </div>

      {error ? (
        <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
          <CircleAlert
            className="size-5 text-muted-foreground"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <div className="space-y-1">
            <p className="text-sm font-medium">Metrics could not be read</p>
            <p className="text-sm text-muted-foreground">{error.message}</p>
          </div>
          <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
            Try again
          </Button>
        </Card>
      ) : null}

      {isPending && !error ? <MetricsSkeleton /> : null}

      {data ? (
        <div className="grid gap-4 sm:grid-cols-2">
          {data.metrics.map((series) => (
            <MetricPanel key={series.kind} series={series} focused={series.kind === focusKind} />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function windowDescription(window: MetricWindow): string {
  return window === "1h"
    ? "The last hour, one point a minute."
    : "The last day, one point every 15 minutes.";
}

type WindowToggleProps = {
  window: MetricWindow;
  onChange: (window: MetricWindow) => void;
};

/**
 * The 1h and 24h toggle. Both buttons are outline: purple is reserved for the
 * one primary action on a screen, and picking a window is not it.
 */
function WindowToggle({ window, onChange }: WindowToggleProps) {
  return (
    <div role="group" aria-label="Time window" className="flex gap-1">
      {metricWindows.map((option) => {
        const active = option === window;

        return (
          <Button
            key={option}
            variant="outline"
            size="sm"
            aria-pressed={active}
            className={cn("font-mono", active && "bg-accent")}
            onClick={() => onChange(option)}
          >
            {option}
          </Button>
        );
      })}
    </div>
  );
}

type MetricPanelProps = {
  series: MetricSeries;
  /** True for the chart an alert or a chat action sent the developer here to read. */
  focused: boolean;
};

/** One chart with the numbers a chart cannot carry on its own. */
function MetricPanel({ series, focused }: MetricPanelProps) {
  const hasReadings = !series.pending && series.points.length > 0;
  const panel = useRef<HTMLDivElement>(null);

  // A grid of five needs somewhere to land the developer, and moving focus
  // rather than only scrolling keeps the keyboard path on the same chart.
  useEffect(() => {
    if (!focused) {
      return;
    }

    panel.current?.focus();
    panel.current?.scrollIntoView?.({ block: "nearest" });
  }, [focused]);

  return (
    <Card
      ref={panel}
      tabIndex={focused ? -1 : undefined}
      data-testid={`metric-panel-${series.kind}`}
      data-kind={series.kind}
      data-focused={focused || undefined}
      className="space-y-4 px-5 py-4"
    >
      <div className="flex items-baseline justify-between gap-3">
        <p className="text-base font-semibold">{metricLabel(series.kind)}</p>
        {series.pending ? <MetricsPendingPill /> : null}
        {hasReadings ? (
          <span className="flex items-baseline gap-2">
            <span data-testid="metric-current" className="font-mono text-lg">
              {formatMetricValue(series.current, series.unit)}
            </span>
            <span className="text-xs text-muted-foreground">{trendLabel(series.trend)}</span>
          </span>
        ) : null}
      </div>

      {hasReadings ? (
        <>
          <MetricChart series={series} />
          <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
            <Summary id="min" label="Low" value={formatMetricValue(series.min, series.unit)} />
            <Summary id="max" label="High" value={formatMetricValue(series.max, series.unit)} />
            <Summary
              id="average"
              label="Average"
              value={formatMetricValue(series.average, series.unit)}
            />
          </dl>
        </>
      ) : null}

      {!series.pending && series.points.length === 0 ? (
        <p className="text-sm text-muted-foreground">No readings in this window</p>
      ) : null}
    </Card>
  );
}

type SummaryProps = {
  id: string;
  label: string;
  value: string;
};

function Summary({ id, label, value }: SummaryProps) {
  return (
    <div data-testid={`metric-summary-${id}`} className="flex gap-1">
      <dt>{label}</dt>
      <dd className="font-mono text-foreground">{value}</dd>
    </div>
  );
}

/** Skeletons shaped like the loaded panels, two to a row. */
function MetricsSkeleton() {
  return (
    <div data-testid="metrics-loading" className="grid gap-4 sm:grid-cols-2">
      {allMetricKinds.map((kind) => (
        <Card key={kind} className="space-y-4 px-5 py-4">
          <div className="flex items-baseline justify-between gap-3">
            <Skeleton className="h-5 w-28" />
            <Skeleton className="h-5 w-16" />
          </div>
          <Skeleton className="h-40 w-full" />
          <Skeleton className="h-3 w-48" />
        </Card>
      ))}
    </div>
  );
}
