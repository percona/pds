import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api and internal/engine.
const members = [
  "orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017",
  "orders-rs0-1.orders-rs0.pds.svc.cluster.local:27017",
  "orders-rs0-2.orders-rs0.pds.svc.cluster.local:27017",
];
const uri = `mongodb://databaseAdmin:s3cret@${members.join(",")}/admin?replicaSet=rs0&ssl=false`;
const maskedUri = `mongodb://databaseAdmin:••••••••@${members.join(",")}/admin?replicaSet=rs0&ssl=false`;
const mongoshCommand =
  "mongosh 'mongodb://databaseAdmin@127.0.0.1:27017/admin?directConnection=true&ssl=false'";

const provisioning = {
  id: "spoke-a/pds.orders",
  environment: "spoke-a",
  name: "orders",
  engine: "mongodb",
  profile: "dev",
  namespace: "pds",
  status: "provisioning",
  message: "The database is being created",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
};

const ready = {
  ...provisioning,
  status: "ready",
  message: "Environment is ready",
  createdAt: "2026-08-20T09:30:00Z",
  connection: {
    host: "orders-rs0-0.orders-rs0.pds.svc.cluster.local",
    port: 27017,
    members,
    username: "databaseAdmin",
    maskedUri,
    secretName: "orders-conn",
    serviceName: "orders-rs0-0",
  },
};

const detailRoute = "databases/spoke-a/pds.orders";

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

type Posted = { create: unknown[]; scale: unknown[]; backup: number };

/**
 * Routes the create, the detail reads, the scale write and the backup write.
 * The bodies the UI posts are collected so a test can assert what was sent.
 */
async function stubMongoDB(page: Page, states: unknown[] = [ready]) {
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
      id: "orders-20260825143000",
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

test("create a MongoDB database and land on its connect steps", async ({ page }) => {
  const posted = await stubMongoDB(page, [provisioning, ready]);

  await page.goto("environments/spoke-a/new");
  await page.getByTestId("engine-card-mongodb").click();
  await page.getByLabel("Name").fill("orders");
  await expect(
    page.getByText("Access from outside the Kubernetes cluster is not available for MongoDB yet."),
  ).toBeVisible();
  await expect(page.getByRole("radiogroup", { name: "Access" })).toHaveCount(0);

  const submit = page.getByRole("button", { name: "Create database" });
  await expect(submit).toBeEnabled();
  await submit.click();

  await expect(page.getByText("Usually 6 to 12 minutes")).toBeVisible();
  expect(posted.create).toEqual([
    {
      name: "orders",
      environment: "spoke-a",
      engine: "mongodb",
      profile: "dev",
    },
  ]);

  await expect(page.getByRole("heading", { level: 1, includeHidden: true })).toContainText(
    "orders is ready",
    { timeout: 15_000 },
  );

  await expect(page.getByRole("dialog").getByText(mongoshCommand)).toBeVisible();
  await expect(page.getByText(uri)).toHaveCount(0);
});

test("connect shows the masked string and four commands that never print the password", async ({
  page,
}) => {
  await stubMongoDB(page);

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders" })).toBeVisible();
  await expect(page.getByText(maskedUri).first()).toBeVisible();

  await page.getByTestId("database-actions").getByRole("button", { name: "Connect" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/get secret 'orders-conn'/)).toBeVisible();
  await expect(dialog.getByText(/port-forward 'svc\/orders-rs0-0' '27017:27017'/)).toBeVisible();
  await expect(dialog.getByText(mongoshCommand)).toBeVisible();
  await expect(page.getByText(uri)).toHaveCount(0);
});

test("scale a MongoDB database to three members", async ({ page }) => {
  const posted = await stubMongoDB(page);

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders" })).toBeVisible();
  await page.getByRole("button", { name: "Scale" }).click();

  const dialog = page.getByRole("dialog");
  await dialog.getByRole("radio", { name: "3 members" }).click({ force: true });
  await dialog.getByRole("button", { name: "Scale" }).click();

  await expect(page.getByText("Scale requested")).toBeVisible();
  expect(posted.scale).toEqual([{ replicas: 3 }]);
});

test("back up a MongoDB database now", async ({ page }) => {
  const posted = await stubMongoDB(page);

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders" })).toBeVisible();
  await page.getByRole("button", { name: "Back up now" }).click();

  await expect(page.getByText("Backup started")).toBeVisible();
  await expect(page.getByTestId("backup-row-orders-20260825143000")).toContainText("Pending");
  expect(posted.backup).toBe(1);
});

for (const theme of ["light", "dark"] as const) {
  test(`the MongoDB create and connect flows render in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubMongoDB(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/spoke-a/new");
    await page.getByTestId("engine-card-mongodb").click();
    await page.getByLabel("Name").fill("orders");
    await expect(page.getByRole("radio", { name: /MongoDB/ })).toBeChecked();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/mongodb-create-${theme}.png`, fullPage: true });

    await page.goto(detailRoute);
    await page.screenshot({ path: `test-results/mongodb-database-${theme}.png`, fullPage: true });
    await page.getByTestId("database-actions").getByRole("button", { name: "Connect" }).click();
    await expect(page.getByRole("dialog").getByText(mongoshCommand)).toBeVisible();
    await page.screenshot({ path: `test-results/mongodb-connect-${theme}.png`, fullPage: true });
  });
}
