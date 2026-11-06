import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { ConnectedAgents } from "./components/connected-agents";
import type { AgentSession } from "./types";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
const later = new Date(Date.now() + 86_400_000).toISOString();

function session(overrides: Partial<AgentSession> = {}): AgentSession {
  return {
    id: "s1",
    name: "Cursor",
    host: "cursor.example",
    kind: "cimd",
    member: "Ann Lee",
    mine: true,
    team: "payments",
    access: "change",
    passwords: false,
    sqlWrites: false,
    createdAt: hourAgo,
    lastUsedAt: hourAgo,
    expiresAt: later,
    ...overrides,
  };
}

type Server = {
  sessions: AgentSession[];
  status?: number;
  deletes: string[];
  revokeStatus?: number;
};

function serve(initial: AgentSession[], extra: Partial<Server> = {}) {
  const server: Server = { sessions: initial, deletes: [], ...extra };
  vi.stubGlobal(
    "fetch",
    vi.fn((input: string, init?: RequestInit) => {
      const json = (status: number, body: unknown) =>
        Promise.resolve(
          new Response(status === 204 ? null : JSON.stringify(body), {
            status,
            headers: { "Content-Type": "application/json" },
          }),
        );
      if (init?.method === "DELETE") {
        server.deletes.push(String(input));
        if (server.revokeStatus === 404) {
          server.sessions = [];

          return json(404, { code: "not_found", message: "That agent is not connected any more." });
        }
        server.sessions = server.sessions.filter((s) => !String(input).endsWith(`/${s.id}`));

        return json(204, null);
      }
      if (server.status) {
        return json(server.status, { code: "not_found", message: "That endpoint does not exist." });
      }

      return json(200, { sessions: server.sessions });
    }),
  );

  return server;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("ConnectedAgents", () => {
  it("shows what each agent may do, who named it, when it last connected and a Revoke action", async () => {
    serve([
      session({ passwords: true, sqlWrites: true, kind: "local", host: "", name: "PDS local" }),
      session({ id: "s2", name: "Claude", host: "claude.example", access: "read" }),
    ]);

    renderWithProviders(<ConnectedAgents />);

    const rows = await screen.findAllByRole("row");
    const first = within(rows[1] as HTMLElement);
    expect(first.getByText("PDS local")).toBeInTheDocument();
    expect(first.getByText("Read and change")).toBeInTheDocument();
    expect(first.getByText("Fetches database passwords")).toBeInTheDocument();
    expect(first.getByText("Changes data with SQL")).toBeInTheDocument();
    expect(first.getByText("payments")).toBeInTheDocument();
    expect(first.getByText("1 hour ago")).toBeInTheDocument();
    expect(first.getByRole("button", { name: "Revoke PDS local" })).toBeInTheDocument();

    const second = within(rows[2] as HTMLElement);
    expect(second.getByText("Claude")).toBeInTheDocument();
    expect(second.getByText("claude.example")).toHaveClass("font-mono");
    expect(second.getByText("Name chosen by the app")).toBeInTheDocument();
    expect(second.getByText("Read only")).toBeInTheDocument();
    expect(second.queryByText("Fetches database passwords")).not.toBeInTheDocument();
  });

  it("says when an app registered itself and has no address", async () => {
    serve([session({ kind: "dcr", host: "" })]);

    renderWithProviders(<ConnectedAgents />);

    expect(await screen.findByText("No address to check")).toBeInTheDocument();
  });

  it("shows an empty state when no agent is connected", async () => {
    serve([]);

    renderWithProviders(<ConnectedAgents />);

    expect(await screen.findByText("No agents connected")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("names who signed an agent in when it is not the person looking", async () => {
    serve([session({ mine: true }), session({ id: "s2", mine: false, member: "Bob Ray" })]);

    renderWithProviders(<ConnectedAgents />);

    expect(await screen.findByText("Bob Ray")).toBeInTheDocument();
    expect(screen.getByText("You")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Revoke Cursor for Bob Ray" })).toBeInTheDocument();
  });

  it("asks before revoking, then removes the row", async () => {
    const user = userEvent.setup();
    const server = serve([session()]);
    renderWithProviders(<ConnectedAgents />);

    await user.click(await screen.findByRole("button", { name: "Revoke Cursor" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/Cursor stops working at once and has to sign in again/),
    ).toBeInTheDocument();
    expect(server.deletes).toEqual([]);

    await user.click(within(dialog).getByRole("button", { name: "Revoke" }));

    expect(await screen.findByText("No agents connected")).toBeInTheDocument();
    expect(server.deletes).toEqual(["/api/agent-sessions/s1"]);
    expect(toast.success).toHaveBeenCalledWith("Agent revoked");
  });

  it("keeps the agent when the person cancels", async () => {
    const user = userEvent.setup();
    const server = serve([session()]);
    renderWithProviders(<ConnectedAgents />);

    await user.click(await screen.findByRole("button", { name: "Revoke Cursor" }));
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Cancel" }),
    );

    expect(server.deletes).toEqual([]);
    expect(screen.getByText("Cursor")).toBeInTheDocument();
  });

  it("treats an agent that is already gone as revoked", async () => {
    const user = userEvent.setup();
    serve([session()], { revokeStatus: 404 });
    renderWithProviders(<ConnectedAgents />);

    await user.click(await screen.findByRole("button", { name: "Revoke Cursor" }));
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Revoke" }),
    );

    expect(await screen.findByText("No agents connected")).toBeInTheDocument();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("renders nothing when the control plane does not serve agents", async () => {
    serve([], { status: 404 });

    const { container } = renderWithProviders(<ConnectedAgents />);

    await vi.waitFor(() => expect(container).toBeEmptyDOMElement());
  });
});
