import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Calls = Array<{ method: string; path: string; body: unknown }>;

const admin = {
  username: "acme",
  user: "Ada",
  organization: "Acme",
  team: "default",
  role: "admin",
};

type Adapter = {
  session?: Record<string, unknown>;
  teams?: string[];
  environments?: () => Response;
};

/** Stubs the control plane for a Team's page and records every write. */
function stubAdapter(options: Adapter = {}) {
  const calls: Calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (method !== "GET") {
        const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
        calls.push({ method, path, body });

        return Promise.resolve(
          path === "/api/invitations"
            ? jsonResponse(
                {
                  ...(body as object),
                  id: 9,
                  expiresAt: "2026-12-01T10:00:00Z",
                  createdAt: "2026-11-24T10:00:00Z",
                  link: "https://pds.example/claim?token=team-token",
                  emailed: true,
                },
                201,
              )
            : new Response(null, { status: 204 }),
        );
      }
      if (path === "/api/auth/session") {
        return Promise.resolve(jsonResponse(options.session ?? admin));
      }
      if (path === "/api/teams") {
        return Promise.resolve(
          jsonResponse({
            teams: (options.teams ?? ["default", "payments"]).map((name) => ({ name })),
          }),
        );
      }
      if (path === "/api/users") {
        return Promise.resolve(
          jsonResponse({
            users: [
              { id: 1, name: "Ada Lovelace", team: "default", role: "admin" },
              {
                id: 2,
                name: "Grace Hopper",
                email: "grace@example.com",
                team: "payments",
                role: "user",
              },
            ],
          }),
        );
      }
      if (path === "/api/invitations") {
        return Promise.resolve(
          jsonResponse({
            invitations: [
              {
                id: 7,
                email: "linus@example.com",
                anyEmail: false,
                team: "payments",
                role: "user",
                expiresAt: "2026-12-01T10:00:00Z",
                createdAt: "2026-11-24T10:00:00Z",
              },
              {
                id: 8,
                email: "ken@example.com",
                anyEmail: false,
                team: "default",
                role: "user",
                expiresAt: "2026-12-01T10:00:00Z",
                createdAt: "2026-11-24T10:00:00Z",
              },
            ],
          }),
        );
      }
      if (path === "/api/environments?team=all") {
        return Promise.resolve(
          options.environments?.() ??
            jsonResponse({
              environments: [
                { name: "orders-prod", status: "connected", team: "payments" },
                {
                  name: "orders-next",
                  status: "unreachable",
                  origin: "managed",
                  state: "creating",
                  team: "payments",
                },
                { name: "core", status: "connected", team: "default" },
              ],
            }),
        );
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );

  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Team page", () => {
  it("shows the team's users, its pending invites and the environments it owns", async () => {
    stubAdapter();

    renderWithProviders(<AppRoutes />, { route: "/teams/payments" });

    expect(await screen.findByRole("heading", { level: 1, name: "payments" })).toBeVisible();
    expect(await screen.findByRole("cell", { name: "Grace Hopper" })).toBeVisible();
    expect(screen.queryByRole("cell", { name: "Ada Lovelace" })).not.toBeInTheDocument();
    expect(await screen.findByRole("cell", { name: "linus@example.com" })).toBeVisible();
    expect(screen.queryByRole("cell", { name: "ken@example.com" })).not.toBeInTheDocument();
    const environments = await screen.findByRole("list", { name: "Environments of payments" });
    expect(within(environments).getByText("orders-prod")).toBeVisible();
    expect(within(environments).getByText("Connected")).toBeVisible();
    expect(within(environments).getByText("Creating")).toBeVisible();
    expect(within(environments).queryByText("core")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to teams" })).toHaveAttribute("href", "/teams");
  });

  it("invites people into this team", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/teams/payments" });

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    const dialog = await screen.findByRole("dialog", { name: "Invite people" });
    await waitFor(() => expect(within(dialog).getByLabelText("Team")).toHaveValue("payments"));
    await userEvent.type(within(dialog).getByLabelText("Email addresses"), "alan@example.com");
    await userEvent.click(within(dialog).getByRole("button", { name: "Send invite" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/invitations",
          body: { email: "alan@example.com", anyEmail: false, team: "payments", role: "user" },
        },
      ]),
    );
    expect(await screen.findByRole("status")).toHaveTextContent("Invite sent to alan@example.com.");
  });

  it("offers to create an environment only where the console would create it in this team", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/teams/default" });

    expect(await screen.findByRole("link", { name: "Create environment" })).toHaveAttribute(
      "href",
      "/environments/new",
    );
  });

  it("says how to create an environment for a team the console does not act in", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/teams/payments" });

    expect(
      await screen.findByText(
        "A new environment belongs to the team the console acts in. Switch to payments to create one here.",
      ),
    ).toBeVisible();
    expect(screen.queryByRole("link", { name: "Create environment" })).not.toBeInTheDocument();
  });

  it("says when the team has no users and no environments", async () => {
    stubAdapter({ teams: ["default", "payments", "scratch"] });
    renderWithProviders(<AppRoutes />, { route: "/teams/scratch" });

    expect(await screen.findByText("No users in scratch yet")).toBeVisible();
    expect(await screen.findByText("No environments yet")).toBeVisible();
    expect(screen.getByText("No pending invites")).toBeVisible();
  });

  it("explains an environment read that failed", async () => {
    stubAdapter({
      environments: () =>
        jsonResponse({ code: "ErrInternal", message: "The environments could not be read." }, 500),
    });
    renderWithProviders(<AppRoutes />, { route: "/teams/payments" });

    expect(await screen.findByText("Environments could not be read")).toBeVisible();
    expect(screen.getByText("The environments could not be read.")).toBeVisible();
  });

  it("follows a rename to the team's new address", async () => {
    const calls = stubAdapter({ teams: ["default", "payments", "billing"] });
    renderWithProviders(<AppRoutes />, { route: "/teams/payments" });

    await userEvent.click(await screen.findByRole("button", { name: "Rename" }));
    const dialog = await screen.findByRole("dialog", { name: "Rename payments" });
    await userEvent.clear(within(dialog).getByLabelText("Team name"));
    await userEvent.type(within(dialog).getByLabelText("Team name"), "billing");
    await userEvent.click(within(dialog).getByRole("button", { name: "Rename" }));

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "PATCH", path: "/api/teams/payments", body: { name: "billing" } },
      ]),
    );
    expect(await screen.findByRole("heading", { level: 1, name: "billing" })).toBeVisible();
  });

  it("goes back to the teams once the team is deleted", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/teams/payments" });

    await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete payments" });
    await userEvent.type(within(dialog).getByLabelText("Team name"), "payments");
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(calls).toEqual([{ method: "DELETE", path: "/api/teams/payments", body: undefined }]),
    );
    expect(await screen.findByRole("heading", { level: 1, name: "Teams" })).toBeVisible();
  });

  it("says a team that is not in the organization is not there", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/teams/gone" });

    expect(await screen.findByText("No such team")).toBeVisible();
    expect(screen.getByRole("link", { name: "View teams" })).toHaveAttribute("href", "/teams");
  });

  it("is not found for somebody who is not an admin", async () => {
    stubAdapter({ session: { ...admin, role: "user" } });
    renderWithProviders(<AppRoutes />, { route: "/teams/payments" });

    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeVisible();
  });
});
