import type { Page } from "@playwright/test";
import { expect, spokeJoining, spokeQuiet, stubHub, test } from "./fixtures";

// The preview server serves the built SPA only, so the adapter is stubbed
// here. The adapter side is covered by Go tests in internal/api and
// internal/hub.

const hubEnvironments = [
  { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
  spokeQuiet,
  spokeJoining,
];

async function stubEnvironments(page: Page, environments: unknown[] = hubEnvironments) {
  await stubHub(page, environments);
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("the environment list tells joining apart from gone quiet", async ({ page }) => {
  await stubEnvironments(page);

  await page.goto("environments");

  await expect(page.getByRole("heading", { level: 1, name: "Environments" })).toBeVisible();
  // Exact, because getByText matches a substring case-insensitively and
  // "Joining" is inside the name "spoke-joining".
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
  await expect(page.getByText("Unreachable", { exact: true })).toBeVisible();
  await expect(page.getByText("Joining", { exact: true })).toBeVisible();
});

// The environment list is the page that offers the connection forms.
test("the hub pastes its tokens from the environment list", async ({ page }) => {
  await stubEnvironments(page);

  await page.goto("environments");

  await expect(page.getByRole("heading", { level: 2, name: "Connections" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 3, name: "Support" })).toHaveCount(0);
  await page.getByRole("button", { name: "Connections" }).click();
  await expect(page.getByRole("heading", { level: 3, name: "Support" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Save token" })).toBeVisible();
});

for (const theme of ["light", "dark"] as const) {
  test(`the connections panel offers three keys in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubEnvironments(page);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments");
    await page.getByRole("button", { name: "Connections" }).click();

    await expect(page.getByTestId("connection-servicenow")).toBeVisible();
    await expect(page.getByTestId("connection-assistant")).toBeVisible();
    await expect(page.getByTestId("connection-cursor")).toBeVisible();
    await expect(page.locator("[data-testid^='connection-']")).toHaveCount(3);
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/connections-${theme}.png`, fullPage: true });
  });
}

test("an unscoped databases url is not a page", async ({ page }) => {
  const crashes: string[] = [];
  page.on("pageerror", (error) => crashes.push(error.message));
  await stubEnvironments(page);

  await page.goto("databases");

  await expect(page.getByRole("heading", { level: 1, name: "Page not found" })).toBeVisible();
  expect(crashes.filter((message) => message.includes("hooks"))).toHaveLength(0);
});

test("an environment that is not on the list says so", async ({ page }) => {
  await stubEnvironments(page);

  await page.goto("environments/spoke-typo");

  await expect(page.getByText("No environment named")).toBeVisible();
  await expect(page.getByRole("link", { name: "See the environments" })).toBeVisible();
});

test("connect says where it builds and what it costs before printing the command", async ({
  page,
}) => {
  await stubEnvironments(page);

  await page.goto("environments/new");

  await page.getByLabel("Environment name").fill("spoke-new");
  // The hub mints its own operator token, so there is nothing here to paste.
  await expect(page.getByLabel("Hub operator token")).toHaveCount(0);
  await expect(page.getByText("eu-west-1")).toBeVisible();
  await expect(page.getByText(/cost money/)).toBeVisible();

  await page.getByRole("button", { name: "Create command" }).click();

  await expect(page.getByText(/--token jnt_abc123/)).toBeVisible();
  await expect(page.getByText(/join secret/)).toBeVisible();
  await expect(page.getByText(/valid for a day/)).toHaveCount(0);
});

test("the delete guide gives one command that asks for the region and deletes by tag", async ({
  page,
}) => {
  await stubHub(page);

  await page.goto("clusters/spoke-a/aws-cleanup?namespace=pds");

  await expect(page.getByRole("heading", { level: 1, name: "Delete AWS resources" })).toBeVisible();
  const steps = page.getByRole("region");
  await expect(steps.filter({ hasText: "2. Choose the region" })).toContainText("read -r REGION");
  await expect(steps.filter({ hasText: "3. Run the command" })).toContainText(
    "--delete --name spoke-a --region",
  );
  await expect(steps.filter({ hasText: "What the command deletes" })).toContainText(
    "percona.com/pds",
  );
});

test("an invalid environment name says why", async ({ page }) => {
  await stubEnvironments(page);

  await page.goto("environments/new");

  await page.getByLabel("Environment name").fill("Spoke New");
  await page.getByLabel("Environment name").blur();

  await expect(page.getByText(/Use lowercase letters, digits, and dashes/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Create command" })).toBeDisabled();
});

for (const theme of ["light", "dark"] as const) {
  test(`the empty environment list renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubHub(page, []);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments");
    await expect(page.getByRole("heading", { level: 1, name: "Environments" })).toBeVisible();
    await expect(page.getByText("No environments yet")).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({
      path: `test-results/environments-empty-${theme}.png`,
      fullPage: true,
    });
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`the hub screens render in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubEnvironments(page);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments");
    await expect(page.getByRole("heading", { level: 1, name: "Environments" })).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "Connections" })).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/environments-${theme}.png`, fullPage: true });

    await page.goto("environments/new");
    await expect(
      page.getByRole("heading", { level: 1, name: "Create an environment" }),
    ).toBeVisible();
    await page.getByLabel("Environment name").fill("spoke-new");
    await expect(page.getByLabel("Hub operator token")).toHaveCount(0);
    await page.getByRole("button", { name: "Create command" }).click();
    await expect(page.getByText(/--token jnt_abc123/)).toBeVisible();
    await page.screenshot({ path: `test-results/environments-new-${theme}.png`, fullPage: true });

    await page.goto("environments/spoke-a");
    await expect(page.getByRole("heading", { level: 1, name: "Databases" })).toBeVisible();
    await page.screenshot({
      path: `test-results/environment-databases-${theme}.png`,
      fullPage: true,
    });

    await page.goto("environments/spoke-quiet");
    await expect(page.getByText("This environment is unreachable")).toBeVisible();
    await page.screenshot({
      path: `test-results/environment-unreachable-${theme}.png`,
      fullPage: true,
    });

    await page.goto("clusters/spoke-a/aws-cleanup?namespace=pds");
    await expect(
      page.getByRole("heading", { level: 1, name: "Delete AWS resources" }),
    ).toBeVisible();
    await page.screenshot({ path: `test-results/aws-cleanup-${theme}.png`, fullPage: true });
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`the update page renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubEnvironments(page);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments");
    // A joining environment has nothing to update, so only the other two offer it.
    await expect(page.getByRole("link", { name: "Update" })).toHaveCount(2);
    await page.screenshot({ path: `test-results/environments-update-row-${theme}.png` });
    await page.getByRole("link", { name: "Update" }).first().click();

    await expect(page.getByRole("heading", { level: 1, name: "Update spoke-a" })).toBeVisible();
    await expect(page.getByText(/not a tested upgrade path/)).toBeVisible();
    await page.getByRole("button", { name: "Create command" }).click();
    await expect(page.getByText(/--update --token 'jnt_abc123'/)).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/environment-update-${theme}.png`, fullPage: true });
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`the disconnect panel renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubEnvironments(page);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments");
    await page.getByRole("button", { name: "Disconnect" }).first().click();

    await expect(page.getByRole("link", { name: "View delete command" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Continue with OpenEverest" })).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/environments-disconnect-${theme}.png` });
  });
}

test("the disconnect panel offers the hand-over beside the delete command", async ({ page }) => {
  await stubEnvironments(page);

  await page.goto("environments");
  await expect(page.getByRole("link", { name: "Continue with OpenEverest" })).toHaveCount(0);
  await page.getByRole("button", { name: "Disconnect" }).first().click();

  await expect(page.getByRole("link", { name: "View delete command" })).toBeVisible();
  await page.getByRole("link", { name: "Continue with OpenEverest" }).click();

  await expect(
    page.getByRole("heading", { level: 1, name: "Continue with OpenEverest" }),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/environments\/spoke-a\/openeverest$/);
});

for (const theme of ["light", "dark"] as const) {
  test(`the OpenEverest hand-over renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubEnvironments(page, []);

    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/spoke-a/openeverest?namespace=databases");

    await expect(
      page.getByRole("heading", { level: 1, name: "Continue with OpenEverest" }),
    ).toBeVisible();
    await expect(page.getByRole("region", { name: "What changes" })).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/openeverest-${theme}.png`, fullPage: true });
  });
}

test("an old clusters bookmark keeps its tail and query", async ({ page }) => {
  await stubHub(page);

  await page.goto("clusters/spoke-a/aws-cleanup?namespace=pds");

  await expect(page).toHaveURL(/\/environments\/spoke-a\/aws-cleanup\?namespace=pds$/);
  await expect(page.getByRole("heading", { level: 1, name: "Delete AWS resources" })).toBeVisible();

  await page.goto("clusters/connect?x=1");

  await expect(page).toHaveURL(/\/environments\/new\?x=1$/);
  await expect(
    page.getByRole("heading", { level: 1, name: "Create an environment" }),
  ).toBeVisible();
});
