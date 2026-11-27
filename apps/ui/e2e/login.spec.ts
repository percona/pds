import { expect, test, type Page } from "@playwright/test";
import { e2eUsername, stubSignedOut } from "./session";

// This spec imports the plain test rather than the signed-in fixture: the
// session is what it is driving, so it cannot be set up for it.

const password = "percona-dev-login";

/**
 * Answers login the way the adapter does: the right password sets the session,
 * and anything else is refused with the one message that covers both halves of
 * the form.
 */
async function stubLogin(page: Page) {
  let signedIn = false;

  await page.route("**/api/**", (route) =>
    route.fulfill({
      status: 501,
      json: { code: "ErrNotImplemented", message: "Not stubbed." },
    }),
  );
  await page.route("**/api/auth/session", (route) =>
    signedIn
      ? route.fulfill({ json: { username: e2eUsername } })
      : route.fulfill({
          status: 401,
          json: { code: "ErrValidation", message: "Sign in to continue." },
        }),
  );
  await page.route("**/api/features", (route) => route.fulfill({ json: { flags: {} } }));
  await page.route("**/api/environments", (route) =>
    route.fulfill({
      json: {
        hub: true,
        environments: [{ name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" }],
      },
    }),
  );
  await page.route("**/api/databases**", (route) => route.fulfill({ json: { databases: [] } }));
  await page.route("**/api/connections", (route) =>
    route.fulfill({
      json: {
        servicenow: { configured: false },
        assistant: { configured: false },
        cursor: { configured: false },
      },
    }),
  );
  await page.route("**/api/activity*", (route) =>
    route.fulfill({ json: { entries: [], cursor: "", truncated: false } }),
  );
  await page.route("**/api/support/tickets**", (route) =>
    route.fulfill({ json: { tickets: [], portalUrl: "https://support.example.com" } }),
  );

  await page.route("**/api/auth/login", async (route) => {
    const body = route.request().postDataJSON() as { username?: string; password?: string };
    if (body.username === e2eUsername && body.password === password) {
      signedIn = true;

      return route.fulfill({ json: { username: e2eUsername } });
    }

    return route.fulfill({
      status: 401,
      json: { code: "ErrValidation", message: "That username and password do not match." },
    });
  });

  await page.route("**/api/auth/logout", (route) => {
    signedIn = false;

    return route.fulfill({ status: 204, body: "" });
  });
}

test("a signed out visitor gets the login page instead of the product", async ({ page }) => {
  await stubSignedOut(page);

  await page.goto("environments");

  await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
  await expect(page.getByTestId("sidebar")).toHaveCount(0);
});

test("signing in opens the product, and signing out closes it again", async ({ page }) => {
  await stubLogin(page);

  await page.goto("environments");

  await page.getByLabel("Username").fill(e2eUsername);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();

  // Signing in settles the acting team, which opens Home.
  await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
  await expect(page.getByText(e2eUsername)).toBeVisible();

  await page.getByRole("button", { name: "Sign out" }).click();

  await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
});

test("a wrong password says so and stays on the form", async ({ page }) => {
  await stubLogin(page);

  await page.goto("login");

  await page.getByLabel("Username").fill(e2eUsername);
  await page.getByLabel("Password").fill("the wrong secret");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByRole("alert")).toHaveText("That username and password do not match.");
  await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
});

test("the login page offers no way to create an organization", async ({ page }) => {
  await stubSignedOut(page);

  await page.goto("login");

  await expect(page.getByText("There is no sign up.", { exact: false })).toBeVisible();
  await expect(page.getByRole("link", { name: /sign up/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /create account/i })).toHaveCount(0);
});

test("the login page renders in both themes", async ({ page }) => {
  await stubSignedOut(page);

  await page.goto("login");
  await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();

  const isDark = () => page.locator("html").evaluate((node) => node.classList.contains("dark"));
  const startsDark = await isDark();

  await page.getByRole("button", { name: /Switch to (dark|light) theme/ }).click();

  await expect.poll(isDark).toBe(!startsDark);
  await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
  await expect(page.getByLabel("Username")).toBeVisible();
});

test("an invite link sets a password and hands over to sign in", async ({ page }) => {
  await stubLogin(page);
  // Reading the link and claiming it both answer with the username it is for.
  await page.route("**/api/auth/invite**", (route) =>
    route.fulfill({ json: { username: e2eUsername } }),
  );

  await page.goto("claim?token=a-real-token");

  await expect(page.getByText(`This link sets the password for ${e2eUsername}.`)).toBeVisible();
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Password again").fill(password);
  await page.getByRole("button", { name: "Set password" }).click();

  await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
  await expect(page.getByLabel("Username")).toHaveValue(e2eUsername);
});

test("a spent invite link says so and offers no form", async ({ page }) => {
  await stubSignedOut(page);
  await page.route("**/api/auth/invite**", (route) =>
    route.fulfill({
      status: 409,
      json: {
        code: "ErrConflict",
        message: "That invite link has already been used. Ask Percona for a new one.",
      },
    }),
  );

  await page.goto("claim?token=a-spent-token");

  await expect(
    page.getByRole("heading", { level: 1, name: "This link cannot be used" }),
  ).toBeVisible();
  await expect(page.getByLabel("Password", { exact: true })).toHaveCount(0);
});
