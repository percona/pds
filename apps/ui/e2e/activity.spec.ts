import { expect, test } from "./fixtures";
import type { Page, Route } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api and internal/service.
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
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev",
    secretName: "orders-dev-app-credentials",
  },
};

const seeded = [
  {
    id: "000003",
    at: "2026-08-25T11:59:00Z",
    actor: "unknown",
    client: "mcp",
    agent: "Claude Code",
    requestId: "a3f19c02",
    action: "database.backup",
    databaseId: ordersDev.id,
    target: "orders-dev",
    outcome: "succeeded",
    detail: "on demand",
  },
  {
    id: "000002",
    at: "2026-08-25T11:40:00Z",
    actor: "unknown",
    action: "connection.update",
    target: "servicenow",
    outcome: "succeeded",
  },
  {
    id: "000001",
    at: "2026-08-25T11:00:00Z",
    actor: "unknown",
    client: "chat",
    agent: "Anthropic",
    action: "database.create",
    databaseId: ordersDev.id,
    target: "orders-dev",
    outcome: "succeeded",
    detail: "engine mysql, profile prod",
  },
];

/** One row that only a later poll knows about, so the feed has to be live. */
const arrivesLater = {
  id: "000004",
  at: "2026-08-25T12:00:00Z",
  actor: "unknown",
  action: "database.scale",
  databaseId: ordersDev.id,
  target: "orders-dev",
  outcome: "failed",
  detail: "replicas 3",
  error: "the operator webhook is restarting",
};

type ActivityOverrides = {
  empty?: boolean;
  notImplemented?: boolean;
  /** Serves the extra row from the second poll on, which is what live means. */
  growAfterFirstPoll?: boolean;
};

/**
 * Answers the activity reads and records every URL, so a test can assert that
 * the cursor and the database filter reached the adapter rather than only that
 * the UI redrew.
 */
async function stubActivity(page: Page, overrides: ActivityOverrides = {}): Promise<string[]> {
  const urls: string[] = [];
  let polls = 0;

  await page.route("**/api/activity*", (route: Route) => {
    const url = route.request().url();
    urls.push(url);
    polls += 1;

    if (overrides.notImplemented) {
      return route.fulfill({
        status: 501,
        json: {
          code: "ErrNotImplemented",
          message: "The activity log is not enabled.",
        },
      });
    }

    const requested = new URL(url).searchParams;
    const since = requested.get("since") ?? "";
    const databaseId = requested.get("databaseId");

    if (overrides.empty) {
      return route.fulfill({ json: { entries: [], cursor: "", truncated: false } });
    }

    const all = overrides.growAfterFirstPoll && polls > 1 ? [arrivesLater, ...seeded] : [...seeded];
    const scoped = databaseId ? all.filter((row) => row.databaseId === databaseId) : all;
    const fresh = since ? scoped.filter((row) => row.id > since) : scoped;

    return route.fulfill({
      json: { entries: fresh, cursor: all[0]?.id ?? "", truncated: false },
    });
  });

  return urls;
}

/** The detail page reads a database and its tabs read PMM, so both are stubbed. */
async function stubDetail(page: Page) {
  await page.route("**/api/databases/**/metrics*", (route) =>
    route.fulfill({
      json: {
        databaseId: ordersDev.id,
        window: "1h",
        start: "2026-08-25T11:00:00Z",
        end: "2026-08-25T12:00:00Z",
        stepSeconds: 60,
        pending: true,
        pendingReason: "PMM has no target for this database yet.",
        metrics: [],
      },
    }),
  );
  await page.route("**/api/databases/**/backups", (route) =>
    route.fulfill({ json: { backups: [] } }),
  );
  await page.route("**/api/databases/*/*", (route) => route.fulfill({ json: ordersDev }));
}

/**
 * Answers the detail read for one row. It is registered after the feed stub so
 * it wins the `/api/activity/:id` path, which the feed pattern also matches.
 */
async function stubActivityDetail(page: Page, detail: unknown) {
  await page.route("**/api/activity/*", (route: Route) => route.fulfill({ json: detail }));
}

/** A backup the request accepted and the operator then failed, which is the
 * case the feed row alone cannot show. */
const failedBackupDetail = {
  event: { ...seeded[0], resourceKind: "backup", resourceName: "orders-dev-20260825115900" },
  resource: {
    kind: "backup",
    name: "orders-dev-20260825115900",
    namespace: "pds",
    state: "failed",
    message: "exit code 137, agent terminated",
    startedAt: "2026-08-25T11:59:01Z",
    endedAt: "2026-08-25T12:02:11Z",
  },
  timeline: [
    {
      at: "2026-08-25T11:59:00Z",
      source: "request",
      type: "normal",
      reason: "Requested",
      message: "on demand",
    },
    {
      at: "2026-08-25T12:00:41Z",
      source: "cluster",
      type: "warning",
      reason: "BackOff",
      message: "Pod/orders-dev-backup-agent Back-off restarting failed container",
      count: 3,
    },
    {
      at: "2026-08-25T12:02:11Z",
      source: "resource",
      type: "warning",
      reason: "Failed",
      message: "exit code 137, agent terminated",
    },
  ],
  truncated: false,
};

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("the activity page lists every mutation, newest first", async ({ page }) => {
  await stubActivity(page);

  await page.goto("activity");

  await expect(page.getByRole("heading", { level: 1, name: "Activity" })).toBeVisible();

  const rows = page.getByTestId("activity-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.first()).toContainText("Back up");
  await expect(rows.first()).toContainText("orders-dev");
  // A backup is handed to the operator, so the row says what it knows: the
  // platform took the request. Whether the backup ran is on the detail.
  await expect(rows.first().getByTestId("activity-outcome")).toHaveText("Accepted");

  // A connection change belongs to the environment, not to a database. It also
  // finishes inside the request, so its recorded outcome is the real one.
  await expect(rows.nth(1)).toContainText("Update connection");
  await expect(rows.nth(1)).toContainText("servicenow");
  await expect(rows.nth(1).getByTestId("activity-outcome")).toHaveText("Succeeded");
});

test("the filters narrow the feed, and only the database filter asks the control plane", async ({
  page,
}) => {
  const urls = await stubActivity(page);

  await page.goto("activity");
  await expect(page.getByTestId("activity-row")).toHaveCount(3);
  await expect(page.getByTestId("activity-count")).toContainText("Showing 3 of 3 loaded changes.");

  await page.getByLabel("Made by").selectOption("chat");
  await expect(page.getByTestId("activity-row")).toHaveCount(1);
  await expect(page.getByTestId("activity-count")).toContainText("Showing 1 of 3 loaded changes.");
  await expect(page).toHaveURL(/by=chat/);

  await page.getByRole("button", { name: "Clear filters" }).click();
  await page.getByLabel("Database", { exact: true }).selectOption(ordersDev.id);
  await expect(page.getByTestId("activity-row")).toHaveCount(2);
  expect(urls.some((url) => new URL(url).searchParams.get("databaseId") === ordersDev.id)).toBe(
    true,
  );
});

test("the feed picks up a change without a manual refresh", async ({ page }) => {
  const urls = await stubActivity(page, { growAfterFirstPoll: true });

  await page.goto("activity");
  await expect(page.getByTestId("activity-row")).toHaveCount(3);

  // The row only exists from the second poll on, so seeing it proves the feed
  // is live rather than a one shot read.
  await expect(page.getByTestId("activity-row")).toHaveCount(4, { timeout: 15_000 });
  await expect(page.getByTestId("activity-row").first()).toContainText("Scale database");
  await expect(page.getByTestId("activity-row").first().getByTestId("activity-outcome")).toHaveText(
    "Failed",
  );
  await expect(page.getByTestId("activity-row").first()).toContainText(
    "the operator webhook is restarting",
  );

  // A poll asks only for what it has not seen yet.
  expect(urls.some((url) => url.includes("since=000003"))).toBe(true);
});

test("the live toggle stops and starts the poll", async ({ page }) => {
  const urls = await stubActivity(page);

  await page.goto("activity");
  await expect(page.getByTestId("activity-row")).toHaveCount(3);

  const toggle = page.getByRole("button", { name: "Live" });
  await expect(toggle).toHaveAttribute("aria-pressed", "true");

  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByText("Paused. New changes are not shown.")).toBeVisible();

  const paused = urls.length;
  await page.waitForTimeout(6_000);
  expect(urls.length).toBe(paused);

  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => urls.length, { timeout: 15_000 }).toBeGreaterThan(paused);
});

test("the database tab scopes the feed to that database", async ({ page }) => {
  const urls = await stubActivity(page);
  await stubDetail(page);

  await page.goto("databases/spoke-a/pds.orders-dev");
  await page.getByRole("tab", { name: "Activity" }).click();

  // The connection row belongs to the environment, so the scoped feed drops it.
  await expect(page.getByTestId("activity-row")).toHaveCount(2);
  await expect(page.getByText("servicenow")).toHaveCount(0);
  expect(urls.some((url) => url.includes("databaseId=spoke-a%2Fpds.orders-dev"))).toBe(true);
});

test("an environment that has not changed anything says so", async ({ page }) => {
  await stubActivity(page, { empty: true });

  await page.goto("activity");

  await expect(page.getByText("Nothing has changed yet")).toBeVisible();
  await expect(page.getByTestId("activity-row")).toHaveCount(0);
});

test("an adapter without a ring explains itself", async ({ page }) => {
  await stubActivity(page, { notImplemented: true });

  await page.goto("activity");

  await expect(page.getByText("The activity log is not enabled", { exact: true })).toBeVisible();
  await expect(page.getByTestId("activity-row")).toHaveCount(0);
});

test("a row opens the full timeline of what the platform did about it", async ({ page }) => {
  await stubActivity(page);
  await stubActivityDetail(page, failedBackupDetail);

  await page.goto("activity");

  // Clicking the row, not the link: the row is the target a person goes for.
  await page.getByTestId("activity-row").first().getByText("orders-dev").click();

  await expect(page).toHaveURL(/\/activity\/000003$/);
  await expect(page.getByRole("heading", { level: 1, name: "Change" })).toBeVisible();

  // The row said the request was accepted. The backup it started then failed.
  await expect(page.getByTestId("detail-outcome")).toHaveText("Request accepted");
  await expect(page.getByTestId("status-pill")).toHaveText("Failed");
  await expect(page.getByTestId("detail-message")).toContainText("exit code 137");

  const rows = page.getByTestId("timeline-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.first()).toContainText("Requested");
  await expect(rows.nth(1)).toContainText("Back-off restarting failed container");
  await expect(rows.nth(1).getByTestId("timeline-count")).toContainText("3 times");
});

test("the action stays a real link, so it can be opened in a new tab", async ({ page }) => {
  await stubActivity(page);

  await page.goto("activity");

  const link = page.getByTestId("activity-row").first().getByTestId("activity-detail-link");
  await expect(link).toHaveAttribute("href", "/app/activity/000003");
});

test("a change that aged out of the ring says so", async ({ page }) => {
  await stubActivity(page);
  await page.route("**/api/activity/*", (route: Route) =>
    route.fulfill({
      status: 404,
      json: {
        code: "ErrNotFound",
        message:
          "That activity entry is not in the log. The log keeps the most recent changes, " +
          "so an older one may have aged out.",
      },
    }),
  );

  await page.goto("activity/00000000000000009999");

  await expect(page.getByText("This entry could not be read")).toBeVisible();
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
});

for (const theme of ["light", "dark"] as const) {
  test(`the change detail renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubActivity(page);
    await stubActivityDetail(page, failedBackupDetail);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("activity/000003");

    await expect(page.getByTestId("timeline-row")).toHaveCount(3);
    await expect(page.getByText("MCP server")).toBeVisible();
    await expect(page.getByText("Claude Code")).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/activity-detail-${theme}.png`, fullPage: true });
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`the activity feed renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubActivity(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("activity");

    await expect(page.getByTestId("activity-row")).toHaveCount(3);
    await expect(page.getByTestId("activity-client")).toHaveText([
      "MCP server (Claude Code)",
      "Chat (Anthropic)",
    ]);
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/activity-${theme}.png`, fullPage: true });
  });
}
