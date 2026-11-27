import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

const agent = (overrides: Record<string, unknown>) => ({
  id: "a1",
  name: "Cursor",
  host: "cursor.example",
  kind: "cimd",
  member: "Ann Lee",
  mine: true,
  team: "default",
  access: "change",
  passwords: false,
  sqlWrites: false,
  createdAt: "2026-10-28T09:00:00Z",
  lastUsedAt: new Date(Date.now() - 20 * 60_000).toISOString(),
  expiresAt: "2026-11-27T09:00:00Z",
  ...overrides,
});

const connected = [
  agent({}),
  agent({
    id: "a2",
    name: "PDS local",
    host: "",
    kind: "local",
    access: "change_delete",
    passwords: true,
    sqlWrites: true,
    lastUsedAt: new Date(Date.now() - 3 * 3_600_000).toISOString(),
  }),
  agent({
    id: "a3",
    name: "Claude",
    host: "",
    kind: "dcr",
    mine: false,
    member: "Bob Ray",
    access: "read",
    team: "payments",
  }),
];

/** Answers the list of connected agents, and the revoke, from one array. */
async function stubAgentSessions(page: Page, sessions: unknown[]) {
  let rows = sessions as { id: string }[];
  await page.route("**/api/agent-sessions**", (route) => {
    if (route.request().method() === "DELETE") {
      const id = route.request().url().split("/").pop();
      rows = rows.filter((row) => row.id !== id);

      return route.fulfill({ status: 204 });
    }

    return route.fulfill({ json: { sessions: rows } });
  });
}

test.beforeEach(async ({ page }) => {
  await stubAgentSessions(page, []);
});

test.describe("AI agents", () => {
  test("the sidebar opens the page and copies the install commands and the address", async ({
    context,
    page,
  }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await page.goto("./");

    await page.getByRole("link", { name: "AI agents" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "AI agents" })).toBeVisible();

    await page.getByRole("button", { name: "Copy install commands" }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      "claude plugin marketplace add percona/pds\nclaude plugin install pds@percona-pds",
    );

    const origin = new URL(page.url()).origin;
    await page
      .getByRole("region", { name: "Any MCP client" })
      .getByRole("button", { name: "Copy address" })
      .click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`${origin}/mcp`);

    await page.getByText("My client only runs a local command").click();
    await page.getByRole("button", { name: /Read and change/ }).click();
    await expect(
      page.getByText(`PDS_URL=${origin} npx -y @gas-lab/pds-mcp@next login --access change`),
    ).toBeVisible();
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`the AI agents page renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("agents");

    await expect(page.getByRole("heading", { level: 1, name: "AI agents" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Copy install commands" })).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/agents-${theme}.png`, fullPage: true });
  });
}

test.describe("Connected agents", () => {
  test("revoking asks first and then removes the row", async ({ page }) => {
    await stubAgentSessions(page, connected);
    await page.goto("agents");

    const table = page.getByRole("table");
    await expect(table.getByRole("row")).toHaveCount(4);

    await page.getByRole("button", { name: "Revoke Cursor" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText(/Cursor stops working at once/)).toBeVisible();
    await dialog.getByRole("button", { name: "Revoke" }).click();

    await expect(table.getByRole("row")).toHaveCount(3);
    await expect(page.getByRole("button", { name: "Revoke Cursor" })).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the list renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubAgentSessions(page, connected);

      await page.goto("agents");
      await expect(page.getByRole("heading", { level: 2, name: "Connected agents" })).toBeVisible();
      await expect(page.getByRole("table")).toBeVisible();
      await page
        .getByRole("heading", { level: 2, name: "Connected agents" })
        .scrollIntoViewIfNeeded();
      await page.screenshot({ path: `test-results/agents-connected-${theme}.png`, fullPage: true });

      await page.getByRole("button", { name: "Revoke Cursor" }).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.screenshot({ path: `test-results/agents-revoke-${theme}.png` });
    });

    test(`the empty list renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("agents");
      await expect(page.getByText("No agents connected")).toBeVisible();
      await page.screenshot({ path: `test-results/agents-empty-${theme}.png`, fullPage: true });
    });
  }
});

for (const theme of ["light", "dark"] as const) {
  test(`the bridge step renders in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("agents");

    await page.getByText("My client only runs a local command").click();
    await expect(page.getByRole("button", { name: "Copy login command" })).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/agents-login-${theme}.png`, fullPage: true });
  });

  test(`the AI agents page fits a phone in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 390, height: 844 });

    await page.goto("agents");

    await expect(page.getByRole("heading", { level: 1, name: "AI agents" })).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
    await page.screenshot({ path: `test-results/agents-phone-${theme}.png`, fullPage: true });
  });
}
