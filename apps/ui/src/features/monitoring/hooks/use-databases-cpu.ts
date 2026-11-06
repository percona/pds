import { useQueries } from "@tanstack/react-query";
import { useFeature } from "@/features/flags";
import { fetchDatabaseMetrics, findMetric } from "../api";
import { metricsKey, metricsPollMs } from "./use-database-metrics";

/** The list row draws a CPU line, so it asks for that one chart. */
const listKinds = ["cpu"] as const;

/**
 * CPU percentages per database id, for the sparkline on each list row. One
 * call per database, narrowed to the CPU chart, so a row that PMM has not
 * registered costs the adapter a single probe query.
 *
 * A row with no data is absent from the result, and the row then keeps its
 * pending badge. An adapter with metrics turned off is asked for nothing: the
 * route answers 501 there, and a row without a line is what the list shows
 * anyway.
 */
export function useDatabasesCpu(ids: string[]): Record<string, number[]> {
  const metricsOn = useFeature("metrics");
  const wanted = metricsOn ? ids : [];
  const results = useQueries({
    queries: wanted.map((id) => ({
      queryKey: metricsKey(id, "1h" as const, listKinds),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        fetchDatabaseMetrics(id, { window: "1h", kinds: listKinds, signal }),
      refetchInterval: metricsPollMs,
    })),
  });

  const series: Record<string, number[]> = {};

  results.forEach((result, index) => {
    const id = wanted[index];
    const cpu = findMetric(result.data, "cpu");

    if (id === undefined || !cpu || cpu.pending || cpu.points.length === 0) {
      return;
    }

    series[id] = cpu.points.map((point) => point.v);
  });

  return series;
}
