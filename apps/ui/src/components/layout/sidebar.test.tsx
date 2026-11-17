import { screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Sidebar } from "@/components/layout/sidebar";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const acme = { id: 1, name: "Acme", role: "admin", current: true };
const initech = { id: 2, name: "Initech", role: "user", current: false };

function stubSession(session: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/auth/session") {
        return Promise.resolve(jsonResponse(session));
      }

      return Promise.resolve(
        jsonResponse({ code: "ErrNotImplemented", message: "Not stubbed." }, 501),
      );
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Sidebar organization", () => {
  it("gives an admin the Organization group with Teams, and a switcher", async () => {
    stubSession({
      username: "acme",
      user: "Ada",
      organization: "Acme",
      role: "admin",
      organizations: [acme],
    });

    renderWithProviders(<Sidebar />);

    const group = await screen.findByRole("group", { name: "Organization" });
    expect(within(group).getByRole("link", { name: "Teams" })).toHaveAttribute("href", "/teams");
    expect(within(group).getByRole("link", { name: "Users" })).toHaveAttribute("href", "/users");
    expect(within(group).getByRole("link", { name: "Settings" })).toHaveAttribute(
      "href",
      "/settings",
    );
    expect(screen.getByRole("button", { name: /Acme/ })).toBeVisible();
  });

  it("leaves the Organization group out for a user", async () => {
    stubSession({
      username: "acme",
      user: "Grace",
      organization: "Acme",
      role: "user",
      organizations: [{ ...acme, role: "user" }],
    });

    renderWithProviders(<Sidebar />);

    expect(await screen.findByText("Acme")).toBeVisible();
    expect(screen.queryByRole("group", { name: "Organization" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Teams" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Users" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Settings" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Acme/ })).not.toBeInTheDocument();
  });

  it("gives a user of several organizations the switcher but not the group", async () => {
    stubSession({
      username: "acme",
      user: "Grace",
      organization: "Acme",
      role: "user",
      organizations: [{ ...acme, role: "user" }, initech],
    });

    renderWithProviders(<Sidebar />);

    expect(await screen.findByRole("button", { name: /Acme/ })).toBeVisible();
    expect(screen.queryByRole("group", { name: "Organization" })).not.toBeInTheDocument();
  });

  it("names the person rather than the username when the session knows them", async () => {
    stubSession({ username: "acme", user: "Ada Lovelace", organization: "Acme", role: "admin" });

    renderWithProviders(<Sidebar />);

    expect(await screen.findByText("Ada Lovelace")).toBeVisible();
  });

  it("shows the Experimental label to a user, not only to an admin", async () => {
    stubSession({ username: "acme", user: "Grace", organization: "Acme", role: "user" });

    renderWithProviders(<Sidebar />);

    expect(await screen.findByRole("button", { name: /Experimental/ })).toBeVisible();
  });
});
