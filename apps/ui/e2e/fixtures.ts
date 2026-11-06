import type { Page } from "@playwright/test";
import { test as base } from "@playwright/test";
import { stubSignedIn } from "./session";

export const spokeA = {
  name: "spoke-a",
  status: "connected",
  lastSeen: "2026-09-08T12:00:00Z",
};

export const spokeQuiet = {
  name: "spoke-quiet",
  status: "unreachable",
  lastSeen: "2026-09-08T11:00:00Z",
};

export const spokeJoining = { name: "spoke-joining", status: "unreachable" };

export const ordersDev = {
  id: "spoke-a/pds.orders-dev",
  name: "orders-dev",
  engine: "mysql",
  profile: "dev",
  namespace: "pds",
  environment: "spoke-a",
  status: "ready",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
  createdAt: "2026-09-08T09:30:00Z",
};

/**
 * Answers environment discovery as a hub with a connected environment.
 * The preview server has no adapter, so without this the shell keeps asking.
 */
export async function stubHub(page: Page, environments: unknown[] = [spokeA]) {
  await page.route("**/api/environments", (route) =>
    route.fulfill({ json: { hub: true, hubUrl: "https://hub.example", environments } }),
  );
  await page.route("**/api/connect", (route) =>
    route.fulfill({
      json: {
        hub: true,
        hubUrl: "https://hub.example",
        gitUrl: "https://git.example/hub/pds.git",
        clusterRegion: "eu-west-1",
      },
    }),
  );
  await page.route("**/api/connect/tokens", (route) =>
    route.fulfill({
      status: 201,
      json: {
        environment: "spoke-new",
        token: "jnt_abc123",
        region: "eu-west-1",
        command:
          "curl -fsSL https://hub.example/api/internal/connect/bootstrap.sh | bash -s -- --token jnt_abc123",
      },
    }),
  );
  await page.route("**/api/databases*", (route) =>
    route.fulfill({ json: { databases: [ordersDev] } }),
  );
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
  await page.route("**/api/alerts", (route) =>
    route.fulfill({ json: { environments: [], alerts: [], criticals: 0 } }),
  );
  // An unstubbed support read reaches a live control plane and answers 401,
  // which signs the browser out. A spec that needs tickets or a missing
  // connection registers its own route afterwards.
  await page.route("**/api/support/tickets**", (route) =>
    route.fulfill({
      json: { tickets: [], portalUrl: "https://support.example.com" },
    }),
  );
}

/**
 * The signed-in browser every spec below the login page assumes.
 *
 * It is a fixture rather than a call in each spec so that adding a screen
 * cannot accidentally leave one testing the login page instead of itself.
 * Every spec runs against a hub with the connected environment spoke-a.
 */
export const test = base.extend({
  page: async ({ page }, use) => {
    // Registered first, so a later stub wins. Anything left over must not reach
    // a live control plane: a 401 there signs the browser out.
    await page.route("**/api/**", (route) =>
      route.fulfill({
        status: 501,
        json: { code: "ErrNotImplemented", message: "Not stubbed." },
      }),
    );
    await stubSignedIn(page);
    await stubHub(page);
    await use(page);
  },
});

export { expect } from "@playwright/test";
