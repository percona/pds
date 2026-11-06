import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api.
const uri = "mysql://app:s3cret@orders-dev.pds.svc:3306/orders-dev";
/** What the card shows: the password is masked, the copy button carries it. */
const maskedUri = "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev";

const ordersDev = {
  id: "spoke-a/pds.orders-dev",
  environment: "spoke-a",
  name: "orders-dev",
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
  lastBackupAt: "2026-08-24T02:00:00Z",
  access: { external: true, visibility: "public", address: "orders-dev.pds.svc" },
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri,
    secretName: "orders-dev-app-credentials",
    serviceName: "orders-dev",
  },
};

const cartsStaging = {
  id: "spoke-a/pds.carts-staging",
  environment: "spoke-a",
  name: "carts-staging",
  engine: "mysql",
  profile: "staging",
  namespace: "pds",
  status: "provisioning",
  members: 3,
  cpu: 2,
  memoryGb: 8,
  storageGb: 100,
};

const metricKinds = ["cpu", "memory", "connections", "replication-lag", "disk"] as const;

const metricUnits: Record<(typeof metricKinds)[number], string> = {
  cpu: "percent",
  memory: "percent",
  connections: "count",
  "replication-lag": "seconds",
  disk: "percent",
};

/**
 * A snapshot shaped like the adapter's. Pending is the state an environment is in
 * before PMM registers the target, and the UI must say so rather than draw an
 * empty chart.
 */
function metricsSnapshot(pending: boolean) {
  const points = [
    { t: "2026-08-25T11:58:00Z", v: 18.5 },
    { t: "2026-08-25T11:59:00Z", v: 24 },
    { t: "2026-08-25T12:00:00Z", v: 31 },
  ];

  return {
    databaseId: ordersDev.id,
    window: "1h",
    start: "2026-08-25T11:00:00Z",
    end: "2026-08-25T12:00:00Z",
    stepSeconds: 60,
    pending,
    pendingReason: pending ? "PMM has no target for this database yet." : undefined,
    metrics: metricKinds.map((kind) => ({
      kind,
      unit: metricUnits[kind],
      pending,
      current: pending ? 0 : 31,
      min: pending ? 0 : 18.5,
      max: pending ? 0 : 31,
      average: pending ? 0 : 24.5,
      trend: pending ? "steady" : "up",
      points: pending ? [] : points,
    })),
  };
}

async function stubList(page: Page, databases: unknown[], metricsPending = true) {
  await page.route("**/api/databases?*", (route) => route.fulfill({ json: { databases } }));
  await page.route("**/api/databases/**/credential", (route) => route.fulfill({ json: { uri } }));
  await page.route("**/api/databases/*/*", (route) => route.fulfill({ json: ordersDev }));
  await page.route("**/api/databases/**/metrics*", (route) =>
    route.fulfill({ json: metricsSnapshot(metricsPending) }),
  );
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("a row opens the detail page and the connection string copies", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await stubList(page, [ordersDev, cartsStaging]);

  await page.goto("environments/spoke-a");

  const row = page.getByTestId("database-row-spoke-a/pds.orders-dev");
  await expect(row).toContainText("MySQL 8.4.3");
  await expect(row).toContainText("Ready");
  await expect(row).toContainText("Prod");
  await expect(row).toContainText("Metrics pending");

  await row.getByRole("link").click();

  await expect(page).toHaveURL("databases/spoke-a/pds.orders-dev");
  await expect(page.getByRole("heading", { level: 1, name: "orders-dev" })).toBeVisible();
  await expect(page.getByRole("tab")).toHaveText([
    "Overview",
    "Backups",
    "Metrics",
    "Activity",
    "Alerts",
    "History",
    "Settings",
  ]);
  // The connection card leads the overview until the string has been copied.
  await expect(page.getByTestId("overview-blocks").locator("> *").first()).toHaveAttribute(
    "data-block",
    "connect",
  );

  // The password never reaches the screen, only the clipboard.
  await expect(page.getByText(maskedUri).first()).toBeVisible();
  await expect(page.getByText(uri)).toHaveCount(0);

  await page.getByTestId("database-actions").getByRole("button", { name: "Connect" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/This database runs on spoke-a/)).toBeVisible();
  await expect(dialog.getByText("1. Point kubectl at the Kubernetes cluster")).toBeVisible();
  await expect(dialog.getByText(/get secret 'orders-dev-app-credentials'/)).toBeVisible();
  await expect(page.getByText(uri)).toHaveCount(0);

  await dialog.getByRole("button", { name: "Copy connection string" }).click();

  await expect(page.getByText("Connection string copied")).toBeVisible();
  await expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(uri);

  await page.keyboard.press("Escape");

  // After the copy the question changes, so metrics take the top spot.
  await expect(page.getByTestId("overview-blocks").locator("> *").first()).toHaveAttribute(
    "data-block",
    "metrics",
  );

  await page.reload();
  await expect(page.getByTestId("overview-blocks").locator("> *").first()).toHaveAttribute(
    "data-block",
    "metrics",
  );
});

test("the metrics tab charts each series and keeps the numbers in text", async ({ page }) => {
  await stubList(page, [ordersDev], false);

  await page.goto("databases/spoke-a/pds.orders-dev");
  await page.getByRole("tab", { name: "Metrics" }).click();

  const cpu = page.getByTestId("metric-panel-cpu");
  await expect(cpu).toBeVisible();
  await expect(page.locator("[data-testid^='metric-panel-']")).toHaveCount(5);

  // The chart is never the only carrier of the reading.
  await expect(cpu.getByTestId("metric-current")).toHaveText("31%");
  await expect(cpu).toContainText("trending up");
  await expect(cpu.getByRole("img", { name: /CPU 31%/ })).toBeVisible();

  const windows = page.getByRole("group", { name: "Time window" });
  await expect(windows.getByRole("button", { name: "1h" })).toHaveAttribute("aria-pressed", "true");

  await windows.getByRole("button", { name: "24h" }).click();
  await expect(windows.getByRole("button", { name: "24h" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(cpu).toBeVisible();
});

test("a database PMM has not seen says metrics are pending", async ({ page }) => {
  await stubList(page, [ordersDev]);

  await page.goto("databases/spoke-a/pds.orders-dev");

  const metrics = page.getByTestId("overview-blocks").locator("[data-block='metrics']");
  await expect(metrics.getByText("Metrics pending")).toHaveCount(3);

  await page.getByRole("tab", { name: "Metrics" }).click();
  await expect(page.getByText("PMM has no target for this database yet.")).toBeVisible();
  await expect(page.locator("[data-testid^='metric-panel-']").first()).toBeVisible();
});

test("an empty environment explains itself and offers the create button", async ({ page }) => {
  await stubList(page, []);

  await page.goto("environments/spoke-a");

  await expect(page.getByText("No databases yet")).toBeVisible();
  await expect(
    page.getByText("Create a MySQL or PostgreSQL database on this Kubernetes cluster."),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: "Create database" })).toHaveAttribute(
    "href",
    "/app/environments/spoke-a/new",
  );
});

test("the creating pulse stops under reduced motion", async ({ page }) => {
  await stubList(page, [cartsStaging]);

  await page.goto("environments/spoke-a");

  const pill = page.getByTestId("status-pill").filter({ hasText: "Creating" });
  await expect(pill).toBeVisible();

  const iterations = () =>
    pill.evaluate((element) => window.getComputedStyle(element).animationIterationCount);

  expect(await iterations()).toBe("infinite");

  await page.emulateMedia({ reducedMotion: "reduce" });

  expect(await iterations()).toBe("1");
});

for (const theme of ["light", "dark"] as const) {
  test(`the list and detail render in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubList(page, [ordersDev, cartsStaging], false);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/spoke-a");
    await expect(page.getByTestId("database-row-spoke-a/pds.orders-dev")).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/database-list-${theme}.png`, fullPage: true });

    await page.goto("databases/spoke-a/pds.orders-dev");
    await expect(page.getByRole("heading", { level: 1, name: "orders-dev" })).toBeVisible();
    await expect(page.getByTestId("metric-mini-cpu")).toHaveAttribute("data-state", "data");
    await page.screenshot({ path: `test-results/database-detail-${theme}.png`, fullPage: true });

    await page.getByTestId("database-actions").getByRole("button", { name: "Connect" }).click();
    await expect(page.getByRole("dialog").getByText(/This database runs on spoke-a/)).toBeVisible();
    await page.screenshot({ path: `test-results/connect-dialog-${theme}.png` });
    await page.getByRole("button", { name: "Close" }).click();

    await page.getByRole("tab", { name: "Metrics" }).click();
    await expect(page.getByTestId("metric-panel-cpu")).toBeVisible();
    await page.screenshot({ path: `test-results/database-metrics-${theme}.png`, fullPage: true });
  });
}
