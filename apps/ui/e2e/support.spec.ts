import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// The sanitization and the ServiceNow calls are covered by Go tests in
// internal/service and internal/client/servicenow.
const openTicket = {
  number: "CS0061234",
  id: "3f1c9a2e6b7d4e8fa0c1d2e3f4a5b6c7",
  state: "Awaiting customer",
  priority: "3 - Moderate",
  account: "Example Corp",
  contact: "Dana Platform",
  subject: "Cannot create database orders-prod",
  description: "Database: orders-prod\nProfile: prod\nEngine: mysql 8.0.36",
  technology: "MySQL",
  openedAt: "2026-08-20T09:12:33Z",
  updatedAt: "2026-08-21T14:02:10Z",
  portalUrl: "https://example.service-now.com/percona",
  externalUrl: "https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
  comments: [
    {
      at: "2026-08-20T11:40:02Z",
      author: "percona.support",
      label: "Additional comments",
      body: "Thanks for the report. Could you attach the operator events?",
    },
  ],
};

const resolvedTicket = {
  number: "CS0061198",
  state: "Resolved",
  priority: "4 - Low",
  subject: "Backup of orders-staging failed",
  openedAt: "2026-08-11T08:04:00Z",
  resolvedAt: "2026-08-12T16:30:41Z",
  updatedAt: "2026-08-12T16:30:41Z",
};

const publicPortal = "https://www.percona.com/services/support";

/** An adapter with a ServiceNow Secret. */
async function stubSupport(page: Page) {
  const replies: string[] = [];
  await page.route("**/api/support/tickets/**", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as { body?: string };
      if (body.body) {
        replies.push(body.body);
      }

      return route.fulfill({ status: 204, body: "" });
    }

    return route.fulfill({
      json: {
        ...openTicket,
        comments: [
          ...openTicket.comments,
          ...replies.map((body) => ({
            at: "2026-09-03T12:00:00Z",
            author: "Dana Platform",
            body,
          })),
        ],
      },
    });
  });
  // The list carries a scan cursor, so this matches the path with or without a
  // query. It stops short of the ticket detail path, which is routed above.
  await page.route(/\/api\/support\/tickets(\?.*)?$/, (route) =>
    route.fulfill({
      json: {
        tickets: [openTicket, resolvedTicket],
        portalUrl: "https://example.service-now.com/percona",
      },
    }),
  );
}

/** An adapter with no ServiceNow Secret, which answers 501 everywhere. */
async function stubUnconfigured(page: Page) {
  await page.route("**/api/support/tickets**", (route) =>
    route.fulfill({
      status: 501,
      json: {
        code: "ErrNotImplemented",
        message:
          "Support tickets need a ServiceNow connection, which this adapter does not have. " +
          "Use the Percona support portal instead.",
      },
    }),
  );
}

test.describe("support tickets", () => {
  test("the sidebar reaches the support page and a ticket", async ({ page }) => {
    await stubSupport(page);
    await page.goto("./");

    await page.getByRole("link", { name: "Support" }).click();

    await expect(page.getByRole("heading", { level: 1, name: "Support" })).toBeVisible();
    await expect(page.getByTestId("ticket-row-CS0061234")).toContainText(
      "Cannot create database orders-prod",
    );
    await expect(page.getByTestId("ticket-row-CS0061198")).toContainText("Resolved");

    await page.getByRole("link", { name: "CS0061234" }).click();

    await expect(page.getByTestId("ticket-detail")).toContainText("CS0061234");
    await expect(page.getByTestId("ticket-state")).toContainText("Awaiting customer");
    await expect(page.getByTestId("ticket-awaiting-customer")).toContainText("closes after 7 days");
    await expect(page.getByTestId("ticket-comment")).toContainText(
      "Could you attach the operator events?",
    );
    await expect(page.getByTestId("ticket-comment-label")).toContainText("Additional comments");
    await expect(page.getByRole("link", { name: "Open in ServiceNow" })).toHaveAttribute(
      "href",
      openTicket.externalUrl,
    );
  });

  test("a ticket reads the same in dark mode", async ({ page }) => {
    await stubSupport(page);
    await page.goto("support/CS0061234");

    await expect(page.getByTestId("ticket-detail")).toBeVisible();

    const isDark = () => page.locator("html").evaluate((node) => node.classList.contains("dark"));
    const startsDark = await isDark();

    await page.getByRole("button", { name: /Switch to (dark|light) theme/ }).click();
    await expect.poll(isDark).toBe(!startsDark);

    await expect(page.getByTestId("ticket-state")).toBeVisible();
    await expect(page.getByTestId("ticket-comment")).toBeVisible();
  });

  test("a ticket accepts a public reply", async ({ page }) => {
    await stubSupport(page);
    await page.goto("support/CS0061234");

    const reply = "The operator events are attached.";
    await page.getByLabel("Reply").fill(reply);
    await page.getByRole("button", { name: "Send reply" }).click();

    await expect(page.getByLabel("Reply")).toHaveValue("");
    await expect(page.getByTestId("ticket-comments")).toContainText(reply);
  });

  // No Secret means no send button, but the entry stays and the page says
  // where the connection is made and still offers a route to Percona.
  test("an adapter with no ServiceNow Secret points at Connectors and the portal", async ({
    page,
  }) => {
    await stubUnconfigured(page);
    await page.goto("./");

    await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
    await page.getByRole("link", { name: "Support" }).click();

    await expect(page.getByText("Support is not connected")).toBeVisible();
    await expect(page.getByRole("link", { name: "Open Connectors" })).toHaveAttribute(
      "href",
      /\/connectors$/,
    );
    await expect(page.getByRole("link", { name: "Open the support portal" })).toHaveAttribute(
      "href",
      publicPortal,
    );
  });
});
