import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { FeatureFlags } from "@/features/flags";
import { AppRoutes } from "@/routes";
import {
  createMockActivityEntry,
  createMockAlert,
  createMockAlertReport,
  createMockDatabase,
  renderWithProviders,
} from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Reply = unknown | { status: number; body: unknown };

function isReply(value: unknown): value is { status: number; body: unknown } {
  return typeof value === "object" && value !== null && "status" in value && "body" in value;
}

/** Answers each path from the table, and records every path that was asked for. */
function stubHome(bodies: Record<string, Reply>) {
  const asked: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: string) => {
      const path = String(input).split("?")[0];
      asked.push(path);
      if (path in bodies) {
        const reply = bodies[path];
        return Promise.resolve(
          isReply(reply) ? jsonResponse(reply.body, reply.status) : jsonResponse(reply),
        );
      }
      if (path === "/api/auth/session") {
        return Promise.resolve(
          jsonResponse({ username: "ada", role: "admin", organization: "Acme" }),
        );
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );

  return asked;
}

function renderHome(flags?: Partial<FeatureFlags>) {
  return renderWithProviders(<AppRoutes />, { route: "/", flags });
}

const readyAccount = {
  id: "cc-1",
  accountId: "123456789012",
  alias: "prod-aws",
  state: "ready",
  allowedRegions: ["us-east-1"],
  stackRegion: "us-east-1",
  templateVersion: 1,
  latestTemplateVersion: 1,
  createdAt: "2026-11-01T00:00:00Z",
  environments: [],
};

const connected = { name: "spoke-a", status: "connected", lastSeen: new Date().toISOString() };

afterEach(() => {
  vi.unstubAllGlobals();
});

// Home renders the whole app shell, which is slow to mount the first time.
const slow = { timeout: 20_000 };
const settle = { timeout: 10_000 };

describe("Home setup checklist", slow, () => {
  it("starts with creating an environment when there is none", async () => {
    stubHome({
      "/api/environments": { environments: [] },
      "/api/databases": { databases: [] },
      "/api/cloud-connections": { cloudConnections: [] },
      "/api/agent-sessions": { sessions: [] },
    });

    renderHome();

    expect(
      await screen.findByRole("progressbar", { name: "Setup progress" }, settle),
    ).toHaveAttribute("aria-valuenow", "0");
    expect(screen.getByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connect an AWS account" })).toHaveAttribute(
      "href",
      "/environments/aws-accounts/new",
    );
    expect(screen.getByRole("link", { name: "Create an environment" })).toHaveAttribute(
      "href",
      "/environments/new",
    );
    expect(screen.getByRole("button", { name: "Create database" })).toBeDisabled();
    expect(screen.getByText(/Needs an environment first/)).toBeInTheDocument();
    expect(screen.queryByText(/join/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Needs attention" })).not.toBeInTheDocument();
  });

  it("names the connected AWS account and creates the environment through it", async () => {
    stubHome({
      "/api/environments": { environments: [] },
      "/api/databases": { databases: [] },
      "/api/cloud-connections": { cloudConnections: [readyAccount] },
      "/api/agent-sessions": { sessions: [] },
    });

    renderHome();

    expect(await screen.findByText("prod-aws", {}, settle)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Connect an AWS account" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create an environment" })).toHaveAttribute(
      "href",
      "/environments/new",
    );
  });

  it("leads to the create form of the environment once one exists", async () => {
    stubHome({
      "/api/environments": { environments: [connected] },
      "/api/databases": { databases: [] },
      "/api/cloud-connections": { cloudConnections: [] },
      "/api/agent-sessions": { sessions: [] },
    });

    renderHome();

    const link = await screen.findByRole("link", { name: "Create database" }, settle);
    expect(link).toHaveAttribute("href", "/environments/spoke-a/new");
    expect(screen.getByRole("progressbar", { name: "Setup progress" })).toHaveAttribute(
      "aria-valuenow",
      "1",
    );
    expect(screen.queryByRole("link", { name: "Create an environment" })).not.toBeInTheDocument();
  });

  it("marks the AI agent step done when an agent is signed in as the person", async () => {
    stubHome({
      "/api/environments": { environments: [] },
      "/api/databases": { databases: [] },
      "/api/cloud-connections": { cloudConnections: [] },
      "/api/agent-sessions": {
        sessions: [{ id: "s1", name: "Claude Code", mine: true, member: "ada" }],
      },
    });

    renderHome();

    const step = (await screen.findByText("Connect an AI agent", {}, settle)).closest("li");
    expect(step).not.toBeNull();
    expect(await within(step as HTMLElement).findByText("Done")).toBeInTheDocument();
    expect(within(step as HTMLElement).queryByRole("link", { name: "Set up" })).toBeNull();
  });

  it("does not count another member's agent", async () => {
    stubHome({
      "/api/environments": { environments: [] },
      "/api/databases": { databases: [] },
      "/api/cloud-connections": { cloudConnections: [] },
      "/api/agent-sessions": {
        sessions: [{ id: "s1", name: "Cursor", mine: false, member: "grace" }],
      },
    });

    renderHome();

    const step = (await screen.findByText("Connect an AI agent", {}, settle)).closest("li");
    expect(within(step as HTMLElement).getByRole("link", { name: "Set up" })).toHaveAttribute(
      "href",
      "/agents",
    );
  });

  it("leaves out the AI agent step and its read when MCP is off", async () => {
    const asked = stubHome({
      "/api/environments": { environments: [] },
      "/api/databases": { databases: [] },
      "/api/cloud-connections": { cloudConnections: [] },
    });

    renderHome({ mcp: false });

    expect(
      await screen.findByRole("progressbar", { name: "Setup progress" }, settle),
    ).toBeInTheDocument();
    expect(screen.queryByText("Connect an AI agent")).not.toBeInTheDocument();
    expect(asked).not.toContain("/api/agent-sessions");
  });
});

describe("Home fleet", slow, () => {
  const environments = [
    connected,
    { name: "spoke-b", status: "unreachable", lastSeen: "2026-09-08T12:00:00Z" },
    { name: "spoke-c", status: "pending" },
    {
      name: "aws-new",
      status: "unreachable",
      origin: "managed",
      state: "creating",
      connectionId: "cc-1",
    },
    {
      name: "aws-broken",
      status: "unreachable",
      origin: "managed",
      state: "failed",
      connectionId: "cc-1",
      provisioning: {
        run: { id: 1, kind: "create", status: "failed", startedAt: "2026-11-01T00:00:00Z" },
        progress: "failed",
        phases: [],
        failure: {
          sentence: "AWS refused to create the VPC: the account has reached its VPC limit.",
          action: "retry",
        },
      },
    },
  ];

  const databases = [
    createMockDatabase({
      id: "spoke-a/pds.orders",
      name: "orders",
      environment: "spoke-a",
      status: "ready",
      connection: { host: "orders", port: 3306 } as never,
    }),
    createMockDatabase({
      id: "spoke-a/pds.ledger",
      name: "ledger",
      environment: "spoke-a",
      status: "failed",
    }),
    createMockDatabase({
      id: "spoke-b/pds.cache",
      name: "cache",
      environment: "spoke-b",
      status: "updating",
    }),
  ];

  const alerts = createMockAlertReport({
    criticals: 1,
    alerts: [
      createMockAlert({
        id: "a",
        severity: "critical",
        environment: "spoke-a",
        databaseId: "spoke-a/pds.orders",
        summary: "orders is down",
      }),
      createMockAlert({
        id: "b",
        severity: "warning",
        environment: "spoke-a",
        databaseId: "spoke-a/pds.ledger",
        summary: "ledger disk is filling up",
      }),
    ],
  });

  function stubFleet(extra: Record<string, Reply> = {}) {
    return stubHome({
      "/api/environments": { environments },
      "/api/databases": { databases },
      "/api/cloud-connections": { cloudConnections: [readyAccount] },
      "/api/alerts": alerts,
      "/api/activity": { entries: [], cursor: "", truncated: false },
      ...extra,
    });
  }

  it("counts environments, databases and alerts with their breakdowns", async () => {
    stubFleet();

    renderHome();

    await screen.findByRole("region", { name: "Needs attention" }, settle);
    const main = screen.getByRole("main");
    const environmentsTile = within(main).getByText("Environments").closest("a");
    expect(environmentsTile).toHaveAttribute("href", "/environments");
    expect(environmentsTile).toHaveTextContent("5");
    expect(environmentsTile).toHaveTextContent("1 connected");
    expect(environmentsTile).toHaveTextContent("1 unreachable");
    expect(environmentsTile).toHaveTextContent("1 joining");
    expect(environmentsTile).toHaveTextContent("1 being created");
    expect(environmentsTile).toHaveTextContent("1 failed");

    const databasesTile = within(main).getByText("Databases").parentElement?.parentElement;
    expect(databasesTile).toHaveTextContent("3");
    expect(databasesTile).toHaveTextContent("1 ready");
    expect(databasesTile).toHaveTextContent("1 in progress");
    expect(databasesTile).toHaveTextContent("1 failed");

    const alertsTile = within(main).getByText("Alerts firing").closest("a");
    expect(alertsTile).toHaveAttribute("href", "/alerts");
    expect(alertsTile).toHaveTextContent("1 critical");
    expect(alertsTile).toHaveTextContent("1 warning");
  });

  it("says nothing is firing when no alert is", async () => {
    stubFleet({ "/api/alerts": createMockAlertReport({ criticals: 0, alerts: [] }) });

    renderHome();

    await screen.findByRole("region", { name: "Needs attention" }, settle);
    const main = screen.getByRole("main");
    const alertsTile = within(main).getByText("Alerts firing").closest("a");
    expect(
      await within(alertsTile as HTMLElement).findByText("Nothing firing"),
    ).toBeInTheDocument();
  });

  it("lists what needs attention, one row per alert and environment", async () => {
    stubFleet();

    renderHome();

    const region = await screen.findByRole("region", { name: "Needs attention" }, settle);
    expect(await within(region).findByText("orders is down")).toBeInTheDocument();
    const alertRow = within(region).getByText("orders is down").closest("li") as HTMLElement;
    expect(alertRow).toHaveTextContent("orders on spoke-a");
    expect(within(alertRow).getByRole("link", { name: "Open database" })).toHaveAttribute(
      "href",
      "/databases/spoke-a/pds.orders",
    );
    expect(within(region).getByText("ledger disk is filling up")).toBeInTheDocument();

    expect(
      within(region)
        .getByText(/stopped reporting/)
        .closest("li"),
    ).toHaveTextContent("spoke-b stopped reporting");
    expect(
      within(region)
        .getByText(/has not reported yet/)
        .closest("li"),
    ).toHaveTextContent("spoke-c has not reported yet");
    const failed = within(region)
      .getByText(/could not be created/)
      .closest("li");
    expect(failed).toHaveTextContent("aws-broken could not be created");
    expect(failed).toHaveTextContent("the account has reached its VPC limit");

    expect(within(region).queryByText(/aws-new/)).not.toBeInTheDocument();
    expect(within(region).getByText("5")).toBeInTheDocument();
    expect(region).toHaveTextContent("5 items");
  });

  it("does not call a managed environment that is being created unreachable", async () => {
    stubFleet();

    renderHome();

    const region = await screen.findByRole("region", { name: "Needs attention" }, settle);
    expect(within(region).queryByText(/aws-new stopped reporting/)).not.toBeInTheDocument();
    expect(screen.getAllByText(/unreachable/).length).toBe(1);
  });

  it("shows the last changes the way the Activity page names them", async () => {
    stubFleet({
      "/api/activity": {
        entries: [
          createMockActivityEntry({
            id: "000002",
            actor: "ada",
            action: "database.create",
            databaseId: "spoke-a/pds.orders",
            target: "orders",
            at: new Date().toISOString(),
          }),
          createMockActivityEntry({
            id: "000001",
            actor: "ada",
            agent: "Cursor",
            client: "mcp",
            action: "database.backup",
            databaseId: "spoke-a/pds.ledger",
            target: "ledger",
            outcome: "failed",
          }),
        ],
        cursor: "000002",
        truncated: false,
      },
    });

    renderHome();

    const region = await screen.findByRole("region", { name: "Recent activity" }, settle);
    const create = await within(region).findByRole("link", { name: "Create database" });
    expect(create).toHaveAttribute("href", "/activity/000002");
    expect(create.closest("li")).toHaveTextContent("Create database orders on spoke-a");
    expect(create.closest("li")).toHaveTextContent("ada");
    const backup = within(region).getByRole("link", { name: "Back up" }).closest("li");
    expect(backup).toHaveTextContent("Cursor for ada");
    expect(backup).toHaveTextContent("Failed");
    expect(within(region).getByRole("link", { name: "View all" })).toHaveAttribute(
      "href",
      "/activity",
    );
  });

  it("hides recent activity when the control plane has no activity log", async () => {
    stubFleet({
      "/api/activity": {
        status: 501,
        body: { code: "ErrNotImplemented", message: "activity is not enabled" },
      },
    });

    renderHome();

    expect(
      await screen.findByRole("region", { name: "Needs attention" }, settle),
    ).toBeInTheDocument();
    await vi.waitFor(() =>
      expect(screen.queryByRole("region", { name: "Recent activity" })).not.toBeInTheDocument(),
    );
  });

  it("asks which environment a new database runs on when there are several", async () => {
    stubFleet();
    const user = userEvent.setup();

    renderHome();

    await user.click(await screen.findByRole("button", { name: "Create database" }, settle));
    const dialog = await screen.findByRole("dialog", { name: "Create database" });
    expect(within(dialog).getByRole("radio", { name: /spoke-a/ })).toBeChecked();
    expect(within(dialog).getByRole("radio", { name: /spoke-b/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole("radio", { name: /spoke-c/ })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("radio", { name: /aws-new/ })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("radio", { name: /spoke-b/ }));
    await user.click(within(dialog).getByRole("button", { name: "Continue" }));

    expect(
      await screen.findByRole("heading", { level: 1, name: "Create database" }, settle),
    ).toBeInTheDocument();
  });
});
