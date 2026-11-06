import type { MetricKind, MetricSeries, MetricTrend, MetricUnit } from "./types";

const labels: Record<MetricKind, string> = {
  cpu: "CPU",
  memory: "Memory",
  connections: "Connections",
  "replication-lag": "Replication lag",
  disk: "Disk",
};

/** The chart title. Sentence case, except the acronym. */
export function metricLabel(kind: MetricKind): string {
  return labels[kind];
}

/**
 * One reading as text. Percentages and seconds keep one decimal, which is the
 * precision PMM actually has; a connection count is a whole number.
 */
export function formatMetricValue(value: number, unit: MetricUnit): string {
  if (unit === "count") {
    return String(Math.round(value));
  }

  const rounded = trimDecimal(value);

  return unit === "seconds" ? `${rounded}s` : `${rounded}%`;
}

function trimDecimal(value: number): string {
  const rounded = Math.round(value * 10) / 10;

  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
}

/** The shape of a chart in words. */
export function trendLabel(trend: MetricTrend): string {
  return trend === "steady" ? "steady" : `trending ${trend}`;
}

/**
 * The text equivalent of a chart: what it currently reads and which way it is
 * going. A chart with nothing to draw says so instead of reading a zero out as
 * if it were a measurement.
 */
export function metricTextEquivalent(series: MetricSeries): string {
  const label = metricLabel(series.kind);

  if (series.pending) {
    return `${label} metrics pending`;
  }
  if (series.points.length === 0) {
    return `${label} has no readings in this window`;
  }

  return `${label} ${formatMetricValue(series.current, series.unit)}, ${trendLabel(series.trend)}`;
}
