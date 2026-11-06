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

type Invitation = {
  id: number;
  email?: string;
  anyEmail: boolean;
  team: string;
  role: string;
  expiresAt: string;
  createdAt: string;
};

type Adapter = {
  invitations?: Invitation[];
  session?: Record<string, unknown>;
  /** Answers a write, or returns undefined to accept it. */
  write?: (method: string, path: string) => Response | undefined;
};

const admin = {
  username: "acme",
  user: "Ada",
  organization: "Acme",
  team: "default",
  role: "admin",
};

const pending: Invitation = {
  id: 7,
  email: "grace@example.com",
  anyEmail: false,
  team: "payments",
  role: "user",
  expiresAt: "2026-11-01T10:00:00Z",
  createdAt: "2026-10-16T10:00:00Z",
};

const created = {
  ...pending,
  id: 8,
  email: "linus@example.com",
  link: "https://pds.example/claim?token=fresh-token",
  emailed: true,
};

/** Stubs the control plane for the Users screen and records every write. */
function stubAdapter(options: Adapter = {}) {
  const calls: Calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/auth/session") {
        return Promise.resolve(jsonResponse(options.session ?? admin));
      }
      if (path === "/api/teams") {
        return Promise.resolve(
          jsonResponse({ teams: [{ name: "default" }, { name: "payments" }] }),
        );
      }
      if (path === "/api/users") {
        return Promise.resolve(
          jsonResponse({
            users: [
              {
                id: 1,
                name: "Ada Lovelace",
                email: "ada@example.com",
                team: "default",
                role: "admin",
              },
              { id: 2, name: "Grace Hopper", team: "payments", role: "user" },
            ],
          }),
        );
      }
      if (path === "/api/invitations" && method === "GET") {
        return Promise.resolve(jsonResponse({ invitations: options.invitations ?? [pending] }));
      }
      if (method !== "GET") {
        calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
        const answer = options.write?.(method, path);
        if (answer) {
          return Promise.resolve(answer);
        }

        return Promise.resolve(
          method === "DELETE" ? new Response(null, { status: 204 }) : jsonResponse(created, 201),
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

describe("Users screen", () => {
  it("lists the users and the invitations that are still pending", async () => {
    stubAdapter();

    renderWithProviders(<AppRoutes />, { route: "/users" });

    expect(await screen.findByRole("heading", { level: 1, name: "Users" })).toBeVisible();
    const ada = (await screen.findByRole("cell", { name: "Ada Lovelace" })).closest("tr");
    expect(within(ada as HTMLElement).getByRole("cell", { name: "ada@example.com" })).toBeVisible();
    expect(within(ada as HTMLElement).getByRole("cell", { name: "Admin" })).toBeVisible();
    const invite = (await screen.findByRole("cell", { name: "grace@example.com" })).closest("tr");
    expect(within(invite as HTMLElement).getByRole("cell", { name: "payments" })).toBeVisible();
  });

  it("has no Users page for somebody who is not an admin", async () => {
    stubAdapter({ session: { ...admin, role: "user" } });

    renderWithProviders(<AppRoutes />, { route: "/users" });

    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeVisible();
  });

  it("invites an address into a team with a role and shows the link once", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.type(await screen.findByLabelText("Email"), "linus@example.com");
    await userEvent.selectOptions(screen.getByLabelText("Team"), "payments");
    await userEvent.selectOptions(screen.getByLabelText("Role"), "admin");
    await userEvent.click(screen.getByRole("button", { name: "Send invite" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/invitations",
          body: { email: "linus@example.com", anyEmail: false, team: "payments", role: "admin" },
        },
      ]),
    );
    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent("Invite sent to linus@example.com");
    expect(within(notice).getByText("https://pds.example/claim?token=fresh-token")).toBeVisible();
    expect(within(notice).getByRole("button", { name: "Copy link" })).toBeVisible();
  });

  it("says the link was not emailed when the control plane could not send it", async () => {
    stubAdapter({
      write: () => jsonResponse({ ...created, emailed: false }, 201),
    });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.type(await screen.findByLabelText("Email"), "linus@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Send invite" }));

    expect(await screen.findByRole("status")).toHaveTextContent(
      "Invite created for linus@example.com. It was not emailed, so send them this link.",
    );
  });

  it("keeps the send button off until the address looks like one", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    const send = await screen.findByRole("button", { name: "Send invite" });
    expect(send).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Email"), "linus");
    expect(send).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Email"), "@example.com");
    expect(send).toBeEnabled();
  });

  it("invites any email as a user, with no address", async () => {
    const calls = stubAdapter({
      write: () =>
        jsonResponse(
          { ...created, email: undefined, anyEmail: true, role: "user", emailed: false },
          201,
        ),
    });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.selectOptions(await screen.findByLabelText("Role"), "admin");
    await userEvent.click(screen.getByLabelText("Anyone with the link can join"));

    expect(screen.getByLabelText("Email")).toBeDisabled();
    expect(screen.getByLabelText("Role")).toHaveValue("user");
    expect(screen.getByLabelText("Role")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Send invite" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/invitations",
          body: { email: "", anyEmail: true, team: "default", role: "user" },
        },
      ]),
    );
    expect(await screen.findByRole("status")).toHaveTextContent("Invite created for anyone");
  });

  it("shows the reason an invite was refused", async () => {
    stubAdapter({
      write: () =>
        jsonResponse(
          {
            code: "ErrConflict",
            message: "That address already has a pending invitation. Resend or revoke it.",
          },
          409,
        ),
    });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.type(await screen.findByLabelText("Email"), "grace@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Send invite" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "That address already has a pending invitation. Resend or revoke it.",
    );
  });

  it("resends an invitation and shows its new link", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(await screen.findByRole("button", { name: "Resend grace@example.com" }));

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "POST", path: "/api/invitations/7/resend", body: undefined },
      ]),
    );
    expect(await screen.findByRole("status")).toBeVisible();
  });

  it("revokes an invitation only after it is confirmed", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(await screen.findByRole("button", { name: "Revoke grace@example.com" }));
    const dialog = await screen.findByRole("dialog", { name: "Revoke invite" });
    expect(calls).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));

    await waitFor(() =>
      expect(calls).toEqual([{ method: "DELETE", path: "/api/invitations/7", body: undefined }]),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("shows an empty state when nothing is pending", async () => {
    stubAdapter({ invitations: [] });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    expect(await screen.findByText("No pending invites")).toBeVisible();
  });
});
