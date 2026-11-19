import { expect, test } from "./fixtures";

test("the app loads the environments page", async ({ page }) => {
  await page.goto("./");

  await expect(page.getByRole("heading", { level: 1, name: "Environments" })).toBeVisible();
  await expect(page.getByTestId("sidebar").getByText("PDS", { exact: true })).toBeVisible();
});

test("Open chat is on the page and Chat is not in the sidebar", async ({ page }) => {
  await page.goto("./");

  await expect(page.getByRole("button", { name: "Chat", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Open chat" })).toBeVisible();
});

test("the theme toggle switches the html class", async ({ page }) => {
  await page.goto("./");

  const isDark = () => page.locator("html").evaluate((node) => node.classList.contains("dark"));
  const startsDark = await isDark();

  await page.getByRole("button", { name: /Switch to (dark|light) theme/ }).click();

  await expect.poll(isDark).toBe(!startsDark);
});
