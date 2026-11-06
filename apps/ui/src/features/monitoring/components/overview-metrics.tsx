import { cn } from "@/lib/utils";
import { findMetric } from "../api";
import { useDatabaseMetrics } from "../hooks/use-database-metrics";
import { overviewMetricKinds } from "../types";
import { MetricMini } from "./metric-mini";

/**
 * The three minis on the Overview: CPU, memory, connections. One adapter call
 * feeds all three, and every mini carries the same state, so the row never
 * mixes a chart with a skeleton.
 */
export function OverviewMetrics({
  databaseId,
  className,
}: {
  databaseId: string;
  className?: string;
}) {
  const { data, error, isPending } = useDatabaseMetrics(databaseId, {
    kinds: overviewMetricKinds,
  });

  return (
    <div className={cn("grid gap-4 sm:grid-cols-3", className)}>
      {overviewMetricKinds.map((kind) => (
        <MetricMini
          key={kind}
          kind={kind}
          series={findMetric(data, kind)}
          loading={isPending && !error}
          error={error}
        />
      ))}
    </div>
  );
}
