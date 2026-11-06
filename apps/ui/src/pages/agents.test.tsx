import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

type Stub = {
  session?: Record<string, unknown>;
  teams?: string[];
  ai?: { access: string; passwords: boolean; sqlWrites: boolean };
};

function stubSession({ session = { username: "acme" }, teams = [], ai }: Stub = {}) {
  const answers: Record<string, unknown> = {
    "/api/auth/session": session,
    "/api/auth/methods": { methods: ["oidc", "password"] },
    "/api/teams": { teams: teams.map((name) => ({ name })) },
    "/api/users": { users: [] },
    "/api/agent-sessions": { sessions: [] },
    ...(ai ? { "/api/organization/ai": ai } : {}),
  };
  vi.stubGlobal(
    "fetch",
    vi.fn((input: string) =>
      Promise.resolve(
        new Response(JSON.stringify(answers[String(input)] ?? {}), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    ),
  );
}

beforeEach(() => stubSession());

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

async function openPage() {
  const user = userEvent.setup();
  renderWithProviders(<AppRoutes />, { route: "/agents" });
  await screen.findByRole("heading", { level: 1, name: "AI agents" });

  return user;
}

async function copied(user: ReturnType<typeof userEvent.setup>, button: string) {
  await user.click(await screen.findByRole("button", { name: button }));

  return navigator.clipboard.readText();
}

const origin = window.location.origin;

function headings() {
  return screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
}

describe("AgentsPage", () => {
  it("offers the plugins first, then the address, then the bridge", async () => {
    await openPage();
    await screen.findByRole("heading", { level: 2, name: "Connected agents" });

    expect(headings()).toEqual([
      "Claude Code plugin",
      "Cursor plugin",
      "Other clients with remote servers",
      "Clients that only run a local command",
      "Connected agents",
    ]);
  });

  it("copies the exact Claude Code install commands and the update commands", async () => {
    const user = await openPage();

    expect(await copied(user, "Copy install commands")).toBe(
      "claude plugin marketplace add percona/pds\nclaude plugin install pds@percona-pds",
    );
    expect(toast.success).toHaveBeenLastCalledWith("Install commands copied");
    expect(await copied(user, "Copy update commands")).toBe(
      "claude plugin marketplace update percona-pds\nclaude plugin update pds@percona-pds",
    );
  });

  it("starts Claude Code with this control plane's address when it is not the default", async () => {
    const user = await openPage();

    expect(await copied(user, "Copy start command")).toBe(`PDS_URL=${origin} claude`);
  });

  it("gives this control plane's /mcp address to add by hand in Cursor", async () => {
    const user = await openPage();

    const cursor = screen.getByRole("region", { name: "Cursor plugin" });
    expect(within(cursor).getByText("percona-pds")).toBeInTheDocument();
    await user.click(within(cursor).getByRole("button", { name: "Copy address" }));

    expect(await navigator.clipboard.readText()).toBe(`${origin}/mcp`);
    expect(toast.success).toHaveBeenLastCalledWith("Address copied");
  });

  it("leaves out the by-hand address and the start command on pds.percona.com", async () => {
    vi.stubGlobal("location", { ...window.location, origin: "https://pds.percona.com" });
    await openPage();

    const cursor = screen.getByRole("region", { name: "Cursor plugin" });
    expect(within(cursor).queryByRole("button", { name: "Copy address" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy start command" })).not.toBeInTheDocument();
    const others = screen.getByRole("region", { name: "Other clients with remote servers" });
    expect(within(others).getByText("https://pds.percona.com/mcp")).toBeInTheDocument();
  });

  it("shows the address for other clients and copies it", async () => {
    const user = await openPage();

    const others = screen.getByRole("region", { name: "Other clients with remote servers" });
    expect(within(others).getByText(`${origin}/mcp`)).toBeInTheDocument();
    expect(within(others).getByText(/Claude Desktop/)).toBeInTheDocument();
    await user.click(within(others).getByRole("button", { name: "Copy address" }));

    expect(await navigator.clipboard.readText()).toBe(`${origin}/mcp`);
  });

  it("copies the bridge login and config with no password or environment sign in", async () => {
    const user = await openPage();

    const login = await copied(user, "Copy login command");
    const config = await copied(user, "Copy config");

    expect(login).toBe(`PDS_URL=${origin} npx -y @gas-lab/pds-mcp@next login --access read`);
    expect(JSON.parse(config)).toEqual({
      mcpServers: {
        pds: { command: "npx", args: ["-y", "@gas-lab/pds-mcp@next"], env: { PDS_URL: origin } },
      },
    });
    expect(toast.success).toHaveBeenCalledWith("Login command copied");
    expect(toast.success).toHaveBeenLastCalledWith("Config copied");
    expect(document.body.textContent).not.toMatch(
      /PDS_USERNAME|PDS_PASSWORD|PDS_ORGANIZATION|PDS_TEAM|PDS_ALLOW|your-password/,
    );
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();
  });

  it("names the access the person chose in the login command, with more than color", async () => {
    const user = await openPage();

    expect(screen.getByRole("button", { name: /Read only/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    const write = screen.getByRole("button", { name: /Read and change/ });
    await user.click(write);

    expect(write).toHaveAttribute("aria-pressed", "true");
    expect(within(write).getByTestId("access-check")).toBeInTheDocument();
    expect(await copied(user, "Copy login command")).toContain(" login --access change");

    await user.click(screen.getByRole("button", { name: /Read, change and delete/ }));
    expect(await copied(user, "Copy login command")).toContain(" login --access delete");
  });

  it("says the agent signs in as the person", async () => {
    await openPage();

    expect(await screen.findByText(/signs in as you/)).toBeInTheDocument();
    expect(screen.queryByText(/username and password/)).not.toBeInTheDocument();
  });
});

describe("What the organization allows", () => {
  it("shows a member the limit and the two switches, read only", async () => {
    stubSession({
      session: { username: "bob", role: "user" },
      ai: { access: "change", passwords: true, sqlWrites: false },
    });
    await openPage();

    const summary = await screen.findByRole("region", { name: "What your organization allows" });
    expect(within(summary).getByText(/Read and change/)).toBeVisible();
    expect(within(summary).getByText("Fetch database passwords: allowed")).toBeVisible();
    expect(within(summary).getByText("Change data with SQL: not allowed")).toBeVisible();
    expect(within(summary).queryByRole("checkbox")).not.toBeInTheDocument();
    expect(within(summary).queryByRole("link")).not.toBeInTheDocument();
  });

  it("points an admin to Settings to change it", async () => {
    stubSession({
      session: { username: "ada", role: "admin" },
      ai: { access: "read", passwords: false, sqlWrites: false },
    });
    await openPage();

    const summary = await screen.findByRole("region", { name: "What your organization allows" });
    expect(within(summary).getByRole("link", { name: "Change in settings" })).toHaveAttribute(
      "href",
      "/settings",
    );
  });
});
