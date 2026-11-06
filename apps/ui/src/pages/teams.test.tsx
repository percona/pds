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

type Adapter = {
  teams?: Array<{ name: string }>;
  users?: Array<{ id: number; name: string; team: string; role: string }>;
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

/** Stubs the control plane for the Teams screen and records every write. */
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
      if (path === "/api/teams" && method === "GET") {
        return Promise.resolve(
          jsonResponse({ teams: options.teams ?? [{ name: "default" }, { name: "payments" }] }),
        );
      }
      if (path === "/api/users") {
        return Promise.resolve(
          jsonResponse({
            users: options.users ?? [
              { id: 1, name: "Ada", team: "default", role: "admin" },
              { id: 2, name: "Grace", team: "payments", role: "user" },
              { id: 3, name: "Linus", team: "payments", role: "user" },
            ],
          }),
        );
      }
      if (method !== "GET") {
        calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined });

        return Promise.resolve(
          options.write?.(method, path) ?? new Response(null, { status: 204 }),
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

describe("Teams screen", () => {
  it("lists the teams with how many users each has", async () => {
    stubAdapter();

    renderWithProviders(<AppRoutes />, { route: "/teams" });

    expect(await screen.findByRole("heading", { level: 1, name: "Teams" })).toBeVisible();
    const payments = (await screen.findByRole("cell", { name: "payments" })).closest("tr");
    expect(payments).not.toBeNull();
    expect(within(payments as HTMLElement).getByRole("cell", { name: "2" })).toBeVisible();
    const fallback = screen.getByRole("cell", { name: "default" }).closest("tr");
    expect(within(fallback as HTMLElement).getByRole("cell", { name: "1" })).toBeVisible();
  });

  it("creates a team from its name", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/teams" });

    await userEvent.click(await screen.findByRole("button", { name: "Create team" }));
    const dialog = await screen.findByRole("dialog", { name: "Create team" });
    const submit = within(dialog).getByRole("button", { name: "Create team" });
    expect(submit).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText("Team name"), "platform");
    await userEvent.click(submit);

    await waitFor(() =>
      expect(calls).toEqual([{ method: "POST", path: "/api/teams", body: { name: "platform" } }]),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("says a team name is taken and keeps the dialog open", async () => {
    stubAdapter({
      write: () =>
        jsonResponse(
          { code: "ErrConflict", message: "A team with that name already exists." },
          409,
        ),
    });
    renderWithProviders(<AppRoutes />, { route: "/teams" });

    await userEvent.click(await screen.findByRole("button", { name: "Create team" }));
    const dialog = await screen.findByRole("dialog", { name: "Create team" });
    await userEvent.type(within(dialog).getByLabelText("Team name"), "payments");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create team" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "A team with that name already exists.",
    );
  });

  it("renames a team, starting from its current name", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/teams" });

    await userEvent.click(await screen.findByRole("button", { name: "Rename payments" }));
    const dialog = await screen.findByRole("dialog", { name: "Rename payments" });
    const field = within(dialog).getByLabelText("Team name");
    expect(field).toHaveValue("payments");
    await userEvent.clear(field);
    await userEvent.type(field, "billing");
    await userEvent.click(within(dialog).getByRole("button", { name: "Rename" }));

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "PATCH", path: "/api/teams/payments", body: { name: "billing" } },
      ]),
    );
  });

  it("encodes a team name in the address", async () => {
    const calls = stubAdapter({ teams: [{ name: "data / ml" }], users: [] });
    renderWithProviders(<AppRoutes />, { route: "/teams" });

    await userEvent.click(await screen.findByRole("button", { name: "Rename data / ml" }));
    const dialog = await screen.findByRole("dialog", { name: "Rename data / ml" });
    await userEvent.type(within(dialog).getByLabelText("Team name"), "s");
    await userEvent.click(within(dialog).getByRole("button", { name: "Rename" }));

    await waitFor(() => expect(calls[0]?.path).toBe("/api/teams/data%20%2F%20ml"));
  });

  it("deletes a team only once its name is typed", async () => {
    const calls = stubAdapter({ teams: [{ name: "default" }, { name: "scratch" }], users: [] });
    renderWithProviders(<AppRoutes />, { route: "/teams" });

    await userEvent.click(await screen.findByRole("button", { name: "Delete scratch" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete scratch" });
    const confirm = within(dialog).getByRole("button", { name: "Delete" });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText("Team name"), "scratch");
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);

    await waitFor(() =>
      expect(calls).toEqual([{ method: "DELETE", path: "/api/teams/scratch", body: undefined }]),
    );
  });

  it("says why a team could not be deleted", async () => {
    stubAdapter({
      write: () =>
        jsonResponse(
          { code: "ErrConflict", message: "That team still has users. Move or remove them first." },
          409,
        ),
    });
    renderWithProviders(<AppRoutes />, { route: "/teams" });

    await userEvent.click(await screen.findByRole("button", { name: "Delete payments" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete payments" });
    await userEvent.type(within(dialog).getByLabelText("Team name"), "payments");
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "That team still has users. Move or remove them first.",
    );
  });

  it("explains a failed read and offers a retry", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input) === "/api/auth/session"
            ? jsonResponse(admin)
            : jsonResponse({ code: "ErrInternal", message: "The teams could not be read." }, 500),
        ),
      ),
    );

    renderWithProviders(<AppRoutes />, { route: "/teams" });

    expect(await screen.findByRole("alert")).toHaveTextContent("The teams could not be read.");
    expect(screen.getByRole("button", { name: "Try again" })).toBeVisible();
  });

  it("is not found for somebody who is not an admin", async () => {
    stubAdapter({ session: { ...admin, role: "user" } });

    renderWithProviders(<AppRoutes />, { route: "/teams" });

    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Teams" })).not.toBeInTheDocument();
  });
});
