import { ApiError, requestJson } from "@/lib/api";
import { databaseResourcePath } from "@/features/databases/api";
import type { MetricKind, MetricWindow, MetricsSnapshot } from "./types";

type MetricsQuery = {
  window: MetricWindow;
  kinds: readonly MetricKind[];
  signal?: AbortSignal;
};

/**
 * Reads the metric series behind the Overview minis and the Metrics tab.
 *
 * The adapter answers a pending snapshot while PMM has no target, so a caller
 * checks `pending` rather than treating an empty body as a failure. A 200 that
 * is not a snapshot means /api is not proxied, and that is a failure.
 */
export async function fetchDatabaseMetrics(
  id: string,
  { window, kinds, signal }: MetricsQuery,
): Promise<MetricsSnapshot> {
  const params = new URLSearchParams({ window, metrics: kinds.join(",") });
  const payload = await requestJson<MetricsSnapshot>(
    `/api/databases/${databaseResourcePath(id)}/metrics?${params.toString()}`,
    { signal },
  );

  if (!payload || !Array.isArray(payload.metrics)) {
    throw new ApiError("ErrInternal", "The metrics response could not be read.", 200);
  }

  return payload;
}

/** One chart out of a snapshot, or undefined while the snapshot has not landed. */
export function findMetric(
  snapshot: MetricsSnapshot | undefined,
  kind: MetricKind,
): MetricsSnapshot["metrics"][number] | undefined {
  return snapshot?.metrics.find((series) => series.kind === kind);
}
