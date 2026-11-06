import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { MetricsPendingPill } from "@/features/databases";
import { formatMetricValue, metricLabel, trendLabel } from "../format";
import type { MetricKind, MetricSeries } from "../types";
import { MetricChart } from "./metric-chart";

type MetricMiniProps = {
  kind: MetricKind;
  /** Absent while the read has not landed, which reads as pending. */
  series?: MetricSeries;
  loading?: boolean;
  error?: Error | null;
};

type MiniState = "loading" | "error" | "pending" | "empty" | "data";

function miniState(props: MetricMiniProps): MiniState {
  if (props.loading) return "loading";
  if (props.error) return "error";
  if (!props.series || props.series.pending) return "pending";

  return props.series.points.length === 0 ? "empty" : "data";
}

/**
 * One metric mini on the Overview. The card is the same size in every state, so
 * the layout does not jump between a pending badge and a chart.
 */
export function MetricMini(props: MetricMiniProps) {
  const { kind, series } = props;
  const state = miniState(props);

  return (
    <Card
      data-testid={`metric-mini-${kind}`}
      data-state={state}
      className="space-y-3 px-5 py-4"
      aria-busy={state === "loading" || undefined}
    >
      <p className="text-sm text-muted-foreground">{metricLabel(kind)}</p>
      <MiniBody state={state} series={series} />
    </Card>
  );
}

function MiniBody({ state, series }: { state: MiniState; series?: MetricSeries }) {
  if (state === "loading") {
    return (
      <div className="space-y-2">
        <Skeleton className="h-6 w-16" />
        <Skeleton className="h-8 w-full" />
      </div>
    );
  }

  if (state === "error") {
    return <p className="text-sm text-muted-foreground">Metrics could not be read</p>;
  }

  if (state === "pending" || !series) {
    return <MetricsPendingPill />;
  }

  if (state === "empty") {
    return <p className="text-sm text-muted-foreground">No readings in this window</p>;
  }

  return (
    <div className="space-y-1">
      <div className="flex items-baseline gap-2">
        <span className="font-mono text-lg">{formatMetricValue(series.current, series.unit)}</span>
        <span className="text-xs text-muted-foreground">{trendLabel(series.trend)}</span>
      </div>
      <MetricChart series={series} compact />
    </div>
  );
}
