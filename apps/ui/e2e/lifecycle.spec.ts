import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The adapter side is covered by Go tests in internal/api and internal/managed.

const now = new Date().toISOString();

function lifecycle(overrides: Record<string, unknown> = {}) {
  return {
    chartVersion: "1.4.0",
    deferred: false,
    maintenanceWindow: {
      day: 0,
      hour: 2,
      timeZone: "Europe/Berlin",
      nextStart: "2026-11-22T01:00:00Z",
      nextEnd: "2026-11-22T03:00:00Z",
    },
    templateRevision: 3,
    latestTemplateRevision: 3,
    templateOutOfDate: false,
    kubernetesVersion: "1.31",
    kubernetesUpgradeAvailable: false,
    drift: [],
    lost: false,
    ...overrides,
  };
}

function managed(name: string, extra: Record<string, unknown> = {}) {
  return {
    name,
    status: "connected",
    lastSeen: now,
    origin: "managed",
    state: "ready",
    connectionId: "cc_1",
    region: "eu-west-1",
    ...extra,
  };
}

const environments = [
  managed("orders", {
    lifecycle: lifecycle({
      pendingChartVersion: "1.5.0",
      templateRevision: 2,
      latestTemplateRevision: 3,
      templateOutOfDate: true,
      templateSummary: "Template revision 2, latest 3",
      kubernetesUpgradeAvailable: true,
      drift: [
        { resource: "NodeGroup", detail: "Instance type is m5.xlarge, PDS set m5.large" },
        { resource: "BackupBucket", detail: "Versioning is off" },
      ],
      driftCheckedAt: "2026-11-20T09:00:00Z",
    }),
  }),
  managed("legacy", {
    status: "pending",
    state: "lost",
    lastSeen: undefined,
    lifecycle: lifecycle({
      lost: true,
      lostReason: "Infrastructure missing",
      lostAt: "2026-11-20T08:00:00Z",
    }),
  }),
];

const account = {
  id: "cc_1",
  accountId: "123456789012",
  alias: "payments",
  state: "ready",
  allowedRegions: ["eu-west-1"],
  stackRegion: "eu-west-1",
  templateVersion: 2,
  latestTemplateVersion: 2,
  createdAt: "2026-11-01T00:00:00Z",
  environments: ["orders", "legacy"],
};

const safePreview = {
  replacesNetwork: false,
  replacesNodeGroup: true,
  changes: [
    { resource: "NodeGroup", type: "AWS::EKS::Nodegroup", action: "Modify", replacement: "True" },
    { resource: "BackupBucket", type: "AWS::S3::Bucket", action: "Modify", replacement: "False" },
  ],
};

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

async function stubList(page: Page) {
  await page.route("**/api/environments", (route) =>
    route.fulfill({ json: { hub: true, environments } }),
  );
  await page.route("**/api/cloud-connections", (route) =>
    route.fulfill({ json: { cloudConnections: [account] } }),
  );
}

async function shoot(page: Page, name: string, theme: "light" | "dark") {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
  await page.screenshot({ path: `test-results/${name}-${theme}.png`, fullPage: true });
}

function row(page: Page, name: string) {
  return page.locator("li", { hasText: name }).first();
}

test("a row notes an update and drift, and Updates opens the panel", async ({ page }) => {
  await stubList(page);

  await page.goto("environments");
  await expect(page.getByText("Update available")).toBeVisible();
  await expect(row(page, "orders").getByText("Changed outside PDS")).toBeVisible();
  await page.goto("environments/orders");
  await page.getByRole("tab", { name: "Updates" }).click();

  await expect(page.getByText("PDS 1.5.0 is available.")).toBeVisible();
  await expect(page.getByText("Template revision 2, latest 3")).toBeVisible();
  await expect(page.getByRole("list", { name: "Changed outside PDS" })).toContainText("NodeGroup");
  await expect(page.getByRole("button", { name: /repair/i })).toHaveCount(0);
});

test("Upgrade now, Defer and the window editor send their requests", async ({ page }) => {
  await stubList(page);
  const sent: Record<string, string> = {};
  await page.route("**/api/environments/orders/runs", (route) => {
    sent.run = route.request().postData() ?? "";

    return route.fulfill({
      status: 202,
      json: { run: { id: 5 }, provisioning: { phases: [] } },
    });
  });
  await page.route("**/api/environments/orders/upgrades/defer", (route) => {
    sent.defer = route.request().postData() ?? "";

    return route.fulfill({ json: lifecycle({ deferred: true }) });
  });
  await page.route("**/api/environments/orders/maintenance-window", (route) => {
    sent.window = route.request().postData() ?? "";

    return route.fulfill({ json: lifecycle() });
  });

  await page.goto("environments/orders");
  await page.getByRole("tab", { name: "Updates" }).click();
  await page.getByRole("button", { name: "Upgrade now" }).click();
  await expect.poll(() => sent.run).toBe(JSON.stringify({ kind: "upgrade_pds" }));

  await page.getByLabel("Defer for").selectOption("14");
  await page.getByRole("button", { name: "Defer", exact: true }).click();
  await expect.poll(() => sent.defer).toBe(JSON.stringify({ days: 14 }));

  await page.getByRole("button", { name: "Edit window" }).click();
  await page.getByLabel("Day").selectOption("3");
  await page.getByLabel("Hour").selectOption("4");
  await page.getByRole("button", { name: "Save window" }).click();
  await expect
    .poll(() => sent.window)
    .toBe(JSON.stringify({ day: 3, hour: 4, timeZone: "Europe/Berlin" }));
});

test("an update that would replace the network is explained and never offered", async ({
  page,
}) => {
  await stubList(page);
  await page.route("**/api/environments/orders/updates/preview", (route) =>
    route.fulfill({
      json: {
        replacesNetwork: true,
        replacesNodeGroup: false,
        changes: [
          { resource: "Vpc", type: "AWS::EC2::VPC", action: "Modify", replacement: "True" },
        ],
      },
    }),
  );

  await page.goto("environments/orders");
  await page.getByRole("tab", { name: "Updates" }).click();
  await page.getByRole("button", { name: "Review update" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("alert")).toContainText("would replace the network");
  await expect(dialog.getByRole("button", { name: "Update", exact: true })).toHaveCount(0);
});

test("Rebuild and Remove from PDS only on a lost environment", async ({ page }) => {
  await stubList(page);
  let rebuilt = "";
  let removed = "";
  await page.route("**/api/environments/legacy/runs", (route) => {
    rebuilt = route.request().postData() ?? "";

    return route.fulfill({ status: 202, json: { run: { id: 6 }, provisioning: { phases: [] } } });
  });
  await page.route("**/api/environments/legacy/remains", (route) =>
    route.fulfill({
      json: {
        accountId: "123456789012",
        region: "eu-west-1",
        stackName: "pds-legacy",
        eksName: "pds-legacy",
        bucket: "pds-legacy-backups",
      },
    }),
  );
  await page.route("**/api/environments/legacy", (route) => {
    if (route.request().method() !== "DELETE") {
      return route.fallback();
    }
    removed = route.request().postData() ?? "";

    return route.fulfill({ status: 204 });
  });

  await page.goto("environments");
  const lost = row(page, "legacy");
  await expect(lost.getByText("Infrastructure missing")).toBeVisible();
  await expect(lost.getByRole("button", { name: "Delete" })).toHaveCount(0);

  await lost.getByRole("button", { name: "Rebuild" }).click();
  await expect.poll(() => rebuilt).toBe(JSON.stringify({ kind: "rebuild" }));

  await lost.getByRole("button", { name: "Remove from PDS only" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("list", { name: "What stays in AWS" })).toContainText(
    "pds-legacy-backups",
  );
  await dialog.getByLabel("Environment name").fill("legacy");
  await dialog.getByRole("button", { name: "Remove from PDS only" }).click();
  await expect.poll(() => removed).toBe(JSON.stringify({ confirm: "legacy", scope: "pds_only" }));
});

for (const theme of ["light", "dark"] as const) {
  test(`the lifecycle panel in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubList(page);
    await page.setViewportSize({ width: 1440, height: 1500 });

    await page.goto("environments/orders");
    await page.getByRole("tab", { name: "Updates" }).click();
    await page.getByRole("button", { name: "Edit window" }).click();
    await expect(page.getByLabel("Time zone")).toBeVisible();

    await shoot(page, "lifecycle-panel", theme);
  });

  test(`the update preview in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubList(page);
    await page.route("**/api/environments/orders/updates/preview", (route) =>
      route.fulfill({ json: safePreview }),
    );
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/orders");
    await page.getByRole("tab", { name: "Updates" }).click();
    await page.getByRole("button", { name: "Review update" }).click();
    await expect(page.getByRole("list", { name: "Changes to the stack" })).toBeVisible();

    await shoot(page, "lifecycle-update-preview", theme);
  });

  test(`the blocked update in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubList(page);
    await page.route("**/api/environments/orders/updates/preview", (route) =>
      route.fulfill({
        json: {
          replacesNetwork: true,
          replacesNodeGroup: false,
          changes: [
            { resource: "Vpc", type: "AWS::EC2::VPC", action: "Modify", replacement: "True" },
          ],
        },
      }),
    );
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/orders");
    await page.getByRole("tab", { name: "Updates" }).click();
    await page.getByRole("button", { name: "Review update" }).click();
    await expect(page.getByRole("dialog").getByRole("alert")).toBeVisible();

    await shoot(page, "lifecycle-update-blocked", theme);
  });

  test(`a lost environment in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubList(page);
    await page.setViewportSize({ width: 1440, height: 1100 });

    await page.goto("environments");
    await expect(row(page, "legacy").getByText("Infrastructure missing")).toBeVisible();

    await shoot(page, "lifecycle-lost", theme);
  });

  test(`Remove from PDS only in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubList(page);
    await page.route("**/api/environments/legacy/remains", (route) =>
      route.fulfill({
        json: {
          accountId: "123456789012",
          region: "eu-west-1",
          stackName: "pds-legacy",
          eksName: "pds-legacy",
          bucket: "pds-legacy-backups",
        },
      }),
    );
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments");
    await row(page, "legacy").getByRole("button", { name: "Remove from PDS only" }).click();
    await expect(page.getByRole("list", { name: "What stays in AWS" })).toBeVisible();
    await page.getByLabel("Environment name").fill("legacy");

    await shoot(page, "lifecycle-remove-dialog", theme);
  });
}
