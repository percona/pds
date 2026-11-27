import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AppShell } from "@/components/layout/app-shell";
import { DatabaseTabs } from "@/features/databases";
import type { FeatureFlags } from "@/features/flags";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

/**
 * The chart defaults are what a hub deploys. Reading them here is the
 * check that a surface named false there is gone from the shell, and that a
 * surface left unnamed is still reachable.
 */
function hubOverlayFlags(): Partial<FeatureFlags> {
  const source = readFileSync(resolve(process.cwd(), "../../charts/pds/values.yaml"), "utf8");
  const block = source.match(/\n  features:\n((?:    [a-z-]+: (?:true|false)\n)+)/);
  if (!block) {
    throw new Error("values.yaml has no adapter.features block");
  }

  const flags: Partial<FeatureFlags> = {};
  for (const line of block[1].trim().split("\n")) {
    const [name, value] = line.trim().split(": ");
    flags[name as keyof FeatureFlags] = value === "true";
  }

  return flags;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** An environment with everything the sidebar and the pages ask for wired up. */
function stubAdapter(overrides: Record<string, unknown> = {}) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: string) => {
      const path = String(input);
      for (const [prefix, body] of Object.entries(overrides)) {
        if (path.startsWith(prefix)) {
          return Promise.resolve(jsonResponse(body));
        }
      }
      if (path === "/api/connections") {
        return Promise.resolve(
          jsonResponse({
            servicenow: { configured: false },
            assistant: { configured: false },
            cursor: { configured: false },
          }),
        );
      }
      if (path === "/api/environments") {
        return Promise.resolve(jsonResponse({ environments: [] }));
      }
      if (path.startsWith("/api/activity")) {
        return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
      }
      if (path.startsWith("/api/support/tickets")) {
        return Promise.resolve(jsonResponse({ tickets: [] }));
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );
}

beforeEach(() => {
  stubAdapter();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderShell() {
  return renderWithProviders(
    <AppShell>
      <p>page body</p>
    </AppShell>,
  );
}

describe("navigation", () => {
  it("drops environments when the surface is off", async () => {
    renderWithProviders(
      <AppShell>
        <p>page body</p>
      </AppShell>,
      { flags: { clusters: false } },
    );

    await waitFor(() =>
      expect(screen.getByRole("navigation", { name: "Main" })).toBeInTheDocument(),
    );
    expect(screen.queryByRole("link", { name: "Environments" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Databases" })).not.toBeInTheDocument();
  });

  it("drops activity when the surface is off", async () => {
    renderWithProviders(
      <AppShell>
        <p>page body</p>
      </AppShell>,
      { flags: { activity: false } },
    );

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Environments" })).toBeInTheDocument(),
    );
    expect(screen.queryByRole("link", { name: "Activity" })).not.toBeInTheDocument();
  });

  it("drops support when the surface is off", async () => {
    renderWithProviders(
      <AppShell>
        <p>page body</p>
      </AppShell>,
      { flags: { support: false } },
    );

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Environments" })).toBeInTheDocument(),
    );
    expect(screen.queryByRole("link", { name: "Support" })).not.toBeInTheDocument();
  });

  it("drops AI agents when the surface is off", async () => {
    renderWithProviders(
      <AppShell>
        <p>page body</p>
      </AppShell>,
      { flags: { mcp: false } },
    );

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Environments" })).toBeInTheDocument(),
    );
    expect(screen.queryByRole("link", { name: "AI agents" })).not.toBeInTheDocument();
  });

  it("links to AI agents when the surface is on", async () => {
    renderShell();

    expect(await screen.findByRole("link", { name: "AI agents" })).toHaveAttribute(
      "href",
      "/agents",
    );
  });

  it("keeps every entry when nothing is turned off", async () => {
    renderShell();

    const nav = await screen.findByRole("navigation", { name: "Main" });
    await within(nav).findByRole("link", { name: "Support" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent?.trim()),
    ).toEqual(["Environments", "Alerts", "AI agents", "Connectors", "Activity", "Support"]);
  });
});

describe("chat", () => {
  it("leaves the chat widget out when the surface is off", async () => {
    renderWithProviders(
      <AppShell>
        <p>page body</p>
      </AppShell>,
      { flags: { chat: false } },
    );

    await waitFor(() => expect(screen.getByRole("main")).toHaveTextContent("page body"));
    expect(screen.queryByRole("button", { name: "Ask PDS" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Chats" })).not.toBeInTheDocument();
  });

  it("renders the chat widget when the surface is on", () => {
    renderShell();

    expect(screen.getByRole("button", { name: "Ask PDS" })).toBeInTheDocument();
  });
});

describe("hub overlay", () => {
  const flags = hubOverlayFlags();

  it("names only activity, which a hub turns on", () => {
    expect(flags).toEqual({ activity: true });
  });

  it("offers chat with the Claude and Cursor keys", async () => {
    stubAdapter({ "/api/environments": { hub: true, environments: [] } });

    renderWithProviders(<AppRoutes />, { route: "/connectors", flags });

    await userEvent.click(await screen.findByRole("button", { name: "Connections" }));
    expect(await screen.findByTestId("connection-assistant")).toBeInTheDocument();
    expect(screen.getByTestId("connection-cursor")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ask PDS" })).toBeInTheDocument();
  });

  it("shows activity and keeps environments and support", async () => {
    stubAdapter({ "/api/environments": { hub: true, environments: [] } });

    renderWithProviders(
      <AppShell>
        <p>page body</p>
      </AppShell>,
      { flags },
    );

    const nav = await screen.findByRole("navigation", { name: "Main" });
    await within(nav).findByRole("link", { name: "Environments" });
    await within(nav).findByRole("link", { name: "Support" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent?.trim()),
    ).toEqual(["Environments", "Alerts", "AI agents", "Connectors", "Activity", "Support"]);
    expect(screen.queryByRole("link", { name: "Databases" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ask PDS" })).toBeInTheDocument();
  });

  it("serves activity on the hub", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/activity", flags });
    expect(await screen.findByRole("heading", { level: 1, name: "Activity" })).toBeInTheDocument();
  });

  it("still serves the environments page", async () => {
    stubAdapter({ "/api/environments": { hub: true, environments: [] } });

    renderWithProviders(<AppRoutes />, { route: "/environments", flags });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Environments" }),
    ).toBeInTheDocument();
  });

  it("answers not found for the old platform status path", async () => {
    renderWithProviders(<AppRoutes />, { route: "/health", flags });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Page not found" }),
    ).toBeInTheDocument();
  });
});

/** A surface that is off must not stay reachable by its own link. */
describe("routes", () => {
  const cases: { name: string; route: string; flags: Record<string, boolean> }[] = [
    { name: "databases", route: "/environments/spoke-a", flags: { databases: false } },
    { name: "create database", route: "/environments/spoke-a/new", flags: { databases: false } },
    {
      name: "database",
      route: "/databases/spoke-a/pds.orders",
      flags: { databases: false },
    },
    { name: "environments", route: "/environments", flags: { clusters: false } },
    { name: "connect environment", route: "/environments/new", flags: { clusters: false } },
    { name: "activity", route: "/activity", flags: { activity: false } },
    { name: "one change", route: "/activity/000042", flags: { activity: false } },
    { name: "support", route: "/support", flags: { support: false } },
    { name: "one ticket", route: "/support/CS0001", flags: { support: false } },
    { name: "AI agents", route: "/agents", flags: { mcp: false } },
  ];

  for (const { name, route, flags } of cases) {
    it(`answers not found for ${name} when the surface is off`, async () => {
      renderWithProviders(<AppRoutes />, { route, flags });

      expect(
        await screen.findByRole("heading", { level: 1, name: "Page not found" }),
      ).toBeInTheDocument();
    });
  }
});

async function tabNames(): Promise<string[]> {
  const list = await screen.findByRole("tablist");

  return within(list)
    .getAllByRole("tab")
    .map((tab) => tab.textContent?.trim() ?? "");
}

describe("database detail", () => {
  const database = {
    id: "spoke-a/pds.orders-dev",
    environment: "spoke-a",
    name: "orders-dev",
    engine: "mysql",
    profile: "dev",
    namespace: "pds",
    status: "ready",
    members: 1,
    cpu: 1,
    memoryGb: 2,
    storageGb: 20,
    connection: {
      host: "orders-dev",
      port: 3306,
      username: "app",
      maskedUri: "mysql://app:••••••••@orders-dev:3306/app",
    },
  };

  function renderDetail(flags: Record<string, boolean> = {}) {
    stubAdapter({ "/api/databases/spoke-a/pds.orders-dev": database });

    return renderWithProviders(<AppRoutes />, {
      route: "/databases/spoke-a/pds.orders-dev",
      flags,
    });
  }

  it("shows every tab when nothing is turned off", async () => {
    renderDetail();

    expect(await tabNames()).toEqual([
      "Overview",
      "Backups",
      "Metrics",
      "Activity",
      "Alerts",
      "History",
      "Settings",
    ]);
  });

  it("drops the backups tab and the backup action when backups are off", async () => {
    renderDetail({ backups: false });

    expect(await tabNames()).not.toContain("Backups");
    expect(screen.queryByRole("button", { name: "Back up now" })).not.toBeInTheDocument();
  });

  it("drops the metrics tab when metrics are off", async () => {
    renderDetail({ metrics: false });

    expect(await tabNames()).not.toContain("Metrics");
  });

  it("drops the activity tab when activity is off", async () => {
    renderDetail({ activity: false });

    expect(await tabNames()).not.toContain("Activity");
  });

  it("keeps overview and settings whatever else is off", async () => {
    renderDetail({ backups: false, metrics: false, activity: false });

    expect(await tabNames()).toEqual(["Overview", "Alerts", "History", "Settings"]);
  });

  it("puts the page back on overview when the remembered tab is turned off", async () => {
    // A chat action can ask for a tab that is off. That tab has no
    // trigger, so the action would otherwise do nothing visible. The page has
    // to be told, not just rendered differently.
    const onTabChange = vi.fn();
    renderWithProviders(
      <DatabaseTabs
        database={database as never}
        tab="activity"
        onTabChange={onTabChange}
        onBackUpNow={() => {}}
        backingUp={false}
        onRestore={() => {}}
        onDelete={() => {}}
      />,
      { flags: { activity: false } },
    );

    await waitFor(() => expect(onTabChange).toHaveBeenCalledWith("overview"));
  });

  it("leaves the page on a tab that is still reachable", async () => {
    const onTabChange = vi.fn();
    renderWithProviders(
      <DatabaseTabs
        database={database as never}
        tab="metrics"
        onTabChange={onTabChange}
        onBackUpNow={() => {}}
        backingUp={false}
        onRestore={() => {}}
        onDelete={() => {}}
      />,
    );

    await screen.findByRole("tablist");
    expect(onTabChange).not.toHaveBeenCalled();
  });

  it("leaves the overview metrics out when metrics are off", async () => {
    renderDetail({ metrics: false });

    const blocks = await screen.findByTestId("overview-blocks");

    expect(blocks.querySelector('[data-block="metrics"]')).toBeNull();
    expect(blocks.querySelector('[data-block="details"]')).not.toBeNull();
  });

  it("keeps backups and metrics on the hub overlay", async () => {
    renderDetail(hubOverlayFlags());

    expect(await tabNames()).toEqual([
      "Overview",
      "Backups",
      "Metrics",
      "Activity",
      "Alerts",
      "History",
      "Settings",
    ]);

    const blocks = await screen.findByTestId("overview-blocks");
    expect(blocks.querySelector('[data-block="metrics"]')).not.toBeNull();
    expect(blocks.querySelector('[data-block="alerts"]')).toBeNull();
    expect(blocks.querySelector('[data-block="recommendations"]')).toBeNull();
    expect(blocks.querySelector('[data-block="details"]')).not.toBeNull();
    expect(screen.getByRole("button", { name: "Back up now" })).toBeInTheDocument();
  });
});
