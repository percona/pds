import type { Page } from "@playwright/test";

/** The user the signed-in specs run as. */
export const e2eUsername = "dev";

/**
 * Answers the session and the flag set as a signed-in browser.
 *
 * The preview server the e2e suite runs against has no adapter to issue a real
 * session, and every screen below the login page needs one. Login itself is
 * driven against these same routes in login.spec.ts, where the answers are the
 * point rather than the setup.
 */
export async function stubSignedIn(page: Page) {
  // Registered first, so a later route wins. The preview proxies /api to a
  // local adapter, and a 401 from any read signs the browser out. Anything a
  // spec did not stub answers 501 instead.
  await page.route("**/api/**", (route) =>
    route.fulfill({
      status: 501,
      json: { code: "ErrNotImplemented", message: "Not stubbed." },
    }),
  );
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({ json: { username: e2eUsername } }),
  );
  await page.route("**/api/features", (route) => route.fulfill({ json: { flags: {} } }));
}

/** Answers the session route as nobody signed in. */
export async function stubSignedOut(page: Page) {
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({
      status: 401,
      json: { code: "ErrValidation", message: "Sign in to continue." },
    }),
  );
}
