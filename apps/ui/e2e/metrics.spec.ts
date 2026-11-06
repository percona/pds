import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The PMM queries behind the numbers are covered by Go tests in internal/service.
const ordersProd = {
  id: "spoke-a/pds.orders-prod",
  environment: "spoke-a",
  name: "orders-prod",
  engine: "mysql",
  version: "8.4.3",
  profile: "prod",
  namespace: "pds",
  status: "ready",
  members: 3,
  cpu: 4,
  memoryGb: 16,
  storageGb: 250,
  createdAt: "2026-08-20T09:30:00Z",
  backupSchedule: { name: "daily", cron: "0 2 * * *", retentionCopies: 7 },
  connection: {
    host: "orders-prod.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-prod.pds.svc:3306/orders-prod",
    secretName: "orders-prod-app-credentials",
  },
};

const detailRoute = "databases/spoke-a/pds.orders-prod";

const kinds = [
  { kind: "cpu", unit: "percent" },
  { kind: "memory", unit: "percent" },
  { kind: "connections", unit: "count" },
  { kind: "replication-lag", unit: "seconds" },
  { kind: "disk", unit: "percent" },
] as const;

/** Readings a minute apart, ending at the end of the window. */
function points(values: number[], stepMs: number) {
  const end = Date.parse("2026-08-25T12:00:00Z");

  return values.map((v, index) => ({
    t: new Date(end - (values.length - 1 - index) * stepMs).toISOString(),
    v,
  }));
}

const readings: Record<string, number[]> = {
  cpu: [31.2, 44.8, 52.4, 47.1, 61.5],
  memory: [58, 59.4, 60.1, 62.8, 63.2],
  connections: [18, 24, 31, 27, 22],
  "replication-lag": [0.2, 0.4, 0.3, 0.9, 0.5],
  disk: [41, 41.2, 41.4, 41.6, 41.9],
};

function snapshot(window: string) {
  const stepMs = window === "24h" ? 15 * 60 * 1000 : 60 * 1000;

  return {
    databaseId: ordersProd.id,
    window,
    start: window === "24h" ? "2026-08-24T12:00:00Z" : "2026-08-25T11:00:00Z",
    end: "2026-08-25T12:00:00Z",
    stepSeconds: stepMs / 1000,
    pending: false,
    metrics: kinds.map(({ kind, unit }) => {
      const values = readings[kind];

      return {
        kind,
        unit,
        pending: false,
        current: values.at(-1) ?? 0,
        min: Math.min(...values),
        max: Math.max(...values),
        average: values.reduce((total, v) => total + v, 0) / values.length,
        trend: "up",
        points: points(values, stepMs),
      };
    }),
  };
}

/** What the adapter answers when PMM has no target for the database yet. */
const pendingSnapshot = {
  databaseId: ordersProd.id,
  window: "1h",
  start: "2026-08-25T11:00:00Z",
  end: "2026-08-25T12:00:00Z",
  stepSeconds: 60,
  pending: true,
  pendingReason: "PMM has no target for this database yet.",
  metrics: kinds.map(({ kind, unit }) => ({
    kind,
    unit,
    pending: true,
    current: 0,
    min: 0,
    max: 0,
    average: 0,
    trend: "steady",
    points: [],
  })),
};

type Overrides = {
  /** Answers the metrics route with a 502, which is a read nobody could make. */
  failed?: boolean;
  pending?: boolean;
};

/** Answers the detail reads, with the metrics route under the test's control. */
async function stubDetail(page: Page, overrides: Overrides = {}): Promise<string[]> {
  const metricsUrls: string[] = [];

  await page.route("**/api/databases/**/metrics*", (route) => {
    const url = route.request().url();
    metricsUrls.push(url);

    if (overrides.failed) {
      return route.fulfill({
        status: 502,
        json: {
          code: "ErrDownstream",
          message: "Metrics are not available right now. Try again in a moment.",
        },
      });
    }

    if (overrides.pending) {
      return route.fulfill({ json: pendingSnapshot });
    }

    return route.fulfill({ json: snapshot(new URL(url).searchParams.get("window") ?? "1h") });
  });

  await page.route("**/api/databases/**/backups", (route) =>
    route.fulfill({ json: { backups: [] } }),
  );
  await page.route("**/api/databases/*/*", (route) => route.fulfill({ json: ordersProd }));

  return metricsUrls;
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

async function openMetrics(page: Page) {
  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders-prod" })).toBeVisible();
  await page.getByRole("tab", { name: "Metrics" }).click();
}

test("the tab draws the five charts with the numbers a chart cannot carry", async ({ page }) => {
  await stubDetail(page);

  await openMetrics(page);

  for (const { kind } of kinds) {
    await expect(page.getByTestId(`metric-panel-${kind}`)).toBeVisible();
  }

  const cpu = page.getByTestId("metric-panel-cpu");
  await expect(cpu.getByTestId("metric-current")).toHaveText("61.5%");
  await expect(cpu.getByTestId("metric-summary-min")).toContainText("31.2%");
  await expect(cpu.getByTestId("metric-summary-max")).toContainText("61.5%");
  await expect(cpu.getByTestId("metric-summary-average")).toContainText("47.4%");

  // The unit comes from the adapter, so a count is a whole number and a lag
  // reads in seconds.
  await expect(
    page.getByTestId("metric-panel-connections").getByTestId("metric-current"),
  ).toHaveText("22");
  await expect(
    page.getByTestId("metric-panel-replication-lag").getByTestId("metric-current"),
  ).toHaveText("0.5s");
});

test("the window toggle re-reads the adapter rather than resampling in the browser", async ({
  page,
}) => {
  const metricsUrls = await stubDetail(page);

  await openMetrics(page);

  const windows = page.getByRole("group", { name: "Time window" });
  await expect(windows.getByRole("button", { name: "1h" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("The last hour, one point a minute.")).toBeVisible();

  await windows.getByRole("button", { name: "24h" }).click();

  await expect(windows.getByRole("button", { name: "24h" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(page.getByText("The last day, one point every 15 minutes.")).toBeVisible();
  await expect.poll(() => metricsUrls.some((url) => url.includes("window=24h"))).toBe(true);
});

test("a database PMM has no target for says so on every chart", async ({ page }) => {
  await stubDetail(page, { pending: true });

  await openMetrics(page);

  await expect(page.getByText("PMM has no target for this database yet.")).toBeVisible();
  await expect(page.getByTestId("metric-panel-cpu")).toContainText("Metrics pending");
  await expect(page.getByTestId("metric-panel-cpu").getByTestId("metric-current")).toHaveCount(0);
  await expect(page.getByText("No readings in this window")).toHaveCount(0);
});

test("a read nobody could make says so rather than drawing flat", async ({ page }) => {
  await stubDetail(page, { failed: true });

  await openMetrics(page);

  await expect(page.getByText("Metrics could not be read")).toBeVisible();
  await expect(
    page.getByText("Metrics are not available right now. Try again in a moment."),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  await expect(page.getByTestId("metric-panel-cpu")).toHaveCount(0);
});

for (const theme of ["light", "dark"] as const) {
  test(`the charts render in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubDetail(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await openMetrics(page);

    await expect(page.getByTestId("metric-panel-cpu")).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    // connect.spec.ts already captures database-metrics-*.png from the list
    // walk, so this one is named for the tab rather than overwriting it.
    await page.screenshot({ path: `test-results/metrics-tab-${theme}.png`, fullPage: true });
  });
}
