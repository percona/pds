import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// Deleting for real needs a live OpenEverest, which is a manual run. What this
// file covers is the browser half: the typed confirmation, the refusals, and
// where the page goes once the database is gone.
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
  connection: {
    host: "orders-prod.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-prod.pds.svc:3306/orders-prod",
    secretName: "orders-prod-app-credentials",
  },
};

const detailRoute = "databases/spoke-a/pds.orders-prod";

type StubOptions = {
  /** The answer to the DELETE. The default is the adapter's 204. */
  remove?: { status?: number; json?: unknown };
};

/**
 * Routes the detail reads and the delete. A delete the adapter accepts takes the
 * database out of the list, which is where the page lands, so the list answers
 * without it from then on.
 */
async function stubDetail(page: Page, options: StubOptions = {}) {
  let deleted = false;

  await page.route("**/api/databases?*", (route) =>
    route.fulfill({ json: { databases: deleted ? [] : [ordersProd] } }),
  );
  await page.route("**/api/databases/**/backups", (route) =>
    route.fulfill({ json: { backups: [] } }),
  );
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
  await page.route("**/api/databases/*/*", (route) => {
    if (route.request().method() !== "DELETE") {
      return route.fulfill({ json: ordersProd });
    }

    const answer = options.remove ?? {};
    const status = answer.status ?? 204;

    if (status < 400) {
      deleted = true;

      return route.fulfill({ status });
    }

    return route.fulfill({ status, json: answer.json });
  });
}

async function openDetail(page: Page) {
  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders-prod" })).toBeVisible();
}

async function openDelete(page: Page) {
  await page.getByTestId("database-actions").getByRole("button", { name: "Delete" }).click();

  return page.getByRole("dialog");
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("the dialog states the consequence and holds confirm until the name is typed", async ({
  page,
}) => {
  await stubDetail(page);
  await openDetail(page);

  const dialog = await openDelete(page);

  await expect(dialog.getByTestId("delete-consequence")).toHaveText(
    "This deletes orders-prod with its data and its backups, and there is no undo.",
  );

  const confirm = dialog.getByRole("button", { name: "Delete" });
  await expect(confirm).toBeDisabled();

  await dialog.getByLabel("Database name").fill("orders");
  await expect(confirm).toBeDisabled();

  await dialog.getByLabel("Database name").fill("orders-prod");
  await expect(confirm).toBeEnabled();
});

test("a confirmed delete takes the database off the list the page lands on", async ({ page }) => {
  await stubDetail(page);
  await openDetail(page);

  const dialog = await openDelete(page);
  await dialog.getByLabel("Database name").fill("orders-prod");
  await dialog.getByRole("button", { name: "Delete" }).click();

  await expect(page.getByText("Database deleted")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "spoke-a" })).toBeVisible();
  await expect(page.getByText("No databases yet")).toBeVisible();
  await expect(page.getByText("orders-prod")).toBeHidden();
});

test("the same dialog opens from the settings danger zone", async ({ page }) => {
  await stubDetail(page);
  await openDetail(page);

  await page.getByRole("tab", { name: "Settings" }).click();
  await page.getByTestId("danger-zone").getByRole("button", { name: "Delete" }).click();

  await expect(page.getByRole("dialog").getByTestId("delete-consequence")).toBeVisible();
});

test("the whole confirmation works from the keyboard", async ({ page }) => {
  await stubDetail(page);
  await openDetail(page);

  await openDelete(page);

  // The dialog opens on the field the confirmation is typed into, so the name
  // and Enter are the only keys a delete needs.
  await expect(page.getByLabel("Database name")).toBeFocused();
  await page.keyboard.type("orders-prod");
  await page.keyboard.press("Enter");

  await expect(page.getByText("Database deleted")).toBeVisible();
});

test("Esc leaves a delete dialog open, Cancel closes it", async ({ page }) => {
  await stubDetail(page);
  await openDetail(page);

  const dialog = await openDelete(page);
  await page.keyboard.press("Escape");
  await expect(dialog).toBeVisible();

  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(page.getByRole("heading", { level: 1, name: "orders-prod" })).toBeVisible();
});

test("a refused delete says why and keeps the page where it is", async ({ page }) => {
  await stubDetail(page, {
    remove: {
      status: 404,
      json: { code: "ErrNotFound", message: "No database named `orders-prod`." },
    },
  });
  await openDetail(page);

  const dialog = await openDelete(page);
  await dialog.getByLabel("Database name").fill("orders-prod");
  await dialog.getByRole("button", { name: "Delete" }).click();

  await expect(dialog.getByRole("alert")).toContainText("No database named `orders-prod`.");
  await expect(dialog).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`${detailRoute}$`));
});

for (const theme of ["light", "dark"] as const) {
  test(`the delete dialog renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubDetail(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await openDetail(page);
    const dialog = await openDelete(page);

    await dialog.getByLabel("Database name").fill("orders");
    await dialog.getByLabel("Database name").blur();
    await expect(dialog.getByText("That is not this database name.")).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);

    await page.screenshot({ path: `test-results/delete-dialog-${theme}.png` });
  });

  test(`the settings danger zone renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubDetail(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await openDetail(page);
    await page.getByRole("tab", { name: "Settings" }).click();
    await expect(page.getByTestId("danger-zone")).toBeVisible();

    await page.screenshot({ path: `test-results/delete-danger-zone-${theme}.png` });
  });
}
