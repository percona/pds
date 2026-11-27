import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { stubSignedOut } from "./session";

// The Users screen and the page an invite link opens. The preview server has no
// control plane, so both run against stubs that answer the way its own tests
// show it does.

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

type Pending = {
  id: number;
  email?: string;
  anyEmail: boolean;
  team: string;
  role: "admin" | "user";
  expiresAt: string;
  createdAt: string;
};

const grace: Pending = {
  id: 7,
  email: "grace@example.com",
  anyEmail: false,
  team: "payments",
  role: "user",
  expiresAt: "2026-11-01T10:00:00Z",
  createdAt: "2026-10-16T10:00:00Z",
};

/** A control plane that keeps invitations in memory, as the admin routes do. */
async function stubDirectory(page: Page) {
  const invitations = [grace];
  let next = 8;
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({
      json: {
        username: "acme",
        user: "Ada Lovelace",
        organization: "Acme",
        team: "default",
        role: "admin",
        organizations: [{ id: 1, name: "Acme", role: "admin", current: true }],
      },
    }),
  );
  await page.route("**/api/teams", (route) =>
    route.fulfill({ json: { teams: [{ name: "default" }, { name: "payments" }] } }),
  );
  await page.route("**/api/users", (route) =>
    route.fulfill({
      json: {
        users: [
          { id: 1, name: "Ada Lovelace", email: "ada@example.com", team: "default", role: "admin" },
          { id: 2, name: "Grace Hopper", team: "payments", role: "user" },
        ],
      },
    }),
  );
  await page.route("**/api/invitations", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Omit<
        Pending,
        "id" | "expiresAt" | "createdAt"
      >;
      const invitation: Pending = {
        ...body,
        id: next++,
        expiresAt: "2026-11-02T10:00:00Z",
        createdAt: "2026-10-16T11:00:00Z",
      };
      invitations.push(invitation);

      return route.fulfill({
        status: 201,
        json: { ...invitation, link: "https://pds.example/claim?token=fresh-token", emailed: true },
      });
    }

    return route.fulfill({ json: { invitations } });
  });
  await page.route("**/api/invitations/*/resend", (route) =>
    route.fulfill({
      status: 201,
      json: { ...grace, link: "https://pds.example/claim?token=resent-token", emailed: true },
    }),
  );
  await page.route("**/api/invitations/*", (route) => {
    if (route.request().method() !== "DELETE") {
      return route.fallback();
    }
    const id = Number(new URL(route.request().url()).pathname.split("/").pop());
    invitations.splice(
      invitations.findIndex((invitation) => invitation.id === id),
      1,
    );

    return route.fulfill({ status: 204, body: "" });
  });
}

test.describe("the Users screen", () => {
  test("an admin reaches Users from the Organization group", async ({ page }) => {
    await stubDirectory(page);

    await page.goto("");
    await page.getByRole("button", { name: "Organization" }).click();
    await page.getByRole("link", { name: "Users" }).click();

    await expect(page.getByRole("heading", { level: 1, name: "Users" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Ada Lovelace", exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: "grace@example.com", exact: true })).toBeVisible();
  });

  test("invites an address, then resends and revokes it", async ({ page }) => {
    await stubDirectory(page);
    await page.goto("users");

    await page.getByRole("button", { name: "Invite people" }).click();
    await page.getByLabel("Email addresses").fill("linus@example.com");
    await page.getByLabel("Team", { exact: true }).selectOption("payments");
    await page.getByRole("button", { name: "Send invite" }).click();

    const notice = page.getByRole("status");
    await expect(notice).toContainText("Invite sent to linus@example.com");
    await expect(notice).toContainText("https://pds.example/claim?token=fresh-token");
    await expect(page.getByRole("cell", { name: "linus@example.com", exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Resend grace@example.com" }).click();
    await expect(notice).toContainText("https://pds.example/claim?token=resent-token");

    await page.getByRole("button", { name: "Revoke linus@example.com" }).click();
    await page
      .getByRole("dialog", { name: "Revoke invite" })
      .getByRole("button", { name: "Revoke" })
      .click();
    await expect(page.getByRole("cell", { name: "linus@example.com", exact: true })).toHaveCount(0);
    await expect(page.getByRole("cell", { name: "grace@example.com", exact: true })).toBeVisible();
  });

  test("invites several addresses and opens the claim screen of one link", async ({ page }) => {
    await stubDirectory(page);
    await page.goto("users");

    await page.getByRole("button", { name: "Invite people" }).click();
    await page.getByLabel("Email addresses").fill("linus@example.com, ken@example.com");
    await page.getByRole("button", { name: "Send 2 invites" }).click();

    const links = page.getByRole("status");
    await expect(links).toContainText("Invite sent to linus@example.com");
    await expect(links).toContainText("Invite sent to ken@example.com");

    const opened = page.context().waitForEvent("page");
    await links.getByRole("link", { name: "Open as the invited person" }).first().click();
    const claim = await opened;
    await expect(claim).toHaveURL(/\/claim\?token=fresh-token$/);
  });

  test("invites anyone with the link as a user", async ({ page }) => {
    await stubDirectory(page);
    await page.goto("users");

    await page.getByRole("button", { name: "Invite people" }).click();
    await page.getByLabel("Role", { exact: true }).selectOption("admin");
    await page.getByRole("checkbox", { name: "Anyone with the link can join" }).check();

    await expect(page.getByLabel("Email addresses")).toBeDisabled();
    await expect(page.getByLabel("Role", { exact: true })).toBeDisabled();
    await expect(page.getByLabel("Role", { exact: true })).toHaveValue("user");
    await page.getByRole("button", { name: "Create link" }).click();

    await expect(page.getByRole("status")).toContainText("Invite created for anyone");
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the Users screen renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await stubDirectory(page);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("users");
      await page.getByRole("button", { name: "Invite people" }).click();
      await page.getByLabel("Email addresses").fill("linus@example.com ken@example.com");
      await page.getByRole("button", { name: "Send 2 invites" }).click();

      await expect(page.getByRole("status")).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/users-${theme}.png`, fullPage: true });
    });
  }
});

test.describe("the invite page", () => {
  async function stubInvite(page: Page, methods: string[]) {
    await stubSignedOut(page);
    await page.route("**/api/auth/methods", (route) => route.fulfill({ json: { methods } }));
    await page.route("**/api/auth/invite**", (route) => {
      if (route.request().method() === "POST") {
        return route.fulfill({ json: { username: "grace" } });
      }

      return route.fulfill({
        json: {
          organization: "Acme",
          team: "payments",
          role: "user",
          email: "g***e@example.com",
          anyEmail: false,
        },
      });
    });
  }

  test("claims with a login and a password, then lands on sign in", async ({ page }) => {
    await stubInvite(page, ["password"]);

    await page.goto("claim?token=a-real-token");

    await expect(page.getByRole("heading", { level: 1, name: "Join Acme" })).toBeVisible();
    await expect(page.getByText("g***e@example.com")).toBeVisible();
    await page.getByLabel("Login").fill("grace");
    await page.getByLabel("Password", { exact: true }).fill("a long enough secret");
    await page.getByLabel("Password again").fill("a long enough secret");
    await page.getByRole("button", { name: "Set password" }).click();

    await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
    await expect(page.getByLabel("Username")).toHaveValue("grace");
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the invite page renders in ${theme} mode`, async ({ page }) => {
      await useTheme(page, theme);
      await stubInvite(page, ["oidc", "password"]);
      await page.emulateMedia({ reducedMotion: "reduce" });

      await page.goto("claim?token=a-real-token");

      await expect(page.getByRole("link", { name: "Continue with Google" })).toHaveAttribute(
        "href",
        "/api/auth/oidc/start?provider=google&invite=a-real-token",
      );
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/invite-${theme}.png`, fullPage: true });
    });
  }
});
