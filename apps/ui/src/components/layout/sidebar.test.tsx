import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Sidebar } from "@/components/layout/sidebar";
import { SidebarPreferenceProvider } from "@/components/layout/sidebar-preference";
import { renderWithProviders } from "@/test/utils";

function renderSidebar(route = "/") {
  return renderWithProviders(
    <SidebarPreferenceProvider>
      <Sidebar />
    </SidebarPreferenceProvider>,
    { route },
  );
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const acme = { id: 1, name: "Acme", role: "admin", current: true };
const initech = { id: 2, name: "Initech", role: "user", current: false };

const adminSession = {
  username: "acme",
  user: "Ada",
  organization: "Acme",
  role: "admin",
  organizations: [acme],
};

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
  window.localStorage.clear();
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

    renderSidebar();

    const toggle = await screen.findByRole("button", { name: "Organization" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "Teams" })).not.toBeInTheDocument();

    await userEvent.click(toggle);

    const group = screen.getByRole("group", { name: "Organization" });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
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

    renderSidebar();

    expect(await screen.findByText("Acme")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Organization" })).not.toBeInTheDocument();
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

    renderSidebar();

    expect(await screen.findByRole("button", { name: /Acme/ })).toBeVisible();
    expect(screen.queryByRole("group", { name: "Organization" })).not.toBeInTheDocument();
  });

  it("names the person rather than the username when the session knows them", async () => {
    stubSession({ username: "acme", user: "Ada Lovelace", organization: "Acme", role: "admin" });

    renderSidebar();

    expect(await screen.findByText("Ada Lovelace")).toBeVisible();
  });

  it("opens Organization beside its icon when the sidebar is collapsed", async () => {
    stubSession({
      username: "acme",
      user: "Ada",
      organization: "Acme",
      role: "admin",
      organizations: [acme],
    });
    window.localStorage.setItem("pds-sidebar", "collapsed");

    renderSidebar();

    expect(screen.getByTestId("sidebar").className).toContain("w-16");
    expect(screen.queryByRole("link", { name: "Teams" })).not.toBeInTheDocument();

    await userEvent.click(await screen.findByRole("button", { name: "Organization" }));

    const menu = screen.getByRole("group", { name: "Organization" });
    expect(within(menu).getByRole("link", { name: "Teams" })).toBeVisible();
    expect(menu.className).toContain("absolute");
  });

  it("shows the Experimental label to a user, not only to an admin", async () => {
    stubSession({ username: "acme", user: "Grace", organization: "Acme", role: "user" });

    renderSidebar();

    expect(await screen.findByRole("button", { name: /Experimental/ })).toBeVisible();
  });

  it.each(["/teams", "/users", "/settings"])(
    "opens the Organization group on %s so the current page shows",
    async (route) => {
      stubSession(adminSession);

      renderSidebar(route);

      const toggle = await screen.findByRole("button", { name: "Organization" });
      expect(toggle).toHaveAttribute("aria-expanded", "true");
      const group = screen.getByRole("group", { name: "Organization" });
      expect(within(group).getByRole("link", { current: "page" })).toHaveAttribute("href", route);
    },
  );

  it("marks Organization as the current place in the collapsed rail", async () => {
    stubSession(adminSession);
    window.localStorage.setItem("pds-sidebar", "collapsed");

    renderSidebar("/users");

    const toggle = await screen.findByRole("button", { name: "Organization" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle.className).toContain("bg-accent text-foreground");
  });

  it("leaves Organization closed and plain on another page", async () => {
    stubSession(adminSession);

    renderSidebar("/environments");

    const toggle = await screen.findByRole("button", { name: "Organization" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle.className).not.toContain("bg-accent text-foreground");
  });
});

describe("Sidebar rail tooltips", () => {
  it("places a tooltip outside the rail on hover and on keyboard focus", async () => {
    stubSession(adminSession);
    window.localStorage.setItem("pds-sidebar", "collapsed");

    renderSidebar();

    const link = screen.getByRole("link", { name: "Alerts" });
    const tip = within(link).getByRole("tooltip", { hidden: true });
    expect(tip).not.toBeVisible();

    await userEvent.hover(link);
    expect(tip).toBeVisible();
    // Fixed to the viewport, so the scrolling nav cannot clip it.
    expect(tip.className).toContain("fixed");
    expect(tip.style.left).not.toBe("");

    await userEvent.unhover(link);
    expect(tip).not.toBeVisible();

    link.focus();
    expect(await within(link).findByRole("tooltip", { hidden: true })).toBeVisible();
  });
});
