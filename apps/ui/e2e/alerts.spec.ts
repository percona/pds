import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the control plane is
// stubbed here. The serving side is covered by the Go tests in internal/service.
/** A time this many minutes ago, so "Firing since" reads the same on any day. */
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

const report = {
  environments: [
    {
      name: "spoke-a",
      state: "live",
      readAt: ago(1),
      total: 2,
      shown: 2,
    },
    {
      name: "spoke-quiet",
      state: "stale",
      reason:
        "Alerts are not current in spoke-quiet because it has not reported in 12 minutes. Check that the Kubernetes cluster is running.",
      action: { kind: "view_environment", label: "View environment" },
      readAt: ago(12),
      total: 0,
      shown: 0,
    },
    {
      name: "spoke-new",
      state: "off",
      pending: "not-set-up",
      reason:
        "Alerts are off in spoke-new because monitoring is not set up. Set up monitoring for this environment.",
      action: { kind: "set_up_monitoring", label: "Set up monitoring" },
      readAt: null,
      total: 0,
      shown: 0,
    },
  ],
  alerts: [
    {
      id: "a1",
      environment: "spoke-a",
      databaseId: "spoke-a/pds.orders-dev",
      rule: "service_down",
      severity: "critical",
      summary: "orders-dev is down",
      startsAt: ago(180),
      labels: { cluster: "orders-dev", namespace: "pds" },
      action: { kind: "open_database", label: "Open database" },
    },
    {
      id: "a2",
      environment: "spoke-a",
      databaseId: "spoke-a/pds.orders-dev",
      rule: "disk_used",
      severity: "warning",
      summary: "orders-dev storage is 91% full",
      startsAt: ago(45),
      labels: { cluster: "orders-dev", namespace: "pds" },
      action: { kind: "scale_storage", label: "Scale storage" },
    },
  ],
  criticals: 1,
};

async function stubAlerts(page: Page, body: unknown = report) {
  await page.route("**/api/alerts", (route) => route.fulfill({ json: body }));
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("lists firing alerts, calls out environments with no reading, and counts criticals", async ({
  page,
}) => {
  await stubAlerts(page);

  await page.goto("alerts");

  await expect(page.getByRole("heading", { level: 1, name: "Alerts" })).toBeVisible();
  await expect(page.getByText("orders-dev is down")).toBeVisible();
  await expect(page.getByText("orders-dev storage is 91% full")).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(2);
  await expect(page.getByRole("link", { name: "Set up monitoring" })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Alerts/ })).toContainText(
    "1 critical alert firing",
  );
});

test("keeps the filters in the URL across a reload", async ({ page }) => {
  await stubAlerts(page);

  await page.goto("alerts");
  await page.getByLabel("Severity").selectOption("warning");
  await expect(page).toHaveURL(/severity=warning/);
  await page.reload();

  await expect(page.getByLabel("Severity")).toHaveValue("warning");
  await expect(page.getByText("orders-dev is down")).toHaveCount(0);
  await expect(page.getByText("orders-dev storage is 91% full")).toBeVisible();
});

test("Open database leads to the database", async ({ page }) => {
  await stubAlerts(page);

  await page.goto("alerts");
  await page.getByRole("link", { name: "Open database" }).click();

  await expect(page).toHaveURL(/databases\/spoke-a\/pds\.orders-dev/);
});

test("an empty report with every environment live says nothing is firing", async ({ page }) => {
  await stubAlerts(page, {
    environments: [report.environments[0]],
    alerts: [],
    criticals: 0,
  });

  await page.goto("alerts");

  await expect(page.getByText("No alerts are firing")).toBeVisible();
});

for (const theme of ["light", "dark"] as const) {
  test(`the alerts page renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubAlerts(page);

    await page.goto("alerts");
    await expect(page.getByText("orders-dev is down")).toBeVisible();

    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    // The page fades in, and a capture mid-fade is washed out.
    await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished)));
    await page.screenshot({ path: `test-results/alerts-${theme}.png`, fullPage: true });
  });
}

const ordersDev = {
  id: "spoke-a/pds.orders-dev",
  name: "orders-dev",
  engine: "mysql",
  version: "8.4.3",
  profile: "dev",
  namespace: "pds",
  environment: "spoke-a",
  status: "ready",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
  createdAt: "2026-09-08T09:30:00Z",
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev",
    secretName: "orders-dev-app-credentials",
  },
};

async function stubDatabase(page: Page, databaseReport: unknown) {
  await page.route("**/api/databases/spoke-a/pds.orders-dev", (route) =>
    route.fulfill({ json: ordersDev }),
  );
  await page.route("**/api/databases/spoke-a/pds.orders-dev/alerts", (route) =>
    route.fulfill({ json: databaseReport }),
  );
}

const databaseReport = {
  databaseId: ordersDev.id,
  environment: report.environments[0],
  alerts: report.alerts,
};

test("the database list marks the database with its firing alerts", async ({ page }) => {
  await stubAlerts(page);
  await page.route("**/api/databases*", (route) =>
    route.fulfill({ json: { databases: [ordersDev] } }),
  );

  await page.goto("environments/spoke-a");

  const row = page.getByTestId(`database-row-${ordersDev.id}`);
  await expect(row).toBeVisible();
  await expect(row.getByText("1 critical, 1 more")).toBeVisible();
});

test("the database page lists its alerts on the Alerts tab", async ({ page }) => {
  await stubDatabase(page, databaseReport);

  await page.goto(`databases/${ordersDev.id}`);
  await page.getByRole("tab", { name: "Alerts" }).click();

  await expect(page.getByText("orders-dev is down")).toBeVisible();
  await expect(page.getByText("orders-dev storage is 91% full")).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(0);
});

test("a stale environment shows its reason on the tab instead of an empty state", async ({
  page,
}) => {
  await stubDatabase(page, {
    databaseId: ordersDev.id,
    environment: report.environments[1],
    alerts: [],
  });

  await page.goto(`databases/${ordersDev.id}`);
  await page.getByRole("tab", { name: "Alerts" }).click();

  await expect(page.getByRole("status")).toContainText("has not reported in 12 minutes");
  await expect(page.getByText("Alerts for this database are unknown")).toBeVisible();
  await expect(page.getByText("No alerts are firing for this database")).toHaveCount(0);
});

/** Waits out the page fade, skipping the soft pulse a pending badge loops forever. */
async function settle(page: Page) {
  await page.evaluate(() =>
    Promise.all(
      document
        .getAnimations()
        .filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
        .map((a) => a.finished),
    ),
  );
}

for (const theme of ["light", "dark"] as const) {
  test(`the database Alerts tab and list indicator render in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubAlerts(page);
    await stubDatabase(page, databaseReport);
    await page.route("**/api/databases*", (route) =>
      route.fulfill({ json: { databases: [ordersDev] } }),
    );

    await page.goto("environments/spoke-a");
    await expect(page.getByText("1 critical, 1 more")).toBeVisible();
    await settle(page);
    await page.screenshot({ path: `test-results/database-list-alerts-${theme}.png` });

    await page.goto(`databases/${ordersDev.id}`);
    await page.getByRole("tab", { name: "Alerts" }).click();
    await expect(page.getByText("orders-dev is down")).toBeVisible();
    await settle(page);
    await page.screenshot({ path: `test-results/database-alerts-${theme}.png`, fullPage: true });
  });
}
