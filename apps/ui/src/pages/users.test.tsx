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
  write?: (method: string, path: string, body: unknown) => Response | undefined;
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
        const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined;
        calls.push({ method, path, body });
        const answer = options.write?.(method, path, body);
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

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    await userEvent.type(await screen.findByLabelText("Email addresses"), "linus@example.com");
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

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    await userEvent.type(await screen.findByLabelText("Email addresses"), "linus@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Send invite" }));

    expect(await screen.findByRole("status")).toHaveTextContent(
      "Invite created for linus@example.com. It was not emailed, so send them this link.",
    );
  });

  it("keeps the send button off until the address looks like one", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    const send = await screen.findByRole("button", { name: "Send invite" });
    expect(send).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Email addresses"), "linus");
    expect(send).toBeDisabled();
    expect(await screen.findByText("linus is not an email address.")).toBeVisible();
    await userEvent.type(screen.getByLabelText("Email addresses"), "@example.com");
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

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    await userEvent.selectOptions(await screen.findByLabelText("Role"), "admin");
    await userEvent.click(screen.getByLabelText("Anyone with the link can join"));

    expect(screen.getByLabelText("Email addresses")).toBeDisabled();
    expect(screen.getByLabelText("Role")).toHaveValue("user");
    expect(screen.getByLabelText("Role")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Create link" }));

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

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    await userEvent.type(await screen.findByLabelText("Email addresses"), "grace@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Send invite" }));

    const dialog = screen.getByRole("dialog", { name: "Invite people" });
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "That address already has a pending invitation. Resend or revoke it.",
    );
  });

  it("sends one invite per address and lists the one that was refused", async () => {
    const calls = stubAdapter({
      write: (_method, _path, body) => {
        const email = (body as { email: string }).email;
        if (email === "grace@example.com") {
          return jsonResponse(
            {
              code: "ErrConflict",
              message: "That address already has a pending invitation. Resend or revoke it.",
            },
            409,
          );
        }

        return jsonResponse(
          { ...created, email, link: `https://pds.example/claim?token=${email}` },
          201,
        );
      },
    });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    await userEvent.type(
      await screen.findByLabelText("Email addresses"),
      "linus@example.com, grace@example.com{enter}ken@example.com linus@example.com",
    );
    const addresses = screen.getByRole("list", { name: "Addresses to invite" });
    expect(within(addresses).getAllByRole("listitem")).toHaveLength(3);
    await userEvent.click(screen.getByRole("button", { name: "Send 3 invites" }));

    await waitFor(() => expect(calls).toHaveLength(3));
    expect(calls.map((call) => (call.body as { email: string }).email)).toEqual([
      "linus@example.com",
      "grace@example.com",
      "ken@example.com",
    ]);
    const links = await screen.findByRole("status");
    expect(within(links).getByText("Invite sent to linus@example.com.")).toBeVisible();
    expect(within(links).getByText("Invite sent to ken@example.com.")).toBeVisible();
    expect(links).toHaveTextContent(
      "grace@example.com. Not sent: That address already has a pending invitation. Resend or revoke it.",
    );
    expect(within(links).getAllByRole("button", { name: "Copy link" })).toHaveLength(2);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("keeps each link until it is dismissed", async () => {
    stubAdapter({
      write: (_method, _path, body) => {
        const email = (body as { email: string }).email;

        return jsonResponse(
          { ...created, email, link: `https://pds.example/claim?token=${email}` },
          201,
        );
      },
    });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    await userEvent.type(
      await screen.findByLabelText("Email addresses"),
      "linus@example.com ken@example.com",
    );
    await userEvent.click(screen.getByRole("button", { name: "Send 2 invites" }));

    const links = await screen.findByRole("status");
    await userEvent.click(
      within(links).getByRole("button", { name: "Dismiss the invite for linus@example.com" }),
    );
    expect(within(links).queryByText("Invite sent to linus@example.com.")).not.toBeInTheDocument();
    expect(within(links).getByText("Invite sent to ken@example.com.")).toBeVisible();
    await userEvent.click(
      within(links).getByRole("button", { name: "Dismiss the invite for ken@example.com" }),
    );
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("opens the claim screen of a new link in a new tab", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(await screen.findByRole("button", { name: "Invite people" }));
    await userEvent.type(await screen.findByLabelText("Email addresses"), "linus@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Send invite" }));

    const open = await screen.findByRole("link", { name: "Open as the invited person" });
    expect(open).toHaveAttribute("href", "/claim?token=fresh-token");
    expect(open).toHaveAttribute("target", "_blank");
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

  it("moves a user to another team", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(
      await screen.findByRole("button", { name: "Move Grace Hopper to another team" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Move Grace Hopper" });
    expect(within(dialog).getByLabelText("New team")).toHaveValue("default");
    await userEvent.click(within(dialog).getByRole("button", { name: "Move" }));

    await waitFor(() =>
      expect(calls).toEqual([{ method: "PATCH", path: "/api/users/2", body: { team: "default" } }]),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("changes a role and keeps save off until it differs", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(
      await screen.findByRole("button", { name: "Change the role of Grace Hopper" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Change the role of Grace Hopper" });
    const save = within(dialog).getByRole("button", { name: "Save role" });
    expect(save).toBeDisabled();
    await userEvent.click(within(dialog).getByRole("radio", { name: /Admin/ }));
    await userEvent.click(save);

    await waitFor(() =>
      expect(calls).toEqual([{ method: "PATCH", path: "/api/users/2", body: { role: "admin" } }]),
    );
  });

  it("shows the control plane's refusal for the last admin as it stands", async () => {
    stubAdapter({
      write: () =>
        jsonResponse(
          { code: "ErrConflict", message: "An organization needs at least one admin." },
          409,
        ),
    });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(
      await screen.findByRole("button", { name: "Change the role of Ada Lovelace" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Change the role of Ada Lovelace" });
    await userEvent.click(within(dialog).getByRole("radio", { name: /User/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Save role" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "An organization needs at least one admin.",
    );
  });

  it("removes a user only after it is confirmed", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/users" });

    await userEvent.click(await screen.findByRole("button", { name: "Remove Grace Hopper" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove Grace Hopper" });
    expect(dialog).toHaveTextContent("Grace Hopper loses access to Acme on their next request.");
    expect(calls).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove" }));

    await waitFor(() =>
      expect(calls).toEqual([{ method: "DELETE", path: "/api/users/2", body: undefined }]),
    );
  });

  it("shows an empty state when nothing is pending", async () => {
    stubAdapter({ invitations: [] });
    renderWithProviders(<AppRoutes />, { route: "/users" });

    expect(await screen.findByText("No pending invites")).toBeVisible();
  });
});
