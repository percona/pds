import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { stubSignedOut } from "./session";

// The signed out card, the organization switcher and the Teams screen. The
// preview server has no control plane, so each is driven against stubs that
// answer the way the control plane's own tests show it does.

type OrganizationRow = { id: number; name: string; role: "admin" | "user"; current: boolean };

const acmeAdmin: OrganizationRow = { id: 1, name: "Acme", role: "admin", current: true };
const initechUser: OrganizationRow = { id: 2, name: "Initech", role: "user", current: false };

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

/** Answers the session as a person with the given role and organizations. */
async function stubSession(
  page: Page,
  role: "admin" | "user",
  organizations: OrganizationRow[],
  organization = organizations.find((row) => row.current)?.name ?? "Acme",
) {
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({
      json: {
        username: "acme",
        user: "Ada Lovelace",
        organization,
        team: "default",
        role,
        organizations,
      },
    }),
  );
}

const switcher = (page: Page, name: string) => page.getByRole("button", { name: new RegExp(name) });

test.describe("the organization switcher", () => {
  test("an admin of one organization gets a switcher", async ({ page }) => {
    await stubSession(page, "admin", [acmeAdmin]);

    await page.goto("");

    await expect(switcher(page, "Acme")).toBeVisible();
  });

  test("a user of several organizations gets a switcher", async ({ page }) => {
    await stubSession(page, "user", [{ ...acmeAdmin, role: "user" }, initechUser]);

    await page.goto("");

    await expect(switcher(page, "Acme")).toBeVisible();
  });

  test("a user of one organization gets a static label", async ({ page }) => {
    await stubSession(page, "user", [{ ...acmeAdmin, role: "user" }]);

    await page.goto("");

    await expect(page.getByTestId("sidebar").getByText("Acme", { exact: true })).toBeVisible();
    await expect(switcher(page, "Acme")).toHaveCount(0);
  });

  test("picking another organization moves the session and names it in the sidebar", async ({
    page,
  }) => {
    let current = 1;
    const rows = () => [
      { ...acmeAdmin, current: current === 1 },
      { ...initechUser, role: "admin" as const, current: current === 2 },
    ];
    await page.route("**/api/auth/session", (route) =>
      route.fulfill({
        json: {
          username: "acme",
          user: "Ada Lovelace",
          organization: current === 1 ? "Acme" : "Initech",
          team: "default",
          role: "admin",
          organizations: rows(),
        },
      }),
    );
    await page.route("**/api/auth/organization", (route) => {
      current = (route.request().postDataJSON() as { organizationId: number }).organizationId;

      return route.fulfill({ status: 204, body: "" });
    });

    await page.goto("");
    await switcher(page, "Acme").click();
    await page
      .getByRole("dialog", { name: "Switch organization" })
      .getByRole("button", { name: /Initech/ })
      .click();

    await expect(switcher(page, "Initech")).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });
});

test.describe("the Teams screen", () => {
  /** A control plane that keeps Teams in memory, as the admin routes do. */
  async function stubTeams(page: Page, initial: string[] = ["default", "payments"]) {
    const teams = [...initial];
    await page.route("**/api/teams", (route) => {
      if (route.request().method() === "POST") {
        const { name } = route.request().postDataJSON() as { name: string };
        teams.push(name);

        return route.fulfill({ status: 201, json: { name } });
      }

      return route.fulfill({ json: { teams: teams.map((name) => ({ name })) } });
    });
    await page.route("**/api/teams/*", (route) => {
      const name = decodeURIComponent(
        new URL(route.request().url()).pathname.split("/").pop() ?? "",
      );
      const at = teams.indexOf(name);
      if (route.request().method() === "PATCH") {
        teams[at] = (route.request().postDataJSON() as { name: string }).name;
      } else {
        teams.splice(at, 1);
      }

      return route.fulfill({ status: 204, body: "" });
    });
    await page.route("**/api/users", (route) =>
      route.fulfill({
        json: {
          users: [
            { id: 1, name: "Ada Lovelace", team: "default", role: "admin" },
            { id: 2, name: "Grace Hopper", team: "payments", role: "user" },
          ],
        },
      }),
    );
    await page.route("**/api/invitations", (route) => route.fulfill({ json: { invitations: [] } }));
    // The Teams screens read every Team's environments, which name their Team.
    await page.route(
      (url) => url.pathname === "/api/environments",
      (route) =>
        route.fulfill({
          json: {
            environments: [
              {
                name: "spoke-a",
                status: "connected",
                lastSeen: "2026-09-08T12:00:00Z",
                team: "payments",
              },
            ],
          },
        }),
    );
  }

  test.beforeEach(async ({ page }) => {
    await stubSession(page, "admin", [acmeAdmin]);
  });

  test("an admin reaches Teams from the Organization group", async ({ page }) => {
    await stubTeams(page);

    await page.goto("");
    await page.getByRole("button", { name: "Organization" }).click();
    await page.getByRole("link", { name: "Teams" }).click();

    await expect(page.getByRole("heading", { level: 1, name: "Teams" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "payments", exact: true })).toBeVisible();
  });

  test("a user has no Organization group and no Teams page", async ({ page }) => {
    await stubSession(page, "user", [{ ...acmeAdmin, role: "user" }]);

    await page.goto("");
    await expect(page.getByTestId("sidebar")).toBeVisible();
    await expect(page.getByRole("group", { name: "Organization" })).toHaveCount(0);

    await page.goto("teams");

    await expect(page.getByRole("heading", { name: "Page not found" })).toBeVisible();
  });

  test("creates, renames and deletes a team", async ({ page }) => {
    await stubTeams(page);
    await page.goto("teams");

    await page.getByRole("button", { name: "Create team" }).click();
    await page
      .getByRole("dialog", { name: "Create team" })
      .getByLabel("Team name")
      .fill("platform");
    await page
      .getByRole("dialog", { name: "Create team" })
      .getByRole("button", { name: "Create team" })
      .click();
    await expect(page.getByRole("heading", { level: 1, name: "platform" })).toBeVisible();
    await page.getByRole("link", { name: "Back to teams" }).click();
    await expect(page.getByRole("cell", { name: "platform", exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Rename platform" }).click();
    const rename = page.getByRole("dialog", { name: "Rename platform" });
    await rename.getByLabel("Team name").fill("infra");
    await rename.getByRole("button", { name: "Rename" }).click();
    await expect(page.getByRole("cell", { name: "infra", exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: "platform", exact: true })).toHaveCount(0);

    await page.getByRole("button", { name: "Delete infra" }).click();
    const remove = page.getByRole("dialog", { name: "Delete infra" });
    await expect(remove.getByRole("button", { name: "Delete" })).toBeDisabled();
    await remove.getByLabel("Team name").fill("infra");
    await remove.getByRole("button", { name: "Delete" }).click();

    await expect(page.getByRole("cell", { name: "infra", exact: true })).toHaveCount(0);
    await expect(page.getByRole("cell", { name: "payments", exact: true })).toBeVisible();
  });

  test("a team that still has users says so", async ({ page }) => {
    await stubTeams(page);
    await page.route("**/api/teams/payments", (route) =>
      route.fulfill({
        status: 409,
        json: {
          code: "ErrConflict",
          message: "That team still has users. Move or remove them first.",
        },
      }),
    );
    await page.goto("teams");

    await page.getByRole("button", { name: "Delete payments" }).click();
    const remove = page.getByRole("dialog", { name: "Delete payments" });
    await remove.getByLabel("Team name").fill("payments");
    await remove.getByRole("button", { name: "Delete" }).click();

    await expect(remove.getByRole("alert")).toHaveText(
      "That team still has users. Move or remove them first.",
    );
  });

  test("a team's page shows its users and the environments it owns", async ({ page }) => {
    await stubTeams(page);
    await page.goto("teams");

    await page.getByRole("link", { name: "payments", exact: true }).click();

    await expect(page.getByRole("heading", { level: 1, name: "payments" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Grace Hopper", exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Ada Lovelace", exact: true })).toHaveCount(0);
    await expect(
      page.getByRole("list", { name: "Environments of payments" }).getByText("spoke-a"),
    ).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the team page renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await stubTeams(page);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("teams/payments");

      await expect(page.getByRole("cell", { name: "Grace Hopper", exact: true })).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/team-${theme}.png`, fullPage: true });
    });
  }

  for (const theme of ["light", "dark"] as const) {
    test(`the Teams screen renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await stubTeams(page);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("teams");

      await expect(page.getByRole("cell", { name: "payments", exact: true })).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/teams-${theme}.png`, fullPage: true });
    });
  }
});

test.describe("the signed out card", () => {
  async function stubIssuerSignedOut(page: Page) {
    await stubSignedOut(page);
    await page.route("**/api/auth/methods", (route) =>
      route.fulfill({ json: { methods: ["oidc", "password"] } }),
    );
  }

  test("offers Google and GitHub first, with the password form behind a disclosure", async ({
    page,
  }) => {
    await stubIssuerSignedOut(page);

    await page.goto("login");

    await expect(page.getByRole("link", { name: "Continue with Google" })).toHaveAttribute(
      "href",
      "/api/auth/oidc/start?provider=google",
    );
    await expect(page.getByRole("link", { name: "Continue with GitHub" })).toBeVisible();
    await expect(page.getByLabel("Username")).toHaveCount(0);

    await page.getByRole("button", { name: "Use a password" }).click();

    await expect(page.getByLabel("Username")).toBeVisible();
  });

  test("signing up asks only for the organization name", async ({ page }) => {
    await stubIssuerSignedOut(page);

    await page.goto("login");
    await page.getByRole("link", { name: "Create an organization" }).click();

    await expect(
      page.getByRole("heading", { level: 1, name: "Create your organization" }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Continue with Google" })).toBeDisabled();

    await page.getByLabel("Organization name").fill("Analytical Engines");

    await expect(page.getByRole("link", { name: "Continue with GitHub" })).toHaveAttribute(
      "href",
      "/api/auth/oidc/start?provider=github&organization=Analytical%20Engines",
    );
    await expect(page.getByLabel("Username")).toHaveCount(0);
  });

  test("a refused sign in says why", async ({ page }) => {
    await stubIssuerSignedOut(page);

    await page.goto("login?error=no_membership");

    await expect(page.getByRole("alert")).toContainText(
      "This account does not belong to an organization yet.",
    );
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the sign in card renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await stubIssuerSignedOut(page);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("login");

      await expect(page.getByRole("link", { name: "Continue with Google" })).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/sign-in-${theme}.png`, fullPage: true });
    });

    test(`the sign up card renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await stubIssuerSignedOut(page);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("login?signup");
      await page.getByLabel("Organization name").fill("Analytical Engines");

      await expect(page.getByRole("link", { name: "Continue with Google" })).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/sign-up-${theme}.png`, fullPage: true });
    });
  }
});
