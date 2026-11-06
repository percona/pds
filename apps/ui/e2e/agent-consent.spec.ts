import type { Page } from "@playwright/test";
import { expect, test } from "@playwright/test";
import { stubSignedIn } from "./session";

// The sign in page and the consent page are driven without the shared fixture:
// the session is part of what they decide.

type Client = {
  name: string;
  host: string;
  kind: "cimd" | "dcr" | "local";
  redirectHost: string;
  loopback: boolean;
};

const claude: Client = {
  name: "Claude Code",
  host: "claude.example",
  kind: "cimd",
  redirectHost: "claude.example",
  loopback: false,
};
const registered: Client = {
  name: "Cursor",
  host: "",
  kind: "dcr",
  redirectHost: "127.0.0.1",
  loopback: true,
};
const local: Client = {
  name: "PDS local client",
  host: "",
  kind: "local",
  redirectHost: "127.0.0.1",
  loopback: true,
};

const acme = {
  id: 1,
  name: "Acme",
  role: "admin",
  teams: [
    { id: 5, name: "payments" },
    { id: 6, name: "search" },
  ],
  ownTeamId: 5,
  access: "change_delete",
  passwords: false,
  sqlWrites: false,
};
const globex = {
  id: 2,
  name: "Globex",
  role: "user",
  teams: [{ id: 9, name: "core" }],
  ownTeamId: 9,
  access: "read",
  passwords: false,
  sqlWrites: false,
};

async function stubConsent(page: Page, client: Client, organizations: unknown[]) {
  await stubSignedIn(page);
  await page.route("**/api/agent-consent/lookup", (route) =>
    route.fulfill({
      json: { client, expiresAt: "2026-10-29T12:10:00Z", organizations },
    }),
  );
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1280, height: 1100 });
}

for (const theme of ["light", "dark"] as const) {
  test(`an app with a hosted identity asks for consent in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubConsent(page, claude, [{ ...acme, role: "user", teams: [acme.teams[0]] }]);

    await page.goto("authorize-agent?request=parked");

    await expect(page.getByRole("radio", { name: "Read only" })).toBeChecked();
    await expect(page.getByRole("checkbox")).toHaveCount(0);
    await page.screenshot({
      path: `test-results/agent-consent-hosted-${theme}.png`,
      fullPage: true,
    });
  });

  test(`the local client is offered both boxes, unticked, in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubConsent(page, local, [{ ...acme, passwords: true, sqlWrites: true }]);

    await page.goto("authorize-agent?request=parked");

    await expect(page.getByRole("checkbox")).toHaveCount(2);
    for (const box of await page.getByRole("checkbox").all()) {
      await expect(box).not.toBeChecked();
    }
    await page.screenshot({
      path: `test-results/agent-consent-local-${theme}.png`,
      fullPage: true,
    });
  });

  test(`a registered app on this computer with two organizations in ${theme} mode`, async ({
    page,
  }) => {
    await useTheme(page, theme);
    await stubConsent(page, registered, [acme, globex]);

    await page.goto("authorize-agent?request=parked");

    await page.getByRole("combobox", { name: "Organization" }).selectOption("2");
    await expect(page.getByRole("radio")).toHaveCount(1);
    await page.getByRole("combobox", { name: "Organization" }).selectOption("1");
    await expect(page.getByRole("radio")).toHaveCount(3);
    await page.screenshot({
      path: `test-results/agent-consent-registered-${theme}.png`,
      fullPage: true,
    });
  });

  test(`a request that is no longer waiting in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubSignedIn(page);
    await page.route("**/api/agent-consent/lookup", (route) =>
      route.fulfill({
        status: 404,
        json: { code: "ErrNotFound", message: "That sign in request has expired." },
      }),
    );

    await page.goto("authorize-agent?request=parked");

    await expect(
      page.getByRole("heading", { name: "This request is no longer waiting" }),
    ).toBeVisible();
    await page.screenshot({ path: `test-results/agent-consent-gone-${theme}.png`, fullPage: true });
  });
}

test("allowing posts the choice and follows the address the control plane answers with", async ({
  page,
}) => {
  await stubConsent(page, local, [{ ...acme, passwords: true, sqlWrites: true }]);
  let posted: unknown;
  await page.route("**/api/agent-consent/approve", (route) => {
    posted = route.request().postDataJSON();

    return route.fulfill({ json: { redirectUrl: "http://127.0.0.1:43211/callback?code=c0de" } });
  });
  await page.route("http://127.0.0.1:43211/**", (route) =>
    route.fulfill({ contentType: "text/html", body: "<p>the app's own page</p>" }),
  );

  await page.goto("authorize-agent?request=parked");
  await page.getByRole("radio", { name: "Read and change", exact: true }).check();
  await page.getByRole("checkbox", { name: /change data with SQL/ }).check();
  await page.getByRole("button", { name: "Allow access" }).click();

  await page.waitForURL("http://127.0.0.1:43211/callback?code=c0de");
  expect(posted).toEqual({
    request: "parked",
    organizationId: 1,
    teamId: 5,
    access: "change",
    passwords: false,
    sqlWrites: true,
  });
});

test("cancelling follows the access_denied address", async ({ page }) => {
  await stubConsent(page, claude, [acme]);
  await page.route("**/api/agent-consent/deny", (route) =>
    route.fulfill({ json: { redirectUrl: "http://127.0.0.1:43211/callback?error=access_denied" } }),
  );
  await page.route("http://127.0.0.1:43211/**", (route) =>
    route.fulfill({ contentType: "text/html", body: "<p>the app's own page</p>" }),
  );

  await page.goto("authorize-agent?request=parked");
  await page.getByRole("button", { name: "Cancel" }).click();

  await page.waitForURL("http://127.0.0.1:43211/callback?error=access_denied");
});

test("signing in returns to the authorization request, and only to one", async ({ page }) => {
  await page.route("**/api/**", (route) =>
    route.fulfill({ status: 501, json: { code: "ErrNotImplemented", message: "Not stubbed." } }),
  );
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({
      status: 401,
      json: { code: "ErrValidation", message: "Sign in to continue." },
    }),
  );
  await page.route("**/api/auth/methods", (route) => route.fulfill({ json: { methods: [] } }));
  await page.route("**/api/auth/login", (route) => route.fulfill({ json: { username: "dev" } }));
  await page.route("**/oauth/authorize?**", (route) =>
    route.fulfill({ contentType: "text/html", body: "<p>consent next</p>" }),
  );

  const next = "/oauth/authorize?client_id=https%3A%2F%2Fagent.example%2Fc.json&state=abc";
  await page.goto(`login?next=${encodeURIComponent(next)}`);
  await page.getByLabel("Username").fill("dev");
  await page.getByLabel("Password").fill("percona-dev-login");
  const arrived = page.waitForRequest(
    (request) => new URL(request.url()).pathname === "/oauth/authorize",
  );
  await page.getByRole("button", { name: "Sign in" }).click();

  const request = await arrived;
  expect(new URL(request.url()).pathname + new URL(request.url()).search).toBe(next);
});
