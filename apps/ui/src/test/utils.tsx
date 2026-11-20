import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, renderHook } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { MemoryRouter } from "react-router";
import type { Alert, AlertEnvironment, AlertReport } from "@/features/alerts";
import type { ActivityDetail, ActivityEntry, ActivityPage } from "@/features/activity";
import type { Backup, Database } from "@/features/databases";
import type { Lifecycle } from "@/features/environments";
import type { MetricKind, MetricPoint, MetricSeries, MetricsSnapshot } from "@/features/monitoring";
import {
  FeatureFlagProvider,
  allFeaturesOn,
  installFeatureFlags,
  type FeatureFlags,
} from "@/features/flags";
import { ThemeProvider } from "@/lib/theme";

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      // Every test builds its own client, so isolation does not need gcTime 0,
      // and keeping the cache lets a test read what a mutation seeded.
      queries: { retry: false, staleTime: 0 },
      mutations: { retry: false },
    },
  });
}

type RenderOptions = {
  route?: string;
  /** Router state, for screens that read what the previous route handed them. */
  state?: unknown;
  /**
   * Surfaces to turn off for this render. Everything not named stays on, which
   * is what a control plane with no flags configured serves.
   */
  flags?: Partial<FeatureFlags>;
};

function createProviders(queryClient: QueryClient, options: RenderOptions) {
  const [pathname, search] = (options.route ?? "/").split("?");
  const entry = {
    pathname,
    // A route with a query string has to arrive as a search, or a screen that
    // reads one would see an empty set and the test would prove nothing.
    search: search === undefined ? "" : `?${search}`,
    state: options.state ?? null,
  };
  installFeatureFlags({ ...allFeaturesOn(), ...options.flags });

  return function Providers({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <ThemeProvider>
          <FeatureFlagProvider>
            <MemoryRouter initialEntries={[entry]}>{children}</MemoryRouter>
          </FeatureFlagProvider>
        </ThemeProvider>
      </QueryClientProvider>
    );
  };
}

export function renderWithProviders(ui: ReactElement, options: RenderOptions = {}) {
  const testQueryClient = createTestQueryClient();

  return {
    queryClient: testQueryClient,
    ...render(ui, { wrapper: createProviders(testQueryClient, options) }),
  };
}

export function renderHookWithProviders<TResult>(hook: () => TResult, options: RenderOptions = {}) {
  const testQueryClient = createTestQueryClient();

  return {
    queryClient: testQueryClient,
    ...renderHook(hook, { wrapper: createProviders(testQueryClient, options) }),
  };
}

export function createMockDatabase(overrides?: Partial<Database>): Database {
  return {
    id: "pds.test-db",
    name: "test-db",
    engine: "mysql",
    profile: "dev",
    namespace: "pds",
    status: "ready",
    members: 1,
    cpu: 1,
    memoryGb: 2,
    storageGb: 20,
    environment: "spoke-a",
    ...overrides,
  };
}

export function createMockBackup(overrides?: Partial<Backup>): Backup {
  return {
    id: "test-db-20260825020000",
    status: "succeeded",
    sizeBytes: 2147483648,
    scheduled: true,
    createdAt: "2026-08-25T02:00:00Z",
    ...overrides,
  };
}

/** The units the adapter reports per chart, so a mock cannot drift from them. */
const mockMetricUnits: Record<MetricKind, MetricSeries["unit"]> = {
  cpu: "percent",
  memory: "percent",
  connections: "count",
  "replication-lag": "seconds",
  disk: "percent",
};

export function createMockMetricSeries(
  kind: MetricKind,
  overrides?: Partial<MetricSeries>,
): MetricSeries {
  return {
    kind,
    unit: mockMetricUnits[kind],
    pending: false,
    current: 31,
    min: 18.5,
    max: 31,
    average: 24.5,
    trend: "up",
    points: [
      { t: "2026-08-25T11:58:00Z", v: 18.5 },
      { t: "2026-08-25T11:59:00Z", v: 24 },
      { t: "2026-08-25T12:00:00Z", v: 31 },
    ],
    ...overrides,
  };
}

/**
 * A run of readings one minute apart, oldest first, ending at `end`. The rules
 * that ask how long a breach has held need a window with a shape rather than a
 * single reading.
 */
export function createMockMetricPoints(
  values: number[],
  end = "2026-08-25T12:00:00Z",
): MetricPoint[] {
  const endAt = Date.parse(end);

  return values.map((v, index) => ({
    t: new Date(endAt - (values.length - 1 - index) * 60_000).toISOString(),
    v,
  }));
}

/** A snapshot holding one chart, which is all a single rule reads. */
export function createMockMetricsFor(
  kind: MetricKind,
  overrides?: Partial<MetricSeries>,
): MetricsSnapshot {
  return { ...createMockMetrics([kind]), metrics: [createMockMetricSeries(kind, overrides)] };
}

export function createMockMetrics(
  kinds: readonly MetricKind[],
  overrides?: Partial<MetricsSnapshot>,
): MetricsSnapshot {
  const pending = overrides?.pending ?? false;

  return {
    databaseId: "pds.test-db",
    window: "1h",
    start: "2026-08-25T11:00:00Z",
    end: "2026-08-25T12:00:00Z",
    stepSeconds: 60,
    pending,
    metrics: kinds.map((kind) =>
      createMockMetricSeries(kind, pending ? { pending: true, current: 0, points: [] } : undefined),
    ),
    ...overrides,
  };
}

/** One recorded mutation, shaped like the adapter's activity row. */
export function createMockActivityEntry(overrides?: Partial<ActivityEntry>): ActivityEntry {
  return {
    id: "000001",
    at: "2026-08-25T12:00:00Z",
    actor: "unknown",
    requestId: "a3f19c02",
    action: "database.backup",
    databaseId: "pds.orders-dev",
    target: "orders-dev",
    outcome: "succeeded",
    detail: "on demand",
    ...overrides,
  };
}

/** One activity row expanded into a timeline, the way the adapter serves it. */
export function createMockActivityDetail(overrides?: Partial<ActivityDetail>): ActivityDetail {
  return {
    event: createMockActivityEntry({
      resourceKind: "backup",
      resourceName: "orders-dev-20260825120000",
    }),
    resource: {
      kind: "backup",
      name: "orders-dev-20260825120000",
      namespace: "pds",
      state: "succeeded",
      startedAt: "2026-08-25T12:00:01Z",
      endedAt: "2026-08-25T12:02:11Z",
      sizeBytes: 2147483648,
    },
    timeline: [
      {
        at: "2026-08-25T12:00:00Z",
        source: "request",
        type: "normal",
        reason: "Requested",
        message: "on demand",
      },
      {
        at: "2026-08-25T12:01:04Z",
        source: "cluster",
        type: "normal",
        reason: "Scheduled",
        message: "Pod/orders-dev-backup assigned to node-2",
      },
      {
        at: "2026-08-25T12:02:11Z",
        source: "resource",
        type: "normal",
        reason: "Finished",
      },
    ],
    truncated: false,
    ...overrides,
  };
}

/** A page of activity, newest first, the way the adapter serves it. */
export function createMockActivity(overrides?: Partial<ActivityPage>): ActivityPage {
  const entries = overrides?.entries ?? [
    createMockActivityEntry({ id: "000003", action: "database.scale", detail: "replicas 3" }),
    createMockActivityEntry({ id: "000002" }),
    createMockActivityEntry({
      id: "000001",
      action: "database.create",
      detail: "engine mysql, profile dev",
    }),
  ];

  return {
    entries,
    cursor: entries[0]?.id ?? "",
    truncated: false,
    ...overrides,
  };
}

/** One firing critical alert on a database, the way the adapter serves it. */
export function createMockAlert(overrides?: Partial<Alert>): Alert {
  return {
    id: "alert-1",
    environment: "spoke-a",
    databaseId: "spoke-a/pds.orders-dev",
    rule: "service_down",
    severity: "critical",
    summary: "orders-dev is down",
    startsAt: "2026-10-13T10:00:00Z",
    labels: { cluster: "orders-dev", namespace: "pds" },
    action: { kind: "open_database", label: "Open database" },
    ...overrides,
  };
}

/** An environment whose monitoring was read on its newest report. */
export function createMockAlertEnvironment(
  overrides?: Partial<AlertEnvironment>,
): AlertEnvironment {
  return {
    name: "spoke-a",
    state: "live",
    readAt: "2026-10-13T11:59:00Z",
    total: 0,
    shown: 0,
    ...overrides,
  };
}

export function createMockAlertReport(overrides?: Partial<AlertReport>): AlertReport {
  const alerts = overrides?.alerts ?? [createMockAlert()];

  return {
    environments: [createMockAlertEnvironment({ total: alerts.length, shown: alerts.length })],
    alerts,
    criticals: alerts.filter((alert) => alert.severity === "critical").length,
    ...overrides,
  };
}

/** A lifecycle read with nothing to do, for tests and screenshots to override. */
export function createMockLifecycle(overrides: Partial<Lifecycle> = {}): Lifecycle {
  return {
    chartVersion: "1.4.0",
    deferred: false,
    maintenanceWindow: {
      day: 0,
      hour: 2,
      timeZone: "Europe/Berlin",
      nextStart: "2026-11-22T01:00:00Z",
      nextEnd: "2026-11-22T03:00:00Z",
    },
    templateRevision: 3,
    latestTemplateRevision: 3,
    templateOutOfDate: false,
    kubernetesVersion: "1.31",
    kubernetesUpgradeAvailable: false,
    drift: [],
    lost: false,
    ...overrides,
  };
}
