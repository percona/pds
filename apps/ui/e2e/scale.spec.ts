import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api and internal/engine.
const ordersDev = {
  id: "spoke-a/pds.orders-dev",
  environment: "spoke-a",
  name: "orders-dev",
  engine: "mysql",
  version: "8.4.3",
  profile: "dev",
  namespace: "pds",
  status: "ready",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
  createdAt: "2026-08-20T09:30:00Z",
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev",
    secretName: "orders-dev-app-credentials",
  },
};

const detailRoute = "databases/spoke-a/pds.orders-dev";

type ScaleAnswer = { status?: number; json: unknown };

/**
 * Routes the detail reads and the scale write. The bodies the UI posts are
 * collected so a test can assert that only the fields that moved were sent.
 */
async function stubDetail(page: Page, answer: ScaleAnswer = { json: ordersDev }) {
  const posted: unknown[] = [];

  await page.route("**/api/databases/**/scale", (route) => {
    posted.push(route.request().postDataJSON());

    return route.fulfill({ status: answer.status ?? 200, json: answer.json });
  });
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
  await page.route("**/api/databases/*/*", (route) => route.fulfill({ json: ordersDev }));

  return posted;
}

async function openScaleDialog(page: Page) {
  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders-dev" })).toBeVisible();
  await page.getByRole("button", { name: "Scale" }).click();

  return page.getByRole("dialog");
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("the dialog opens on the current sizing and sends only what moved", async ({ page }) => {
  const posted = await stubDetail(page, { json: { ...ordersDev, members: 3, status: "updating" } });

  const dialog = await openScaleDialog(page);

  await expect(dialog.getByLabel("CPU cores")).toHaveValue("1");
  await expect(dialog.getByLabel("Memory in GB")).toHaveValue("2");
  await expect(dialog.getByLabel("Storage in GB")).toHaveValue("20");
  await expect(dialog.getByRole("button", { name: "Scale" })).toBeDisabled();

  await dialog.getByRole("radio", { name: "3 members" }).click({ force: true });
  await expect(
    dialog.getByText("Three members with HA. The database stays up while one restarts."),
  ).toBeVisible();

  await dialog.getByRole("button", { name: "Scale" }).click();

  await expect(page.getByText("Scale requested")).toBeVisible();
  await expect(page.getByRole("dialog")).toBeHidden();
  expect(posted).toEqual([{ replicas: 3 }]);
});

test("a storage shrink is refused before the round trip", async ({ page }) => {
  const posted = await stubDetail(page);

  const dialog = await openScaleDialog(page);

  await dialog.getByLabel("Storage in GB").fill("10");
  await dialog.getByLabel("CPU cores").focus();

  await expect(dialog.getByText("Storage can only be increased.")).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Scale" })).toBeDisabled();
  expect(posted).toEqual([]);
});

test("a conflict keeps the dialog open and states why", async ({ page }) => {
  await stubDetail(page, {
    status: 409,
    json: { code: "ErrConflict", message: "A backup is running. Scale once it finishes." },
  });

  const dialog = await openScaleDialog(page);

  await dialog.getByLabel("CPU cores").fill("4");
  await dialog.getByRole("button", { name: "Scale" }).click();

  await expect(dialog.getByRole("alert")).toContainText(
    "A backup is running. Scale once it finishes.",
  );
  await expect(dialog).toBeVisible();
});

for (const theme of ["light", "dark"] as const) {
  test(`the scale dialog renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubDetail(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    const dialog = await openScaleDialog(page);
    await expect(dialog).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);

    await page.screenshot({ path: `test-results/scale-dialog-${theme}.png` });
  });
}
