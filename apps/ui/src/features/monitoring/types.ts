/** The five charts that ship, in the order the Metrics tab renders them. */
export const allMetricKinds = ["cpu", "memory", "connections", "replication-lag", "disk"] as const;

export type MetricKind = (typeof allMetricKinds)[number];

/** The three minis on Overview. */
export const overviewMetricKinds = ["cpu", "memory", "connections"] as const satisfies MetricKind[];

/** The window toggle on the Metrics tab. */
export type MetricWindow = "1h" | "24h";

export const metricWindows = ["1h", "24h"] as const satisfies MetricWindow[];

/** How a value is read out. The adapter decides, so the UI never guesses. */
export type MetricUnit = "percent" | "count" | "seconds";

/** The shape of a series, which is the text half of a chart. */
export type MetricTrend = "up" | "down" | "steady";

/** One reading. Short names because a 24 hour window holds 96 of them. */
export type MetricPoint = {
  /** ISO timestamp. */
  t: string;
  v: number;
};

/**
 * One chart as `GET /api/databases/:id/metrics` returns it.
 *
 * `pending` means PMM has no target for the database yet, which is a state and
 * not a failure. A series that is not pending and has no points is a window
 * with no readings, which the UI states rather than drawing flat.
 */
export type MetricSeries = {
  kind: MetricKind;
  unit: MetricUnit;
  pending: boolean;
  current: number;
  min: number;
  max: number;
  average: number;
  trend: MetricTrend;
  points: MetricPoint[];
};

/** One answer from `GET /api/databases/:id/metrics`. */
export type MetricsSnapshot = {
  databaseId: string;
  window: MetricWindow;
  start: string;
  end: string;
  stepSeconds: number;
  pending: boolean;
  /** Why there is nothing to draw yet, when the snapshot is pending. */
  pendingReason?: string;
  metrics: MetricSeries[];
};
