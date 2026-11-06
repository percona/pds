import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// Restoring for real needs a backup bucket the kind lab has none of, so the
// restore contract is covered by Go tests in internal/api and internal/engine,
// and the browser flow is covered here.
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

async function openRestore(page: Page, backupId: string) {
  await page.getByTestId(`backup-row-${backupId}`).getByRole("button", { name: "Restore" }).click();

  return page.getByRole("dialog");
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

// Restore creates a new database on the same environment. The adapter half is
// covered by Go tests; this is the browser half.
const hubOrders = {
  ...ordersProd,
  id: "spoke-a/pds.orders-prod",
  environment: "spoke-a",
};

const hubDetailRoute = "databases/spoke-a/pds.orders-prod";

async function stubHubDetail(page: Page) {
  await page.route("**/api/environments", (route) =>
    route.fulfill({
      json: {
        hub: true,
        hubUrl: "https://hub.example",
        environments: [{ name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" }],
      },
    }),
  );
  await page.route("**/api/databases?*", (route) =>
    route.fulfill({ json: { databases: [hubOrders] } }),
  );
  await page.route("**/api/databases/**/backups", (route) =>
    route.fulfill({ json: { backups: [nightly] } }),
  );
  await page.route("**/api/databases/**/restore", (route) =>
    route.fulfill({
      status: 202,
      json: {
        status: "accepted",
        environment: "spoke-a",
        name: "orders-prod-restore-1",
        id: "spoke-a/pds.orders-prod-restore-1",
      },
    }),
  );
  await page.route("**/api/databases/**/metrics*", (route) =>
    route.fulfill({
      json: {
        databaseId: hubOrders.id,
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
  await page.route("**/api/databases/spoke-a/*", (route) => route.fulfill({ json: hubOrders }));
}

async function openHubRestore(page: Page) {
  await page.goto(hubDetailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders-prod" })).toBeVisible();
  await page.getByRole("tab", { name: "Backups" }).click();

  return openRestore(page, nightly.id);
}

test("a hub restore names the new database and builds no environment", async ({ page }) => {
  await stubHubDetail(page);
  const dialog = await openHubRestore(page);

  await expect(dialog.getByTestId("restore-consequence")).toHaveText(
    "This restores the backup into a new database on spoke-a. orders-prod is not touched.",
  );
  await expect(dialog.getByLabel("New database name")).toHaveValue("orders-pr-restore-1");
  await expect(dialog.getByRole("button", { name: "Restore" })).toBeEnabled();

  // Nothing is provisioned for a restore, so none of this may be offered.
  await expect(dialog.getByLabel("New environment name")).toHaveCount(0);
  await expect(dialog.getByRole("checkbox")).toHaveCount(0);
  await expect(dialog.getByText(/EKS/)).toHaveCount(0);

  const request = page.waitForRequest(
    (candidate) => candidate.url().includes("/restore") && candidate.method() === "POST",
  );
  await dialog.getByRole("button", { name: "Restore" }).click();
  expect((await request).postDataJSON()).toEqual({
    backupId: nightly.id,
    name: "orders-pr-restore-1",
  });
  await expect(dialog).toBeHidden();
});

test("a hub restore refuses a name the environment already runs", async ({ page }) => {
  await stubHubDetail(page);
  const dialog = await openHubRestore(page);

  await dialog.getByLabel("New database name").fill("orders-prod");
  await dialog.getByLabel("New database name").blur();

  await expect(
    dialog.getByText("A database named orders-prod is already on spoke-a."),
  ).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Restore" })).toBeDisabled();
});

for (const theme of ["light", "dark"] as const) {
  test(`the hub restore dialog renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubHubDetail(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    const dialog = await openHubRestore(page);
    await dialog.getByLabel("New database name").fill("orders-prod");
    await dialog.getByLabel("New database name").blur();
    await expect(
      dialog.getByText("A database named orders-prod is already on spoke-a."),
    ).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);

    await page.screenshot({ path: `test-results/hub-restore-dialog-${theme}.png` });
  });
}
