import type { Page } from "@playwright/test";
import { expect, spokeA, stubHub, test } from "./fixtures";

// The preview server serves the built SPA only, so the adapter is stubbed
// here. The adapter side is covered by Go tests in internal/api and
// internal/hub.

type Window = {
  state: string;
  level: string;
  expiresAt: string;
  extendingTo?: string;
  closedReason?: string;
};

/** A hub whose one environment keeps the window the page opens and ends. */
async function stubSupportAccess(page: Page) {
  let window: Window | null = null;
  await stubHub(page, [spokeA]);
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({ json: { username: "dev", role: "admin" } }),
  );
  // The summary rides the list while a window is opening or open.
  await page.route("**/api/environments", (route) =>
    route.fulfill({
      json: {
        hub: true,
        environments: [
          {
            ...spokeA,
            ...(window && window.state !== "closed" ? { supportAccess: window } : {}),
          },
        ],
      },
    }),
  );
  await page.route("**/api/environments/spoke-a/support-access", async (route) => {
    const method = route.request().method();
    if (method === "POST") {
      const body = route.request().postDataJSON() as { level: string; hours: number };
      window = {
        state: "open",
        level: body.level,
        expiresAt: new Date(Date.now() + body.hours * 3_600_000).toISOString(),
      };

      return route.fulfill({ status: 201, json: { window } });
    }
    if (method === "PATCH") {
      const body = route.request().postDataJSON() as { hours: number };
      window = window && {
        ...window,
        extendingTo: new Date(Date.now() + body.hours * 3_600_000).toISOString(),
      };

      return route.fulfill({ json: { window } });
    }
    if (method === "DELETE") {
      window = window && { ...window, state: "closed", closedReason: "ended" };

      return route.fulfill({ status: 204 });
    }

    // The environment confirms an extension by the panel's next read.
    if (window?.extendingTo) {
      window = { ...window, expiresAt: window.extendingTo, extendingTo: undefined };
    }

    return route.fulfill({ json: { window } });
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`an admin opens and ends support access in ${theme} mode`, async ({ page }) => {
    await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
    await stubSupportAccess(page);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("environments/spoke-a");
    await page.getByRole("tab", { name: "Support access" }).click();
    await expect(page.getByRole("radio", { name: /Read only/ })).toBeChecked();
    await expect(page.getByLabel("Duration")).toHaveValue("4");
    await page.screenshot({
      path: `test-results/support-access-open-${theme}.png`,
      fullPage: true,
    });

    await page.getByRole("radio", { name: /Admin/ }).check();
    await page.getByLabel("ServiceNow case number").fill("CS0012345");
    await page.getByRole("button", { name: "Open support access" }).click();

    await expect(page.getByText(/Support access is open for/)).toBeVisible();
    await expect(page.getByRole("group", { name: "Support access status" })).toContainText(
      "Support access on, admin",
    );
    await page.screenshot({ path: `test-results/support-access-on-${theme}.png`, fullPage: true });

    await page.getByLabel("Extend access").selectOption("24");
    await page.getByRole("button", { name: "Extend", exact: true }).click();
    await expect(page.getByText(/Extending until/)).toBeVisible();
    await page.screenshot({
      path: `test-results/support-access-extending-${theme}.png`,
      fullPage: true,
    });
    await expect(page.getByText(/Extending until/)).toHaveCount(0);
    await expect(page.getByText(/23h \d\dm/).first()).toBeVisible();

    await page.getByRole("button", { name: "End access" }).click();
    const dialog = page.getByRole("dialog", { name: "End support access?" });
    await dialog.getByRole("button", { name: "End access" }).click();

    await expect(page.getByText(/Access was ended/)).toBeVisible();
    await expect(page.getByRole("group", { name: "Support access status" })).toHaveCount(0);
    await page.screenshot({
      path: `test-results/support-access-ended-${theme}.png`,
      fullPage: true,
    });
  });
}
