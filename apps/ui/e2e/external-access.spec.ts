import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The adapter side is covered by Go tests in internal/api.
const base = {
  id: "spoke-a/pds.orders-dev",
  environment: "spoke-a",
  name: "orders-dev",
  engine: "mysql",
  profile: "dev",
  namespace: "pds",
  status: "ready",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
  createdAt: "2026-09-23T09:30:00Z",
};

/** The load balancer exists but has no address, so the in-cluster host is published. */
const pending = {
  ...base,
  access: { external: true, visibility: "public", sourceRanges: ["203.0.113.7/32", "10.0.0.0/8"] },
  connection: {
    host: "orders-dev-pxc.pds.svc",
    port: 3306,
    username: "root",
    maskedUri: "mysql://root:••••••••@orders-dev-pxc.pds.svc:3306/",
  },
};

const reachable = {
  ...base,
  access: {
    external: true,
    visibility: "public",
    sourceRanges: ["203.0.113.7/32", "10.0.0.0/8"],
    address: "a1b2c3.elb.us-east-1.amazonaws.com",
  },
  connection: {
    host: "a1b2c3.elb.us-east-1.amazonaws.com",
    port: 3306,
    username: "root",
    maskedUri: "mysql://root:••••••••@a1b2c3.elb.us-east-1.amazonaws.com:3306/",
  },
};

const privateReachable = {
  ...base,
  access: {
    external: true,
    visibility: "private",
    address: "internal-a1b2c3.elb.us-east-1.amazonaws.com",
  },
  connection: {
    host: "internal-a1b2c3.elb.us-east-1.amazonaws.com",
    port: 3306,
    username: "root",
    maskedUri: "mysql://root:••••••••@internal-a1b2c3.elb.us-east-1.amazonaws.com:3306/",
  },
};

async function stubDetail(page: Page, database: unknown) {
  await page.route("**/api/databases/*/*", (route) =>
    route.fulfill({ json: database as Record<string, unknown> }),
  );
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("create defaults to private access", async ({ page }) => {
  await stubDetail(page, privateReachable);
  let sent: unknown;
  await page.route("**/api/databases", (route) => {
    sent = route.request().postDataJSON();

    return route.fulfill({ status: 201, json: privateReachable });
  });

  await page.goto("environments/spoke-a/new");
  await expect(page.getByRole("radio", { name: "Private" })).toBeChecked();
  await expect(page.getByRole("checkbox")).toHaveCount(0);
  await expect(page.getByLabel("Allowed addresses")).toHaveCount(0);
  await page.getByLabel("Name").fill("orders-dev");
  await page.getByRole("button", { name: "Create database" }).click();

  await expect
    .poll(() => sent)
    .toEqual({
      name: "orders-dev",
      engine: "mysql",
      profile: "dev",
      access: "private",
      environment: "spoke-a",
    });
});

test("create sends the allow list for a public database", async ({ page }) => {
  await stubDetail(page, pending);
  let sent: unknown;
  await page.route("**/api/databases", (route) => {
    sent = route.request().postDataJSON();

    return route.fulfill({ status: 201, json: pending });
  });

  await page.goto("environments/spoke-a/new");
  await page.getByLabel("Name").fill("orders-dev");
  await page.getByText("Public", { exact: true }).click();
  await expect(page.getByLabel("Allowed addresses")).toHaveValue("0.0.0.0/0");
  await expect(
    page.getByText("Any address on the internet can connect. Replace 0.0.0.0/0 to limit who can."),
  ).toBeVisible();
  await page.getByLabel("Allowed addresses").fill("203.0.113.7, 10.0.0.0/8");
  await page.getByRole("button", { name: "Create database" }).click();

  await expect
    .poll(() => sent)
    .toEqual({
      name: "orders-dev",
      engine: "mysql",
      profile: "dev",
      access: "public",
      sourceRanges: ["203.0.113.7", "10.0.0.0/8"],
      environment: "spoke-a",
    });
});

test("the detail page says the external address is pending", async ({ page }) => {
  await stubDetail(page, pending);

  await page.goto("databases/spoke-a/pds.orders-dev");

  await expect(page.getByText("Public address pending")).toBeVisible();
  await expect(page.getByText(/orders-dev-pxc\.pds\.svc/)).toHaveCount(0);
});

test("the detail page states a private database's address", async ({ page }) => {
  await stubDetail(page, privateReachable);

  await page.goto("databases/spoke-a/pds.orders-dev");

  await expect(page.getByText("Private, reachable from your VPC")).toBeVisible();
  await expect(
    page.getByText("internal-a1b2c3.elb.us-east-1.amazonaws.com", { exact: true }).first(),
  ).toBeVisible();
  await expect(page.getByText(/Allowed from/)).toHaveCount(0);
});

test("the detail page states a public database's address", async ({ page }) => {
  await stubDetail(page, reachable);

  await page.goto("databases/spoke-a/pds.orders-dev");

  await expect(page.getByText("Public, reachable from the internet")).toBeVisible();
  await expect(
    page.getByText("a1b2c3.elb.us-east-1.amazonaws.com", { exact: true }).first(),
  ).toBeVisible();
  await expect(page.getByText("203.0.113.7/32, 10.0.0.0/8")).toBeVisible();
});

for (const theme of ["light", "dark"] as const) {
  test(`external access renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubDetail(page, pending);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/spoke-a/new");
    await page.getByLabel("Name").fill("orders-dev");
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({
      path: `test-results/external-access-form-private-${theme}.png`,
      fullPage: true,
    });
    await page.getByText("Public", { exact: true }).click();
    await page.screenshot({
      path: `test-results/external-access-form-public-${theme}.png`,
      fullPage: true,
    });

    await page.goto("databases/spoke-a/pds.orders-dev");
    await expect(page.getByText("Public address pending")).toBeVisible();
    await page.screenshot({
      path: `test-results/external-access-pending-${theme}.png`,
      fullPage: true,
    });

    await stubDetail(page, reachable);
    await page.reload();
    await expect(page.getByText("Public, reachable from the internet")).toBeVisible();
    await page.screenshot({
      path: `test-results/external-access-reachable-${theme}.png`,
      fullPage: true,
    });

    await stubDetail(page, privateReachable);
    await page.reload();
    await expect(page.getByText("Private, reachable from your VPC")).toBeVisible();
    await page.screenshot({
      path: `test-results/external-access-private-${theme}.png`,
      fullPage: true,
    });
  });
}
