import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api and internal/engine.
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

const nightly = {
  id: "orders-prod-20260825020000",
  status: "succeeded",
  sizeBytes: 2147483648,
  scheduled: true,
  createdAt: "2026-08-25T02:00:00Z",
};

const detailRoute = "databases/spoke-a/pds.orders-prod";

// The schedule is stated in the viewer's zone, so every spec here names one.
test.use({ timezoneId: "UTC" });

type StubOptions = {
  database?: unknown;
  backups?: unknown[];
  backup?: { status?: number; json: unknown };
};

/**
 * Routes the detail reads, the backup list, and the backup write. A started
 * backup joins the list the way OpenEverest would, so the row the developer
 * sees is the one the adapter reports rather than a client side fiction.
 */
async function stubDetail(page: Page, options: StubOptions = {}) {
  const database = options.database ?? ordersProd;
  const listed = [...(options.backups ?? [])];
  const started: unknown[] = [];

  await page.route("**/api/databases/**/backups", (route) =>
    route.fulfill({ json: { backups: [...started, ...listed] } }),
  );
  await page.route("**/api/databases/**/backup", (route) => {
    const answer = options.backup ?? {
      json: {
        id: "orders-prod-20260825143000",
        status: "pending",
        sizeBytes: 0,
        scheduled: false,
        createdAt: "2026-08-25T14:30:00Z",
      },
    };

    if ((answer.status ?? 202) < 400) {
      started.push(answer.json);
    }

    return route.fulfill({ status: answer.status ?? 202, json: answer.json });
  });
  await page.route("**/api/databases/**/metrics*", (route) =>
    route.fulfill({
      json: {
        databaseId: ordersProd.id,
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
  await page.route("**/api/databases/*/*", (route) => route.fulfill({ json: database }));
}

async function openBackups(page: Page) {
  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders-prod" })).toBeVisible();
  await page.getByRole("tab", { name: "Backups" }).click();
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("the list states the schedule and what each backup did", async ({ page }) => {
  await stubDetail(page, { backups: [nightly] });

  await openBackups(page);

  await expect(page.getByTestId("backup-schedule")).toContainText("Daily backups");
  await expect(page.getByTestId("backup-schedule")).toContainText(
    "Runs at 02:00 UTC. The last 7 are kept.",
  );

  const row = page.getByTestId(`backup-row-${nightly.id}`);
  await expect(row).toContainText("2 GB");
  await expect(row).toContainText("Scheduled");
  await expect(row.getByTestId("status-pill")).toContainText("Succeeded");
});

test("an empty list says when the first backup runs and offers a backup now", async ({ page }) => {
  await stubDetail(page);

  await openBackups(page);

  await expect(page.getByTestId("backups-empty")).toContainText("No backups yet");
  await expect(page.getByTestId("backups-empty")).toContainText(
    "The first scheduled backup runs at 02:00 UTC.",
  );

  await page.getByTestId("backups-empty").getByRole("button", { name: "Back up now" }).click();

  await expect(page.getByText("Backup started")).toBeVisible();
  await expect(page.getByTestId("backup-row-orders-prod-20260825143000")).toContainText("Pending");
});

test("the header starts a backup and lands on the list", async ({ page }) => {
  await stubDetail(page, { backups: [nightly] });

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders-prod" })).toBeVisible();

  await page.getByRole("button", { name: "Back up now" }).click();

  await expect(page.getByText("Backup started")).toBeVisible();
  await expect(page.getByRole("tab", { name: "Backups" })).toHaveAttribute("data-state", "active");

  const row = page.getByTestId("backup-row-orders-prod-20260825143000");
  await expect(row).toContainText("On demand");
  await expect(row).toContainText("Not reported");
});

test("a database with no schedule does not promise one", async ({ page }) => {
  const { backupSchedule: _schedule, ...ordersDev } = ordersProd;
  await stubDetail(page, { database: { ...ordersDev, profile: "dev" } });

  await openBackups(page);

  await expect(page.getByTestId("backup-schedule")).toContainText("On-demand backups");
  await expect(page.getByTestId("backups-empty")).toContainText(
    "This database has no schedule, so back it up when you need one.",
  );
});

test("a refused backup says why and leaves the list alone", async ({ page }) => {
  await stubDetail(page, {
    backups: [nightly],
    backup: {
      status: 409,
      json: { code: "ErrConflict", message: "A backup of `orders-prod` is already starting." },
    },
  });

  await openBackups(page);
  // The header and the schedule card both offer it; either starts the same backup.
  await page.getByRole("button", { name: "Back up now" }).first().click();

  await expect(page.getByText("A backup of `orders-prod` is already starting.")).toBeVisible();
  await expect(page.getByTestId("backup-list").getByRole("row")).toHaveCount(2);
});

for (const theme of ["light", "dark"] as const) {
  test(`the backup list renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubDetail(page, {
      backups: [
        nightly,
        {
          id: "orders-prod-20260824020000",
          status: "failed",
          sizeBytes: 0,
          scheduled: true,
          createdAt: "2026-08-24T02:00:00Z",
        },
      ],
    });

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await openBackups(page);
    await expect(page.getByTestId("backup-list")).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);

    await page.screenshot({ path: `test-results/backup-list-${theme}.png` });
  });
}

/**
 * Answers the schedule write with the database carrying what was sent, the
 * way the adapter does, and keeps the body for the spec to check.
 */
async function stubScheduleWrite(page: Page) {
  const sent: unknown[] = [];
  await page.route("**/api/databases/**/backup-schedule", async (route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>;
    sent.push(body);
    const cron =
      body.period === "weekly"
        ? `${String(body.minute)} ${String(body.hour)} * * ${String(body.weekday)}`
        : "0 2 * * *";

    return route.fulfill({
      json: {
        ...ordersProd,
        backupSchedule: { name: "daily", enabled: true, cron, ...body },
      },
    });
  });

  return sent;
}

test.describe("in Berlin", () => {
  test.use({ timezoneId: "Europe/Berlin" });

  test.beforeEach(async ({ page }) => {
    // Summer time, two hours ahead of UTC, whatever day the suite runs on.
    await page.clock.setFixedTime(new Date("2026-07-15T12:00:00Z"));
  });

  test("the editor saves a weekly schedule picked in local time as UTC", async ({ page }) => {
    await stubDetail(page, { backups: [nightly] });
    const sent = await stubScheduleWrite(page);

    await openBackups(page);
    await expect(page.getByLabel("Time")).toHaveValue("04:00");

    await page.getByLabel("Frequency").selectOption("weekly");
    await page.getByLabel("Day").selectOption("Monday");
    await page.getByLabel("Time").fill("01:30");
    await page.getByLabel("Copies to keep").fill("4");
    await page.getByRole("button", { name: "Save schedule" }).click();

    await expect(page.getByText("Backup schedule saved")).toBeVisible();
    expect(sent).toEqual([
      { enabled: true, period: "weekly", minute: 30, hour: 23, weekday: 0, retentionCopies: 4 },
    ]);
    await expect(page.getByTestId("backup-schedule")).toContainText(
      "Runs on Monday at 01:30 (Sunday 23:30 UTC). The last 4 are kept.",
    );
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the schedule editor renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await stubDetail(page, { backups: [nightly] });

      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await openBackups(page);
      await page.getByLabel("Frequency").selectOption("weekly");
      await expect(page.getByLabel("Day")).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);

      await page.screenshot({ path: `test-results/backup-schedule-${theme}.png` });
    });
  }
});
