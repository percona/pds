import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The adapter side is covered by Go tests in internal/api and internal/managed.

const phases = [
  "Creating network and Kubernetes cluster",
  "Connecting to the environment",
  "Installing PDS",
  "Finishing",
];

function account(overrides: Record<string, unknown> = {}) {
  return {
    id: "cc_1",
    accountId: "123456789012",
    alias: "payments",
    state: "ready",
    allowedRegions: ["eu-west-1", "us-east-1"],
    stackRegion: "eu-west-1",
    templateVersion: 2,
    latestTemplateVersion: 2,
    createdAt: "2026-11-01T00:00:00Z",
    environments: ["orders", "billing", "reports"],
    ...overrides,
  };
}

const accounts = [
  account(),
  account({
    id: "cc_2",
    alias: "labs",
    accountId: "210987654321",
    state: "unreachable",
    reason: "PDS cannot assume the role in this AWS account. Run the command again.",
    environments: ["sandbox"],
  }),
  account({
    id: "cc_3",
    alias: "",
    accountId: "111122223333",
    state: "out_of_date",
    templateVersion: 1,
    environments: [],
  }),
];

const startedAt = "2026-11-20T10:00:00Z";

const managedEnvironments = [
  {
    name: "orders",
    status: "pending",
    origin: "managed",
    state: "creating",
    connectionId: "cc_1",
    region: "eu-west-1",
    provisioning: {
      run: { id: 1, kind: "create", status: "running", startedAt },
      progress: "in_progress",
      phases: [
        { name: phases[0], status: "succeeded" },
        { name: phases[1], status: "running" },
        { name: phases[2], status: "pending" },
        { name: phases[3], status: "pending" },
      ],
    },
  },
  {
    name: "billing",
    status: "pending",
    origin: "managed",
    state: "failed",
    connectionId: "cc_1",
    region: "eu-west-1",
    provisioning: {
      run: { id: 2, kind: "create", status: "failed", startedAt },
      progress: "failed",
      phases: [
        { name: phases[0], status: "failed" },
        { name: phases[1], status: "pending" },
        { name: phases[2], status: "pending" },
        { name: phases[3], status: "pending" },
      ],
      failure: {
        sentence:
          "AWS account payments has no room for another VPC in eu-west-1. Ask AWS to raise the quota, then retry.",
        action: "retry",
        refusal: { reason: "quota", service: "vpc", item: "VPCs per Region", region: "eu-west-1" },
      },
    },
  },
  {
    name: "reports",
    status: "connected",
    lastSeen: "2026-11-20T10:30:00Z",
    origin: "managed",
    state: "ready",
    connectionId: "cc_1",
    region: "us-east-1",
  },
  {
    name: "sandbox",
    status: "pending",
    origin: "managed",
    state: "creating",
    connectionId: "cc_2",
    region: "eu-west-1",
    provisioning: {
      run: { id: 4, kind: "create", status: "paused", startedAt },
      progress: "waiting",
      message: "Waiting for the AWS connection.",
      phases: [
        { name: phases[0], status: "waiting" },
        { name: phases[1], status: "pending" },
        { name: phases[2], status: "pending" },
        { name: phases[3], status: "pending" },
      ],
    },
  },
  { name: "spoke-a", status: "connected", lastSeen: "2026-11-20T10:30:00Z" },
];

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

async function stubAccounts(page: Page, rows: unknown[] = accounts) {
  await page.route("**/api/cloud-connections", (route) =>
    route.fulfill({ json: { cloudConnections: rows } }),
  );
}

async function stubManaged(page: Page) {
  await page.route("**/api/environments", (route) =>
    route.fulfill({ json: { hub: true, environments: managedEnvironments } }),
  );
  await stubAccounts(page);
}

async function shoot(page: Page, name: string, theme: "light" | "dark") {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
  await page.screenshot({ path: `test-results/${name}-${theme}.png`, fullPage: true });
}

test("the environments page shows accounts and managed environments with their progress", async ({
  page,
}) => {
  await stubManaged(page);

  await page.goto("environments");

  await expect(page.getByRole("heading", { level: 2, name: "AWS accounts" })).toBeVisible();
  await expect(page.getByText("Out of date", { exact: true })).toBeVisible();
  await expect(page.getByText("Unreachable", { exact: true })).toBeVisible();
  await expect(page.getByText("Fix the AWS account")).toBeVisible();
  await expect(page.getByText(phases[0]).first()).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("has no room for another VPC");
});

test("Retry re-runs the failed step of a create", async ({ page }) => {
  await stubManaged(page);
  let retried = "";
  await page.route("**/api/environments/billing/runs/2/retry", (route) => {
    retried = route.request().method();

    return route.fulfill({
      status: 202,
      json: { run: { id: 2 }, provisioning: managedEnvironments[1].provisioning },
    });
  });

  await page.goto("environments");
  await page.getByRole("button", { name: "Retry" }).click();

  await expect.poll(() => retried).toBe("POST");
});

test("Delete asks for the environment name and sends it", async ({ page }) => {
  await stubManaged(page);
  let body = "";
  await page.route("**/api/environments/reports", (route) => {
    body = route.request().postData() ?? "";

    return route.fulfill({
      status: 202,
      json: { run: { id: 9 }, provisioning: managedEnvironments[2].provisioning ?? {} },
    });
  });

  await page.goto("environments/reports");
  await page.getByRole("tab", { name: "Settings" }).click();
  await page.getByRole("button", { name: "Delete" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("button", { name: "Delete" })).toBeDisabled();
  await dialog.getByLabel("Environment name").fill("reports");
  await dialog.getByRole("button", { name: "Delete" }).click();

  await expect.poll(() => body).toBe(JSON.stringify({ confirm: "reports" }));
});

for (const theme of ["light", "dark"] as const) {
  test(`the environments page with AWS accounts in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubManaged(page);
    await page.setViewportSize({ width: 1440, height: 1100 });

    await page.goto("environments");
    await expect(page.getByRole("heading", { level: 2, name: "AWS accounts" })).toBeVisible();
    await page.locator("li", { hasText: "labs" }).getByRole("button", { name: "Fix" }).click();
    await expect(page.getByRole("button", { name: "Create command" })).toBeVisible();

    await shoot(page, "aws-environments", theme);
  });

  test(`the delete dialog in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubManaged(page);
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/sandbox");
    await page.getByRole("tab", { name: "Settings" }).click();
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.getByLabel("Environment name").fill("sandbox");

    await shoot(page, "aws-delete-dialog", theme);
  });

  test(`connecting an AWS account in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await page.setViewportSize({ width: 1440, height: 1000 });
    const created = new Date().toISOString();
    const row = (extra: Record<string, unknown> = {}) =>
      account({ createdAt: created, state: "pending", environments: [], ...extra });
    await page.route("**/api/cloud-connection-tokens", (route) =>
      route.fulfill({
        status: 201,
        json: {
          id: "tok_1",
          command:
            "curl -fsSL 'https://hub.example/api/internal/cloud-connection/connect.sh' | bash -s -- --token 'ccn_example'",
          expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
          allowedRegions: ["eu-west-1"],
        },
      }),
    );
    await page.route("**/api/cloud-connection-tokens/tok_1", (route) =>
      route.fulfill({
        json: {
          id: "tok_1",
          state: "connecting",
          connectionId: "cc_1",
          expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
          allowedRegions: ["eu-west-1"],
        },
      }),
    );
    await page.route("**/api/cloud-connections/cc_1", (route) =>
      route.fulfill({
        json: row({
          steps: [
            { step: "template", status: "succeeded", at: new Date().toISOString() },
            { step: "stack", status: "started", at: new Date().toISOString() },
          ],
        }),
      }),
    );

    await page.goto("environments/aws-accounts/new");
    await page.getByRole("button", { name: "Create command" }).click();
    await expect(page.getByRole("link", { name: "Open CloudShell" })).toBeVisible();
    await expect(page.getByRole("list", { name: "Connect progress" })).toContainText("Running");
    await page.getByText("What it adds to the account").click();

    await shoot(page, "aws-connect-account", theme);
  });

  test(`creating an environment in an AWS account in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubAccounts(page);
    await page.setViewportSize({ width: 1440, height: 1200 });

    await page.goto("environments/new");
    await page.getByLabel("Environment name").fill("orders-prod");
    await page.getByRole("radio", { name: /payments/ }).check();

    await expect(page.getByLabel("Region")).toHaveValue("eu-west-1");
    await expect(page.getByRole("button", { name: "Create environment" })).toBeEnabled();

    await shoot(page, "aws-create-environment", theme);
  });
}

test("a new environment is created through the chosen account and the list follows", async ({
  page,
}) => {
  await stubAccounts(page);
  let posted = "";
  await page.route("**/api/cloud-connections/cc_1/environments", (route) => {
    posted = route.request().postData() ?? "";

    return route.fulfill({
      status: 202,
      json: { environment: { name: "orders-prod", status: "pending" }, run: { id: 1 } },
    });
  });

  await page.goto("environments/new");
  await page.getByLabel("Environment name").fill("orders-prod");
  await page.getByRole("radio", { name: /payments/ }).check();
  await page.getByRole("button", { name: "Create environment" }).click();

  await expect
    .poll(() => posted)
    .toBe(JSON.stringify({ name: "orders-prod", region: "eu-west-1" }));
  await expect(page.getByRole("heading", { level: 1, name: "Environments" })).toBeVisible();
});
