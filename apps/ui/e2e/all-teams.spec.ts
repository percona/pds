import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The All teams choice of the Team selector. The preview server has no control
// plane, so each list answers the way the control plane's own tests show it
// answers `team=all`: every Team's rows, each naming its Team.

const environments = [
  { name: "spoke-default", team: "default", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
  { name: "spoke-pay", team: "payments", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
];

const database = {
  id: "spoke-pay/pds.orders-dev",
  name: "orders-dev",
  engine: "mysql",
  profile: "dev",
  namespace: "pds",
  environment: "spoke-pay",
  team: "payments",
  status: "ready",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
};

async function stubAdmin(page: Page) {
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
  await page.route("**/api/users", (route) => route.fulfill({ json: { users: [] } }));
  await page.route("**/api/environments**", (route) =>
    route.fulfill({ json: { hub: true, hubUrl: "https://hub.example", environments } }),
  );
  await page.route("**/api/databases?**", (route) =>
    route.fulfill({ json: { databases: [database] } }),
  );
  await page.route("**/api/activity?**", (route) =>
    route.fulfill({
      json: {
        entries: [
          {
            id: "0001",
            at: "2026-09-08T12:00:00Z",
            actor: "ada",
            team: "payments",
            action: "database.create",
            target: "orders-dev",
            outcome: "succeeded",
          },
        ],
        cursor: "",
        truncated: false,
      },
    }),
  );
}

async function chooseAllTeams(page: Page) {
  await page.addInitScript(() => window.localStorage.setItem("pds-team:1", "all"));
}

test.describe("All teams", () => {
  test("the environments of every Team are listed with their Team", async ({ page }) => {
    await stubAdmin(page);
    const asked: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/api/environments")) {
        asked.push(new URL(request.url()).searchParams.get("team") ?? "");
      }
    });
    await page.goto("environments");

    await page.getByRole("combobox", { name: "Working in team" }).selectOption("all");
    await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
    await page.goto("environments");

    await expect(page.getByText("spoke-pay").first()).toBeVisible();
    await expect(page.getByText("spoke-default").first()).toBeVisible();
    await expect(page.getByTitle("Team").getByText("payments")).toBeVisible();
    expect(asked).toContain("all");
  });

  test("the databases of every Team are listed with their Team", async ({ page }) => {
    await stubAdmin(page);
    await chooseAllTeams(page);

    await page.goto("environments/spoke-pay");

    const row = page.getByTestId(`database-row-${database.id}`);
    await expect(row).toBeVisible();
    await expect(row.getByText("payments")).toBeVisible();
  });

  test("the activity of every Team names the Team of each change", async ({ page }) => {
    await stubAdmin(page);
    await chooseAllTeams(page);

    await page.goto("activity");

    await expect(page.getByTestId("activity-team")).toHaveText("payments");
  });

  test("connecting an environment asks for a Team and mints the command in it", async ({
    page,
  }) => {
    await stubAdmin(page);
    await chooseAllTeams(page);
    const minted: string[] = [];
    await page.route("**/api/connect/tokens**", (route) => {
      minted.push(new URL(route.request().url()).searchParams.get("team") ?? "");

      return route.fulfill({
        status: 201,
        json: {
          environment: "spoke-new",
          token: "jnt_abc123",
          region: "eu-west-1",
          command: "curl -fsSL https://hub.example/bootstrap.sh | bash -s -- --token jnt_abc123",
        },
      });
    });

    await page.goto("environments/new?mode=command");
    await page.getByLabel("Environment name").fill("spoke-new");
    await expect(page.getByLabel("Team", { exact: true })).toHaveValue("default");
    await page.getByLabel("Team", { exact: true }).selectOption("payments");
    await page.getByRole("button", { name: "Create command" }).click();

    await expect(page.getByText(/--token jnt_abc123/)).toBeVisible();
    expect(minted).toEqual(["payments"]);
  });

  test("creating a database goes to the environment's Team without asking", async ({ page }) => {
    await stubAdmin(page);
    await chooseAllTeams(page);
    const created: string[] = [];
    await page.route("**/api/databases?team=*", (route) => {
      if (route.request().method() !== "POST") {
        return route.fallback();
      }
      created.push(new URL(route.request().url()).searchParams.get("team") ?? "");

      return route.fulfill({ status: 201, json: { ...database, status: "provisioning" } });
    });

    await page.goto("environments/spoke-pay/new");

    await page.getByLabel("Name").fill("orders-new");
    await expect(page.getByLabel("Team", { exact: true })).toHaveCount(0);
    await page.getByRole("button", { name: "Create database" }).click();

    await expect.poll(() => created).toEqual(["payments"]);
  });

  test("a name taken in another Team does not count as connected here", async ({ page }) => {
    await stubAdmin(page);
    await chooseAllTeams(page);

    await page.goto("environments/new?mode=command");
    await page.getByLabel("Environment name").fill("spoke-pay");
    await expect(page.getByLabel("Team", { exact: true })).toHaveValue("default");
    await expect(page.getByText("spoke-pay is already connected.")).toHaveCount(0);

    await page.getByLabel("Team", { exact: true }).selectOption("payments");

    await expect(page.getByText("spoke-pay is already connected.")).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the lists render in ${theme} mode`, async ({ page }) => {
      await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
      await stubAdmin(page);
      await chooseAllTeams(page);
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.setViewportSize({ width: 1440, height: 900 });

      await page.goto("environments");

      await expect(page.getByTitle("Team").getByText("payments")).toBeVisible();
      await expect(page.locator("html")).toHaveClass(theme === "dark" ? /dark/ : /^(?!.*dark).*$/);
      await page.screenshot({ path: `test-results/all-teams-${theme}.png`, fullPage: true });
    });
  }
});
