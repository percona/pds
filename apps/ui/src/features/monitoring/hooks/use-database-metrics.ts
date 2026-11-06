import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchDatabaseMetrics } from "../api";
import type { MetricKind, MetricWindow, MetricsSnapshot } from "../types";
import { allMetricKinds } from "../types";

/**
 * How often a chart re-reads. Charts are a trend rather than a status, so this
 * is slower than the five second status poll: a 1h window at a 1 minute step
 * gains one point every minute.
 */
export const metricsPollMs = 30_000;

/**
 * The cache key. The set of charts is part of it, so the Overview minis and the
 * Metrics tab do not overwrite each other with a narrower answer.
 */
export function metricsKey(id: string, window: MetricWindow, kinds: readonly MetricKind[]) {
  return ["metrics", id, window, kinds.join(",")] as const;
}

type UseDatabaseMetricsOptions = {
  window?: MetricWindow;
  kinds?: readonly MetricKind[];
  /** False stops the read, for a database that has nothing to report yet. */
  enabled?: boolean;
};

export function useDatabaseMetrics(id: string, options: UseDatabaseMetricsOptions = {}) {
  const { window = "1h", kinds = allMetricKinds, enabled = true } = options;

  return useQuery<MetricsSnapshot, ApiError>({
    queryKey: metricsKey(id, window, kinds),
    queryFn: ({ signal }) => fetchDatabaseMetrics(id, { window, kinds, signal }),
    enabled: enabled && id !== "",
    refetchInterval: metricsPollMs,
  });
}
