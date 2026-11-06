import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api and internal/engine.
const uri = "postgres://app:s3cret@reports-dev-pgbouncer.pds.svc:5432/reports-dev";
const maskedUri = "postgres://app:••••••••@reports-dev-pgbouncer.pds.svc:5432/reports-dev";
const psqlCommand = "psql -h 127.0.0.1 -p '5432' -U 'app'";

const provisioning = {
  id: "spoke-a/pds.reports-dev",
  environment: "spoke-a",
  name: "reports-dev",
  engine: "postgresql",
  profile: "dev",
  namespace: "pds",
  status: "provisioning",
  message: "Waiting for volume reports-dev-pvc to bind",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
};

const ready = {
  ...provisioning,
  status: "ready",
  message: "Environment is ready",
  version: "17.5",
  createdAt: "2026-08-20T09:30:00Z",
  connection: {
    host: "reports-dev-pgbouncer.pds.svc",
    port: 5432,
    username: "app",
    maskedUri,
    secretName: "pguser-reports-dev",
    serviceName: "reports-dev-pgbouncer",
  },
};

const detailRoute = "databases/spoke-a/pds.reports-dev";

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

type Posted = { create: unknown[]; scale: unknown[]; backup: number };

/**
 * Routes the create, the detail reads, the scale write and the backup write.
 * The bodies the UI posts are collected so a test can assert what was sent.
 */
async function stubPostgreSQL(page: Page, states: unknown[] = [ready]) {
  const posted: Posted = { create: [], scale: [], backup: 0 };
  const backups: unknown[] = [];
  let reads = 0;

  await page.route("**/api/support/tickets", (route) => route.fulfill({ json: { tickets: [] } }));
  await page.route("**/api/databases", (route) => {
    posted.create.push(route.request().postDataJSON());

    return route.fulfill({ status: 201, json: provisioning });
  });
  await page.route("**/api/databases/**/credential", (route) => route.fulfill({ json: { uri } }));
  await page.route("**/api/databases/**/scale", (route) => {
    posted.scale.push(route.request().postDataJSON());

    return route.fulfill({ json: { ...ready, members: 3, status: "updating" } });
  });
  await page.route("**/api/databases/**/backups", (route) => route.fulfill({ json: { backups } }));
  await page.route("**/api/databases/**/backup", (route) => {
    posted.backup += 1;
    const started = {
      id: "reports-dev-20260825143000",
      status: "pending",
      sizeBytes: 0,
      scheduled: false,
      createdAt: "2026-08-25T14:30:00Z",
    };
    backups.push(started);

    return route.fulfill({ status: 202, json: started });
  });
  await page.route("**/api/databases/**/metrics*", (route) =>
    route.fulfill({
      json: {
        databaseId: ready.id,
        window: "1h",
        start: "2026-08-25T11:00:00Z",
        end: "2026-08-25T12:00:00Z",
        stepSeconds: 60,
        pending: true,
        pendingReason: "Monitoring has no target for this database yet.",
        metrics: [],
      },
    }),
  );
  await page.route("**/api/databases/*/*", (route) => {
    const state = states[Math.min(reads, states.length - 1)];
    reads += 1;

    return route.fulfill({ json: state as Record<string, unknown> });
  });

  return posted;
}

test("create a PostgreSQL database and land on its connect steps", async ({ page }) => {
  const posted = await stubPostgreSQL(page, [provisioning, ready]);

  await page.goto("environments/spoke-a/new");
  await page.getByTestId("engine-card-postgresql").click();
  await page.getByLabel("Name").fill("reports-dev");

  const submit = page.getByRole("button", { name: "Create database" });
  await expect(submit).toBeEnabled();
  await submit.click();

  await expect(page.getByText("Usually 6 to 12 minutes")).toBeVisible();
  expect(posted.create).toEqual([
    {
      name: "reports-dev",
      environment: "spoke-a",
      engine: "postgresql",
      profile: "dev",
      access: "private",
    },
  ]);

  await expect(page.getByRole("heading", { level: 1, includeHidden: true })).toContainText(
    "reports-dev is ready",
    { timeout: 15_000 },
  );

  await expect(page.getByRole("dialog").getByText(psqlCommand)).toBeVisible();
  await expect(page.getByText(uri)).toHaveCount(0);
});

test("connect shows the masked string and four commands that never print the password", async ({
  page,
}) => {
  await stubPostgreSQL(page);

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "reports-dev" })).toBeVisible();
  await expect(page.getByText(maskedUri).first()).toBeVisible();

  await page.getByTestId("database-actions").getByRole("button", { name: "Connect" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/get secret 'pguser-reports-dev'/)).toBeVisible();
  await expect(
    dialog.getByText(/port-forward 'svc\/reports-dev-pgbouncer' '5432:5432'/),
  ).toBeVisible();
  await expect(dialog.getByText(psqlCommand)).toBeVisible();
  await expect(page.getByText(uri)).toHaveCount(0);
});

test("scale a PostgreSQL database to three members", async ({ page }) => {
  const posted = await stubPostgreSQL(page);

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "reports-dev" })).toBeVisible();
  await page.getByRole("button", { name: "Scale" }).click();

  const dialog = page.getByRole("dialog");
  await dialog.getByRole("radio", { name: "3 members" }).click({ force: true });
  await dialog.getByRole("button", { name: "Scale" }).click();

  await expect(page.getByText("Scale requested")).toBeVisible();
  expect(posted.scale).toEqual([{ replicas: 3 }]);
});

test("back up a PostgreSQL database now", async ({ page }) => {
  const posted = await stubPostgreSQL(page);

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "reports-dev" })).toBeVisible();
  await page.getByRole("button", { name: "Back up now" }).click();

  await expect(page.getByText("Backup started")).toBeVisible();
  await expect(page.getByTestId("backup-row-reports-dev-20260825143000")).toContainText("Pending");
  expect(posted.backup).toBe(1);
});

for (const theme of ["light", "dark"] as const) {
  test(`the PostgreSQL create and connect flows render in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubPostgreSQL(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/spoke-a/new");
    await page.getByTestId("engine-card-postgresql").click();
    await page.getByLabel("Name").fill("reports-dev");
    await expect(page.getByRole("radio", { name: /PostgreSQL/ })).toBeChecked();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/postgresql-create-${theme}.png`, fullPage: true });

    await page.goto(detailRoute);
    await page.getByTestId("database-actions").getByRole("button", { name: "Connect" }).click();
    await expect(page.getByRole("dialog").getByText(psqlCommand)).toBeVisible();
    await page.screenshot({ path: `test-results/postgresql-connect-${theme}.png`, fullPage: true });
  });
}
