import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The Experimental label under the product name in the sidebar. The console is
// signed in by the fixtures, and the label needs nothing from the control plane.

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

for (const theme of ["light", "dark"] as const) {
  test(`the Experimental label and its popup render in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await page.goto("environments");

    const sidebar = page.getByTestId("sidebar");
    const label = sidebar.locator("button", { hasText: "Experimental" });
    await expect(label).toBeVisible();

    // The header keeps the theme toggle inside the sidebar and does not grow.
    const header = sidebar.locator("div").first();
    const toggle = sidebar.getByRole("button", { name: /Switch to/ });
    const sidebarBox = await sidebar.boundingBox();
    const headerBox = await header.boundingBox();
    const toggleBox = await toggle.boundingBox();
    expect(headerBox?.height).toBe(56);
    // One line of text-sm type is under 24px tall, and a wrapped name is not.
    expect(
      (await sidebar.getByText("Percona Database Service").boundingBox())?.height,
    ).toBeLessThan(24);
    const nameBox = await sidebar.getByText("Percona Database Service").boundingBox();
    expect(nameBox!.x + nameBox!.width).toBeLessThanOrEqual(toggleBox!.x);
    expect(toggleBox!.width).toBe(28);
    expect(toggleBox!.x + toggleBox!.width).toBeLessThanOrEqual(sidebarBox!.x + sidebarBox!.width);
    await page.screenshot({ path: `test-results/experimental-label-${theme}.png` });

    const before = await label.boundingBox();
    await label.click();
    const popup = page.getByRole("dialog");
    await expect(popup).toBeVisible();
    expect(await label.boundingBox()).toEqual(before);
    await expect(
      popup.getByText("Percona Database Service is an experimental project."),
    ).toBeVisible();
    await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
    await page.screenshot({ path: `test-results/experimental-popup-${theme}.png` });

    await page.keyboard.press("Escape");
    await expect(popup).toBeHidden();
    await expect(label).toBeFocused();
  });
}
