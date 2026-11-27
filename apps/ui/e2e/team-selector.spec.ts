import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The Team selector in the sidebar. The preview server has no control plane, so
// the Teams and the environments of each Team are stubbed the way the control
// plane's own tests show it answers.

const environmentsOf: Record<string, string> = { default: "spoke-default", payments: "spoke-pay" };

async function stubSession(page: Page, role: "admin" | "user") {
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({
      json: {
        username: "acme",
        user: "Ada Lovelace",
        organization: "Acme",
        team: "default",
        role,
        organizations: [{ id: 1, name: "Acme", role, current: true }],
      },
    }),
  );
}

/** Answers each Team's environment list, and records which Team was asked for. */
async function stubTeams(page: Page): Promise<string[]> {
  const asked: string[] = [];
  await page.route("**/api/teams", (route) =>
    route.fulfill({ json: { teams: [{ name: "default" }, { name: "payments" }] } }),
  );
  await page.route("**/api/users", (route) => route.fulfill({ json: { users: [] } }));
  await page.route("**/api/environments**", (route) => {
    const team = new URL(route.request().url()).searchParams.get("team") ?? "default";
    asked.push(team);

    return route.fulfill({
      json: {
        hub: true,
        hubUrl: "https://hub.example",
        environments: [
          { name: environmentsOf[team], status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
        ],
      },
    });
  });

  return asked;
}

test.describe("the Team selector", () => {
  test("an admin switches Team and the environments follow", async ({ page }) => {
    await stubSession(page, "admin");
    const asked = await stubTeams(page);

    await page.goto("environments");
    await expect(page.getByText("spoke-default").first()).toBeVisible();

    await page.getByRole("combobox", { name: "Working in team" }).selectOption("payments");
    await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
    await page.goto("environments");

    await expect(page.getByText("spoke-pay").first()).toBeVisible();
    await expect(page.getByText("spoke-default")).toHaveCount(0);
    expect(asked).toContain("payments");

    await page.reload();
    await expect(page.getByRole("combobox", { name: "Working in team" })).toHaveValue("payments");
    await expect(page.getByText("spoke-pay").first()).toBeVisible();
  });

  test("a user sees their Team as a label", async ({ page }) => {
    await stubSession(page, "user");
    await stubTeams(page);

    await page.goto("environments");

    await expect(page.getByTestId("sidebar").getByText("default", { exact: true })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Working in team" })).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the selector renders in ${theme} mode`, async ({ page }) => {
      await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
      await stubSession(page, "admin");
      await stubTeams(page);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("environments");

      await expect(page.getByRole("combobox", { name: "Working in team" })).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/team-selector-${theme}.png`, fullPage: true });
    });
  }
});
