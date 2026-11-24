import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api.
const provisioning = {
  id: "spoke-a/pds.orders-dev",
  environment: "spoke-a",
  name: "orders-dev",
  engine: "mysql",
  profile: "prod",
  namespace: "pds",
  status: "provisioning",
  message: "Waiting for volume orders-dev-pvc to bind",
  members: 3,
  cpu: 4,
  memoryGb: 16,
  storageGb: 250,
};

const uri = "mysql://app:s3cret@orders-dev.pds.svc:3306/orders-dev";
/** What the card shows: the password is masked, the copy button carries it. */
const maskedUri = "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev";

const ready = {
  ...provisioning,
  status: "ready",
  message: "Environment is ready",
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri,
  },
};

const failed = {
  ...provisioning,
  status: "failed",
  message: "No node has enough memory for 3 members.",
};

async function stubDatabases(page: Page, states: unknown[]) {
  await page.route("**/api/databases", (route) =>
    route.fulfill({ status: 201, json: provisioning }),
  );

  let call = 0;
  await page.route("**/api/databases/*/*", (route) => {
    const state = states[Math.min(call, states.length - 1)];
    call += 1;

    return route.fulfill({ json: state as Record<string, unknown> });
  });
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("create a database and land on the connection string", async ({ page }) => {
  await stubDatabases(page, [ready]);

  await page.goto("environments/spoke-a");
  await page.getByRole("link", { name: "Create database" }).click();

  await expect(page.getByRole("heading", { level: 1, name: "Create database" })).toBeVisible();
  await expect(page.getByTestId("profile-card-dev")).toContainText("1 member, on-demand backups");
  await expect(page.getByTestId("profile-card-prod")).toContainText("3 members, HA, daily backups");

  const submit = page.getByRole("button", { name: "Create database" });
  await expect(submit).toBeDisabled();

  await page.getByTestId("profile-card-prod").click();
  await page.getByLabel("Name").fill("orders-dev");
  await expect(submit).toBeEnabled();
  await submit.click();

  await expect(page.getByText("Usually 6 to 12 minutes")).toBeVisible();
  await expect(page.getByTestId("provisioning-step-volumes")).toContainText("Creating volumes");

  // The screen waits for the next status poll, which runs every five seconds.
  // The connect dialog opens itself on the command as soon as the connection
  // is published, so the landing behind it is aria-hidden, which is what a
  // modal does to its background.
  await expect(page.getByRole("heading", { level: 1, includeHidden: true })).toContainText(
    "orders-dev is ready",
    { timeout: 15_000 },
  );

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/This database runs on spoke-a/)).toBeVisible();
  await expect(dialog.getByText("1. Point kubectl at the Kubernetes cluster")).toBeVisible();
  await expect(dialog.getByText("mysql -h 127.0.0.1 -P '3306' -u 'app' -p")).toBeVisible();
  await expect(page.getByText(uri)).toHaveCount(0);
});

test("a provisioning step expands to its last operator event", async ({ page }) => {
  await stubDatabases(page, [provisioning]);

  await page.goto("databases/spoke-a/pds.orders-dev");

  const step = page.getByTestId("provisioning-step-volumes");
  await step.getByRole("button").click();

  await expect(step).toContainText("Waiting for volume orders-dev-pvc to bind");
});

test("a create the operator might still recover stays on screen", async ({ page }) => {
  await stubDatabases(page, [failed]);

  await page.goto("databases/spoke-a/pds.orders-dev");

  await expect(page.getByText("Usually 6 to 12 minutes")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1 })).toContainText("Creating orders-dev");
  await expect(page.getByRole("button", { name: "Try again" })).toHaveCount(0);
});

test("a failed create names the step and offers support", async ({ page }) => {
  await stubDatabases(page, [failed]);
  // The signed-in fixture answers an unstubbed read with 501, and that is how
  // the console learns support is not connected. A ticket list keeps this
  // failure on the ticket path.
  await page.route(/\/api\/support\/tickets(\?.*)?$/, (route) =>
    route.fulfill({ json: { tickets: [] } }),
  );

  // A failed status has to hold before the screen reports it, so that window is
  // moved through rather than waited out. It matches failureSettleMs.
  await page.clock.install();
  await page.goto("databases/spoke-a/pds.orders-dev");

  await expect(page.getByText("Usually 6 to 12 minutes")).toBeVisible();
  await page.clock.fastForward(30_000);

  await expect(page.getByRole("heading", { level: 1 })).toContainText(
    "Could not create orders-dev",
  );
  await expect(page.getByText("Starting database failed.")).toBeVisible();
  await expect(page.getByText("No node has enough memory for 3 members.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();

  await page.getByRole("button", { name: "Contact support" }).click();

  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByLabel("Subject")).toHaveValue("Cannot create database orders-dev");
  await expect(page.getByLabel("Description")).toContainText("Failed step: Starting database");
  await expect(page.getByLabel("Description")).not.toContainText("mysql://");
});

test("create offers MySQL, PostgreSQL and MongoDB", async ({ page }) => {
  await page.route("**/api/support/tickets", (route) => route.fulfill({ json: { tickets: [] } }));
  await page.goto("environments/spoke-a/new");

  await expect(page.getByTestId("engine-card-mysql")).toBeVisible();
  await expect(page.getByTestId("engine-card-postgresql")).toBeVisible();
  await expect(page.getByTestId("engine-card-mongodb")).toBeVisible();
  await expect(page.getByTestId(/^engine-card-/)).toHaveCount(3);

  await page.getByTestId("engine-card-postgresql").click();
  await page.getByLabel("Name").fill("reports-dev");
  await expect(page.getByRole("radio", { name: /PostgreSQL/ })).toBeChecked();
});

for (const theme of ["light", "dark"] as const) {
  test(`the create flow renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubDatabases(page, [provisioning]);

    // Reduced motion collapses the entry fade, so the screenshot catches the
    // settled screen instead of a frame partway through it.
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/spoke-a/new");
    await page.getByLabel("Name").fill("orders-dev");
    await page.getByRole("button", { name: "Advanced" }).click();
    await expect(page.getByLabel("CPU cores")).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/create-database-${theme}.png`, fullPage: true });

    await page.goto("databases/spoke-a/pds.orders-dev");
    await expect(page.getByText("Usually 6 to 12 minutes")).toBeVisible();
    await page.screenshot({ path: `test-results/provisioning-${theme}.png`, fullPage: true });
  });
}
