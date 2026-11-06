import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

async function stubOrganization(page: Page, role: "admin" | "user") {
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({
      json: { username: "acme", user: "Ada Lovelace", organization: "Acme", team: "default", role },
    }),
  );
  await page.route("**/api/organization/ai", (route) =>
    route.fulfill({ json: { access: "change", passwords: true, sqlWrites: false } }),
  );
}

for (const theme of ["light", "dark"] as const) {
  test(`an admin sees the AI settings in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await stubOrganization(page, "admin");

    await page.goto("settings");

    await expect(page.getByRole("radio", { name: /Read and change/ })).toBeChecked();
    await expect(page.getByRole("checkbox", { name: /fetch database passwords/ })).toBeChecked();
    await page.screenshot({ path: `test-results/ai-settings-${theme}.png`, fullPage: true });
  });

  test(`a member sees what the organization allows in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await stubOrganization(page, "user");

    await page.goto("agents");

    const summary = page.getByRole("region", { name: "What your organization allows" });
    await expect(summary).toContainText("Read and change");
    await expect(summary.getByRole("checkbox")).toHaveCount(0);
    await page.screenshot({ path: `test-results/ai-agents-member-${theme}.png`, fullPage: true });
  });
}
